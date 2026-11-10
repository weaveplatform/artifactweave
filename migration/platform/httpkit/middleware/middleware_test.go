package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

func TestAccRequestIDReusedOrMinted(t *testing.T) {
	var seen string
	h := RequestID(
		http.HandlerFunc(
			func(_ http.ResponseWriter, r *http.Request) { seen = httpkit.RequestIDFrom(r.Context()) },
		),
	)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(RequestIDHeader, "client-id")
	h.ServeHTTP(rec, r)
	if seen != "client-id" || rec.Header().Get(RequestIDHeader) != "client-id" {
		t.Fatalf("client id not reused: ctx %q header %q", seen, rec.Header().Get(RequestIDHeader))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if seen == "" || seen == "client-id" || rec.Header().Get(RequestIDHeader) != seen {
		t.Fatalf("minted id wrong: ctx %q header %q", seen, rec.Header().Get(RequestIDHeader))
	}
	if httpkit.RequestIDFrom(context.Background()) != "" {
		t.Fatal("bare context should have no id")
	}
	if newRequestID() == newRequestID() {
		t.Fatal("ids must differ")
	}
}

func TestAccAccessLogFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := RequestID(
		AccessLog(logger, plainPath)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("hello"))
		})),
	)
	r := httptest.NewRequest(http.MethodPut, "/artifactory/repo/a.txt", nil)
	r.Header.Set(RequestIDHeader, "rid-1")
	h.ServeHTTP(httptest.NewRecorder(), r)
	for _, want := range []string{`"method":"PUT"`, `"path":"/artifactory/repo/a.txt"`, `"status":201`, `"bytes":5`, `"duration"`, `"request_id":"rid-1"`} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("log line %s lacks %s", buf.String(), want)
		}
	}
}

func TestAccAccessLogLogsTheCauseOfAnUntypedError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		level   string
		cause   bool
	}{{
		name: "untyped error",
		handler: func(w http.ResponseWriter, r *http.Request) {
			httpkit.WriteErr(w, r, errors.New("sqlite: disk I/O error"))
		},
		level: "ERROR",
		cause: true,
	}, {
		name: "untyped error the binder renders as a 500",
		handler: func(w http.ResponseWriter, r *http.Request) {
			BindingError(http.StatusInternalServerError)(w, r, errors.New("sqlite: disk I/O error"))
		},
		level: "ERROR",
		cause: true,
	}, {
		name:    "typed error",
		handler: func(w http.ResponseWriter, r *http.Request) { httpkit.WriteErr(w, r, itemNotFound()) },
		level:   "INFO",
	}, {
		name: "untyped binding error",
		handler: func(w http.ResponseWriter, r *http.Request) {
			BindingError(http.StatusBadRequest)(w, r, errors.New("bad header"))
		},
		level: "INFO",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := RequestID(
				AccessLog(slog.New(slog.NewJSONHandler(&buf, nil)), plainPath)(tc.handler),
			)
			h.ServeHTTP(
				httptest.NewRecorder(),
				httptest.NewRequest(http.MethodGet, "/artifactory/api/x", nil),
			)
			line := buf.String()
			if !strings.Contains(line, `"level":"`+tc.level+`","msg":"request"`) {
				t.Fatalf("log line %s is not at %s", line, tc.level)
			}
			if got := strings.Contains(line, `"error":"sqlite: disk I/O error"`); got != tc.cause {
				t.Fatalf("log line %s: cause logged %t, want %t", line, got, tc.cause)
			}
		})
	}
	// Without AccessLog above it, a handler's error has nowhere to go and
	// must not panic.
	rec := httptest.NewRecorder()
	httpkit.WriteErr(
		rec,
		httptest.NewRequest(http.MethodGet, "/artifactory/api/x", nil),
		errors.New("lost"),
	)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "lost") {
		t.Fatalf("untyped error: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAccRecoverPanicUsesEnvelope(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := httpkit.ErrorProfiles(accessProfile)(RecoverPanic(
		logger,
		plainPath,
	)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }),
	))
	for path, want := range map[string]string{
		"/artifactory/api/x": `{"errors":[{"status":500,"message":"internal error"}]}`,
		"/access/api/x":      `{"errors":[{"code":"INTERNAL_ERROR","message":"internal error"}]}`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if diff := cmp.Diff(decode(t, want), decode(t, rec.Body.String())); diff != "" {
			t.Fatalf("%s: body mismatch (-want +got):\n%s", path, diff)
		}
	}
	if !strings.Contains(buf.String(), `"panic":"boom"`) {
		t.Fatalf("panic not logged: %s", buf.String())
	}
}

