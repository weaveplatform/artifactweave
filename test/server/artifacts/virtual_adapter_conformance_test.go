package artifacts_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// adapterConformance is one native protocol's observation of the same member
// graph. Expectations belong to the format fixtures, never the resolver under
// test. P2's composite hierarchy and Git LFS's content-addressed objects need
// their own observations rather than an invented generic metadata document.
type adapterConformance struct {
	typ repo.PackageType
	run func(*adapterGraph)
}

func adapterConformanceRows() []adapterConformance {
	return append(adapterFileRows(), adapterMetadataRows()...)
}

func TestUnitVirtualAdapterConformanceInventory(t *testing.T) {
	want := []string{
		"alpine",
		"ansible",
		"bazelmodules",
		"bower",
		"chef",
		"cocoapods",
		"composer",
		"conan",
		"conda",
		"cran",
		"debian",
		"gems",
		"gitlfs",
		"hex",
		"huggingfaceml",
		"luarocks",
		"nix",
		"opkg",
		"p2",
		"pub",
		"puppet",
		"rpm",
		"swift",
	}
	var got []string
	for _, row := range adapterConformanceRows() {
		if row.run == nil {
			t.Fatalf("%s has no native observation", row.typ)
		}
		got = append(got, string(row.typ))
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("adopted adapter inventory = %v; want all 23 exactly once: %v", got, want)
	}
}

// TestAccVirtualAdapterConformance is ADP-08's cross-protocol entry point. Each
// row uses the production runtime, real SQL and a private HTTP upstream. The
// PostgreSQL lane uses a fresh schema; no shared database is reset.
func TestAccVirtualAdapterConformance(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, row := range adapterConformanceRows() {
				t.Run(string(row.typ), func(t *testing.T) {
					mutate := func(c *config.Config) {
						if backend == "postgres" {
							c.Database.Driver = "postgres"
							c.Database.DSN = sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
						}
					}
					w := harness.NewRemoteWorld(t, mutate)
					g := &adapterGraph{t: t, w: w, typ: row.typ}
					g.create()
					row.run(g)
				})
			}
		})
	}
}

type adapterGraph struct {
	t   *testing.T
	w   *harness.RemoteWorld
	typ repo.PackageType
}

func (g *adapterGraph) create() {
	g.t.Helper()
	localType := g.typ
	secondClass := repo.Federated
	if g.typ == repo.P2 {
		localType = repo.Generic // decision 0008 R11: P2 consumes local Maven/Generic.
		secondClass = repo.Local
	}
	if g.typ == repo.Hex {
		secondClass = repo.Local // Hex's existing repository class matrix excludes federation.
	}
	for _, c := range []repo.Config{
		{Key: "local", Class: repo.Local, PackageType: localType},
		{Key: "federated", Class: secondClass, PackageType: localType},
		{Key: "origin", Class: repo.Remote, PackageType: g.typ, URL: g.w.Upstream.URL},
		{Key: "inner", Class: repo.Virtual, PackageType: g.typ},
		{Key: "outer", Class: repo.Virtual, PackageType: g.typ},
	} {
		if c.Class == repo.Remote {
			switch g.typ {
			case repo.Bower:
				c.BowerRegistryURL, c.VCSGitProvider = g.w.Upstream.URL, repo.ArtifactoryVCS
			case repo.CocoaPods:
				c.PodsCdnURL = g.w.Upstream.URL
				c.ExternalDependenciesEnabled, c.ExternalDependenciesPatterns = new(true), []string{g.w.Upstream.URL + "/**"}
			case repo.Composer:
				c.ComposerRegistryURL = g.w.Upstream.URL
			case repo.GitLFS:
				c.URL += "/origin"
			}
		}
		g.save(c, http.MethodPut)
	}
	// Empty virtuals and cycles are accepted configurations (decision 0008 R2).
	// Create both endpoints first, then link them through the ordinary API.
	g.graph([]string{"origin", "local", "inner", "origin", "outer"},
		[]string{"federated", "outer", "local", "inner"})
}

func (g *adapterGraph) save(c repo.Config, method string) {
	g.t.Helper()
	body, err := json.Marshal(c)
	if err != nil {
		g.t.Fatal(err)
	}
	r := g.w.Request(method, "/artifactory/api/repositories/"+c.Key, "admin", string(body), "Content-Type", "application/json")
	if r.Code != http.StatusOK {
		g.t.Fatalf("%s repository %s: %d %s", method, c.Key, r.Code, r.Body)
	}
}

func (g *adapterGraph) graph(outer, inner []string) {
	g.t.Helper()
	for _, c := range []repo.Config{
		{Key: "inner", Class: repo.Virtual, PackageType: g.typ, Repositories: inner},
		{Key: "outer", Class: repo.Virtual, PackageType: g.typ, Repositories: outer},
	} {
		g.save(c, http.MethodPost)
	}
}

func (g *adapterGraph) store(key, p string, body []byte) model.Item {
	g.t.Helper()
	u, err := g.w.Runtime.Blobs.BeginUpload(g.t.Context())
	if err != nil {
		g.t.Fatal(err)
	}
	if _, err := u.Append(g.t.Context(), bytes.NewReader(body)); err != nil {
		g.t.Fatal(err)
	}
	info, err := u.Commit(g.t.Context(), blob.Digests{})
	if err != nil {
		g.t.Fatal(err)
	}
	sums := kernel.Checksums{SHA256: info.Digests.SHA256, SHA1: info.Digests.SHA1, MD5: info.Digests.MD5}
	if _, err := g.w.Runtime.Store.Blobs().Ref(g.t.Context(), blob.Blob{Checksums: sums, Size: info.Size}, 1); err != nil {
		g.t.Fatal(err)
	}
	dir, name := path.Split(p)
	it, err := g.w.Runtime.Store.Items().Put(g.t.Context(), model.Item{RepoKey: key, Path: strings.TrimSuffix(dir, "/"), Name: name, Size: info.Size, Actual: sums})
	if err != nil {
		g.t.Fatal(err)
	}
	return it
}

func (g *adapterGraph) upstream(p string, body []byte) {
	g.t.Helper()
	headers := http.Header{}
	if json.Valid(body) {
		headers.Set("Content-Type", "application/json")
	}
	if bytes.HasPrefix(body, []byte("PK")) {
		headers.Set("Content-Type", "application/zip")
	}
	p, _, _ = strings.Cut(p, "?") // fakeupstream resources are keyed by path; requests retain their query.
	g.w.Upstream.Set("/"+strings.TrimPrefix(p, "/"), fakeupstream.Resource{Body: body, Header: headers})
}

func (g *adapterGraph) get(route string) []byte {
	g.t.Helper()
	r := g.w.Request(http.MethodGet, route, "admin", "")
	if r.Code != http.StatusOK {
		for _, q := range g.w.Upstream.Requests() {
			g.t.Logf("private origin request %s %s", q.Method, q.Path)
		}
		g.t.Fatalf("GET %s: %d %s", route, r.Code, r.Body)
	}
	return r.Body.Bytes()
}

func (g *adapterGraph) want(route string, expected []byte) {
	g.t.Helper()
	if got := g.get(route); !bytes.Equal(got, expected) {
		g.t.Fatalf("GET %s = %q; want %q", route, got, expected)
	}
}
