package peers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/apilimit"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const qualificationStream = "test:qualification"

type streamCommand struct {
	Operation string
	Peers     []string
	Origin    string
	After     int64
	Change    streamtest.Change
	Entry     outbox.OutboxEntry
}

type streamReceipt struct {
	Error       string
	State       streamtest.State
	Applied     outbox.AppliedMarker
	Head        int64
	Retained    []outbox.OutboxEntry
	Flags       []model.StreamResync
	Runs        []model.FullSyncRun
	Cursor      outbox.PeerCursor
	Entry       outbox.OutboxEntry
	Gone        bool
	Expected    int64
	BlobPresent bool
}

// These files are private fixture control, never routes on an application
// listener. Requests are atomically renamed into place and receipts survive
// restart, so a completed command is not executed again.
func streamControlDir(dataDir string) string { return filepath.Join(dataDir, "stream-fixture") }

func streamWriteJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// runStreamFixture keeps production runtime costs, middleware, signed remote
// and background workers. Its only extra component is the existing test kind.
// app.serve is private: this fixture follows its independent request context,
// ten-second HTTP drain and subsequent ten-second runtime close.
func runStreamFixture(ctx context.Context, cfg config.Config, log *slog.Logger) (result error) {
	rt, err := app.OpenRuntime(ctx, cfg, log, app.RuntimeOptions{})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		result = errors.Join(result, rt.Close(closeCtx))
	}()
	engine := rt.Federation.Streams()
	kind := &streamtest.Kind{Store: rt.Store}
	dir := streamControlDir(cfg.DataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Subscribe is in memory; reconstruct only its actual saved setup. State
	// and apply history are already durable in streamtest.Kind's own store.
	var subscribed []string
	if data, err := os.ReadFile(filepath.Join(dir, "subscriptions.json")); err == nil {
		if err := json.Unmarshal(data, &subscribed); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	kind.Subscribe(qualificationStream, subscribed...)
	if err := engine.Kinds.Register(kind); err != nil {
		return err
	}
	limits, err := apilimit.New(rt.Store, cfg.API, nil)
	if err != nil {
		return err
	}
	requests, cancelRequests := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRequests()
	router := harness.Router(harness.Config{
		RequestLimits: limits, Store: rt.Store, Blobs: rt.Blobs, Auth: rt.Auth,
		Tokens: rt.Tokens, Keys: rt.Keys, LDAP: rt.LDAP, Logging: rt.Logging,
		Federation: rt.Federation, PeerRemote: rt.PeerRemote, Work: rt.Work,
		BaseURL: cfg.BaseURL, CompatVersion: cfg.Compat.ArtifactoryVersion, Logger: log,
	})
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler: router, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return requests },
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	controlCtx, cancelControl := context.WithCancel(ctx)
	controlled := make(chan error, 1)
	go func() { controlled <- streamControl(controlCtx, dir, rt, kind) }()
	select {
	case <-ctx.Done():
	case err := <-served:
		cancelControl()
		return errors.Join(fmt.Errorf("stream fixture serve: %w", err), <-controlled)
	case err := <-controlled:
		if err != nil {
			result = fmt.Errorf("stream fixture control: %w", err)
		} else if ctx.Err() == nil {
			result = errors.New("stream fixture control stopped before shutdown")
		}
		controlled = nil
	}
	cancelControl()
	if controlled != nil {
		result = errors.Join(result, <-controlled)
	}
	drainCtx, cancelDrain := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelDrain()
	if err := server.Shutdown(drainCtx); err != nil {
		cancelRequests()
		result = errors.Join(result, err, server.Close())
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		result = errors.Join(result, err)
	}
	return result
}

