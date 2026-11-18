package nuget_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func nugetVirtualConfig(t *testing.T, w *harness.RemoteWorld, key string, edit func(*repo.Config)) {
	t.Helper()
	c, err := w.Runtime.Store.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	edit(&c)
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func nugetVirtualPush(t *testing.T, w *harness.RemoteWorld, route, as string, pkg []byte) int {
	t.Helper()
	ct, body := contentTypeOf(nugetForm(pkg))
	r := w.Request("PUT", route, as, body, "Content-Type", ct)
	return r.Code
}

func TestAccNuGetVirtualPushAndRefusedMutations(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			w := nugetVirtualWorld(t)
			nugetVirtualConfig(t, w, "local", func(c *repo.Config) { c.Class = class })
			nugetVirtualConfig(t, w, "virtual", func(c *repo.Config) { c.DefaultDeploymentRepo = "local" })
			ct, body := contentTypeOf(nugetForm(nugetPackage("Virtual.Push", "1.0.0", "")))
			route := "/artifactory/api/nuget/virtual/team/"
			r := w.Request("PUT", route, "admin", body, "Content-Type", ct)
			harness.WantStatus(t, r, 201)
			if r.Body.Len() != 0 {
				t.Fatal("push body", r.Body.String())
			}
			if _, err := w.Runtime.Store.Items().Get(t.Context(), "local", false, "team/Virtual.Push.1.0.0.nupkg"); err != nil {
				t.Fatal(err)
			}
			r = w.Request("PUT", route, "admin", body, "Content-Type", ct)
			harness.WantStatus(t, r, 409)
			if !strings.Contains(r.Body.String(), "The repository 'local' already contains Virtual.Push 1.0.0") {
				t.Fatal(r.Body.String())
			}
			harness.WantStatus(t, w.Request("GET", "/artifactory/api/nuget/v3/virtual/flatcontainer/virtual.push/1.0.0/virtual.push.1.0.0.nupkg", "admin", ""), 200)
			harness.WantStatus(t, w.Request("GET", "/artifactory/api/nuget/virtual/Download/Virtual.Push/1.0.0", "admin", ""), 200)
			for _, method := range []string{"DELETE", "POST"} {
				r = w.Request(method, "/artifactory/api/nuget/virtual/Virtual.Push/1.0.0", "admin", `malformed body`)
				harness.WantStatus(t, r, 404)
				if r.Body.Len() != 0 {
					t.Fatal("virtual mutation disclosed a body", method, r.Body.String())
				}
			}
			if _, err := w.Runtime.Store.Items().Get(t.Context(), "local", false, "team/Virtual.Push.1.0.0.nupkg"); err != nil {
				t.Fatal("virtual delete removed DDR package", err)
			}
			symbolCT, symbolBody := contentTypeOf(nugetForm(nugetSymbolsPackage("Wrong.Route", "1.0.0")))
			r = w.Request("PUT", "/artifactory/api/nuget/virtual/", "admin", symbolBody, "Content-Type", symbolCT)
			if r.Code != 400 || !strings.Contains(r.Body.String(), "/nuget/v3/virtual/symbols") {
				t.Fatal("symbol hint lost virtual address", r.Code, r.Body.String())
			}
		})
	}
}

