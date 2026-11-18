package itemkit_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

type snapshotConfigs struct {
	configdoc.ConfigStore
	reads map[string]int
	after func(string)
}

func (s *snapshotConfigs) Get(ctx context.Context, kind configdoc.ConfigKind, key string) (configdoc.ConfigDocument, error) {
	doc, err := s.ConfigStore.Get(ctx, kind, key)
	if kind == configdoc.ConfigLayout {
		s.reads[key]++
		if s.after != nil {
			s.after(key)
		}
	}
	return doc, err
}

type snapshotStore struct {
	*inmem.Store
	configs *snapshotConfigs
}

func (s snapshotStore) Configs() configdoc.ConfigStore { return s.configs }

func TestUnitItemReadSharesLayoutSnapshot(t *testing.T) {
	db := inmem.New()
	configs := &snapshotConfigs{ConfigStore: db.Configs(), reads: map[string]int{}}
	st := snapshotStore{Store: db, configs: configs}
	w := kittest.New(t, st)
	const root = "request-layout"
	put := func(pattern string) {
		t.Helper()
		doc, _ := json.Marshal(map[string]any{"artifactPathPattern": pattern})
		if err := db.Configs().Put(t.Context(), configdoc.ConfigDocument{Kind: configdoc.ConfigLayout, Key: root, Document: doc}); err != nil {
			t.Fatal(err)
		}
	}
	put("[orgPath]/[module]/[baseRev]/[module]-[baseRev].[ext]")
	w.Virtual.PackageType, w.Virtual.RepoLayoutRef = repo.Maven, root
	w.Virtual.Repositories = []string{w.Local.Key}
	w.Local.PackageType, w.Local.RepoLayoutRef = repo.Maven, "gradle-default"
	for _, c := range []repo.Config{w.Virtual, w.Local} {
		if err := db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	w.Put(t, w.Local, false, "org.acme/app/1.0/app-1.0.jar", []byte("stable snapshot"))
	configs.after = func(key string) {
		if key == root {
			put("changed/[orgPath]/[module]/[baseRev]/[module]-[baseRev].[ext]")
		}
	}
	k := w.Kit(itemkit.Deps{})
	request := func() *httptest.ResponseRecorder {
		r := kittest.Request(t, "GET", "/artifactory/libs/org/acme/app/[RELEASE]/app-[RELEASE].jar")
		rec := httptest.NewRecorder()
		k.GetItem(rec, r)
		return rec
	}
	r := request()
	if r.Code != 200 || r.Body.String() != "stable snapshot" {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, key := range []string{root, "gradle-default"} {
		if configs.reads[key] != 1 {
			t.Fatalf("%s read %d times across translation/selection/Resolve", key, configs.reads[key])
		}
	}
	configs.after = nil
	r = request()
	if r.Code != 404 || configs.reads[root] != 2 || configs.reads["gradle-default"] != 2 {
		t.Fatal(r.Code, r.Body.String(), configs.reads)
	}
}
