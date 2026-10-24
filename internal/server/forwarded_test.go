package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Behind a load balancer and an ingress each proxy appends the host it
// received, so X-Forwarded-Host arrives as a list (#249). Every absolute URL
// the server builds names the client-facing host, the list's first entry:
// file-info URIs, a Docker upload's Location on both prefixes, and the
// registry's token realm.
func TestAccURLsBehindAProxyChain(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")
	behind := func(headers map[string]string) map[string]string {
		headers["X-Forwarded-Host"] = "art.example.com, ingress.internal:8080"
		headers["X-Forwarded-Proto"] = "https, http"
		headers["X-Forwarded-Port"] = "443, 8080"
		return headers
	}
	if rec := do(t, h, http.MethodPut, "/artifactory/libs-local/proxy/chain.txt", admin,
		"behind two proxies"); rec.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, http.MethodGet, "/artifactory/api/storage/libs-local/proxy/chain.txt",
		behind(basic("admin", "pw")), "")
	var info struct {
		URI         string `json:"uri"`
		DownloadURI string `json:"downloadUri"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &info) != nil {
		t.Fatalf("file info: %d %s", rec.Code, rec.Body)
	}
	if info.DownloadURI != "https://art.example.com/artifactory/libs-local/proxy/chain.txt" ||
		!strings.HasPrefix(info.URI, "https://art.example.com/artifactory/api/storage/") {
		t.Errorf("file info names %q and %q", info.URI, info.DownloadURI)
	}

	if rec := do(t, h, http.MethodPut, "/artifactory/api/repositories/docker-local", jsonAs(admin),
		`{"rclass":"local","packageType":"docker"}`); rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	for path, prefix := range map[string]string{
		"/v2/docker-local/app/blobs/uploads/": "https://art.example.com/v2/docker-local/app/blobs/uploads/",
		"/artifactory/api/docker/docker-local/v2/app/blobs/uploads/": "https://art.example.com" +
			"/artifactory/api/docker/docker-local/v2/app/blobs/uploads/",
	} {
		rec := do(t, h, http.MethodPost, path, behind(basic("admin", "pw")), "")
		if rec.Code != http.StatusAccepted ||
			!strings.HasPrefix(rec.Header().Get("Location"), prefix) {
			t.Errorf("POST %s: %d, Location %q, want under %s", path, rec.Code,
				rec.Header().Get("Location"), prefix)
		}
	}

	rec = do(t, h, http.MethodGet, "/v2/", behind(map[string]string{}), "")
	const realm = `Bearer realm="https://art.example.com/artifactory/api/docker/null/v2/token",` +
		`service="art.example.com"`
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != realm {
		t.Errorf(
			"probe: %d, challenge %q, want %q",
			rec.Code,
			rec.Header().Get("WWW-Authenticate"),
			realm,
		)
	}
}

// A configured base_url is the operator naming the public URL, so a request
// header the operator does not control never moves a URL off it (#313), as
// in Artifactory, where the configured urlBase comes before anything the
// request carries (research 17). A client that could set X-Forwarded-Host
// would otherwise choose the token realm other clients send credentials to.
func TestAccConfiguredBaseURLWinsOverForwardedHeaders(t *testing.T) {
	h := statusFixture(t, false, func(c *Config) { c.BaseURL = "https://art.example.com" })
	admin := basic("admin", "pw")
	evil := func(headers map[string]string) map[string]string {
		headers["X-Forwarded-Host"] = "evil.example"
		headers["X-Forwarded-Proto"] = "http"
		headers["X-Forwarded-Port"] = "9999"
		return headers
	}
	if rec := do(t, h, http.MethodPut, "/artifactory/libs-local/proxy/base.txt", admin,
		"configured base url"); rec.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, http.MethodGet, "/artifactory/api/storage/libs-local/proxy/base.txt",
		evil(basic("admin", "pw")), "")
	var info struct {
		DownloadURI string `json:"downloadUri"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &info) != nil ||
		info.DownloadURI != "https://art.example.com/artifactory/libs-local/proxy/base.txt" {
		t.Errorf("file info: %d, downloadUri %q", rec.Code, info.DownloadURI)
	}

	if rec := do(t, h, http.MethodPut, "/artifactory/api/repositories/docker-local", jsonAs(admin),
		`{"rclass":"local","packageType":"docker"}`); rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	for path, prefix := range map[string]string{
		"/v2/docker-local/app/blobs/uploads/": "https://art.example.com/v2/docker-local/app/blobs/uploads/",
		"/artifactory/api/docker/docker-local/v2/app/blobs/uploads/": "https://art.example.com" +
			"/artifactory/api/docker/docker-local/v2/app/blobs/uploads/",
	} {
		rec := do(t, h, http.MethodPost, path, evil(basic("admin", "pw")), "")
		if rec.Code != http.StatusAccepted ||
			!strings.HasPrefix(rec.Header().Get("Location"), prefix) {
			t.Errorf("POST %s: %d, Location %q, want under %s", path, rec.Code,
				rec.Header().Get("Location"), prefix)
		}
	}

	rec = do(t, h, http.MethodGet, "/v2/", evil(map[string]string{}), "")
	const realm = `Bearer realm="https://art.example.com/artifactory/api/docker/null/v2/token",` +
		`service="art.example.com"`
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != realm {
		t.Errorf("probe: %d, challenge %q, want %q", rec.Code,
			rec.Header().Get("WWW-Authenticate"), realm)
	}

	// The override header, which a reverse proxy sets, still wins.
	rec = do(t, h, http.MethodGet, "/v2/", map[string]string{
		"X-JFrog-Override-Base-Url": "https://edge.example.com",
		"X-Forwarded-Host":          "evil.example",
	}, "")
	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(
		got, `Bearer realm="https://edge.example.com/artifactory/api/docker/null/v2/token"`,
	) {
		t.Errorf("probe with the override: challenge %q", got)
	}
}