func streamControl(ctx context.Context, dir string, rt *app.Runtime, kind *streamtest.Kind) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		paths, err := filepath.Glob(filepath.Join(dir, "*.request"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			reply := path + ".receipt"
			if _, err := os.Stat(reply); errors.Is(err, os.ErrNotExist) {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				var cmd streamCommand
				if err := json.Unmarshal(data, &cmd); err != nil {
					return err
				}
				receipt, err := executeStreamCommand(ctx, dir, rt, kind, cmd)
				if err != nil {
					receipt.Error = err.Error()
				}
				if err := streamWriteJSON(reply, receipt); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
}

func executeStreamCommand(ctx context.Context, dir string, rt *app.Runtime, kind *streamtest.Kind, cmd streamCommand) (streamReceipt, error) {
	e := rt.Federation.Streams()
	switch cmd.Operation {
	case "subscribe":
		if err := streamWriteJSON(filepath.Join(dir, "subscriptions.json"), cmd.Peers); err != nil {
			return streamReceipt{}, err
		}
		kind.Subscribe(qualificationStream, cmd.Peers...)
		return streamReceipt{}, e.Sweep(ctx)
	case "commit":
		entry, err := kind.Commit(ctx, e, qualificationStream, cmd.Change, nil)
		return streamReceipt{Entry: entry}, err
	case "sweep":
		return streamReceipt{}, e.Sweep(ctx)
	case "pull", "duplicate":
		peer, err := rt.Store.Peers().Get(ctx, cmd.Origin)
		if err != nil {
			return streamReceipt{}, err
		}
		if cmd.Operation == "pull" {
			_, err := e.Remote.Pull(ctx, peer, qualificationStream, cmd.After, 1)
			if errors.Is(err, streamsync.ErrGone) {
				return streamReceipt{Gone: true}, nil
			}
			return streamReceipt{}, err
		}
		_, err = e.Remote.Push(ctx, peer, streamsync.Batch{
			Origin: cmd.Entry.Origin, Stream: qualificationStream, FromSeq: cmd.Entry.Seq,
			Entries: []outbox.OutboxEntry{cmd.Entry},
		})
		var conflict *streamsync.ConflictError
		if errors.As(err, &conflict) {
			return streamReceipt{Expected: conflict.Expected}, nil
		}
		return streamReceipt{}, err
	case "observe":
		return observeStream(ctx, rt, kind, cmd.Origin)
	default:
		return streamReceipt{}, fmt.Errorf("unknown stream fixture operation %q", cmd.Operation)
	}
}

func observeStream(ctx context.Context, rt *app.Runtime, kind *streamtest.Kind, origin string) (streamReceipt, error) {
	var r streamReceipt
	var err error
	r.State, err = kind.State(ctx, qualificationStream, origin)
	if err != nil {
		return r, err
	}
	r.Applied, err = rt.Store.Outbox().Applied(ctx, origin, qualificationStream)
	if err != nil {
		return r, err
	}
	self, err := rt.Store.Instance().Get(ctx)
	if err != nil {
		return r, err
	}
	r.Head, err = rt.Store.Outbox().LastSeq(ctx, self.InstanceID, qualificationStream)
	if err != nil {
		return r, err
	}
	r.Retained, err = rt.Store.Outbox().Read(ctx, self.InstanceID, qualificationStream, 0, 100)
	if err != nil {
		return r, err
	}
	r.Cursor, err = rt.Store.Outbox().Cursor(ctx, origin, qualificationStream)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return r, err
	}
	r.Flags, err = rt.Store.Resyncs().Resyncs(ctx, "")
	if err != nil {
		return r, err
	}
	r.Runs, _, err = rt.Store.Federation().FullSyncs(ctx, "", kernel.Page{})
	if err != nil {
		return r, err
	}
	r.BlobPresent = true
	for _, it := range r.State.Items {
		if it.SHA256 == "" {
			continue
		}
		held, err := rt.Blobs.Exists(ctx, it.SHA256)
		if err != nil {
			return r, err
		}
		r.BlobPresent = r.BlobPresent && held && !it.Pending
	}
	return r, nil
}
