package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/server/docker"
)

// The registry's own routing is tested in internal/server/docker; these
// rows and tests cover what the server adds: authentication in front of
// it, the version header on the middleware's answers, the mounting under
// both prefixes and the reverse proxy read (#220).

const (
	registryProbe = "GET /v2/"
	prefixProbe   = "GET /artifactory/api/docker/{repoKey}/v2/"
	webServer     = "GET /artifactory/api/system/configuration/webServer"
)

func registryStatusRows() []statusCase {
	const prefixed = "/artifactory/api/docker/libs-local/v2/"
	return []statusCase{
		{registryProbe, "/v2/", "admin", "", 200},
		{registryProbe, "/v2/", "user", "", 200},
		// Challenged even while anonymous access is on (decision 0031).
		{registryProbe, "/v2/", "anonymous", "", 401},
		{registryProbe, "/v2/", "", "", 401},
		{registryProbe, "/v2/", "expired", "", 401},
		{registryProbe, "/v2/", "forged", "", 401},
		{prefixProbe, prefixed, "admin", "", 200},
		{prefixProbe, prefixed, "anonymous", "", 401},
		{prefixProbe, prefixed, "", "", 401},
		// libs-local is a generic repository, which the registry does not
		// serve.
		{registryProbe, "/v2/libs-local/app/manifests/latest", "admin", "", 404},
		{registryProbe, "/v2/libs-local/app/manifests/latest", "anonymous", "", 401},
		// The token endpoint alone lets an anonymous caller through, for
		// its anonymous token; until #221 it answers that it is not
		// implemented.
		{"GET /v2/token", "/v2/token", "anonymous", "", 405},
		{"GET /v2/token", "/v2/token", "", "", 401},
		{webServer, "/artifactory/api/system/configuration/webServer", "admin", "", 200},
		{webServer, "/artifactory/api/system/configuration/webServer", "user", "", 403},
		{webServer, "/artifactory/api/system/configuration/webServer", "anonymous", "", 401},
		{webServer, "/artifactory/api/system/configuration/webServer", "", "", 401},
	}
}

// registryMatrix: the probe answers any caller with credentials. An
// anonymous one is challenged even while anonymous access is on, and
// fetches an anonymous token instead (decision 0031).
func registryMatrix() []matrixRow {
	return []matrixRow{
		at(registryProbe, "", signedIn, 200),
		row(prefixProbe, "/artifactory/api/docker/libs-local/v2/", "", signedIn, 200),
		at(webServer, "", administrators, 200),
	}
}

// A registry client learns it reached a v2 registry from the probe's
// header, on the 401 the authentication middleware writes as well as on
// the 200, and reads the challenge and the OCI envelope from the 401.
func TestRegistryProbeHeaders(t *testing.T) {
	h := statusFixture(t, false)
	for _, path := range []string{"/v2/", "/artifactory/api/docker/any/v2/"} {
		rec := do(t, h, http.MethodGet, path, nil, "")
		if rec.Code != http.StatusUnauthorized ||
			rec.Header().Get(docker.APIVersionHeader) != "registry/2.0" ||
			rec.Header().Get("WWW-Authenticate") != `Basic realm="Artifactory Realm"` {
			t.Fatalf("%s: %d %v", path, rec.Code, rec.Header())
		}
		var body struct{ Errors []struct{ Code string } }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil ||
			len(body.Errors) != 1 || body.Errors[0].Code != docker.CodeUnauthorized {
			t.Fatalf("%s: %s", path, rec.Body)
		}
		rec = do(t, h, http.MethodGet, path, basic("admin", "pw"), "")
		if rec.Code != http.StatusOK ||
			rec.Header().Get(docker.APIVersionHeader) != "registry/2.0" ||
			rec.Body.String() != "{}" {
			t.Fatalf("%s as admin: %d %v %s", path, rec.Code, rec.Header(), rec.Body)
		}
	}
	rec := do(t, h, http.MethodGet, "/artifactory/api/system/ping", nil, "")
	if rec.Header().Get(docker.APIVersionHeader) != "" {
		t.Fatal("a non-registry path carries the registry header")
	}
}

// A manifest's media type is what the registry stores, so registry
// requests keep the client's Content-Type; other +json types still become
// application/json for the strict decoders.
func TestRegistryKeepsContentType(t *testing.T) {
	const manifest = "application/vnd.oci.image.manifest.v1+json"
	for path, want := range map[string]string{
		"/v2/r/app/manifests/v1":                        manifest,
		"/artifactory/api/docker/r/v2/app/manifests/v1": manifest,
		"/artifactory/api/repositories/r":               "application/json",
	} {
		var got string
		h := normalizeContentType(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Content-Type")
		}))
		do(t, h, http.MethodPut, path, map[string]string{"Content-Type": manifest}, "{}")
		if got != want {
			t.Errorf("%s: Content-Type %q, want %q", path, got, want)
		}
	}
}

