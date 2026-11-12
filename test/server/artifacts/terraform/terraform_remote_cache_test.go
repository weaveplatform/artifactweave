package terraform_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// A remote Terraform repository's cache, as the storage API lists it, holds
// a provider package's archive, SHA256SUMS and signature at their URLs'
// paths below the Providers URL, and a module's archive at its URL's path
// below the remote's URL: the paths JFrog's include pattern examples name
// (Terraform repositories doc, "Configure a Remote Terraform Repository";
// RTFACT-30802), not the registry identity's (#1260).
func TestAccTerraformRemoteCacheKeepsFilesAtTheir7xPaths(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	up := w.Upstream
	w.Repo = *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.Terraform, URL: up.URL,
		TerraformRegistryURL: up.URL, TerraformProvidersURL: up.URL + "/releases",
	})
	w.Update()
	// The runtime has no master key, which Terraform's signed URLs need.
	w.H = harness.Router(harness.Config{
		Store:         w.Runtime.Store,
		Blobs:         w.Runtime.Blobs,
		Auth:          w.Runtime.Auth,
		Tokens:        w.Runtime.Tokens,
		Keys:          support.TerraformBackendKeys(t),
		RemoteSources: w.Runtime.Artifacts.RemoteSources(),
		RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
		Logger:        slog.New(slog.DiscardHandler),
	})
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	asJSON := func(path string, v any) {
		body, _ := json.Marshal(v)
		up.Set(
			path,
			fakeupstream.Resource{
				Body:   body,
				Header: http.Header{"Content-Type": {"application/json"}},
			},
		)
	}
	asJSON("/.well-known/terraform.json", map[string]string{
		"providers.v1": "/registry/providers/", "modules.v1": "/registry/modules/",
	})
	release := terraformtest.Release{
		Type:      "null",
		Version:   "1.0.0",
		Platforms: []string{"linux_amd64"},
	}
	dir := "terraform-provider-null/1.0.0/"
	for name, body := range release.Files(key) {
		up.Set("/releases/"+dir+name, fakeupstream.Resource{Body: body})
	}
	archive := release.Files(key)[release.Name("linux_amd64.zip")]
	sum := strings.Fields(string(release.Files(key)[release.Name("SHA256SUMS")]))[0]
	asJSON(
		"/registry/providers/hashicorp/null/versions",
		map[string]any{"versions": []any{map[string]any{
			"version": "1.0.0", "protocols": []string{"5.0"},
			"platforms": []any{map[string]string{"os": "linux", "arch": "amd64"}},
		}}},
	)
	asJSON("/registry/providers/hashicorp/null/1.0.0/download/linux/amd64", map[string]any{
		"protocols": []string{"5.0"}, "os": "linux", "arch": "amd64",
		"filename":              release.Name("linux_amd64.zip"),
		"download_url":          up.URL + "/releases/" + dir + release.Name("linux_amd64.zip"),
		"shasums_url":           up.URL + "/releases/" + dir + release.Name("SHA256SUMS"),
		"shasums_signature_url": up.URL + "/releases/" + dir + release.Name("SHA256SUMS.sig"),
		"shasum":                sum,
		"signing_keys": map[string]any{"gpg_public_keys": []map[string]string{
			{"key_id": key.KeyID(), "ascii_armor": key.Public},
		}},
	})
	asJSON("/registry/modules/ns/name/aws/versions", map[string]any{
		"modules": []any{map[string]any{"versions": []any{map[string]string{"version": "1.0.0"}}}},
	})
	up.Set(
		"/registry/modules/ns/name/aws/1.0.0/download",
		fakeupstream.Resource{
			Status: http.StatusNoContent,
			Header: http.Header{"X-Terraform-Get": {"/archives/name-1.0.0.zip"}},
		},
	)
	up.Set(
		"/archives/name-1.0.0.zip",
		fakeupstream.Resource{Body: terraformtest.ModuleZip(map[string]string{"main.tf": ""})},
	)

	pkg := w.Request(
		"GET",
		"/artifactory/api/terraform/remote/v1/providers/hashicorp/null/1.0.0/download/linux/amd64",
		"reader",
		"",
	)
	harness.WantStatus(t, pkg, http.StatusOK)
	var urls struct {
		Download string `json:"download_url"`
	}
	if err := json.Unmarshal(pkg.Body.Bytes(), &urls); err != nil {
		t.Fatal(err)
	}
	if got := w.Request(
		"GET",
		urls.Download,
		"",
		"",
	); got.Code != http.StatusOK ||
		got.Body.String() != string(archive) {
		t.Fatalf("provider archive %d", got.Code)
	}
	module := w.Request(
		"GET",
		"/artifactory/api/terraform/v1/modules/remote__ns/name/aws/1.0.0/download",
		"reader",
		"",
	)
	harness.WantStatus(t, module, http.StatusNoContent)
	location, err := url.Parse(module.Header().Get("X-Terraform-Get"))
	if err != nil {
		t.Fatal(err)
	}
	harness.WantStatus(
		t,
		w.Request(
			"GET",
			location.Path+"?sig="+url.QueryEscape(location.Query().Get("sig")),
			"",
			"",
		),
		http.StatusOK,
	)

	listing := w.Request("GET", "/artifactory/api/storage/remote-cache?list&deep=1", "admin", "")
	harness.WantStatus(t, listing, http.StatusOK)
	var tree struct {
		Files []struct {
			URI string `json:"uri"`
		} `json:"files"`
	}
	if err := json.Unmarshal(listing.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, f := range tree.Files {
		files = append(files, f.URI)
	}
	slices.Sort(files)
	want := []string{
		"/archives/name-1.0.0.zip",
		"/" + dir + release.Name("SHA256SUMS"),
		"/" + dir + release.Name("SHA256SUMS.sig"),
		"/" + dir + release.Name("linux_amd64.zip"),
	}
	if !slices.Equal(files, want) {
		t.Fatalf("cached files %q, want %q\n%s", files, want, listing.Body)
	}
}
