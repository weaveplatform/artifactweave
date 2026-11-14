package middleware

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

func TestUnitCollectionSlash(t *testing.T) {
	var path, raw string
	h := CollectionSlash(map[string]string{"/api/things/": "/api/things"})(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			path, raw = r.URL.Path, r.URL.RawPath
		}),
	)
	r := httptest.NewRequest(http.MethodGet, "/api/things/", nil)
	r.URL.RawPath = "/api/things/"
	rctx := chi.NewRouteContext()
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	h.ServeHTTP(httptest.NewRecorder(), r)
	if path != "/api/things" || raw != "" || rctx.RoutePath != "/api/things" {
		t.Fatalf("collection: path %q raw %q route %q", path, raw, rctx.RoutePath)
	}
	// Without chi's route context, and for a path the map does not name.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/things/", nil))
	if path != "/api/things" {
		t.Fatalf("no route context: path %q", path)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/things/a/", nil))
	if path != "/api/things/a/" {
		t.Fatalf("member path rewritten: %q", path)
	}
}

func TestUnitAccessLogNamesTheUserAndRedacts(t *testing.T) {
	var buf bytes.Buffer
	redact := func(string) string { return "/redacted" }
	h := AccessLog(slog.New(slog.NewJSONHandler(&buf, nil)), redact)(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			httpkit.NoteUser(r.Context(), "deployer")
		}),
	)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/secret/token", nil))
	line := buf.String()
	if !strings.Contains(line, `"user":"deployer"`) ||
		!strings.Contains(line, `"path":"/redacted"`) ||
		strings.Contains(line, "token") {
		t.Fatalf("log line %s", line)
	}
}

func TestUnitRecoverPanicReraisesAbort(t *testing.T) {
	h := RecoverPanic(slog.New(slog.DiscardHandler), plainPath)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }),
	)
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

// TestUnitAccessLogLogsAnAbortedResponse: a handler that aborts a response
// it has begun still gets its line, at ERROR with the cause it noted, and
// the abort reaches net/http; any other panic passes through unlogged for
// RecoverPanic, and runtime.Goexit is left to run.
func TestUnitAccessLogLogsAnAbortedResponse(t *testing.T) {
	var buf bytes.Buffer
	log := AccessLog(slog.New(slog.NewJSONHandler(&buf, nil)), plainPath)
	serve := func(h http.HandlerFunc) (rec any) {
		defer func() { rec = recover() }()
		log(h).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/list", nil))
		return nil
	}
	rec := serve(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[1,"))
		httpkit.NoteCause(r.Context(), errors.New("store: page 2 failed"))
		panic(http.ErrAbortHandler)
	})
	if rec != http.ErrAbortHandler { //nolint:errorlint // the very value net/http compares
		t.Fatalf("recovered %v, want http.ErrAbortHandler", rec)
	}
	line := buf.String()
	for _, want := range []string{
		`"level":"ERROR"`, `"status":200`, `"bytes":3`, `"aborted":true`,
		`"error":"store: page 2 failed"`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line %s lacks %s", line, want)
		}
	}
	buf.Reset()
	if rec := serve(func(http.ResponseWriter, *http.Request) { panic("bug") }); rec != "bug" {
		t.Fatalf("recovered %v, want the handler's panic", rec)
	}
	if buf.Len() != 0 {
		t.Fatalf("a panic that is not an abort was logged: %s", buf.String())
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(func(http.ResponseWriter, *http.Request) { runtime.Goexit() })
		t.Error("runtime.Goexit was stopped")
	}()
	<-done
	if buf.Len() != 0 {
		t.Fatalf("runtime.Goexit was logged: %s", buf.String())
	}
}