func TestReverseProxyConfiguration(t *testing.T) {
	cases := []struct {
		name string
		tune func(*Config)
		want map[string]any
	}{
		{
			"repository path, base URL from the request", nil,
			map[string]any{
				"key": "nginx", "webServerType": "NGINX", "artifactoryAppContext": "artifactory",
				"publicAppContext": "artifactory", "serverName": "example.com",
				"dockerReverseProxyMethod": "REPOPATHPREFIX",
			},
		},
		{
			"subdomains under the configured base URL",
			func(c *Config) { c.BaseURL, c.DockerSubdomainMatch = "https://Reg.Example.com:8443/artifactory", true },
			map[string]any{
				"key":                      "nginx",
				"webServerType":            "NGINX",
				"artifactoryAppContext":    "artifactory",
				"publicAppContext":         "artifactory",
				"serverName":               "reg.example.com",
				"serverNameExpression":     "*.reg.example.com",
				"dockerReverseProxyMethod": "SUBDOMAIN",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var tune []func(*Config)
			if c.tune != nil {
				tune = append(tune, c.tune)
			}
			h := statusFixture(t, false, tune...)
			rec := do(t, h, http.MethodGet, "/artifactory/api/system/configuration/webServer",
				basic("admin", "pw"), "")
			if rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

// The subdomain method reaches the registry through the server, with the
// repository taken from the host the proxy forwarded.
func TestRegistrySubdomainThroughServer(t *testing.T) {
	h := statusFixture(t, false, func(c *Config) {
		c.BaseURL, c.DockerSubdomainMatch = "https://reg.example.com", true
	})
	admin := basic("admin", "pw")
	if rec := do(t, h, http.MethodPut, "/artifactory/api/repositories/docker-local", jsonAs(admin),
		`{"rclass":"local","packageType":"docker"}`); rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	// On the repository's subdomain app is the image; on the parent it
	// would be a repository, which does not exist.
	for host, want := range map[string]int{
		"docker-local.reg.example.com": http.StatusMethodNotAllowed,
		"reg.example.com":              http.StatusNotFound,
	} {
		headers := basic("admin", "pw")
		headers["X-Forwarded-Host"] = host
		rec := do(t, h, http.MethodGet, "/v2/app/manifests/latest", headers, "")
		if rec.Code != want {
			t.Errorf("%s: %d %s", host, rec.Code, rec.Body)
		}
	}
}

// The JFrog REST operations under /artifactory/api/docker/<repo>/v2/ that
// the generated server owns (parentManifests, promote, and _catalog and
// tags/list until the registry serves them) must still reach
// it: chi prefers their literal and parameter segments to the registry's
// catch-all. Every such operation in the spec is checked, so one added
// later is covered without editing this test.
func TestDockerOperationsReachTheGeneratedServer(t *testing.T) {
	mux, ok := testHandler().(chi.Routes)
	if !ok {
		t.Fatal("the handler is not a chi router")
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	seen := map[string]bool{}
	for path, item := range loadSpec(t, "artifactory.openapi.yaml").Paths.Map() {
		if !strings.HasPrefix(path, "/api/docker/") {
			continue
		}
		for method, op := range item.Operations() {
			if v, ok := op.Extensions["x-handwritten"]; ok && v == true {
				continue
			}
			concrete := "/artifactory" + param.ReplaceAllString(path, "x")
			if got := mux.Find(
				chi.NewRouteContext(),
				method,
				concrete,
			); got != "/artifactory"+path {
				t.Errorf("%s %s reaches %q, not the generated %s", method, concrete, got, path)
			}
			seen[op.OperationID] = true
		}
	}
	for _, id := range []string{"findParentManifestLists", "promoteDockerImage"} {
		if !seen[id] {
			t.Errorf("%s was not checked: %v", id, seen)
		}
	}
	// Over HTTP: the generated stub answers, in the Artifactory envelope,
	// without the registry's header or its anonymous challenge.
	h := statusFixture(t, true)
	want := `{"errors":[{"status":501,"message":"not implemented by artifactweave yet"}]}`
	for op, body := range map[string]string{
		"parentManifests": `{"dockerRepository":"app","tag":"v1"}`,
		"promote":         `{"targetRepo":"libs-b","dockerRepository":"app"}`,
	} {
		rec := do(t, h, http.MethodPost, "/artifactory/api/docker/libs-local/v2/"+op,
			map[string]string{"Content-Type": "application/json"}, body)
		if rec.Code != http.StatusNotImplemented ||
			rec.Header().Get(docker.APIVersionHeader) != "" {
			t.Fatalf("%s: %d %v %s", op, rec.Code, rec.Header(), rec.Body)
		}
		if diff := cmp.Diff(decode(t, want), decode(t, rec.Body.String())); diff != "" {
			t.Fatalf("%s: %s", op, diff)
		}
	}
}
