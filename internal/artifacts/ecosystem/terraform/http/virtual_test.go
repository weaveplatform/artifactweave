package terraformhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	artport "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

func virtualMembers(t *testing.T, f *fixture, members ...string) {
	t.Helper()
	c, err := f.db.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories = members
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func TestAccVirtualModulesUnionAndSignedMembership(t *testing.T) {
	f := newFixture(t)
	virtualMembers(t, f, "provider", "untyped", "Mods__Nested", "mods", "v1")
	base := prefix + "v1/modules/virtual__ns/name/aws/"
	list := f.do(http.MethodGet, base+"versions", "admin")
	expect(t, list, 200)
	if strings.Count(list.Body.String(), `"1.0.0"`) != 1 || !strings.Contains(list.Body.String(), `"2.0.0"`) {
		t.Fatal(list.Body.String())
	}
	result := f.do(http.MethodGet, base+"1.0.0/download", "admin")
	expect(t, result, 204)
	if result.Header().Get("Content-Length") != "0" {
		t.Fatal(result.Header())
	}
	public := result.Header().Get("X-Terraform-Get")
	if !strings.Contains(public, "/virtual/v1/modules/ns/name/aws/1.0.0.zip?sig=") {
		t.Fatal(public)
	}
	token := strings.Split(public, "sig=")[1]
	body, _ := encoding.DecodeString(strings.Split(token, ".")[1])
	var claim struct {
		Member          string `json:"internal_repo"`
		Issued, Expires int64
	}
	if err := json.Unmarshal(body, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.Member != "Mods__Nested" {
		t.Fatal(string(body))
	}
	expect(t, f.do(http.MethodGet, public, ""), 200)
	virtualMembers(t, f, "mods")
	refused := f.do(http.MethodGet, public, "")
	expect(t, refused, 403)
	if !strings.Contains(refused.Body.String(), "Invalid or expired Terraform download URL") {
		t.Fatal(refused.Body.String())
	}
	virtualMembers(t, f, "Mods__Nested")
	f.now = f.now.Add(91 * time.Second)
	expect(t, f.do(http.MethodGet, public, ""), 403)
}

func TestAccVirtualModulesReadPatternsAndDeferredProviders(t *testing.T) {
	f := newFixture(t)
	virtualMembers(t, f, "mods", "v1")
	base := prefix + "virtual/v1/modules/ns/name/aws/"
	w := f.do("GET", base+"versions", "reader")
	expect(t, w, 200)
	if strings.Count(w.Body.String(), `"version"`) != 1 {
		t.Fatal(w.Body.String())
	}
	c, _ := f.db.Repos().Get(t.Context(), "virtual")
	excluded := "**/1.0.0.zip"
	c.ExcludesPattern = &excluded
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	expect(t, f.do("GET", base+"versions", "reader"), 404)
	expect(t, f.do("GET", base+"1.0.0/download", "reader"), 404)
	expect(t, f.do("GET", prefix+"virtual/v1/providers/bad%2F/versions", "admin"), 501)
	expect(t, f.do("PUT", prefix+"virtual/providers/malformed", "admin"), 501)
}

func TestAccVirtualModulesSourceMissesAndPriority(t *testing.T) {
	for _, status := range []int{401, 403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			c, _ := f.db.Repos().Get(t.Context(), "remote")
			priority := true
			c.PriorityResolution = repo.Flex(priority)
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			virtualMembers(t, f.fixture, "remote", "mods")
			f.up.Set("/registry/modules/ns/name/aws/versions", fakeupstream.Resource{Status: status})
			base := prefix + "virtual/v1/modules/ns/name/aws/"
			list := f.do("GET", base+"versions", "reader")
			expect(t, list, 200)
			result := f.do("GET", base+"1.0.0/download", "reader")
			expect(t, result, 204)
			public := result.Header().Get("X-Terraform-Get")
			claim := decodeVirtualClaim(t, public)
			if claim.Member != "mods" {
				t.Fatal(claim)
			}
			expect(t, f.do("GET", public, ""), 200)
		})
	}
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	c, _ := f.db.Repos().Get(t.Context(), "remote")
	priority := true
	c.PriorityResolution = repo.Flex(priority)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	virtualMembers(t, f.fixture, "mods", "remote")
	base := prefix + "virtual/v1/modules/ns/name/aws/"
	result := f.do("GET", base+"1.0.0/download", "reader")
	expect(t, result, 204)
	if claim := decodeVirtualClaim(t, result.Header().Get("X-Terraform-Get")); claim.Member != "remote" {
		t.Fatal(claim)
	}
	public := result.Header().Get("X-Terraform-Get")
	expect(t, f.do("HEAD", public, ""), 200)
	expect(t, f.do("POST", public, ""), 405)
	c.Offline = &priority
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	// A warmed offline member still serves its validated bytes; a cold one
	// contributes nothing and must never contact its upstream.
	expect(t, f.do("GET", public, ""), 200) // offline retains the same source identity and warm bytes
}

