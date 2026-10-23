package helmoci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/test/server/registryfixture"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// jfHelmAQL is the search jf helm push (jf 2.126.0, jfrog-cli-artifactory
// 4a0f3827cfa0, artifactory/commands/helm/push.go) sent after helm pushed
// acme 0.2.0 to helmoci-local, as artifactweave logged it: the chart's
// version folder, <sub>/<chart>/<version>, not recursively.
const jfHelmAQL = "items.find({\n\t  \"repo\": \"helmoci-local\",\n\t  \"path\": \"acme/0.2.0\"\n\t})" +
	`.include("name","repo","path","actual_md5","actual_sha1","sha256","size","type",` +
	`"modified","created","property")`

// A chart pushed as helm 4 pushes it, by digest and then by tag, is what
// jf helm push and jf rt bp then find and record, request by request as
// jf sent them (research 30, 1.3): the AQL over the version folder finds
// manifest.json and the sha256__ blobs, the recursive property edit marks
// the folder and its files with the build, the manifest downloads, and the
// build-info's helm module names the chart. The acceptance feature
// helm_oci.feature runs the pinned jf and helm themselves.
func TestJFHelmPushRequests(t *testing.T) {
	w := registryfixture.New(t, false)
	c := repo.Config{Key: "helmoci-local", Class: repo.Local, PackageType: repo.HelmOCI}
	if err := w.DB.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	config := `{"name":"acme","version":"0.2.0","apiVersion":"v2","appVersion":"1.16.0"}`
	chart := "the packaged chart"
	for _, b := range []string{config, chart} {
		wantStatus(t, w.Do(http.MethodPost, "/v2/helmoci-local/acme/blobs/uploads/?digest="+
			support.SHA256Digest(b), "admin", b), http.StatusCreated)
	}
	// Helm writes no mediaType into its manifest; the Content-Type names it.
	manifest := `{"schemaVersion":2,"config":{"mediaType":"` + layout.MediaTypeHelmConfig +
		`","digest":"` + support.SHA256Digest(config) + `","size":` + strconv.Itoa(len(config)) + `},` +
		`"layers":[{"mediaType":"application/vnd.cncf.helm.chart.content.v1.tar+gzip",` +
		`"digest":"` + support.SHA256Digest(chart) + `","size":` + strconv.Itoa(len(chart)) + `}],` +
		`"annotations":{"org.opencontainers.image.title":"acme",` +
		`"org.opencontainers.image.version":"0.2.0"}}`
	for _, ref := range []string{support.SHA256Digest(manifest), "0.2.0"} {
		wantStatus(t, w.Do(http.MethodPut, "/v2/helmoci-local/acme/manifests/"+ref, "admin",
			manifest, "Content-Type", layout.MediaTypeOCIManifest), http.StatusCreated)
	}

	rec := w.Do(http.MethodPost, "/artifactory/api/search/aql", "admin", jfHelmAQL,
		"Content-Type", "text/plain")
	wantStatus(t, rec, http.StatusOK)
	var found struct {
		Results []struct {
			Path, Name, Type string
			SHA256           string `json:"sha256"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &found); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range found.Results {
		if r.Path != "acme/0.2.0" || r.Type != "file" {
			t.Fatalf("result %+v", r)
		}
		names = append(names, r.Name)
	}
	slices.Sort(names)
	want := []string{
		"manifest.json", "sha256__" + support.SHA256Digest(config)[7:],
		"sha256__" + support.SHA256Digest(chart)[7:],
	}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("found %v, want %v", names, want)
	}

	rec = w.Do(http.MethodGet, "/artifactory/api/repositories/helmoci-local", "admin", "")
	if rec.Code != http.StatusOK || decode(t, rec.Body.String()).(map[string]any)["rclass"] !=
		"local" {
		t.Fatalf("repository: %d %s", rec.Code, rec.Body)
	}
	wantStatus(t, w.Do(http.MethodPut, "/artifactory/api/storage/helmoci-local/acme/0.2.0?"+
		"properties=build.name=charts;build.number=1;build.timestamp=1792451781805&recursive=1",
		"admin", ""), http.StatusNoContent)
	for _, p := range []string{"acme/0.2.0", "acme/0.2.0/manifest.json"} {
		props, err := w.DB.Items().Properties(context.Background(),
			w.Item("helmoci-local", false, p).ID)
		if err != nil || !props.Has("build.name", "charts") || !props.Has("build.number", "1") {
			t.Fatalf("%s: %v %v", p, props, err)
		}
	}
	rec = w.Do(http.MethodGet, "/artifactory/helmoci-local/acme/0.2.0/manifest.json", "admin", "")
	if rec.Code != http.StatusOK || rec.Body.String() != manifest {
		t.Fatalf("manifest: %d %s", rec.Code, rec.Body)
	}

	artifacts := ""
	for i, r := range found.Results {
		if i > 0 {
			artifacts += ","
		}
		artifacts += `{"name":"` + r.Name + `","path":"acme/0.2.0/` + r.Name + `",` +
			`"originalDeploymentRepo":"helmoci-local","sha256":"` + r.SHA256 + `"}`
	}
	doc := `{"name":"charts","number":"1","started":"2026-10-20T00:16:21.770+0100",` +
		`"agent":{"name":"jfrog-cli-go","version":"2.126.0"},` +
		`"buildAgent":{"name":"GENERIC","version":"2.126.0"},` +
		`"modules":[{"type":"helm","id":"acme:0.2.0","artifacts":[` + artifacts + `]}]}`
	wantStatus(t, w.Do(http.MethodPut, "/artifactory/api/build", "admin", doc,
		"Content-Type", "application/json"), http.StatusNoContent)
	rec = w.Do(http.MethodGet, "/artifactory/api/build/charts/1", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	var published struct {
		BuildInfo struct {
			Modules []struct {
				Type, ID  string
				Artifacts []struct{ Name string }
			}
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &published); err != nil {
		t.Fatal(err)
	}
	if m := published.BuildInfo.Modules; len(m) != 1 || m[0].Type != "helm" ||
		m[0].ID != "acme:0.2.0" || len(m[0].Artifacts) != 3 {
		t.Fatalf("build-info %s", rec.Body)
	}
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

func decode(t *testing.T, body string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return value
}