func TestAccNuGetVirtualPushTargetPermissions(t *testing.T) {
	w := nugetVirtualWorld(t)
	nugetVirtualConfig(t, w, "virtual", func(c *repo.Config) { c.DefaultDeploymentRepo = "local" })
	for _, name := range []string{"pusher", "wrong", "virtual-only"} {
		if err := w.Runtime.Store.Users().Create(t.Context(), accesstest.User{Name: name, PasswordHash: cheapHash}); err != nil {
			t.Fatal(err)
		}
	}
	for _, grant := range []struct{ name, key, path string }{{"pusher", "local", "only/Allowed.1.0.0.nupkg"}, {"wrong", "local", "only/Other.1.0.0.nupkg"}, {"virtual-only", "virtual", "**"}} {
		if err := w.Runtime.Store.Permissions().
			Create(t.Context(), accesstest.Permission{Name: grant.name, Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{grant.name: accessapi.ActionWrite}}, Targets: map[string]accesstest.Target{grant.key: {Include: []string{grant.path}}}}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		as     string
		status int
	}{{"wrong", 403}, {"virtual-only", 403}, {"pusher", 201}} {
		ct, body := contentTypeOf(nugetForm(nugetPackage("Allowed", "1.0.0", "")))
		r := w.Request("PUT", "/artifactory/api/nuget/virtual/only/", tc.as, body, "Content-Type", ct)
		harness.WantStatus(t, r, tc.status)
	}
	nugetVirtualConfig(t, w, "virtual", func(c *repo.Config) { c.HideUnauthorizedResources = new(true) })
	ct, body := contentTypeOf(nugetForm(nugetPackage("Hidden", "1.0.0", "")))
	r := w.Request("PUT", "/artifactory/api/nuget/virtual/", "wrong", body, "Content-Type", ct)
	harness.WantStatus(t, r, 404)
	if strings.Contains(r.Body.String(), "local") {
		t.Fatal("hidden deployment target disclosed", r.Body.String())
	}
}

func TestAccNuGetVirtualNoDeploymentTarget(t *testing.T) {
	w := nugetVirtualWorld(t)
	for _, target := range []string{"", "missing", "remote", "nested"} {
		t.Run(target, func(t *testing.T) {
			nugetVirtualConfig(t, w, "virtual", func(c *repo.Config) { c.DefaultDeploymentRepo = target })
			for _, route := range []string{"/artifactory/api/nuget/virtual/", "/artifactory/api/nuget/v3/virtual/symbols"} {
				r := w.Request("PUT", route, "admin", "not a multipart body")
				harness.WantStatus(t, r, 405)
				if r.Header().Get("Allow") != "GET" ||
					!strings.Contains(r.Body.String(), "No local repository was configured as local deployment repository for the (virtual) virtual repository.") {
					t.Fatal(r.Header(), r.Body.String())
				}
			}
		})
	}
}

func TestAccNuGetVirtualSymbolsAndForceAuthentication(t *testing.T) {
	w := nugetVirtualWorld(t)
	if err := w.Runtime.Store.Settings().Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	nugetVirtualConfig(t, w, "virtual", func(c *repo.Config) { c.DefaultDeploymentRepo = "local"; c.ForceNugetAuthentication = new(true) })
	symbol := "/artifactory/api/nuget/v3/virtual/APP.PDB/" + strings.ToLower(nugetPDBKey) + "/app.pdb"
	for _, route := range []string{"/artifactory/api/nuget/v3/virtual/index.json", "/artifactory/api/nuget/virtual/", symbol} {
		r := w.Request("GET", route, "", "")
		harness.WantStatus(t, r, 401)
		if !strings.HasPrefix(r.Header().Get("WWW-Authenticate"), "Basic ") {
			t.Fatal(r.Header())
		}
	}
	if got := nugetVirtualPush(t, w, "/artifactory/api/nuget/v3/virtual/symbols", "admin", nugetPDBPackage("Debug.Virtual", nugetPortablePDB())); got != 201 {
		t.Fatal(got)
	}
	for _, route := range []string{"/artifactory/api/nuget/v3/virtual/index.json", "/artifactory/api/nuget/virtual/", symbol} {
		r := w.Request("GET", route, "admin", "")
		harness.WantStatus(t, r, 200)
		if route == symbol && !bytes.Equal(r.Body.Bytes(), nugetPortablePDB()) {
			t.Fatal("local PDB bytes changed")
		}
	}
	harness.WantStatus(t, w.Request("GET", symbol, "reader", ""), 404)
	// Only the addressed descriptor controls Force Authentication.
	nugetVirtualConfig(t, w, "virtual", func(c *repo.Config) { c.ForceNugetAuthentication = new(false) })
	nugetVirtualConfig(t, w, "local", func(c *repo.Config) { c.ForceNugetAuthentication = new(true) })
	harness.WantStatus(t, w.Request("GET", "/artifactory/api/nuget/v3/virtual/index.json", "", ""), 200)
	if err := w.Runtime.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "anonymous-symbols", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{kernel.AnonymousUser: accessapi.ActionRead}}, Targets: map[string]accesstest.Target{"local": {Include: []string{".symbols/**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	harness.WantStatus(t, w.Request("GET", symbol, "", ""), 200)
	harness.WantStatus(t, w.Request("GET", strings.Replace(symbol, "/virtual/", "/local/", 1), "", ""), 401)

	// An unreadable local does not hide the readable remote symbol holder.
	w.Repo.SymbolServerURL = w.Upstream.URL + "/symbols"
	w.Update()
	w.Upstream.Set("/symbols/app.pdb/"+nugetPDBKey+"/app.pdb", fakeupstream.Resource{Body: nugetPortablePDB()})
	r := w.Request("GET", symbol, "reader", "")
	harness.WantStatus(t, r, 200)
	if !bytes.Equal(r.Body.Bytes(), nugetPortablePDB()) {
		t.Fatal("remote PDB bytes changed")
	}
	harness.WantStatus(t, w.Request("HEAD", symbol, "reader", ""), 200)
	harness.WantStatus(t, w.Request("GET", symbol, "denied", ""), 404)
	nugetVirtualConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"nested"} })
	nugetVirtualConfig(t, w, "nested", func(c *repo.Config) { c.ExcludesPattern = new(".symbols/**") })
	harness.WantStatus(t, w.Request("GET", symbol, "admin", ""), 404)
}