func decodeVirtualClaim(t *testing.T, public string) claims {
	t.Helper()
	u, err := url.Parse(public)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(u.Query().Get("sig"), ".")
	if len(parts) != 3 {
		t.Fatal(public)
	}
	body, err := encoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claim claims
	if err := json.Unmarshal(body, &claim); err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestAccVirtualModulesMemberPoliciesAndWrongRepository(t *testing.T) {
	f := newFixture(t)
	virtualMembers(t, f, "mods", "v1")
	c, _ := f.db.Repos().Get(t.Context(), "mods")
	excluded := "**/1.0.0.zip"
	c.ExcludesPattern = &excluded
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	base := prefix + "virtual/v1/modules/ns/name/aws/"
	result := f.do("GET", base+"1.0.0/download", "admin")
	expect(t, result, 204)
	public := result.Header().Get("X-Terraform-Get")
	if claim := decodeVirtualClaim(t, public); claim.Member != "v1" {
		t.Fatal(claim)
	}
	other := *repo.Defaults(&repo.Config{Key: "another", Class: repo.Virtual, PackageType: repo.Terraform, Repositories: []string{"v1"}})
	if err := f.db.Repos().Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	expect(t, f.do("GET", strings.Replace(public, "/virtual/", "/another/", 1), ""), 403)
	expect(t, f.do("GET", strings.Replace(public, "1.0.0.zip", "2.0.0.zip", 1), ""), 403)
	expect(t, f.do("POST", base+"versions", "admin"), 405)
	expect(t, f.do("GET", base+"bad/path", "admin"), 404)
	virtualMembers(t, f, "provider", "untyped")
	expect(t, f.do("GET", base+"versions", "admin"), 404)
	expect(t, f.do("GET", base+"1.0.0/download", "admin"), 404)
	f.read = failingStore{serverStore: f.db, fail: "package"}
	f.h = f.router(f.keys)
	virtualMembers(t, f, "mods")
	expect(t, f.do("GET", base+"versions", "admin"), 500)
	expect(t, f.do("GET", base+"1.0.0/download", "admin"), 500)
}

type afterPackageStore struct {
	serverStore
	hook func(string)
}

func (s afterPackageStore) Packages() artport.PackageStore {
	return afterPackages{s.serverStore.Packages(), s.hook}
}

type afterPackages struct {
	artport.PackageStore
	hook func(string)
}

func (s afterPackages) Find(ctx context.Context, key, name string) (artmodel.Package, error) {
	p, err := s.PackageStore.Find(ctx, key, name)
	s.hook(key)
	return p, err
}

func TestAccVirtualModulesLateReadLossKeepsLaterOwner(t *testing.T) {
	f := newFixture(t)
	virtualMembers(t, f, "mods", "v1")
	if err := f.db.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "later-read", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}}, Targets: map[string]accesstest.Target{"v1": {}}}}}); err != nil {
		t.Fatal(err)
	}
	changed := false
	f.read = afterPackageStore{serverStore: f.db, hook: func(key string) {
		if key == "v1" && !changed {
			changed = true
			if err := f.db.Permissions().Delete(t.Context(), "read"); err != nil {
				t.Fatal(err)
			}
		}
	}}
	f.h = f.router(f.keys)
	result := f.do("GET", prefix+"virtual/v1/modules/ns/name/aws/versions", "reader")
	expect(t, result, 200)
	if !changed || strings.Count(result.Body.String(), `"version"`) != 1 || !strings.Contains(result.Body.String(), "1.0.0") {
		t.Fatal(result.Body.String(), changed)
	}
}

func TestAccVirtualSignedModuleRetainsStorageFault(t *testing.T) {
	f := newFixture(t)
	virtualMembers(t, f, "mods")
	metadata := f.do("GET", prefix+"virtual/v1/modules/ns/name/aws/1.0.0/download", "admin")
	expect(t, metadata, 204)
	f.read = failingStore{serverStore: f.db, fail: "download"}
	f.h = f.router(f.keys)
	result := f.do("GET", metadata.Header().Get("X-Terraform-Get"), "")
	expect(t, result, 500)
}
