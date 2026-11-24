package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	skillsfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/skills"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
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
// pattern of every GET of the generated servers and of the hand-written
// operations handwrittenHeads names, and of no other hand-written
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
		want := !op.Handwritten || handwrittenHeads[op.Pattern] != 0
		if served(op.Pattern) != want {
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
// header, Content-Length and type included, and no body; and for the
// hand-written GETs #1613 names, the ping and the storage API among them. A
// POST-only operation still answers a HEAD 405, and so does a JFrog Router
// probe.
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
		// Public, as its GET is, and text, not JSON.
		{"/artifactory/api/system/ping", "", http.StatusOK},
		{"/artifactory/api/storage/libs-local", admin, http.StatusOK},
		{"/artifactory/api/storage/libs-local", "", http.StatusUnauthorized},
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
		"/router/api/v1/system/health",
	} {
		res, _ := call(http.MethodHead, path, admin)
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("HEAD %s: %d, want 405", path, res.StatusCode)
		}
	}
}

// TestUnitHeadDecidedForEveryGet holds every GET route the router mounts
// without a HEAD route of the same pattern to one decision about its HEAD
// (#1613): HeadAsGet serves it (a generated GET, or one handwrittenHeads
// names), or headUnserved says what answers instead. Each entry of either
// map is a GET route, and handwrittenHeads names no generated GET and no
// route with a HEAD of its own, so a route that moves or goes stale fails
// here rather than answering 405 unseen.
func TestUnitHeadDecidedForEveryGet(t *testing.T) {
	router := NewRouter(RouterConfig{}).(*chi.Mux)
	routes := servedRoutes(t, router)
	served := headServed()
	generated := map[string]bool{}
	for _, op := range operations.Table {
		if op.Method == http.MethodGet && !op.Handwritten {
			generated[op.Pattern] = true
		}
	}
	for key := range routes {
		pattern, ok := strings.CutPrefix(key, "GET ")
		if !ok || routes["HEAD "+pattern] != nil {
			continue
		}
		if _, left := headUnserved[pattern]; served(pattern) == left {
			t.Errorf("GET %s: served %v, left to chi %v; want one of them",
				pattern, served(pattern), left)
		}
	}
	for pattern, answer := range handwrittenHeads {
		switch {
		case routes["GET "+pattern] == nil:
			t.Errorf("handwrittenHeads: %s is no GET route", pattern)
		case routes["HEAD "+pattern] != nil:
			t.Errorf("handwrittenHeads: %s has a HEAD route of its own", pattern)
		case generated[pattern]:
			t.Errorf("handwrittenHeads: %s is a generated GET", pattern)
		case headRunsGetRoute(pattern) != (answer == headRunsGet):
			t.Errorf("handwrittenHeads: %s answers %d, runs the GET %v",
				pattern, answer, headRunsGetRoute(pattern))
		}
	}
	for pattern := range headUnserved {
		if routes["GET "+pattern] == nil || routes["HEAD "+pattern] != nil {
			t.Errorf("headUnserved: %s is no GET route without a HEAD route", pattern)
		}
	}
	// The generated handlers see the HEAD: the VCS downloads read it.
	if headRunsGetRoute(vcsNestedPattern) || headRunsGetRoute("") {
		t.Error("a generated or unknown route runs the HEAD as the GET")
	}
}

// headCase is one target of TestAccHeadAnswersHandwrittenGets. A case with
// a bearer token is called with it and with no credentials; any other with
// admin's, user's (no rights) and none.
type headCase struct {
	path    string
	headers []string
	bearer  string
}

