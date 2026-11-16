package app

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
)

// headTarget is a request target for op's path that the generated binder
// accepts: each path parameter filled in, and each required query
// parameter given.
func headTarget(prefix, path string, item *openapi3.PathItem, op *openapi3.Operation) string {
	params := append(append(openapi3.Parameters{}, item.Parameters...), op.Parameters...)
	target := prefix + fillParams(path)
	var query []string
	for _, p := range params {
		if p.Value.Required && p.Value.In == "query" {
			query = append(query, p.Value.Name+"="+plausibleValue(p.Value))
		}
	}
	if len(query) > 0 {
		target += "?" + strings.Join(query, "&")
	}
	return target
}

// TestAccHeadAnswersEveryGeneratedGet holds every operation the seven
// generated servers mount as a GET, without a HEAD of its own, to 7.x's
// Jersey rule (JAX-RS 2.1, section 3.3.5): a HEAD answers what the GET
// answers, the status and every header but the request's own ID, with no
// body and the GET's Content-Length. Most are 501 stubs, whose 501 the HEAD
// answers as well. An operation whose path a hand-written route serves
// instead (the PyPI JSON API), or whose path a hand-written route serves a
// HEAD of under other parameter names (the Puppet Forge API), keeps what
// that route mounts.
func TestAccHeadAnswersEveryGeneratedGet(t *testing.T) {
	h := statusFixture(t, false)
	router, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("the fixture is a %T, not a chi router", h)
	}
	admin := basic("admin", "pw")["Authorization"]
	checked := 0
	for _, name := range specFiles {
		doc := loadSpec(t, name)
		prefix := doc.Servers[0].URL
		for path, item := range doc.Paths.Map() {
			op := item.Get
			if op == nil || item.Head != nil {
				continue
			}
			if handwritten, _ := op.Extensions["x-handwritten"].(bool); handwritten {
				continue
			}
			target := headTarget(prefix, path, item, op)
			pattern := prefix + path
			routed, _, _ := strings.Cut(target, "?")
			served := router.Find(chi.NewRouteContext(), http.MethodGet, routed)
			if served != pattern {
				t.Logf("GET %s is served as %s", target, served)
				continue
			}
			shape := pathParameterRE.ReplaceAllString(pattern, "{}")
			if own := router.Find(chi.NewRouteContext(), http.MethodHead, routed); own != "" &&
				pathParameterRE.ReplaceAllString(own, "{}") == shape {
				t.Logf("HEAD %s is served as %s", target, own)
				continue
			}
			answer := func(method string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, target, nil)
				req.Header.Set("Authorization", admin)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				return rec
			}
			get, head := answer(http.MethodGet), answer(http.MethodHead)
			checked++
			want := get.Header().Clone()
			if want.Get("Content-Length") == "" {
				want.Set("Content-Length", strconv.Itoa(get.Body.Len()))
			}
			want.Set(middleware.RequestIDHeader, head.Header().Get(middleware.RequestIDHeader))
			switch {
			case head.Code != get.Code:
				t.Errorf("HEAD %s (%s): %d, GET %d %s",
					target, op.OperationID, head.Code, get.Code, get.Body.String())
			case head.Body.Len() != 0:
				t.Errorf("HEAD %s (%s): %d bytes of body", target, op.OperationID, head.Body.Len())
			case !maps.EqualFunc(head.Header(), want, slices.Equal):
				t.Errorf("HEAD %s (%s): header %v, GET's %v",
					target, op.OperationID, head.Header(), want)
			}
		}
	}
	if checked < 240 {
		t.Fatalf("checked %d operations; the walk lost its way", checked)
	}
	t.Logf("checked %d operations", checked)
}

