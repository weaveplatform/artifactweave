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
func TestURLsBehindAProxyChain(t *testing.T) {
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
