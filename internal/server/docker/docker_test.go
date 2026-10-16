package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		rest string
		want target
		ok   bool
	}{
		{"", target{kind: KindBase}, true},
		{"_catalog", target{kind: KindCatalog}, true},
		{"token", target{kind: KindToken}, true},
		{"r/_catalog", target{kind: kindRepoCatalog, image: "r"}, true},
		{"r/img/blobs/sha256:ab", target{kind: KindBlob, image: "r/img", ref: "sha256:ab"}, true},
		{"r/img/blobs/uploads/", target{kind: KindUploads, image: "r/img"}, true},
		{"r/img/blobs/uploads", target{kind: KindUploads, image: "r/img"}, true},
		{"r/img/blobs/uploads/abc", target{kind: KindUpload, image: "r/img", ref: "abc"}, true},
		{"r/img/manifests/latest", target{kind: KindManifest, image: "r/img", ref: "latest"}, true},
		{"r/img/tags/list", target{kind: KindTags, image: "r/img"}, true},
		{
			"r/img/referrers/sha256:ab",
			target{kind: KindReferrers, image: "r/img", ref: "sha256:ab"},
			true,
		},
		// Names may contain the reserved words; the operation is matched
		// from the end.
		{
			"r/team/manifests/app/manifests/v1",
			target{kind: KindManifest, image: "r/team/manifests/app", ref: "v1"},
			true,
		},
		{
			"r/library/blobs/blobs/sha256:ab",
			target{kind: KindBlob, image: "r/library/blobs", ref: "sha256:ab"},
			true,
		},
		{
			"r/a/blobs/uploads/blobs/uploads/",
			target{kind: KindUploads, image: "r/a/blobs/uploads"},
			true,
		},
		{"r/tags/list/tags/list", target{kind: KindTags, image: "r/tags/list"}, true},
		{"r/x/referrers/tags/list", target{kind: KindTags, image: "r/x/referrers"}, true},
		// The operation alone parses with an empty image, which the router
		// refuses as an invalid name.
		{"manifests/latest", target{kind: KindManifest, ref: "latest"}, true},
		{"blobs/uploads/", target{kind: KindUploads}, true},
		// A missing reference, an unknown operation or a lone segment is no
		// registry operation.
		{"r/img/manifests/", target{}, false},
		{"r/img/blobs/", target{}, false},
		{"r/img/referrers/", target{}, false},
		{"r/img", target{}, false},
		{"r/img/other/x", target{}, false},
		{"auth", target{}, false},
		{"_catalog/x", target{}, false},
		{"/_catalog", target{}, false},
	}
	for _, c := range cases {
		got, ok := parseTarget(c.rest)
		if ok != c.ok || got != c.want {
			t.Errorf("parseTarget(%q) = %+v, %v; want %+v, %v", c.rest, got, ok, c.want, c.ok)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{
		"a", "app", "team/app", "a.b", "a_b", "a__b", "a-b", "a---b", "library/blobs/manifests",
		"0/1/2",
	} {
		if !ValidName(n) {
			t.Errorf("%q should be valid", n)
		}
	}
	for _, n := range []string{
		"", "App", "a/", "/a", "a//b", "_a", "a_", "a___b", ".a", "a..b", "a/-b", "a b", "a:b",
	} {
		if ValidName(n) {
			t.Errorf("%q should be invalid", n)
		}
	}
}

func TestIsRegistryPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/v2":                           true,
		"/v2/":                          true,
		"/v2/r/img/manifests/x":         true,
		"/v2x":                          false,
		"/v22/x":                        false,
		"/artifactory/api/docker/r/v2":  true,
		"/artifactory/api/docker/r/v2/": true,
		"/artifactory/api/docker/r/v2/img/tags/list": true,
		"/artifactory/api/docker/r/v1/auth":          false,
		"/artifactory/api/docker/r":                  false,
		"/artifactory/api/docker/r/v22":              false,
		"/artifactory/api/repositories":              false,
		// The JFrog operations beside the registry are not registry paths.
		"/artifactory/api/docker/r/v2/promote":         false,
		"/artifactory/api/docker/r/v2/parentManifests": false,
		"/artifactory/api/docker/r/v2/delete":          false,
		"/artifactory/api/docker/r/v2/app/promote":     true,
		"/v2/promote": true,
	} {
		if got := IsRegistryPath(path); got != want {
			t.Errorf("IsRegistryPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestChallengesAnonymous(t *testing.T) {
	for path, want := range map[string]bool{
		"/v2":                                   true,
		"/v2/":                                  true,
		"/v2/_catalog":                          true,
		"/v2/r/img/manifests/x":                 true,
		"/v2/r/token/manifests/x":               true,
		"/artifactory/api/docker/r/v2/":         true,
		"/artifactory/api/docker/r/v2/auth":     true,
		"/v2/token":                             false,
		"/artifactory/api/docker/r/v2/token":    false,
		"/artifactory/api/docker/null/v2/token": false,
		"/artifactory/api/docker/r/v1/auth":     false,
		"/artifactory/api/system/ping":          false,
		"/artifactory/api/docker/r/v2/promote":  false,
	} {
		if got := ChallengesAnonymous(path); got != want {
			t.Errorf("ChallengesAnonymous(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestAPIVersionHeader(t *testing.T) {
	h := APIVersion(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	for path, want := range map[string]string{
		"/v2/":                          apiVersion,
		"/artifactory/api/docker/r/v2/": apiVersion,
		"/artifactory/api/system/ping":  "",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get(APIVersionHeader); got != want {
			t.Errorf("%s: %s = %q, want %q", path, APIVersionHeader, got, want)
		}
	}
}

// fixture is a store with one repository of each kind the router tells
// apart.
func fixture(t *testing.T) store.Store {
	t.Helper()
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
		{Key: "Mixed-Case", Class: repo.Local, PackageType: repo.Docker},
		{Key: "oci-local", Class: repo.Local, PackageType: repo.OCI},
		{Key: "helm-oci", Class: repo.Local, PackageType: repo.HelmOCI},
		{Key: "fed-docker", Class: repo.Federated, PackageType: repo.Docker},
		{Key: "generic-local", Class: repo.Local, PackageType: repo.Generic},
		{
			Key: "docker-remote", Class: repo.Remote, PackageType: repo.Docker,
			URL: "https://registry-1.docker.io",
		},
		{
			Key: "docker-virtual", Class: repo.Virtual, PackageType: repo.Docker,
			Repositories: []string{"docker-local"},
		},
	} {
		if err := db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// router mounts g on a chi router, as the server does.
func router(g *Registry) http.Handler {
	r := chi.NewRouter()
	g.Register(r)
	return r
}

// echo is an endpoint that answers with what it was handed.
func echo(w http.ResponseWriter, _ *http.Request, q Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"kind": int(q.Kind), "repoKey": q.RepoKey, "repo": q.Repo.Key, "name": q.Name,
		"ref": q.Reference, "path": q.Path("blobs", "uploads", "id"),
	})
}

// echoing is a registry with echo registered for every kind and method.
func echoing(o Options) *Registry {
	g := New(o)
	for k, ms := range methods {
		for _, m := range ms {
			g.handle(k, m, echo)
		}
	}
	return g
}

func serve(
	t *testing.T,
	h http.Handler,
	method, target string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRoutingHandsEndpointsTheRequest(t *testing.T) {
	h := router(echoing(Options{
		Store: fixture(
			t,
		),
		SubdomainMatch: true,
		BaseURL:        "https://reg.example.com:8443/artifactory",
	}))
	const prefixed = "/artifactory/api/docker/docker-local/v2"
	cases := []struct {
		name, method, target, host string
		want                       map[string]any
	}{
		{
			"repository path",
			"GET",
			"/v2/docker-local/team/app/manifests/v1",
			"",
			map[string]any{
				"kind":    KindManifest,
				"repoKey": "docker-local",
				"repo":    "docker-local",
				"name":    "team/app",
				"ref":     "v1",
				"path":    "/v2/docker-local/team/app/blobs/uploads/id",
			},
		},
		{
			"key in any case", "PATCH", "/v2/mixed-case/app/blobs/uploads/s1", "",
			map[string]any{
				"kind": KindUpload, "repoKey": "mixed-case", "repo": "Mixed-Case", "name": "app",
				"ref": "s1", "path": "/v2/mixed-case/app/blobs/uploads/id",
			},
		},
		{
			"artifactory prefix", "POST", prefixed + "/team/app/blobs/uploads/", "",
			map[string]any{
				"kind": KindUploads, "repoKey": "docker-local", "repo": "docker-local",
				"name": "team/app", "ref": "", "path": prefixed + "/team/app/blobs/uploads/id",
			},
		},
		{
			"subdomain",
			"HEAD",
			"/v2/team/app/blobs/sha256:ab",
			"docker-local.reg.example.com:8443",
			map[string]any{
				"kind": KindBlob, "repoKey": "docker-local", "repo": "docker-local",
				"name": "team/app", "ref": "sha256:ab", "path": "/v2/team/app/blobs/uploads/id",
			},
		},
		{
			// On a repository's subdomain the first segment is the image's,
			// even when it is another repository's key.
			"subdomain shadows keys",
			"GET",
			"/v2/oci-local/app/tags/list",
			"docker-local.reg.example.com",
			map[string]any{
				"kind": KindTags, "repoKey": "docker-local", "repo": "docker-local",
				"name": "oci-local/app", "ref": "", "path": "/v2/oci-local/app/blobs/uploads/id",
			},
		},
		{
			"oci", "GET", "/v2/oci-local/app/referrers/sha256:ab", "",
			map[string]any{
				"kind": KindReferrers, "repoKey": "oci-local", "repo": "oci-local", "name": "app",
				"ref": "sha256:ab", "path": "/v2/oci-local/app/blobs/uploads/id",
			},
		},
		{
			"helm oci", "PUT", "/v2/helm-oci/charts/c/manifests/1.0.0", "",
			map[string]any{
				"kind": KindManifest, "repoKey": "helm-oci", "repo": "helm-oci", "name": "charts/c",
				"ref": "1.0.0", "path": "/v2/helm-oci/charts/c/blobs/uploads/id",
			},
		},
		{
			"federated", "DELETE", "/v2/fed-docker/app/manifests/sha256:ab", "",
			map[string]any{
				"kind": KindManifest, "repoKey": "fed-docker", "repo": "fed-docker", "name": "app",
				"ref": "sha256:ab", "path": "/v2/fed-docker/app/blobs/uploads/id",
			},
		},
		{
			"probe",
			"GET",
			"/v2/",
			"",
			map[string]any{
				"kind":    KindBase,
				"repoKey": "",
				"repo":    "",
				"name":    "",
				"ref":     "",
				"path":    "//blobs/uploads/id",
			},
		},
		{
			"probe without slash",
			"GET",
			"/v2",
			"",
			map[string]any{
				"kind":    KindBase,
				"repoKey": "",
				"repo":    "",
				"name":    "",
				"ref":     "",
				"path":    "//blobs/uploads/id",
			},
		},
		{
			"probe by prefix", "GET", prefixed, "",
			map[string]any{
				"kind": KindBase, "repoKey": "docker-local", "repo": "", "name": "", "ref": "",
				"path": prefixed + "//blobs/uploads/id",
			},
		},
		{
			// The probe and the token answer for any repository, a remote
			// or virtual one included (decision 0003).
			"token for a virtual", "GET", "/artifactory/api/docker/docker-virtual/v2/token", "",
			map[string]any{
				"kind": KindToken, "repoKey": "docker-virtual", "repo": "", "name": "", "ref": "",
				"path": "/artifactory/api/docker/docker-virtual/v2//blobs/uploads/id",
			},
		},
		{
			"token with null", "GET", "/artifactory/api/docker/null/v2/token", "",
			map[string]any{
				"kind": KindToken, "repoKey": "null", "repo": "", "name": "", "ref": "",
				"path": "/artifactory/api/docker/null/v2//blobs/uploads/id",
			},
		},
		{
			"token on the root",
			"GET",
			"/v2/token",
			"",
			map[string]any{
				"kind":    KindToken,
				"repoKey": "",
				"repo":    "",
				"name":    "",
				"ref":     "",
				"path":    "//blobs/uploads/id",
			},
		},
		{
			"token on a subdomain", "GET", "/v2/token", "docker-remote.reg.example.com",
			map[string]any{
				"kind": KindToken, "repoKey": "docker-remote", "repo": "docker-remote", "name": "",
				"ref": "", "path": "/v2//blobs/uploads/id",
			},
		},
		{
			"auth", "GET", "/artifactory/api/docker/docker-local/v2/auth", "",
			map[string]any{
				"kind": KindAuth, "repoKey": "docker-local", "repo": "", "name": "", "ref": "",
				"path": prefixed + "//blobs/uploads/id",
			},
		},
		{
			"registry catalog",
			"GET",
			"/v2/_catalog",
			"",
			map[string]any{
				"kind":    KindCatalog,
				"repoKey": "",
				"repo":    "",
				"name":    "",
				"ref":     "",
				"path":    "//blobs/uploads/id",
			},
		},
		{
			"repository catalog", "GET", "/v2/docker-local/_catalog", "",
			map[string]any{
				"kind": KindCatalog, "repoKey": "docker-local", "repo": "docker-local", "name": "",
				"ref": "", "path": "//blobs/uploads/id",
			},
		},
		{
			"subdomain catalog",
			"GET",
			"/v2/_catalog",
			"oci-local.reg.example.com",
			map[string]any{
				"kind":    KindCatalog,
				"repoKey": "oci-local",
				"repo":    "oci-local",
				"name":    "",
				"ref":     "",
				"path":    "/v2//blobs/uploads/id",
			},
		},
		{
			// A host label that names no registry repository leaves the
			// request on the repository path.
			"label of another type",
			"GET",
			"/v2/docker-local/app/tags/list",
			"generic-local.reg.example.com",
			map[string]any{
				"kind": KindTags, "repoKey": "docker-local", "repo": "docker-local", "name": "app",
				"ref": "", "path": "/v2/docker-local/app/blobs/uploads/id",
			},
		},
		{
			"the parent", "GET", "/v2/docker-local/app/tags/list", "reg.example.com",
			map[string]any{
				"kind": KindTags, "repoKey": "docker-local", "repo": "docker-local", "name": "app",
				"ref": "", "path": "/v2/docker-local/app/blobs/uploads/id",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			headers := map[string]string{}
			if c.host != "" {
				headers["Host"] = c.host
			}
			rec := serve(t, h, c.method, c.target, headers)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{}
			for k, v := range c.want {
				if kind, ok := v.(Kind); ok {
					v = float64(kind)
				}
				want[k] = v
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestSubdomainFromForwardedHost(t *testing.T) {
	h := router(
		echoing(
			Options{Store: fixture(t), SubdomainMatch: true, BaseURL: "https://Reg.Example.com"},
		),
	)
	rec := serve(t, h, http.MethodGet, "/v2/app/tags/list", map[string]string{
		"Host":             "internal:8081",
		"X-Forwarded-Host": "OCI-LOCAL.reg.example.com:443, proxy.internal",
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"repo":"oci-local"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestRoutingErrors(t *testing.T) {
	h := router(New(Options{
		Store: fixture(t), SubdomainMatch: true, BaseURL: "https://reg.example.com",
	}))
	cases := []struct {
		name, method, target, host string
		status                     int
		code, message, allow       string
	}{
		{
			name:    "unknown repository",
			method:  "GET",
			target:  "/v2/nope/app/manifests/latest",
			status:  404,
			code:    CodeNameUnknown,
			message: "repository name not known to registry: nope",
		},
		{
			name:    "unknown repository by prefix",
			method:  "GET",
			target:  "/artifactory/api/docker/nope/v2/app/manifests/latest",
			status:  404,
			code:    CodeNameUnknown,
			message: "repository name not known to registry: nope",
		},
		{
			name:    "unknown repository catalog",
			method:  "GET",
			target:  "/v2/nope/_catalog",
			status:  404,
			code:    CodeNameUnknown,
			message: "repository name not known to registry: nope",
		},
		{
			name:    "another package type",
			method:  "GET",
			target:  "/v2/generic-local/app/manifests/latest",
			status:  404,
			code:    CodeNameUnknown,
			message: "repository name not known to registry: generic-local",
		},
		{
			name:    "remote",
			method:  "GET",
			target:  "/v2/docker-remote/library/nginx/manifests/latest",
			status:  405,
			code:    CodeUnsupported,
			message: "docker-remote is a remote repository, which the registry does not serve yet",
		},
		{
			name:    "virtual",
			method:  "PUT",
			target:  "/v2/docker-virtual/app/manifests/v1",
			status:  405,
			code:    CodeUnsupported,
			message: "docker-virtual is a virtual repository, which the registry does not serve yet",
		},
		{
			name: "remote by subdomain", method: "GET", target: "/v2/app/tags/list",
			host: "docker-remote.reg.example.com", status: 405, code: CodeUnsupported,
			message: "docker-remote is a remote repository, which the registry does not serve yet",
		},
		{
			name:    "remote catalog",
			method:  "GET",
			target:  "/v2/docker-virtual/_catalog",
			status:  405,
			code:    CodeUnsupported,
			message: "docker-virtual is a virtual repository, which the registry does not serve yet",
		},
		{
			name: "no image name", method: "GET", target: "/v2/docker-local/manifests/latest",
			status: 400, code: CodeNameInvalid,
			message: `invalid image name "": reference images as <host>/<repository>/<image>`,
		},
		{
			name: "upper-case name", method: "GET", target: "/v2/docker-local/App/manifests/latest",
			status: 400, code: CodeNameInvalid,
			message: `invalid image name "App": reference images as <host>/<repository>/<image>`,
		},
		{
			name: "empty name by prefix", method: "GET",
			target: "/artifactory/api/docker/docker-local/v2/manifests/latest",
			status: 400, code: CodeNameInvalid,
			message: `invalid image name "": reference images as <host>/<repository>/<image>`,
		},
		{
			name:    "not implemented yet",
			method:  "GET",
			target:  "/v2/docker-local/app/manifests/latest",
			status:  405,
			code:    CodeUnsupported,
			message: "GET manifests is not implemented by artifactweave yet",
		},
		{
			name: "referrers not implemented yet", method: "GET",
			target: "/v2/docker-local/app/referrers/" + digestOf("subject"),
			status: 405, code: CodeUnsupported,
			message: "GET the referrers API is not implemented by artifactweave yet",
		},
		{
			name:    "undefined method",
			method:  "PATCH",
			target:  "/v2/docker-local/app/manifests/latest",
			status:  405,
			code:    CodeUnsupported,
			message: "manifests does not answer PATCH",
			allow:   "GET, HEAD, PUT, DELETE",
		},
		{
			name: "token by POST", method: "POST", target: "/v2/token",
			status: 405, code: CodeUnsupported, message: "the token endpoint does not answer POST",
			allow: "GET",
		},
		{
			name: "probe by POST", method: "POST", target: "/v2/",
			status: 405, code: CodeUnsupported, message: "the version check does not answer POST",
			allow: "GET, HEAD",
		},
		{
			// Under the prefix the generated server answers GET _catalog;
			// the registry answers the rest.
			name: "prefix catalog by DELETE", method: "DELETE",
			target: "/artifactory/api/docker/docker-local/v2/_catalog",
			status: 405, code: CodeUnsupported, message: "the catalog does not answer DELETE",
			allow: "GET",
		},
		{
			name:    "no operation",
			method:  "GET",
			target:  "/v2/docker-local/app",
			status:  404,
			code:    CodeUnsupported,
			message: "not a registry operation: GET /v2/docker-local/app",
		},
		{
			name:    "no operation by prefix",
			method:  "GET",
			target:  "/artifactory/api/docker/docker-local/v2/x/y",
			status:  404,
			code:    CodeUnsupported,
			message: "not a registry operation: GET /artifactory/api/docker/docker-local/v2/x/y",
		},
		{
			name:    "repository catalog by prefix",
			method:  "GET",
			target:  "/artifactory/api/docker/docker-local/v2/x/_catalog",
			status:  404,
			code:    CodeUnsupported,
			message: "not a registry operation: GET /artifactory/api/docker/docker-local/v2/x/_catalog",
		},
		{
			name: "repository catalog on a subdomain", method: "GET", target: "/v2/x/_catalog",
			host: "docker-local.reg.example.com", status: 404, code: CodeUnsupported,
			message: "not a registry operation: GET /v2/x/_catalog",
		},
		{
			name: "auth on the root", method: "GET", target: "/v2/auth",
			status: 404, code: CodeUnsupported, message: "not a registry operation: GET /v2/auth",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			headers := map[string]string{}
			if c.host != "" {
				headers["Host"] = c.host
			}
			rec := serve(t, h, c.method, c.target, headers)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			want := map[string]any{
				"errors": []any{map[string]any{"code": c.code, "message": c.message}},
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatal(diff)
			}
			if got := rec.Header().Get("Allow"); got != c.allow {
				t.Fatalf("Allow %q, want %q", got, c.allow)
			}
		})
	}
}

// The generated server answers the JFrog operations beside the registry;
// chi prefers its routes to the registry's catch-all. A method none of
// them serves, or one not specified yet, still reaches the catch-all,
// which answers as the server would, in the Artifactory envelope.
func TestJFrogOperationsUnderThePrefix(t *testing.T) {
	h := router(New(Options{Store: fixture(t)}))
	const base = "/artifactory/api/docker/docker-local/v2/"
	for _, c := range []struct {
		method, op string
		status     int
		message    string
	}{
		{"GET", "promote", 405, "method not allowed: GET " + base + "promote"},
		{"DELETE", "parentManifests", 405, "method not allowed: DELETE " + base + "parentManifests"},
		{"DELETE", "delete", 404, "not found: DELETE " + base + "delete"},
		{"POST", "promote", 404, "not found: POST " + base + "promote"},
	} {
		rec := serve(t, h, c.method, base+c.op, nil)
		want := map[string]any{
			"errors": []any{map[string]any{"status": float64(c.status), "message": c.message}},
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if rec.Code != c.status {
			t.Errorf("%s %s: status %d, want %d", c.method, c.op, rec.Code, c.status)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s %s: %s", c.method, c.op, diff)
		}
	}
}

func TestProbe(t *testing.T) {
	h := router(New(Options{}))
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		rec := serve(t, h, m, "/v2/", nil)
		want := "{}"
		if m == http.MethodHead {
			want = ""
		}
		if rec.Code != http.StatusOK || rec.Body.String() != want ||
			rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s: %d %q %v", m, rec.Code, rec.Body, rec.Header())
		}
	}
}

// Without a store nothing resolves, and an operation on a repository
// answers as unimplemented, as the server's handlers do: the server's own
// tests build routers without one.
func TestNoStore(t *testing.T) {
	h := router(New(Options{}))
	for _, target := range []string{
		"/v2/r/app/manifests/x",
		"/v2/r/_catalog",
		"/artifactory/api/docker/r/v2/_catalog",
		"/artifactory/api/docker/r/v2/app/tags/list",
	} {
		rec := serve(t, h, http.MethodGet, target, nil)
		if rec.Code != http.StatusNotImplemented ||
			!strings.Contains(rec.Body.String(), CodeUnsupported) {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Body)
		}
	}
}

// Subdomain matching without a base URL has no parent, so it never matches.
func TestSubdomainWithoutParent(t *testing.T) {
	h := router(echoing(Options{Store: fixture(t), SubdomainMatch: true}))
	rec := serve(t, h, http.MethodGet, "/v2/docker-local/app/tags/list",
		map[string]string{"Host": "oci-local.reg.example.com"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"repo":"docker-local"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestBaseHost(t *testing.T) {
	for base, want := range map[string]string{
		"https://Reg.Example.com:8443/artifactory": "reg.example.com",
		"http://reg":  "reg",
		"http://[::1": "",
		"":            "",
	} {
		if got := baseHost(base); got != want {
			t.Errorf("baseHost(%q) = %q, want %q", base, got, want)
		}
	}
}

// faultyRepos fails Get, or List, with a store error.
type faultyRepos struct {
	store.RepoStore
	get, list bool
}

var errBoom = errors.New("boom")

func (f faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if f.get {
		return repo.Config{}, errBoom
	}
	return f.RepoStore.Get(ctx, key) //nolint:wrapcheck // test double
}

func (f faultyRepos) List(ctx context.Context, filter store.RepoFilter) ([]repo.Config, error) {
	if f.list {
		return nil, errBoom
	}
	return f.RepoStore.List(ctx, filter) //nolint:wrapcheck // test double
}

type faultyStore struct {
	store.Store
	repos faultyRepos
}

func (f faultyStore) Repos() store.RepoStore { return f.repos }

func TestStoreFailures(t *testing.T) {
	db := fixture(t)
	for _, c := range []struct {
		name, target, host string
		repos              faultyRepos
	}{
		{"get", "/v2/docker-local/app/manifests/x", "", faultyRepos{get: true}},
		{"list", "/v2/DOCKER-LOCAL/app/manifests/x", "", faultyRepos{list: true}},
		{"subdomain", "/v2/app/manifests/x", "docker-local.reg.example.com", faultyRepos{get: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.repos.RepoStore = db.Repos()
			var noted error
			h := router(New(Options{
				Store: faultyStore{Store: db, repos: c.repos}, SubdomainMatch: true,
				BaseURL: "https://reg.example.com",
				Fault:   func(_ *http.Request, err error) { noted = err },
			}))
			rec := serve(t, h, http.MethodGet, c.target, map[string]string{"Host": c.host})
			if rec.Code != http.StatusInternalServerError || !errors.Is(noted, errBoom) {
				t.Fatalf("%d %s, noted %v", rec.Code, rec.Body, noted)
			}
			if strings.Contains(rec.Body.String(), "boom") {
				t.Fatalf("the cause reached the client: %s", rec.Body)
			}
		})
	}
	// Without Fault the error is still answered.
	h := router(
		New(
			Options{
				Store: faultyStore{Store: db, repos: faultyRepos{RepoStore: db.Repos(), get: true}},
			},
		),
	)
	if rec := serve(
		t,
		h,
		http.MethodGet,
		"/v2/docker-local/app/manifests/x",
		nil,
	); rec.Code != 500 {
		t.Fatalf("%d", rec.Code)
	}
}

func TestRequire(t *testing.T) {
	ctx := context.Background()
	db := fixture(t)
	if err := db.Permissions().Create(ctx, types.Permission{
		Name: "readers",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"reader": types.ActionRead, auth.AnonymousUser: types.ActionRead,
			}},
			Targets: map[string]types.Target{"docker-local": {Include: []string{"pub/**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	c, err := db.Repos().Get(ctx, "docker-local")
	if err != nil {
		t.Fatal(err)
	}
	q := Request{Repo: c, Name: "pub/app"}
	as := func(p auth.Principal, anonymous bool) *http.Request {
		p, err := auth.Authorize(ctx, authz.Sources{
			Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: anonymous,
		}, p)
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewRequest(http.MethodGet, "/", nil).WithContext(auth.WithPrincipal(ctx, p))
	}
	reader := as(auth.Principal{Name: "reader"}, false)
	nobody := as(auth.Principal{Name: "nobody"}, false)
	anonOn := as(auth.Anonymous, true)
	anonOff := as(auth.Anonymous, false)
	cases := []struct {
		name string
		r    *http.Request
		path string
		want types.Actions
		code string
	}{
		{"no principal", httptest.NewRequest(http.MethodGet, "/", nil), "x", types.ActionWrite, ""},
		{"reader reads", reader, "pub/app/v1/manifest.json", types.ActionRead, ""},
		{"reader cannot write", reader, "pub/app/v1/manifest.json", types.ActionWrite, CodeDenied},
		{"reader outside the pattern", reader, "priv/app", types.ActionRead, CodeDenied},
		{"nobody", nobody, "pub/app", types.ActionRead, CodeDenied},
		{"anonymous reads", anonOn, "pub/app", types.ActionRead, ""},
		{"anonymous cannot write", anonOn, "pub/app", types.ActionWrite, CodeUnauthorized},
		{"anonymous while off", anonOff, "pub/app", types.ActionRead, CodeUnauthorized},
	}
	for _, c := range cases {
		err := q.Require(c.r, c.path, c.want)
		code := ""
		if err != nil {
			rec := httptest.NewRecorder()
			Write(rec, err)
			var body struct{ Errors []struct{ Code string } }
			if jerr := json.Unmarshal(
				rec.Body.Bytes(),
				&body,
			); jerr != nil ||
				len(body.Errors) != 1 {
				t.Fatalf("%s: %s", c.name, rec.Body)
			}
			code = body.Errors[0].Code
			if want := map[string]int{CodeDenied: 403, CodeUnauthorized: 401}[code]; rec.Code != want {
				t.Fatalf("%s: status %d for %s", c.name, rec.Code, code)
			}
		}
		if code != c.code {
			t.Errorf("%s: code %q, want %q", c.name, code, c.code)
		}
	}
}

func TestKindStrings(t *testing.T) {
	var seen []string
	for k := KindBase; k <= kindRepoCatalog; k++ {
		s := k.String()
		if s == "unknown" {
			t.Errorf("kind %d has no name", k)
		}
		seen = append(seen, s)
	}
	if Kind(0).String() != "unknown" {
		t.Fatal("the zero kind has a name")
	}
	if !slices.Contains(seen, "manifests") {
		t.Fatalf("names: %v", seen)
	}
}

func TestServes(t *testing.T) {
	for _, pt := range []repo.PackageType{repo.Docker, repo.OCI, repo.HelmOCI} {
		if !Serves(pt) {
			t.Errorf("%s should be served", pt)
		}
	}
	for _, pt := range []repo.PackageType{repo.Helm, repo.Generic, repo.NPM} {
		if Serves(pt) {
			t.Errorf("%s should not be served", pt)
		}
	}
}
