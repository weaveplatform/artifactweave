package middleware

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
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
