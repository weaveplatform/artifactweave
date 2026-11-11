package helmoci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/registryfixture"
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
func TestAccJFHelmPushRequests(t *testing.T) {
	w := registryfixture.New(t, false)
	config, chart, manifest := pushChart(t, w)

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

// A blacked-out Helm OCI repository refuses the chart's tags list, from
// which helm resolves an unversioned or ranged chart, and its referrers,
// through which cosign and notation find signatures, with the policy's 404
// in Artifactory's envelope, naming the chart as a blacked-out remote's
// refusal does. The read permission is checked first: a caller who may not
// read the chart is refused as before, 403 or 401.
func TestAccBlackedOutChartListings(t *testing.T) {
	w := registryfixture.New(t, false)
	_, _, manifest := pushChart(t, w)
	targets := []string{
		"/v2/helmoci-local/acme/tags/list",
		"/artifactory/api/docker/helmoci-local/v2/acme/tags/list",
		"/v2/helmoci-local/acme/referrers/" + support.SHA256Digest(manifest),
	}
	rec := w.Do(http.MethodGet, targets[0], "reader", "")
	if rec.Code != http.StatusOK ||
		rec.Body.String() != `{"name":"helmoci-local/acme","tags":["0.2.0"]}`+"\n" {
		t.Fatalf("tags: %d %s", rec.Code, rec.Body)
	}

	ctx := context.Background()
	c, err := w.DB.Repos().Get(ctx, "helmoci-local")
	if err != nil {
		t.Fatal(err)
	}
	on := true
	c.BlackedOut = &on
	if err := w.DB.Repos().Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	want := `{"errors":[{"message":"The repository 'helmoci-local' is blacked out and cannot ` +
		`serve artifact 'helmoci-local:acme'.","status":404}]}` + "\n"
	for _, target := range targets {
		for _, as := range []string{"admin", "reader"} {
			if rec := w.Do(http.MethodGet, target, as, ""); rec.Code != http.StatusNotFound ||
				rec.Body.String() != want {
				t.Fatalf("%s as %s: %d %s, want %s", target, as, rec.Code, rec.Body, want)
			}
		}
		wantStatus(t, w.Do(http.MethodGet, target, "user", ""), http.StatusForbidden)
		wantStatus(t, w.Do(http.MethodGet, target, "", ""), http.StatusUnauthorized)
	}
}

// pushChart creates helmoci-local and pushes acme 0.2.0 to it as helm 4
// does, its blobs and then its manifest by digest and by tag, and returns
// the chart's config, its packaged chart and its manifest.
func pushChart(t *testing.T, w *registryfixture.World) (config, chart, manifest string) {
	t.Helper()
	c := repo.Config{Key: "helmoci-local", Class: repo.Local, PackageType: repo.HelmOCI}
	if err := w.DB.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	config = `{"name":"acme","version":"0.2.0","apiVersion":"v2","appVersion":"1.16.0"}`
	chart = "the packaged chart"
	for _, b := range []string{config, chart} {
		wantStatus(t, w.Do(http.MethodPost, "/v2/helmoci-local/acme/blobs/uploads/?digest="+
			support.SHA256Digest(b), "admin", b), http.StatusCreated)
	}
	// Helm writes no mediaType into its manifest; the Content-Type names it.
	manifest = `{"schemaVersion":2,"config":{"mediaType":"` + layout.MediaTypeHelmConfig +
		`","digest":"` + support.SHA256Digest(config) + `","size":` + strconv.Itoa(len(config)) + `},` +
		`"layers":[{"mediaType":"application/vnd.cncf.helm.chart.content.v1.tar+gzip",` +
		`"digest":"` + support.SHA256Digest(chart) + `","size":` + strconv.Itoa(len(chart)) + `}],` +
		`"annotations":{"org.opencontainers.image.title":"acme",` +
		`"org.opencontainers.image.version":"0.2.0"}}`
	for _, ref := range []string{support.SHA256Digest(manifest), "0.2.0"} {
		wantStatus(t, w.Do(http.MethodPut, "/v2/helmoci-local/acme/manifests/"+ref, "admin",
			manifest, "Content-Type", layout.MediaTypeOCIManifest), http.StatusCreated)
	}
	return config, chart, manifest
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