// headWorld is newXetWorld with a repository of each package type whose
// hand-written GETs answer a HEAD (#1613), folder downloads on, and
// something in most of them for the GETs to find: files and a folder in
// libs-local, a skill, a Hugging Face model and a Terraform workspace. It
// returns the workspace's ID.
func headWorld(t *testing.T) (*itemWorld, string) {
	t.Helper()
	w := newXetWorld(t)
	ctx := t.Context()
	if err := w.db.Settings().Put(ctx, settings.SettingFolderDownload,
		artmodel.FolderDownloadSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{Key: "cargo-local", Class: repo.Local, PackageType: repo.Cargo},
		{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
		{Key: "lfs-local", Class: repo.Local, PackageType: repo.GitLFS},
		{Key: "lua-local", Class: repo.Local, PackageType: repo.LuaRocks},
		{Key: "ml-local", Class: repo.Local, PackageType: repo.MachineLearning},
		{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
		{Key: "nuget-local", Class: repo.Local, PackageType: repo.NuGet},
		{Key: "pub-local", Class: repo.Local, PackageType: repo.Pub},
		{Key: "pypi-local", Class: repo.Local, PackageType: repo.PyPI},
		{Key: "skills-local", Class: repo.Local, PackageType: repo.Skills},
		{Key: "tfbe-local", Class: repo.Local, PackageType: repo.TerraformBackend},
	} {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"fixture/get.txt", "fixture/deep/a.txt", "top.txt"} {
		wantStatus(t, w.do("PUT", "/artifactory/libs-local/"+p, "admin", "fixture "+p), 201)
	}
	var body strings.Builder
	mw := multipart.NewWriter(&body)
	for _, part := range []struct{ field, file, content string }{
		{"payload", "", `{"slug":"demo","version":"1.0.0"}`},
		{"files[]", "SKILL.md", "---\nname: Demo\ndescription: A demo\n---\n# Demo\n"},
	} {
		var pw io.Writer
		var err error
		if part.file == "" {
			pw, err = mw.CreateFormField(part.field)
		} else {
			pw, err = mw.CreateFormFile(part.field, part.file)
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(pw, part.content)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do("POST", "/artifactory/api/skills/skills-local/api/v1/skills", "admin",
		body.String(), "Content-Type", mw.FormDataContentType()), 201)
	publishXetFixture(t, w, "hf")
	created := w.do("POST",
		"/artifactory/api/terraform/remote/v2/organizations/tfbe-local/workspaces", "admin",
		`{"data":{"type":"workspaces","attributes":{"name":"app"}}}`,
		"Content-Type", "application/vnd.api+json")
	wantStatus(t, created, 201)
	var ws struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &ws); err != nil || ws.Data.ID == "" {
		t.Fatalf("workspace %s: %v", created.Body, err)
	}
	return w, ws.Data.ID
}

// TestAccHeadAnswersHandwrittenGets holds every hand-written GET route
// that handwrittenHeads names to 7.x's Jersey rule, as
// TestAccHeadAnswersEveryGeneratedGet holds the generated ones (#1613): a
// HEAD answers what the GET answers, for each caller, the status and every
// header but the request's own ID, with no body and the GET's
// Content-Length. A folder archive's HEAD goes without the length, which
// only building the archive would tell. The JFrog Router's probes still
// answer a HEAD 405, and the PyPI JSON API answers it through the PyPI
// protocol's own HEAD.
func TestAccHeadAnswersHandwrittenGets(t *testing.T) {
	w, workspace := headWorld(t)
	read := xetTokenFor(t, w, "hf", "acme/demo", "read", "reader")
	cas := strings.TrimPrefix(read.CAS, "https://reg.example")
	hash := strings.Repeat("a", 64)
	lfs := []string{"Accept", "application/vnd.git-lfs+json"}
	cases := []headCase{
		{path: "/access/api/v1/system/ping"},
		{path: "/access/api/v1/system/readiness"},
		{path: "/access/api/v1/system/liveness"},
		{path: "/artifactory/api/system/ping"},
		{path: "/artifactory/api/system/configuration"},
		{path: "/artifactory/api/storage/libs-local"},
		{path: "/artifactory/api/storage/libs-local/top.txt"},
		{path: "/artifactory/api/storage/libs-local/fixture/get.txt?properties"},
		{path: "/artifactory/api/storage/libs-local/absent.txt"},
		{path: "/artifactory/api/storage/absent-repo"},
		{path: "/artifactory/api/storage/auto-trashcan"},
		{path: "/artifactory/api/storage/auto-trashcan/fixture/get.txt"},
		{path: "/artifactory/api/archive/download/libs-local/fixture?archiveType=zip"},
		{path: "/artifactory/api/archive/download/libs-local/fixture/deep?archiveType=tar"},
		{path: "/artifactory/api/archive/download/libs-local/absent?archiveType=zip"},
		{path: "/artifactory/api/download/libs-local/top.txt"},
		{path: "/artifactory/api/download/libs-local/fixture/get.txt"},
		{path: "/artifactory/api/replication/libs-local"},
		{path: "/artifactory/api/replication/libs-local/fixture"},
		{path: "/artifactory/api/versions/libs-local/fixture"},
		{path: "/artifactory/api/versions/libs-local/fixture/deep"},
		{path: "/artifactory/api/artifactweave/remote-status/up"},
		{path: "/artifactory/api/huggingfaceml/hf/api/models/acme/demo"},
		{path: "/artifactory/api/huggingfaceml/hf/api/models/acme/demo/revision/main"},
		{path: "/artifactory/api/huggingfaceml/hf/api/models/acme/demo/tree/main"},
		{path: "/artifactory/api/huggingfaceml/hf/api/models/acme/demo/tree/main/sub"},
		{path: "/artifactory/api/huggingfaceml/hf/api/models/acme/demo/xet-read-token/main"},
		{path: "/artifactory/api/huggingfaceml/hf/api/models/acme/demo/xet-write-token/main"},
		{path: "/artifactory/api/huggingfaceml/hf/api/datasets/acme/demo"},
		{path: "/artifactory/api/huggingfaceml/hf/api/datasets/acme/demo/revision/main"},
		{path: "/artifactory/api/huggingfaceml/hf/api/datasets/acme/demo/tree/main"},
		{path: "/artifactory/api/huggingfaceml/hf/api/datasets/acme/demo/tree/main/sub"},
		{path: "/artifactory/api/huggingfaceml/hf/api/datasets/acme/demo/xet-read-token/main"},
		{path: "/artifactory/api/huggingfaceml/hf/api/datasets/acme/demo/xet-write-token/main"},
		{path: cas + "/v1/reconstructions/" + xetFixtureFile, bearer: read.Token},
		{path: cas + "/v2/reconstructions/" + xetFixtureFile, bearer: read.Token},
		{path: cas + "/v1/chunks/default/" + xetFixtureHash, bearer: read.Token},
		{path: cas + "/data/" + xetFixtureFile + "/0/0/1", bearer: read.Token},
		{path: "/artifactory/api/skills/skills-local/.well-known/clawhub.json"},
		{path: "/artifactory/api/skills/skills-local/api/v1/skills"},
		{path: "/artifactory/api/skills/skills-local/api/v1/skills/demo"},
		{path: "/artifactory/api/skills/skills-local/api/v1/skills/demo/versions"},
		{path: "/artifactory/api/skills/skills-local/api/v1/skills/demo/versions/1.0.0"},
		{path: "/artifactory/api/skills/skills-local/api/v1/skills/demo/file?path=SKILL.md"},
		{path: "/artifactory/api/skills/skills-local/api/v1/search?q=demo"},
		{path: "/artifactory/api/skills/skills-local/api/v1/resolve?slug=demo"},
		{path: "/artifactory/api/skills/skills-local/api/v1/download?slug=demo&version=1.0.0"},
		{path: "/artifactory/api/skills/skills-local/xrayStatus"},
		{path: "/artifactory/api/terraform/remote/v2/ping"},
		{path: "/artifactory/api/terraform/remote/v2/account/details"},
		{path: "/artifactory/api/terraform/remote/v2/organizations/tfbe-local"},
		{path: "/artifactory/api/terraform/remote/v2/organizations/tfbe-local/entitlement-set"},
		{path: "/artifactory/api/terraform/remote/v2/organizations/tfbe-local/workspaces"},
		{path: "/artifactory/api/terraform/remote/v2/organizations/tfbe-local/workspaces/app"},
		{path: "/artifactory/api/terraform/remote/v2/workspaces/" + workspace},
		{
			path: "/artifactory/api/terraform/remote/v2/workspaces/" + workspace + "/current-state-version",
		},
		{path: "/artifactory/api/terraform/remote/v2/state-versions?" +
			"filter%5Borganization%5D%5Bname%5D=tfbe-local&filter%5Bworkspace%5D%5Bname%5D=app"},
		{path: "/artifactory/api/terraform/remote/v2/state-versions/sv-x"},
		{path: "/artifactory/api/terraform/remote/v2/state-versions/sv-x/download"},
		{path: "/artifactory/api/pub/pub-local/api/packages/versions/new"},
		{path: "/artifactory/api/pub/pub-local/api/packages/versions/new/complete"},
		{path: "/artifactory/api/pub/pub-local/api/packages/foo/versions/1.0.0"},
		{path: "/artifactory/api/cargo/cargo-local/api/v1/crates/foo"},
		{path: "/artifactory/api/nuget/v3/nuget-local/query?q=x"},
		{path: "/artifactory/api/nuget/v3/nuget-local/autocomplete?q=x"},
		{path: "/artifactory/api/machinelearning/ml-local/model/entity-manifest/name/1.0"},
		{path: "/artifactory/api/machinelearning/ml-local/model/entity-manifest/ns/name/1.0"},
		{path: "/artifactory/api/lfs/lfs-local/locks", headers: lfs},
		{path: "/artifactory/api/docker/docker-local/v1/auth"},
		{path: "/artifactory/api/npm/npm-local/-/ping"},
		{path: "/artifactory/api/npm/npm-local/-/whoami"},
		{path: "/artifactory/api/npm/npm-local/-/v1/search?text=x"},
		{path: "/artifactory/api/npm/npm-local/-/package/foo/dist-tags"},
		{path: "/artifactory/api/luarocks/lua-local/api/tool_version"},
		{
			path: "/artifactory/api/luarocks/lua-local/api/1/" + hash + "/check_rockspec?package=x&version=1",
		},
		{path: "/router/api/v1/system/health"},
		{path: "/router/api/v1/system/readiness"},
		{path: "/router/api/v1/system/liveness"},
		{path: "/artifactory/api/pypi/pypi-local/pypi/foo/json"},
		{path: "/artifactory/api/pypi/pypi-local/pypi/foo/1.0/json"},
		{path: "/artifactory/api/v1/service_trust/pairings/artifactory-cold"},
		{path: "/artifactory/api/v1/service_trust/pairings/federated-repo/*"},
	}
	router, ok := w.h.(chi.Routes)
	if !ok {
		t.Fatalf("the router is a %T", w.h)
	}
	covered := map[string]bool{}
	answered := map[int]int{}
	for _, c := range cases {
		routed, _, _ := strings.Cut(c.path, "?")
		pattern := router.Find(chi.NewRouteContext(), http.MethodGet, routed)
		covered[pattern] = true
		type caller struct {
			as      string
			headers []string
		}
		callers := []caller{{"admin", c.headers}, {"user", c.headers}, {"", c.headers}}
		if c.bearer != "" {
			callers = []caller{{"", []string{"Authorization", "Bearer " + c.bearer}}, {"", nil}}
		}
		for _, by := range callers {
			get := w.do(http.MethodGet, c.path, by.as, "", by.headers...)
			head := w.do(http.MethodHead, c.path, by.as, "", by.headers...)
			label := fmt.Sprintf("HEAD %s as %q (%s)", c.path, by.as, pattern)
			if reason, left := headUnserved[pattern]; left {
				// Admin's HEAD of a Router probe passes authentication,
				// which has its GET alone public, and meets the 405.
				if strings.HasPrefix(reason, "405") && by.as == "admin" &&
					head.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s: %d, want 405", label, head.Code)
				}
				if !strings.HasPrefix(reason, "405") && head.Code != get.Code {
					t.Errorf("%s: %d, GET %d", label, head.Code, get.Code)
				}
				continue
			}
			answered[get.Code]++
			want := get.Header().Clone()
			want.Del(middleware.RequestIDHeader)
			streamed := strings.HasPrefix(pattern, "/artifactory/api/archive/download/") &&
				get.Code == http.StatusOK
			if want.Get("Content-Length") == "" && !streamed && get.Code != http.StatusNoContent {
				want.Set("Content-Length", strconv.Itoa(get.Body.Len()))
			}
			got := head.Header().Clone()
			got.Del(middleware.RequestIDHeader)
			switch {
			case head.Code != get.Code:
				t.Errorf("%s: %d, GET %d %s", label, head.Code, get.Code, get.Body)
			case head.Body.Len() != 0:
				t.Errorf("%s: %d bytes of body", label, head.Body.Len())
			case !maps.EqualFunc(got, want, slices.Equal):
				t.Errorf("%s: header %v, GET's %v", label, got, want)
			}
		}
	}
	for pattern := range handwrittenHeads {
		if !covered[pattern] {
			t.Errorf("no case reaches %s", pattern)
		}
	}
	for pattern := range headUnserved {
		if !covered[pattern] {
			t.Errorf("no case reaches %s", pattern)
		}
	}
	// The cases reach answers, not only refusals.
	if answered[http.StatusOK] < 50 {
		t.Errorf("answers by status %v: too few 200s", answered)
	}
}

// openCounter is a blob store that counts the blobs opened through it.
type openCounter struct {
	blob.Store

	opens *atomic.Int64
}

func (b openCounter) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	b.opens.Add(1)
	return b.Store.Open(ctx, sha) //nolint:wrapcheck // the wrapped store's own error
}

