// Package app wires configuration, the stores and the HTTP server
// together. cmd/artifactweave stays a thin cobra shim and
// calls Run; tests drive Run with a fake handler.
package app

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/buildinfo"
	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/server"
)

const (
	// readHeaderTimeout bounds slowloris-style clients. There is deliberately
	// no WriteTimeout: artifact downloads stream for as long as the client
	// needs, and per-request deadlines belong in the handlers.
	readHeaderTimeout = 10 * time.Second
	// drainTimeout is how long in-flight requests get after a shutdown
	// signal before they are cancelled and their connections closed.
	drainTimeout = 10 * time.Second
)

// newHandler builds the HTTP handler. It is a variable so tests can run the
// server lifecycle without the real router.
var newHandler = server.New

// listen and drain are variables so tests can inject a broken listener and a
// short drain without waiting ten seconds for a timeout.
var (
	listen = net.Listen
	drain  = drainTimeout
)

// Run opens the store and blob stores, serves until ctx is cancelled, then
// drains and closes them and returns nil. Any startup, listener or serve
// failure is returned wrapped.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	return run(ctx, cfg, log, nil)
}

// run is Run with an optional channel that receives the bound address once
// the listener is open, so tests can connect without racing the startup.
func run(ctx context.Context, cfg config.Config, log *slog.Logger, ready chan<- string) error {
	// The store opens first, so migrations finish before the port accepts
	// a request.
	rt, err := openRuntime(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	err = serve(ctx, cfg, log, rt, ready)
	// Mirror commits still running after the drain get as long again to be
	// recorded.
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
	defer cancel()
	return errors.Join(err, rt.close(closeCtx))
}

func serve(
	ctx context.Context,
	cfg config.Config,
	log *slog.Logger,
	rt *runtime,
	ready chan<- string,
) error {
	ln, err := listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	// Requests must not run on ctx: the shutdown signal cancels it, which
	// would fail every in-flight request with a 500 before the drain below
	// could let it finish. They are cancelled only once the drain has expired.
	reqCtx, cancelRequests := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRequests()
	srv := &http.Server{
		Handler: newHandler(server.Config{
			BaseURL:       cfg.BaseURL,
			CompatVersion: cfg.Compat.ArtifactoryVersion,
			Revision:      buildinfo.Version(),
			Logger:        log,
			Store:         rt.store,
			Blobs:         rt.blobs,
			ColdBlobs:     rt.coldBlobs,
			Auth:          rt.auth,
			GC:            rt.gc,
			BlobLocation:  rt.location,
			Mirrors:       rt.mirrorsAdmin(),
			Multipart:     rt.multipart,
			Tokens:        rt.tokens,
			Redirect: server.Redirect{
				MinSize: int64(cfg.Download.Redirect.MinSize), TTL: cfg.Download.Redirect.TTL,
				CDNMaxSize: cdnMaxSize(cfg.Blob),
			},
			FolderDownloadMaxConcurrent: cfg.Download.Folder.MaxConcurrentRequests,
			AQL: server.AQLLimits{
				MaxResults: cfg.Search.AQL.MaxResults,
				ScanFactor: cfg.Search.AQL.NonAdminScanFactor,
				Timeout:    cfg.AQLTimeout.Query, StreamTimeout: cfg.AQLTimeout.Streamed,
			},
			Search: server.SearchLimits{
				UserQueryLimit:     cfg.Search.UserQueryLimit,
				LimitInternalUsers: !cfg.Search.LimitAnonymousUsersOnly,
			},
			DockerSubdomainMatch:           cfg.Docker.SubdomainMatch,
			AllowBasicAuthTokens:           cfg.Token.AllowBasicAuth,
			AllowDuplicateBuildEntries:     !cfg.Build.BlockDuplicateEntries,
			EnforceIdenticalChecksumDeploy: cfg.Deploy.EnforceIdenticalChecksumDeploy,
			NPMMaxPublishSize:              int64(cfg.NPM.MaxPublishSize),
			GlobalEnvironmentLimit:         cfg.Environments.GlobalLimit,
			Work:                           rt.work,
		}),
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return reqCtx },
		ErrorLog:          stdlog.New(serverLog{log}, "", 0),
	}
	logSigning(ctx, log, rt.store, rt.blobs)
	log.Info("artifactweave listening",
		"addr", ln.Addr().String(),
		"version", buildinfo.Version(),
		"compat_version", cfg.Compat.ArtifactoryVersion,
	)
	if ready != nil {
		ready <- ln.Addr().String()
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		// Serve only returns before shutdown on a real failure.
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	log.Info("artifactweave shutting down", "drain", drain.String())
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Shutdown leaves active connections open when the drain expires;
		// cancelling and closing them keeps shutdown bounded.
		cancelRequests()
		_ = srv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	// Serve returns ErrServerClosed after Shutdown; anything else is a bug.
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// serverLog routes net/http's own messages (TLS handshake errors, panics it
// recovered) into slog. It drops the warning net/http logs for every query
// holding a ";": the properties API separates properties with ";", its
// handlers parse the raw query themselves, and http.AllowQuerySemicolons
// would split "k=v;k2=v2" into separate parameters.
type serverLog struct{ log *slog.Logger }

func (l serverLog) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if !strings.Contains(msg, "URL query contains semicolon") {
		l.log.Warn(msg, "source", "net/http")
	}
	return len(p), nil
}
