package agentpackages_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	apmfmt "github.com/weaveplatform/artifactweave/internal/format/agentpackages"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/agentcommon"
)

var (
	wantStatus  = agentcommon.WantStatus
	setTrashcan = agentcommon.SetTrashcan
)

func packageZIP(t *testing.T, name, version string) string {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, entry := range [][2]string{
		{"apm.yml", "name: " + name + "\nversion: " + version + "\ndescription: example package\ntags: [assistant]\n"},
		{".apm/skills/help/SKILL.md", "---\nname: help\n---\n"},
	} {
		f, err := z.Create(entry[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(entry[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

type repeatingBody struct{}

func (repeatingBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestAccAgentPackagesAPMProtocolAndLifecycle(t *testing.T) {
	w := agentcommon.NewWorld(t)
	ctx := context.Background()
	setTrashcan(t, w.DB, true)
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		c := repo.Config{Key: "apm-" + string(class), Class: class, PackageType: repo.AgentPackages}
		if err := w.DB.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		base := "/artifactory/api/agentpackages/" + c.Key + "/v1/"
		files := "/artifactory/" + c.Key + "/"
		p2 := apmfmt.Path("acme", "my-pkg", "1.2.0")
		if err := w.DB.Permissions().Create(ctx, types.Permission{
			Name: "apm-v1-" + string(class),
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{
					Users: map[string]types.Actions{"deployer": types.ActionRead},
				},
				Targets: map[string]types.Target{c.Key: {
					Include: []string{apmfmt.Path("acme", "my-pkg", "1.0.0")},
				}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
		v1, v2 := packageZIP(t, "my-pkg", "1.0.0"), packageZIP(t, "my-pkg", "1.2.0")
		put := func(version, body string) {
			t.Helper()
			u := base + "packages/acme/my-pkg/versions/" + version
			wantStatus(t, w.Do(http.MethodPut, u, "admin", body,
				"Content-Type", "application/zip"), http.StatusCreated)
		}
		put("1.0.0", v1)
		put("1.2.0", v2)
		versions := base + "packages/acme/my-pkg/versions"
		resp := w.Do(http.MethodGet, versions, "admin", "")
		wantStatus(t, resp, http.StatusOK)
		var doc struct {
			Package  string `json:"package"`
			Versions []struct {
				Version string `json:"version"`
				Digest  string `json:"digest"`
			} `json:"versions"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Package != "acme/my-pkg" || len(doc.Versions) != 2 ||
			doc.Versions[0].Version != "1.2.0" ||
			!strings.HasPrefix(doc.Versions[0].Digest, "sha256:") {
			t.Fatalf("versions: %+v", doc)
		}
		got := w.Do(http.MethodGet, versions+"/1.2.0/download", "admin", "")
		wantStatus(t, got, http.StatusOK)
		if got.Body.String() != v2 {
			t.Fatal("download bytes differ from published ZIP")
		}
		for _, path := range []string{versions, versions + "/1.2.0/download", base + "search"} {
			wantStatus(t, w.Do(http.MethodHead, path, "admin", ""), http.StatusOK)
		}
		for _, path := range []string{
			base + "packages/!/my-pkg/versions",
			versions + "/invalid/download",
		} {
			wantStatus(t, w.Do(http.MethodGet, path, "admin", ""), http.StatusBadRequest)
		}
		wantStatus(t, w.Do(http.MethodGet,
			base+"packages/acme/missing/versions", "admin", ""), http.StatusNotFound)
		wantStatus(t, w.Do(http.MethodPut, versions+"/2.0.0", "deployer", v2,
			"Content-Type", "application/zip"), http.StatusForbidden)
		wantStatus(t, w.Do(http.MethodPut, versions+"/bad", "admin", v2,
			"Content-Type", "application/zip"), http.StatusBadRequest)
		oversized := httptest.NewRequest(http.MethodPut, versions+"/2.0.0",
			io.LimitReader(repeatingBody{}, apmfmt.MaxArchiveBytes+1))
		oversized.SetBasicAuth("admin", "pw")
		oversized.Header.Set("Content-Type", "application/zip")
		oversized.ContentLength = -1
		tooLarge := httptest.NewRecorder()
		w.Handler.ServeHTTP(tooLarge, oversized)
		wantStatus(t, tooLarge, http.StatusRequestEntityTooLarge)
		declared := httptest.NewRequest(http.MethodPut, versions+"/2.0.0", strings.NewReader("x"))
		declared.SetBasicAuth("admin", "pw")
		declared.Header.Set("Content-Type", "application/zip")
		declared.ContentLength = apmfmt.MaxArchiveBytes + 1
		tooLarge = httptest.NewRecorder()
		w.Handler.ServeHTTP(tooLarge, declared)
		wantStatus(t, tooLarge, http.StatusRequestEntityTooLarge)
		wantStatus(t, w.Do(http.MethodPut, versions+"/2.0.0", "admin",
			packageZIP(t, "different", "2.0.0"),
			"Content-Type", "application/zip"), http.StatusConflict)
		wantStatus(t, w.Do(http.MethodPut, versions+"/2.0.0", "admin",
			packageZIP(t, "my-pkg", "3.0.0"),
			"Content-Type", "application/zip"), http.StatusConflict)
		search := w.Do(http.MethodGet, base+"search?q=my-pkg", "admin", "")
		wantStatus(t, search, http.StatusOK)
		if !strings.Contains(search.Body.String(), `"version":"1.2.0"`) {
			t.Fatalf("search: %s", search.Body.String())
		}
		filtered := w.Do(
			http.MethodGet,
			base+"search?q=my-pkg&type=skill&tag=assistant&limit=1&offset=0",
			"admin",
			"",
		)
		wantStatus(t, filtered, http.StatusOK)
		if !strings.Contains(filtered.Body.String(), `"total":1`) {
			t.Fatalf("filtered search: %s", filtered.Body.String())
		}
		absent := w.Do(http.MethodGet, base+"search?type=agent", "admin", "")
		wantStatus(t, absent, http.StatusOK)
		if strings.Contains(absent.Body.String(), "my-pkg") {
			t.Fatalf("type filter leaked package: %s", absent.Body.String())
		}
		for _, query := range []string{"q=unrelated", "tag=absent", "type=agent"} {
			resp := w.Do(http.MethodGet, base+"search?"+query, "admin", "")
			wantStatus(t, resp, http.StatusOK)
			if strings.Contains(resp.Body.String(), "my-pkg") {
				t.Fatalf("search leaked a filtered package: %s", resp.Body.String())
			}
		}
		for _, query := range []string{"limit=-1", "limit=101", "offset=bad", "offset=-1"} {
			wantStatus(t, w.Do(http.MethodGet, base+"search?"+query, "admin", ""),
				http.StatusBadRequest)
		}
		page := w.Do(http.MethodGet, base+"search?limit=1&offset=1", "admin", "")
		wantStatus(t, page, http.StatusOK)
		if !strings.Contains(page.Body.String(), `"total":1`) ||
			!strings.Contains(page.Body.String(), `"results":[]`) {
			t.Fatalf("search offset: %s", page.Body.String())
		}
		limited := w.Do(http.MethodGet, versions, "deployer", "")
		wantStatus(t, limited, http.StatusOK)
		if !strings.Contains(limited.Body.String(), `"version":"1.0.0"`) ||
			strings.Contains(limited.Body.String(), `"version":"1.2.0"`) {
			t.Fatalf("version permission filter: %s", limited.Body.String())
		}
		limitedSearch := w.Do(http.MethodGet, base+"search?q=my-pkg", "deployer", "")
		wantStatus(t, limitedSearch, http.StatusOK)
		if !strings.Contains(limitedSearch.Body.String(), `"version":"1.0.0"`) ||
			strings.Contains(limitedSearch.Body.String(), `"version":"1.2.0"`) {
			t.Fatalf("search permission filter: %s", limitedSearch.Body.String())
		}
		wantStatus(t, w.Do(http.MethodPut, versions+"/1.2.0", "admin", v2,
			"Content-Type", "application/zip"), http.StatusConflict)
		wantStatus(t, w.Do(http.MethodPut, files+p2, "admin", v2), http.StatusConflict)
		wantStatus(t, w.Do(http.MethodPut, versions+"/2.0.0", "admin", "not ZIP",
			"Content-Type", "application/zip"), http.StatusConflict)
		wantStatus(t, w.Do(http.MethodPut, versions+"/2.0.0", "admin", v2,
			"Content-Type", "application/gzip"), http.StatusUnsupportedMediaType)
		wantStatus(t, w.Do(http.MethodPut,
			files+"acme/my-pkg/extra/my-pkg-2.0.0.zip", "admin", v2),
			http.StatusConflict)
		wantStatus(t, w.Do(http.MethodGet, versions, "user", ""), http.StatusForbidden)
		wantStatus(t, w.Do(http.MethodGet, versions+"/1.2.0/download", "user", ""),
			http.StatusForbidden)
		denied := w.Do(http.MethodGet, base+"search?q=my-pkg", "user", "")
		wantStatus(t, denied, http.StatusOK)
		if strings.Contains(denied.Body.String(), "my-pkg") {
			t.Fatalf("search leaked denied package: %s", denied.Body.String())
		}
		if err := w.DB.Permissions().Create(ctx, types.Permission{
			Name: "apm-unpublished-" + string(class),
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
				Targets: map[string]types.Target{c.Key: {
					Include: []string{apmfmt.Path("acme", "my-pkg", "9.0.0")},
				}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, w.Do(http.MethodGet, versions, "user", ""), http.StatusNotFound)
		wantStatus(t, w.Do(http.MethodDelete, files+p2, "admin", ""), http.StatusNoContent)
		resp = w.Do(http.MethodGet, versions, "admin", "")
		wantStatus(t, resp, http.StatusOK)
		if strings.Contains(resp.Body.String(), `"version":"1.2.0"`) {
			t.Fatal("deleted version remained indexed")
		}
		wantStatus(t, w.Do(http.MethodPost,
			"/artifactory/api/trash/restore/"+c.Key+"/"+p2+"?to="+c.Key,
			"admin", ""), http.StatusAccepted)
		if got := w.Do(
			http.MethodGet,
			versions,
			"admin",
			"",
		); got.Code != http.StatusOK ||
			!strings.Contains(got.Body.String(), `"version":"1.2.0"`) {
			t.Fatalf("restore: %d %s", got.Code, got.Body.String())
		}
		p, err := w.DB.Packages().Find(ctx, c.Key, "acme/my-pkg")
		if err != nil {
			t.Fatal(err)
		}
		if err := w.DB.Packages().Delete(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
		if err := (apmfmt.Handler{}).Reindex(
			ctx,
			format.Env{Store: w.DB, Blobs: w.Blobs},
			c,
		); err != nil {
			t.Fatal(err)
		}
		if got := w.Do(
			http.MethodGet,
			versions,
			"admin",
			"",
		); got.Code != http.StatusOK ||
			!strings.Contains(got.Body.String(), `"version":"1.0.0"`) {
			t.Fatalf("reindex: %d %s", got.Code, got.Body.String())
		}
	}
}
