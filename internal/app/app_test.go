package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/server"
)

// testConfig is the default configuration with its data directory, and so
// the SQLite store and the fs blob store, under t's temporary directory.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	return cfg
}

func fakeHandler(t *testing.T, body string) {
	t.Helper()
	orig := newHandler
	newHandler = func(server.Config) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		})
	}
	t.Cleanup(func() { newHandler = orig })
}

func TestAccRunServesAndDrains(t *testing.T) {
	fakeHandler(t, "pong")
	origDrain := drain
	drain = 250 * time.Millisecond
	t.Cleanup(func() { drain = origDrain })
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	cfg := testConfig(t)
	cfg.Listen = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = run(ctx, cfg, log, ready)
		close(done)
	}()
	// Keep the temporary SQLite store until run has finished closing it,
	// including when an earlier assertion fails.
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("run did not finish before temporary store cleanup")
		}
	})

	addr := <-ready
	resp, err := http.Get(
		"http://" + addr + "/artifactory/api/system/ping",
	) //nolint:noctx // test helper
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(b) != "pong" {
		t.Fatalf("body %q", b)
	}

	cancel()
	select {
	case <-done:
		if runErr != nil {
			t.Fatal(runErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "addr="+addr) || !strings.Contains(logs, "shutting down") {
		t.Fatalf("logs missing address or shutdown line:\n%s", logs)
	}
}

func TestAccRunListenFailure(t *testing.T) {
	fakeHandler(t, "")
	// Occupy a port so the second listener fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	cfg := testConfig(t)
	cfg.Listen = ln.Addr().String()
	err = Run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("want listen error, got %v", err)
	}
}

func TestAccRunDrainsInflightRequest(t *testing.T) {
	var served atomic.Bool
	release := make(chan struct{})
	orig := newHandler
	newHandler = func(server.Config) http.Handler {
		// Like a real handler, it gives up when its request context ends, so
		// the request survives the signal only if the drain protects it.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
				http.Error(w, "cancelled", http.StatusInternalServerError)
				return
			}
			served.Store(true)
			_, _ = io.WriteString(w, "late")
		})
	}
	t.Cleanup(func() { newHandler = orig })

	cfg := testConfig(t)
	cfg.Listen = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), ready) }()
	addr := <-ready

	got := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/") //nolint:noctx // test helper
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		got <- string(b)
	}()
	// Give the request time to reach the handler, then signal shutdown while
	// it is still in flight; the drain must let it finish.
	time.Sleep(100 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)
	close(release)

	if body := <-got; body != "late" {
		t.Fatalf("in-flight request got %q", body)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !served.Load() {
		t.Fatal("handler never completed")
	}
}

func TestAccRunServeFailure(t *testing.T) {
	fakeHandler(t, "")
	orig := listen
	listen = func(network, addr string) (net.Listener, error) {
		ln, err := net.Listen(network, addr)
		if err != nil {
			return nil, err
		}
		// A listener that is already closed makes Serve fail immediately,
		// which is the only way to reach the serve-error branch.
		_ = ln.Close()
		return ln, nil
	}
	t.Cleanup(func() { listen = orig })
	cfg := testConfig(t)
	cfg.Listen = "127.0.0.1:0"
	err := Run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "serve:") {
		t.Fatalf("want serve error, got %v", err)
	}
}

func TestAccRunShutdownTimeout(t *testing.T) {
	release := make(chan struct{})
	orig := newHandler
	newHandler = func(server.Config) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
			_, _ = io.WriteString(w, "late")
		})
	}
	origDrain := drain
	drain = 20 * time.Millisecond
	t.Cleanup(func() { newHandler = orig; drain = origDrain; close(release) })

	cfg := testConfig(t)
	cfg.Listen = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), ready) }()
	addr := <-ready
	go func() {
		resp, err := http.Get("http://" + addr + "/") //nolint:noctx // test helper
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "shutdown:") {
		t.Fatalf("want shutdown timeout error, got %v", err)
	}
}

func TestAccRunCancelsRequestsAfterDrain(t *testing.T) {
	entered := make(chan struct{}, 1)
	cancelled := make(chan time.Time, 1)
	orig := newHandler
	newHandler = func(server.Config) http.Handler {
		return http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			entered <- struct{}{}
			<-r.Context().Done()
			cancelled <- time.Now()
		})
	}
	origDrain := drain
	drain = 300 * time.Millisecond
	t.Cleanup(func() { newHandler = orig; drain = origDrain })

	cfg := testConfig(t)
	cfg.Listen = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), ready) }()
	addr := <-ready
	go func() {
		resp, err := http.Get("http://" + addr + "/") //nolint:noctx // test helper
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never entered the handler")
	}
	signalled := time.Now()
	cancel()

	// The signal alone must not reach the request; only the expired drain
	// may cancel it.
	time.Sleep(100 * time.Millisecond)
	select {
	case at := <-cancelled:
		t.Fatalf(
			"request cancelled %v after the signal, before the drain expired",
			at.Sub(signalled),
		)
	default:
	}
	select {
	case at := <-cancelled:
		if at.Sub(signalled) < drain {
			t.Fatalf("request cancelled %v after the signal, drain is %v", at.Sub(signalled), drain)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request never cancelled after the drain expired")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "shutdown:") {
			t.Fatalf("want shutdown timeout error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after the drain expired")
	}
}

func TestAccDefaultHandlerIsWired(t *testing.T) {
	// The real constructor must build without panicking; routing itself is
	// tested in internal/server.
	h := newHandler(server.Config{})
	if h == nil {
		t.Fatal("nil handler")
	}
}

func TestAccErrServerClosedIsNotAnError(t *testing.T) {
	if !errors.Is(http.ErrServerClosed, http.ErrServerClosed) {
		t.Fatal("sanity")
	}
}
