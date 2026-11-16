package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// countingReader reports how many bytes its reader gave, and hides any
// io.WriterTo, so that io.Copy asks the writer to read it.
type countingReader struct {
	r    io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err //nolint:wrapcheck // a test reader
}

// headRouter is a router with HeadAsGet installed as the server's router
// installs it, serving the GET routes whose pattern starts with "/served",
// and the methods each middleware between Enter and Route saw.
func headRouter(t *testing.T, seen *[]string, routes func(chi.Router)) http.Handler {
	t.Helper()
	h := HeadAsGet{Serves: func(p string) bool { return strings.HasPrefix(p, "/served") }}
	r := chi.NewRouter()
	r.Use(h.Enter, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			*seen = append(*seen, req.Method)
			next.ServeHTTP(w, req)
		})
	}, h.Route)
	routes(r)
	return r
}

func TestUnitHeadAsGetAnswersTheGetWithoutItsBody(t *testing.T) {
	var seen []string
	var handled string
	body := strings.Repeat("x", 5000)
	file := &countingReader{r: strings.NewReader(body)}
	stream := &countingReader{r: strings.NewReader("streamed")}
	h := headRouter(t, &seen, func(r chi.Router) {
		r.Get("/served/json", func(w http.ResponseWriter, req *http.Request) {
			handled = req.Method
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Kept", "yes")
			w.WriteHeader(http.StatusAccepted)
			w.Header().Set("X-Late", "after the status")
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, `{"a":`)
			rc := http.NewResponseController(w)
			if err := rc.Flush(); err != nil {
				t.Errorf("flush: %v", err)
			}
			// The recorder underneath has no deadline: the call reaches it.
			if err := rc.SetWriteDeadline(time.Time{}); !errors.Is(err, http.ErrNotSupported) {
				t.Errorf("deadline: %v", err)
			}
			_, _ = io.WriteString(w, body+`}`)
		})
		r.Get("/served/sniffed", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "<html><body>hi</body></html>")
		})
		r.Get("/served/file", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "5000")
			_, _ = io.Copy(w, file)
		})
		r.Get("/served/stream", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.Copy(w, stream)
		})
		r.Get("/served/empty", func(http.ResponseWriter, *http.Request) {})
		r.Get("/served/unchanged", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotModified)
		})
	})
	for _, c := range []struct {
		path, status, length, typ string
	}{
		{"/served/json", "202", "5006", "application/json"},
		{"/served/sniffed", "200", "28", "text/html; charset=utf-8"},
		{"/served/file", "200", "5000", ""},
		{"/served/stream", "200", "8", "text/plain; charset=utf-8"},
		{"/served/empty", "200", "0", ""},
		{"/served/unchanged", "304", "", ""},
	} {
		seen, handled = nil, ""
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, c.path, nil))
		got := []string{
			strconv.Itoa(rec.Code), rec.Header().Get("Content-Length"),
			rec.Header().Get("Content-Type"),
		}
		want := []string{c.status, c.length, c.typ}
		if strings.Join(got, "|") != strings.Join(want, "|") || rec.Body.Len() != 0 {
			t.Errorf("HEAD %s: %q and %d bytes of body, want %q", c.path, got, rec.Body.Len(), want)
		}
		if len(seen) != 1 || seen[0] != http.MethodGet {
			t.Errorf("HEAD %s: the middleware between saw %v, want GET", c.path, seen)
		}
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodHead, "/served/json", nil))
	if handled != http.MethodHead {
		t.Errorf("the handler saw %q, want HEAD", handled)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/served/json", nil))
	if rec.Header().Get("X-Kept") != "yes" || rec.Header().Get("X-Late") != "" {
		t.Errorf("header %v: want the fields as they stood at the status", rec.Header())
	}
	if file.read != 0 {
		t.Errorf("a file of declared length was read for %d bytes", file.read)
	}
	if stream.read != len("streamed") {
		t.Errorf("a stream of undeclared length was read for %d bytes", stream.read)
	}
}

func TestUnitHeadAsGetKeepsOtherRoutes(t *testing.T) {
	var seen []string
	h := headRouter(t, &seen, func(r chi.Router) {
		ok := func(name string) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, name) }
		}
		r.Get("/served/own", ok("get"))
		r.Head("/served/own", ok("own head"))
		r.Post("/served/post", ok("post"))
		r.Get("/other", ok("other"))
		r.Get("/served/bundle/{name}/LATEST", ok("latest"))
		r.Head("/served/bundle/{name}/{version}", ok("version head"))
		r.Get("/served/pkg/{key}/{name}", ok("named get"))
		r.Head("/served/pkg/{repo}/{module}", ok("named head"))
	})
	for _, c := range []struct {
		path   string
		status int
		body   string
	}{
		// A route of its own answers, body and all, as net/http would send
		// it but for the body it drops on the wire.
		{"/served/own", http.StatusOK, "own head"},
		{"/served/post", http.StatusMethodNotAllowed, ""},
		{"/other", http.StatusMethodNotAllowed, ""},
		{"/absent", http.StatusNotFound, "404 page not found\n"},
		// chi backtracks into {version} to find a HEAD; the GET's template
		// has more literal characters, so JAX-RS picks it.
		{"/served/bundle/b/LATEST", http.StatusOK, ""},
		{"/served/bundle/b/1.0", http.StatusOK, "version head"},
		// The same template under other names: the HEAD route keeps it.
		{"/served/pkg/a/b", http.StatusOK, "named head"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, c.path, nil))
		if rec.Code != c.status || rec.Body.String() != c.body {
			t.Errorf("HEAD %s: %d %q, want %d %q",
				c.path, rec.Code, rec.Body.String(), c.status, c.body)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/served/own", nil))
		if m == http.MethodGet && rec.Body.String() != "get" {
			t.Errorf("GET: %q", rec.Body.String())
		}
	}
	// Outside chi there is nothing to look the routes up in.
	rec := httptest.NewRecorder()
	HeadAsGet{Serves: func(string) bool { return true }}.Enter(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, r.Method)
		}),
	).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/served/own", nil))
	if rec.Body.String() != http.MethodHead {
		t.Errorf("without a route context: %q", rec.Body.String())
	}
	if literals("/a/{b:(?:[^/]{2})}/c*") != len("/a//c") {
		t.Errorf("literals counted %d", literals("/a/{b:(?:[^/]{2})}/c*"))
	}
}