// TestAccHeadOfADownloadReadsNothing holds the hand-written downloads that
// see the HEAD (headSeesHead) to the item routes' HEAD: a folder archive's
// HEAD opens no file to build an archive it would drop, and a skill
// bundle's HEAD counts no download, where their GETs do both.
func TestAccHeadOfADownloadReadsNothing(t *testing.T) {
	var opens atomic.Int64
	w := newItemWorld(t, false, func(c *RouterConfig) {
		signer, ok := c.Blobs.(signingBlobs)
		if !ok {
			t.Fatalf("the blob store is a %T", c.Blobs)
		}
		signer.Store = openCounter{Store: signer.Store, opens: &opens}
		c.Blobs = signer
	})
	ctx := t.Context()
	if err := w.db.Settings().Put(ctx, settings.SettingFolderDownload,
		artmodel.FolderDownloadSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	skills := repo.Config{Key: "skills-local", Class: repo.Local, PackageType: repo.Skills}
	if err := w.db.Repos().Create(ctx, *repo.Defaults(&skills)); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/fixture/get.txt", "admin", "fixture"), 201)
	archive := "/artifactory/api/archive/download/libs-local/fixture?archiveType=zip"
	before := opens.Load()
	head := w.do(http.MethodHead, archive, "admin", "")
	if head.Code != http.StatusOK || head.Header().Get("Content-Disposition") == "" ||
		head.Header().Get("Content-Length") != "" || opens.Load() != before {
		t.Errorf("archive HEAD: %d %v, %d blobs opened",
			head.Code, head.Header(), opens.Load()-before)
	}
	if get := w.do(http.MethodGet, archive, "admin", ""); get.Code != http.StatusOK ||
		opens.Load() == before {
		t.Errorf("archive GET: %d, %d blobs opened", get.Code, opens.Load()-before)
	}

	var body strings.Builder
	mw := multipart.NewWriter(&body)
	payload, _ := mw.CreateFormField("payload")
	_, _ = io.WriteString(payload, `{"slug":"demo","version":"1.0.0"}`)
	manifest, _ := mw.CreateFormFile("files[]", "SKILL.md")
	_, _ = io.WriteString(manifest, "---\nname: Demo\ndescription: A demo\n---\n# Demo\n")
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do("POST", "/artifactory/api/skills/skills-local/api/v1/skills", "admin",
		body.String(), "Content-Type", mw.FormDataContentType()), 201)
	bundle := w.item("skills-local", false, skillsfmt.Path("demo", "1.0.0"))
	download := "/artifactory/api/skills/skills-local/api/v1/download?slug=demo&version=1.0.0"
	downloads := func() int64 {
		t.Helper()
		stats, err := w.db.Items().Stats(ctx, bundle.ID)
		if err != nil {
			t.Fatal(err)
		}
		return stats.DownloadCount
	}
	head = w.do(http.MethodHead, download, "admin", "")
	if head.Code != http.StatusOK ||
		head.Header().Get("Content-Length") != strconv.FormatInt(bundle.Size, 10) ||
		downloads() != 0 {
		t.Errorf("skill HEAD: %d %v, %d downloads", head.Code, head.Header(), downloads())
	}
	if get := w.do(
		http.MethodGet,
		download,
		"admin",
		"",
	); get.Code != http.StatusOK ||
		downloads() != 1 {
		t.Errorf("skill GET: %d, %d downloads", get.Code, downloads())
	}
}