func TestAccNuGetVirtualReindexOwnsNoWork(t *testing.T) {
	w := nugetVirtualWorld(t)
	route := "/artifactory/api/nuget/virtual/reindex"
	harness.WantStatus(t, w.Request("POST", route, "reader", ""), 403)
	r := w.Request("POST", route, "admin", "")
	harness.WantStatus(t, r, 200)
	if r.Body.String() != "NuGet reindex calculation for repository 'virtual' accepted." {
		t.Fatal(r.Body.String())
	}
	for _, key := range []string{"virtual", "local", "remote"} {
		if _, err := w.Runtime.Store.WorkQueue().Get(t.Context(), format.KindReindex, key); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("virtual reindex scheduled member work", key, err)
		}
	}
}

func TestAccNuGetNormalizedVirtualSaveAndNestedDownloads(t *testing.T) {
	w := nugetVirtualWorld(t)
	r := w.Request(
		"PUT",
		"/artifactory/api/repositories/normalized",
		"admin",
		`{"rclass":"virtual","packageType":"nuget","repositories":["local"],"enableNormalizedVersion":true}`,
		"Content-Type",
		"application/json",
	)
	harness.WantStatus(t, r, 400)
	if !strings.Contains(r.Body.String(), "Nuget Repository normalized must have only normalized repositories") {
		t.Fatal(r.Body.String())
	}
	for _, tc := range []struct {
		key, body string
		status    int
	}{
		{"enforced", `{"rclass":"local","packageType":"nuget","enableNormalizedVersion":true}`, 200},
		{"normalized", `{"rclass":"virtual","packageType":"nuget","repositories":["enforced"],"enableNormalizedVersion":true}`, 200},
		{"normal-parent", `{"rclass":"virtual","packageType":"nuget","repositories":["normalized"],"enableNormalizedVersion":false}`, 200},
		{"nested-refusal", `{"rclass":"virtual","packageType":"nuget","repositories":["nested"],"enableNormalizedVersion":true}`, 400},
	} {
		r = w.Request("PUT", "/artifactory/api/repositories/"+tc.key, "admin", tc.body, "Content-Type", "application/json")
		harness.WantStatus(t, r, tc.status)
	}
	body := nugetPackage("Layout.Mixed", "1.0.0.0+build", "<description>nested normalized</description>")
	harness.WantStatus(t, w.Request("PUT", "/artifactory/enforced/Layout.Mixed.1.0.0.nupkg", "admin", string(body)), 201)
	for _, route := range []string{"/artifactory/api/nuget/v3/normal-parent/flatcontainer/layout.mixed/1.0.0/layout.mixed.1.0.0.nupkg", "/artifactory/api/nuget/normal-parent/Download/Layout.Mixed/1.0.0"} {
		r = w.Request("GET", route, "admin", "")
		harness.WantStatus(t, r, 200)
		if !bytes.Equal(r.Body.Bytes(), body) {
			t.Fatal("nested layout changed package bytes")
		}
	}
}