func TestAccBodyLimit(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64)
		if _, err := r.Body.Read(buf); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	h := BodyLimit(8)(next)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(
		http.MethodPost,
		"/artifactory/api/repositories/x",
		strings.NewReader("0123456789"),
	)
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(rec.Body.String(), `"status":413`) {
		t.Fatalf("declared length: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	r = httptest.NewRequest(
		http.MethodPost,
		"/artifactory/api/repositories/x",
		strings.NewReader("0123456789"),
	)
	r.ContentLength = -1
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("undeclared length: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(
		rec,
		httptest.NewRequest(
			http.MethodPost,
			"/artifactory/api/repositories/x",
			strings.NewReader("ok"),
		),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("small body: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifactory/api/repositories/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("nil body: %d", rec.Code)
	}
}

func TestAccNormalizeContentType(t *testing.T) {
	var gotHeader, gotOriginal string
	h := NormalizeContentType(
		keepLFS,
	)(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			gotHeader = r.Header.Get("Content-Type")
			gotOriginal = httpkit.OriginalContentType(r.Context())
		}),
	)
	cases := []struct{ in, header, original, path string }{
		{
			"application/vnd.org.jfrog.artifactory.repositories.LocalRepositoryConfiguration+json; charset=utf-8",
			"application/json; charset=utf-8",
			"application/vnd.org.jfrog.artifactory.repositories.LocalRepositoryConfiguration+json",
			"",
		},
		{"application/json", "application/json", "", ""},
		{"application/yaml", "application/yaml", "", ""},
		{"", "", "", ""},
		{"not/a/type;;", "not/a/type;;", "", ""},
		{
			"application/vnd.git-lfs+json; charset=utf-8",
			"application/vnd.git-lfs+json; charset=utf-8",
			"",
			"/artifactory/api/lfs/lfs-local/objects/batch",
		},
	}
	for _, tc := range cases {
		path := tc.path
		if path == "" {
			path = "/"
		}
		r := httptest.NewRequest(http.MethodPost, path, nil)
		if tc.in != "" {
			r.Header.Set("Content-Type", tc.in)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		if gotHeader != tc.header || gotOriginal != tc.original {
			t.Fatalf("%q: header %q original %q", tc.in, gotHeader, gotOriginal)
		}
	}
}

func TestAccBindingError(t *testing.T) {
	rec := httptest.NewRecorder()
	BindingError(
		http.StatusBadRequest,
	)(
		rec,
		httptest.NewRequest(http.MethodGet, "/artifactory/api/x", nil),
		errors.New("bad header"),
	)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad header") {
		t.Fatalf("binding error: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	BindingError(
		http.StatusBadRequest,
	)(
		rec,
		httptest.NewRequest(http.MethodGet, "/access/api/x", nil).WithContext(
			httpkit.WithProfileRules(context.Background(), accessProfile),
		),
		&http.MaxBytesError{Limit: 8},
	)
	if rec.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(rec.Body.String(), `"code":"INTERNAL_ERROR"`) &&
			!strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("max bytes: %d %s", rec.Code, rec.Body.String())
	}
}

// plainPath logs paths as they are: these tests name no credential.
func plainPath(path string) string { return path }

// accessProfile gives /access/ paths the Access profile, as the server's
// rules do, so the tests can tell the two envelopes apart.
var accessProfile = []httpkit.ProfileRule{{
	Match:   func(path string) bool { return strings.HasPrefix(path, "/access/") },
	Profile: apierror.ProfileAccess,
}}

// keepLFS keeps the media type of Git LFS protocol requests, as the
// server's predicate does.
func keepLFS(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/artifactory/api/lfs/")
}

// itemNotFound is a typed error as the server's handlers return one.
func itemNotFound() error { return apierror.New(http.StatusNotFound, "Could not find resource") }

func decode(t *testing.T, body string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}
