package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func testHandler() http.Handler {
	return New(
		Config{
			BaseURL:       "https://cfg.example.com",
			CompatVersion: "7.155.0",
			Revision:      "v0.1.0-3-gabc",
		},
	)
}

func do(
	t *testing.T,
	h http.Handler,
	method, path string,
	headers map[string]string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return support.Do(t, h, method, path, headers, body)
}

func decode(t *testing.T, body string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

// TestAccSystemLicense reads the license as an administrator, the only caller
// it answers.
func TestAccSystemLicense(t *testing.T) {
	rec := do(t, statusFixture(t, false), http.MethodGet, "/artifactory/api/system/license",
		basic("admin", "pw"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	want := `{"type":"artifactweave","validThrough":"never","licensedTo":"artifactweave"}`
	if diff := cmp.Diff(decode(t, want), decode(t, rec.Body.String())); diff != "" {
		t.Errorf("body (-want +got):\n%s", diff)
	}
}

func TestAccSystemEndpoints(t *testing.T) {
	h := testHandler()
	cases := []struct {
		name        string
		method      string
		path        string
		headers     map[string]string
		status      int
		contentType string
		text        string
		jsonBody    string
	}{
		{
			"artifactory ping",
			"GET",
			"/artifactory/api/system/ping",
			nil,
			200,
			"text/plain; charset=utf-8",
			"OK",
			"",
		},
		{
			"access ping",
			"GET",
			"/access/api/v1/system/ping",
			nil,
			200,
			"text/plain; charset=utf-8",
			"OK",
			"",
		},
		{
			"version",
			"GET",
			"/artifactory/api/system/version",
			nil,
			200,
			"application/json",
			"",
			`{"version":"7.155.0","revision":"v0.1.0-3-gabc","addons":[],"license":"artifactweave"}`,
		},
		{
			"version vendor accept",
			"GET",
			"/artifactory/api/system/version",
			map[string]string{"Accept": versionMediaType},
			200,
			versionMediaType,
			"",
			`{"version":"7.155.0","revision":"v0.1.0-3-gabc","addons":[],"license":"artifactweave"}`,
		},
		{
			"readiness",
			"GET",
			"/artifactory/api/v1/system/readiness",
			nil,
			200,
			"application/json",
			"",
			`{"code":"OK"}`,
		},
		{
			"liveness",
			"GET",
			"/artifactory/api/v1/system/liveness",
			nil,
			200,
			"application/json",
			"",
			`{"code":"OK"}`,
		},
		{
			"access version",
			"GET",
			"/access/api/v1/system/version",
			nil,
			200,
			"application/json",
			"",
			`{"name":"7.155.0","version":"7.155.0"}`,
		},
		{
			"access readiness",
			"GET",
			"/access/api/v1/system/readiness",
			nil,
			200,
			"application/json",
			"",
			`{"code":"OK"}`,
		},
		{
			"access liveness",
			"GET",
			"/access/api/v1/system/liveness",
			nil,
			200,
			"application/json",
			"",
			`{"code":"OK"}`,
		},
		{
			"router readiness",
			"GET",
			"/router/api/v1/system/readiness",
			nil,
			200,
			"application/json",
			"",
			`{"code":"OK"}`,
		},
		{
			"router liveness",
			"GET",
			"/router/api/v1/system/liveness",
			nil,
			200,
			"application/json",
			"",
			`{"code":"OK"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.path, tc.headers, "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != tc.contentType {
				t.Fatalf("content type %q, want %q", ct, tc.contentType)
			}
			if tc.text != "" && rec.Body.String() != tc.text {
				t.Fatalf("body %q, want %q", rec.Body.String(), tc.text)
			}
			if tc.jsonBody != "" {
				if diff := cmp.Diff(
					decode(t, tc.jsonBody),
					decode(t, rec.Body.String()),
				); diff != "" {
					t.Fatalf("body mismatch (-want +got):\n%s", diff)
				}
			}
			if rec.Header().Get(requestIDHeader) == "" {
				t.Fatal("missing request id header")
			}
		})
	}
}

func TestAccTelemetrySinks(t *testing.T) {
	h := testHandler()
	for _, path := range []string{"/artifactory/api/system/usage", "/jfconnect/api/v1/backoffice/metrics/log"} {
		rec := do(
			t,
			h,
			"POST",
			path,
			map[string]string{"Content-Type": "application/json"},
			`{"productId":"jfrog-cli-go/2.60.0","features":[]}`,
		)
		if rec.Code != 200 || rec.Body.Len() != 0 {
			t.Fatalf("%s: status %d body %q", path, rec.Code, rec.Body.String())
		}
	}
}

// endlessBody is a request body that never ends, as a client that keeps
// sending would make it. It fails the test once more than budget bytes have
// been read, which is how a handler that reads a body to the end shows.
type endlessBody struct {
	t      *testing.T
	budget int64
	read   int64
}

func (b *endlessBody) Read(p []byte) (int, error) {
	if b.read >= b.budget {
		b.t.Errorf("read past the budget of %d bytes", b.budget)
		return 0, errors.New("read past the budget")
	}
	n := int(min(int64(len(p)), b.budget-b.read))
	for i := range n {
		p[i] = 'x'
	}
	b.read += int64(n)
	return n, nil
}

// TestAccTelemetrySinkBodyLimit holds both sinks to sinkBodyLimit: a body at
// the limit is drained and answered 200, and a longer one answers 413
// without being read to the end, whether its length is declared or not.
func TestAccTelemetrySinkBodyLimit(t *testing.T) {
	h := testHandler()
	tooLarge := `{"errors":[{"status":413,"message":"request body exceeds 1048576 bytes"}]}`
	post := func(path string, body io.Reader, length int64) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, body)
		r.Header.Set("Content-Type", "application/json")
		r.ContentLength = length
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	for _, path := range []string{
		"/artifactory/api/system/usage",
		"/jfconnect/api/v1/backoffice/metrics/log",
	} {
		t.Run(path, func(t *testing.T) {
			// At the limit, declared and undeclared: read to the end.
			atLimit := strings.Repeat("x", sinkBodyLimit)
			if rec := post(
				path,
				strings.NewReader(atLimit),
				sinkBodyLimit,
			); rec.Code != http.StatusOK {
				t.Errorf("declared, at the limit: status %d: %s", rec.Code, rec.Body)
			}
			if rec := post(
				path,
				io.MultiReader(strings.NewReader(atLimit)),
				-1,
			); rec.Code != http.StatusOK {
				t.Errorf("undeclared, at the limit: status %d: %s", rec.Code, rec.Body)
			}

			// A declared length over the limit is refused before any read.
			declared := &endlessBody{t: t}
			rec := post(path, declared, sinkBodyLimit+1)
			assertTooLarge(t, "declared", rec, tooLarge)

			// An undeclared body is read one byte past the limit, which is
			// how MaxBytesReader detects the overrun, and no further. The
			// margin only keeps a regression from looping forever.
			undeclared := &endlessBody{t: t, budget: sinkBodyLimit + 64<<10}
			rec = post(path, undeclared, -1)
			assertTooLarge(t, "undeclared", rec, tooLarge)
			if undeclared.read > sinkBodyLimit+1 {
				t.Errorf(
					"undeclared: read %d bytes, want at most %d",
					undeclared.read,
					sinkBodyLimit+1,
				)
			}
		})
	}
}

func assertTooLarge(t *testing.T, name string, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("%s: status %d, want 413: %s", name, rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s: content type %q", name, ct)
	}
	if diff := cmp.Diff(decode(t, want), decode(t, rec.Body.String())); diff != "" {
		t.Errorf("%s: body (-want +got):\n%s", name, diff)
	}
}

func TestAccStubsAndEnvelopes(t *testing.T) {
	h := testHandler()
	cases := []struct {
		name   string
		method string
		path   string
		status int
		body   string
	}{
		{
			"xray 501 in xray envelope",
			"GET",
			"/xray/api/v1/system/version",
			501,
			`{"error":"Xray is not supported by artifactweave"}`,
		},
		{
			"lifecycle 501",
			"POST",
			"/lifecycle/api/v2/release_bundle",
			501,
			`{"errors":[{"status":501,"message":"Release Lifecycle Management is not supported by artifactweave"}]}`,
		},
		{
			"apptrust 501",
			"GET",
			"/apptrust/api/v1/applications",
			501,
			`{"errors":[{"status":501,"message":"AppTrust is not supported by artifactweave"}]}`,
		},
		{
			"evidence 501",
			"GET",
			"/evidence/api/v1/x",
			501,
			`{"errors":[{"status":501,"message":"Evidence is not supported by artifactweave"}]}`,
		},
		{
			"worker specified, unimplemented",
			"GET",
			"/worker/api/v1/workers",
			501,
			`{"errors":[{"code":"NOT_IMPLEMENTED","message":"not implemented by artifactweave yet"}]}`,
		},
		{
			"federation specified, unimplemented",
			"POST",
			"/artifactory/api/federation/fullSync/x",
			501,
			`{"errors":[{"status":501,"message":"not implemented by artifactweave yet"}]}`,
		},
		{
			"distribution specified, unimplemented",
			"GET",
			"/distribution/api/v1/release_bundle",
			501,
			`{"status_code":501,"message":"not implemented by artifactweave yet","errors":[{"status":501,"message":"not implemented by artifactweave yet"}]}`,
		},
		{
			"mc 404",
			"GET",
			"/mc/api/v1/jpds",
			404,
			`{"errors":[{"status":404,"message":"not found: GET /mc/api/v1/jpds"}]}`,
		},
		{
			"grid specified, unimplemented",
			"GET",
			"/grid/api/v1/topology",
			501,
			`{"code":"NOT_IMPLEMENTED","message":"not implemented by artifactweave yet"}`,
		},
		{
			"distribution unknown path",
			"GET",
			"/distribution/api/v9/nothing",
			404,
			`{"status_code":404,"message":"not found: GET /distribution/api/v9/nothing","errors":[{"status":404,"message":"not found: GET /distribution/api/v9/nothing"}]}`,
		},
		// /pfed carries the status as an integer code; /grid keeps its
		// symbolic one.
		{
			"pfed unknown path",
			"GET",
			"/pfed/api/v9/nothing",
			404,
			`{"code":404,"message":"not found: GET /pfed/api/v9/nothing"}`,
		},
		{
			"pfed specified, unimplemented",
			"GET",
			"/pfed/api/v1/federations",
			501,
			`{"code":501,"message":"not implemented by artifactweave yet"}`,
		},
		// Import and export put the message in error, with no top-level
		// message; retention keeps both.
		{
			"import specified, unimplemented",
			"POST",
			"/artifactory/api/import/system",
			501,
			`{"errors":[{"status":501,"message":"not implemented by artifactweave yet"}],"error":"not implemented by artifactweave yet"}`,
		},
		{
			"export unknown path",
			"GET",
			"/artifactory/api/export/nothing",
			404,
			`{"errors":[{"status":404,"message":"not found: GET /artifactory/api/export/nothing"}],"error":"not found: GET /artifactory/api/export/nothing"}`,
		},
		{
			"retention unknown path",
			"GET",
			"/artifactory/api/retention/nothing",
			404,
			`{"errors":[{"status":404,"message":"not found: GET /artifactory/api/retention/nothing"}],"error":"Not Found","message":"not found: GET /artifactory/api/retention/nothing"}`,
		},
		{
			"artifactory 404",
			"GET",
			"/artifactory/api/nonexistent/nope",
			404,
			`{"errors":[{"status":404,"message":"not found: GET /artifactory/api/nonexistent/nope"}]}`,
		},
		{
			"access 404",
			"GET",
			"/access/api/v9/nothing",
			404,
			`{"errors":[{"code":"NOT_FOUND","message":"not found: GET /access/api/v9/nothing"}]}`,
		},
		// A path that is no registry operation; this router has no store
		// to resolve a repository in.
		{
			"oci 404",
			"GET",
			"/v2/nope/nothing",
			404,
			`{"errors":[{"code":"UNSUPPORTED","message":"not a registry operation: GET /v2/nope/nothing"}]}`,
		},
		{
			"docker api 404",
			"GET",
			"/artifactory/api/docker/nope/v2/img/nothing",
			404,
			`{"errors":[{"code":"UNSUPPORTED","message":"not a registry operation: GET /artifactory/api/docker/nope/v2/img/nothing"}]}`,
		},
		// Without a repository key, which the npm registry's routes need.
		{
			"npm 404",
			"GET",
			"/artifactory/api/npm/",
			404,
			`{"error":"not found: GET /artifactory/api/npm/"}`,
		},
		// Terraform's 404 is p8Paths'.
		{
			"root 404",
			"GET",
			"/nothing",
			404,
			`{"errors":[{"status":404,"message":"not found: GET /nothing"}]}`,
		},
		{
			"method not allowed",
			"DELETE",
			"/artifactory/api/system/ping",
			405,
			`{"errors":[{"status":405,"message":"method not allowed: DELETE /artifactory/api/system/ping"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.path, nil, "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if diff := cmp.Diff(decode(t, tc.body), decode(t, rec.Body.String())); diff != "" {
				t.Fatalf("body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAccTextSurfaces404(t *testing.T) {
	h := testHandler()
	// Go's paths are the router kit's, which answers 501 without a store;
	// its checksum database is 404 before any lookup. Go's is the only text
	// surface: PyPI's 404 is p8Paths', in Artifactory's envelope.
	path := "/artifactory/api/go/nope/sumdb/sum.golang.org/supported"
	rec := do(t, h, "GET", path, nil, "")
	if rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("%s: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
	}
}

// p8Paths are a path of each P8 protocol, under a repository that does not
// exist, and the catch-all's answer to it while the protocol is not
// mounted: a 404 in the envelope surfaceFor gives the protocol's clients.
// Once the format's pull request adds the protocol to protocols, its router
// kit answers instead, with a 404 in the protocol's own envelope, and the
// row checks that. So no format's pull request has to change a row here.
var p8Paths = []struct {
	protocol, path string
	// contentType and body are the catch-all's.
	contentType, body string
}{
	{
		"pypi",
		"/artifactory/api/pypi/nope/simple/x/",
		"application/json",
		`{"errors":[{"status":404,"message":"not found: GET /artifactory/api/pypi/nope/simple/x/"}]}`,
	},
	{
		"nuget/v3",
		"/artifactory/api/nuget/v3/nope/index.json",
		"application/json",
		`{"errors":[{"status":404,"message":"not found: GET /artifactory/api/nuget/v3/nope/index.json"}]}`,
	},
	{
		"helm",
		"/artifactory/api/helm/nope/index.yaml",
		"application/json",
		`{"errors":[{"status":404,"message":"not found: GET /artifactory/api/helm/nope/index.yaml"}]}`,
	},
	{
		"terraform", "/artifactory/api/terraform/v1/providers/x/y/versions", "application/json",
		`{"errors":["not found: GET /artifactory/api/terraform/v1/providers/x/y/versions"]}`,
	},
	// Go's protocol is mounted, so its row is what each row above becomes.
	{
		"go",
		"/artifactory/api/go/nope/example.com/m/@v/list",
		"text/plain; charset=utf-8",
		"not found: GET /artifactory/api/go/nope/example.com/m/@v/list\n",
	},
}

// mounted returns the built-in protocol named name, if it is mounted.
func mounted(name string) (pkgroute.Protocol, bool) {
	for _, p := range protocols {
		if proto := p(Config{}); proto.Name == name {
			return proto, true
		}
	}
	return pkgroute.Protocol{}, false
}

// surfaceType is the Content-Type an error in surface s is written with.
func surfaceType(s apierror.Surface) string {
	rec := httptest.NewRecorder()
	apierror.Write(rec, s, apierror.New(http.StatusNotFound, "probe"))
	return rec.Header().Get("Content-Type")
}

func TestAccP8ProtocolPaths(t *testing.T) {
	// A store with no repositories, so a mounted protocol finds none.
	h := New(Config{CompatVersion: "7.155.0", Store: inmem.New()})
	for _, c := range p8Paths {
		t.Run(c.protocol, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, c.path, nil, "")
			p, ok := mounted(c.protocol)
			want := c.contentType
			if ok {
				want = surfaceType(p.Surface)
			}
			if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != want {
				t.Fatalf("mounted %v: %d %q, want 404 %q: %s",
					ok, rec.Code, rec.Header().Get("Content-Type"), want, rec.Body)
			}
			switch {
			case ok:
			case c.contentType == "application/json":
				if diff := cmp.Diff(decode(t, c.body), decode(t, rec.Body.String())); diff != "" {
					t.Fatalf("body (-want +got):\n%s", diff)
				}
			case rec.Body.String() != c.body:
				t.Fatalf("body %q, want %q", rec.Body, c.body)
			}
		})
	}
}

func TestAccNativeComposerRoutesAvoidHandwrittenStubs(t *testing.T) {
	h := New(Config{CompatVersion: "7.155.0", Store: inmem.New()})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/artifactory/api/composer/missing/packages.json"},
		{http.MethodGet, "/artifactory/api/composer/missing/p2/acme/widget.json"},
		{http.MethodGet, "/artifactory/api/composer/missing/dist/acme/widget/1.0.0.zip"},
		{http.MethodPost, "/artifactory/api/composer/missing/reindex"},
	} {
		rec := do(t, h, tc.method, tc.path, nil, "")
		if rec.Code != http.StatusNotFound ||
			!strings.Contains(rec.Body.String(), "Repository 'missing' does not exist") {
			t.Errorf("native route %s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
	rec := do(t, h, http.MethodGet, "/artifactory/api/system/configuration", nil, "")
	if rec.Code != http.StatusNotImplemented ||
		!strings.Contains(rec.Body.String(), "specified but not implemented yet") {
		t.Fatalf("untagged handwritten stub: %d %s", rec.Code, rec.Body)
	}
}

func TestAccNewUsesConfiguredLogger(t *testing.T) {
	var buf bytes.Buffer
	h := New(Config{
		CompatVersion: "7.155.0",
		Logger:        slog.New(slog.NewJSONHandler(&buf, nil)),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifactory/api/system/ping", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(buf.String(), `"path":"/artifactory/api/system/ping"`) {
		t.Fatalf("access log did not go to the configured logger: %q", buf.String())
	}
}
