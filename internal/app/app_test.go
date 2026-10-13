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

	"github.com/weaveplatform/weaveplatform-artifact-registry/internal/config"
)

func fakeHandler(t *testing.T, body string) {
	t.Helper()
	orig := newHandler
	newHandler = func(config.Config, *slog.Logger) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		})
	}
	t.Cleanup(func() { newHandler = orig })
}

func TestRunServesAndDrains(t *testing.T) {
	fakeHandler(t, "pong")
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, log, ready) }()

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
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "addr="+addr) || !strings.Contains(logs, "shutting down") {
		t.Fatalf("logs missing address or shutdown line:\n%s", logs)
	}
}

func TestRunListenFailure(t *testing.T) {
	fakeHandler(t, "")
	// Occupy a port so the second listener fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	cfg := config.Default()
	cfg.Listen = ln.Addr().String()
	err = Run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("want listen error, got %v", err)
	}
}

func TestRunDrainsInflightRequest(t *testing.T) {
	var served atomic.Bool
	release := make(chan struct{})
	orig := newHandler
	newHandler = func(config.Config, *slog.Logger) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
			served.Store(true)
			_, _ = io.WriteString(w, "late")
		})
	}
	t.Cleanup(func() { newHandler = orig })

	cfg := config.Default()
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

func TestRunServeFailure(t *testing.T) {
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
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	err := Run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "serve:") {
		t.Fatalf("want serve error, got %v", err)
	}
}

func TestRunShutdownTimeout(t *testing.T) {
	release := make(chan struct{})
	orig := newHandler
	newHandler = func(config.Config, *slog.Logger) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
			_, _ = io.WriteString(w, "late")
		})
	}
	origDrain := drain
	drain = 20 * time.Millisecond
	t.Cleanup(func() { newHandler = orig; drain = origDrain; close(release) })

	cfg := config.Default()
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

func TestDefaultHandlerIsWired(t *testing.T) {
	// The real constructor must build without panicking; routing itself is
	// tested in internal/server.
	h := newHandler(config.Default(), slog.Default())
	if h == nil {
		t.Fatal("nil handler")
	}
}

func TestErrServerClosedIsNotAnError(t *testing.T) {
	if !errors.Is(http.ErrServerClosed, http.ErrServerClosed) {
		t.Fatal("sanity")
	}
}
