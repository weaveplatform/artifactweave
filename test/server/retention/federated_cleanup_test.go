package retention_test

import (
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	fedcleanup "github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
)

func TestUnitFederatedCleanupSelfMember(t *testing.T) {
	w := newSelectionWorld(t, inmem.New())
	ctx := t.Context()
	if err := instance.EnsureInstance(ctx, w.db); err != nil {
		t.Fatal(err)
	}
	w.service.Federation = &fedcleanup.Service{Store: w.db.(fedcleanup.Store), Metadata: pkgindex.NewCleanupMetadata(w.db), Now: func() time.Time { return w.now }}
	if err := w.db.Configs().Put(ctx, configdoc.ConfigDocument{
		Kind: configdoc.ConfigURLBase, Key: configdoc.BaseURLKey,
		Document: []byte(`{"urlBase":"https://self.example"}`),
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"alpha-local", "alpha-self", "beta-self"} {
		project := "alpha"
		if key == "beta-self" {
			project = "beta"
		}
		w.repository(key, project, repo.NPM)
		w.version(key, repo.NPM, "widget", "1.0.0", []string{npm.CanonicalPath("widget", "1.0.0")})
		w.version(key, repo.NPM, "widget", "2.0.0", []string{npm.CanonicalPath("widget", "2.0.0")})
	}
	c, err := w.db.Repos().Get(ctx, "alpha-self")
	if err != nil {
		t.Fatal(err)
	}
	c.Class = repo.Federated
	c.Members = []repo.Member{{URL: repo.SelfMemberURL(c.Key), Enabled: repo.Flex(true)}}
	if err := w.db.Repos().Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	foreign, err := w.db.Repos().Get(ctx, "beta-self")
	if err != nil {
		t.Fatal(err)
	}
	foreign.Class = repo.Federated
	foreign.Members = []repo.Member{{URL: repo.SelfMemberURL(foreign.Key), Enabled: repo.Flex(true)}}
	if err := w.db.Repos().Update(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	p := selectionInput(repo.NPM)
	if _, err := w.service.Create(ctx, p.Key, p); err != nil {
		t.Fatal(err)
	}
	selection, err := w.service.Select(ctx, p.Key)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := collectSelection(t, selection, 1)
	if len(files) != 2 || files[0].Path != files[1].Path || files[0].Version.Version != "1.0.0" || files[1].Version.Version != "1.0.0" {
		t.Fatalf("configured self-member cleanup differs from equivalent local content: selected %d files, want both old versions", len(files))
	}
}