// TestUnitHeadServedRoutes holds headServed to the router: it serves the
// pattern of every GET of the generated servers and of no hand-written
// operation, each pattern is a GET route the router mounts (or one a
// hand-written route mounts the same path under, with other parameter
// names), and the route of a spec HEAD is still there to answer through
// itself.
func TestUnitHeadServedRoutes(t *testing.T) {
	router := NewRouter(RouterConfig{}).(*chi.Mux)
	routes := servedRoutes(t, router)
	shapes := map[string]bool{}
	for key := range routes {
		shapes[pathParameterRE.ReplaceAllString(key, "{}")] = true
	}
	served := headServed()
	n := 0
	for _, op := range operations.Table {
		if op.Method != http.MethodGet {
			continue
		}
		if served(op.Pattern) == op.Handwritten {
			t.Errorf("%s %s (handwritten %v): served %v",
				op.Method, op.Pattern, op.Handwritten, served(op.Pattern))
		}
		if !served(op.Pattern) {
			continue
		}
		n++
		if !shapes[pathParameterRE.ReplaceAllString("GET "+op.Pattern, "{}")] {
			t.Errorf("GET %s (%s) is no route", op.Pattern, op.OperationID)
		}
	}
	if !served(vcsNestedPattern) || routes["GET "+vcsNestedPattern] == nil {
		t.Errorf("the VCS subgroup route %s is not served", vcsNestedPattern)
	}
	if served("") || n < 240 {
		t.Errorf("served %d patterns, the empty one %v", n, served(""))
	}
	if routes["HEAD /artifactory/api/v2/security/permissions/{permissionTargetName}"] == nil {
		t.Error("the permission target check's own HEAD route is gone")
	}
}

// TestAccHeadOnTheWire calls a served server over HTTP, so that what
// net/http adds or leaves out is part of the answer, for the cases #1598
// names: JSON operations, and operations that refuse with 404, 401 and 403,
// each answering a HEAD with the GET's status and
// header, Content-Length and type included, and no body; a POST-only
// operation still answers a HEAD 405, and so does a hand-written GET-only
// route.
func TestAccHeadOnTheWire(t *testing.T) {
	srv := httptest.NewServer(statusFixture(t, false))
	t.Cleanup(srv.Close)
	admin := basic("admin", "pw")["Authorization"]
	call := func(method, path, auth string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res, body
	}
	for _, c := range []struct {
		path, auth string
		status     int
	}{
		{"/artifactory/api/repositories", admin, http.StatusOK},
		{"/artifactory/api/repositories/libs-local", admin, http.StatusOK},
		{"/artifactory/api/security/users/nobody", admin, http.StatusNotFound},
		{"/artifactory/api/repositories", "", http.StatusUnauthorized},
		// Public while anonymous access is off, as its GET is.
		{"/artifactory/api/v1/system/readiness", "", http.StatusOK},
		{"/artifactory/api/repositories/absent", admin, http.StatusNotFound},
		{"/artifactory/api/security/users", basic("user", "pw")["Authorization"], http.StatusForbidden},
	} {
		get, getBody := call(http.MethodGet, c.path, c.auth)
		head, headBody := call(http.MethodHead, c.path, c.auth)
		if get.StatusCode != c.status {
			t.Fatalf("GET %s: %d %s, want %d", c.path, get.StatusCode, getBody, c.status)
		}
		want := get.Header.Clone()
		want.Del("Date")
		want.Del(middleware.RequestIDHeader)
		if want.Get("Content-Length") == "" {
			// net/http chunks a body it cannot hold: a HEAD says its length.
			want.Set("Content-Length", strconv.Itoa(len(getBody)))
		}
		got := head.Header.Clone()
		got.Del("Date")
		got.Del(middleware.RequestIDHeader)
		if head.StatusCode != get.StatusCode || len(headBody) != 0 ||
			!maps.EqualFunc(got, want, slices.Equal) {
			t.Errorf("HEAD %s: %d, %d bytes, header %v; GET %d, header %v",
				c.path, head.StatusCode, len(headBody), got, get.StatusCode, want)
		}
	}
	for _, path := range []string{
		"/artifactory/api/alpine/alpine-local/reindex",
		"/artifactory/api/system/ping",
	} {
		res, _ := call(http.MethodHead, path, admin)
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("HEAD %s: %d, want 405", path, res.StatusCode)
		}
	}
}
