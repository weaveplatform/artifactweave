package huggingface

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitAdmissionPreservesCurrentLayoutAndRepositoryPolicy(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML})
	rev := "0123456789012345678901234567890123456789"
	good := "models--acme--demo/snapshots/" + rev + "/config.json"
	for _, path := range []string{good, "models--acme--demo/refs/main"} {
		w := format.Write{Op: format.OpDeploy, Path: path}
		got, err := h.Admit(t.Context(), format.Env{}, c, w)
		if err != nil || got.Path != path {
			t.Fatalf("current-layout write %q: %+v %v", path, got, err)
		}
	}
	for _, path := range []string{"acme/demo/resolve/main/config.json", "models--acme--demo/snapshots/../config.json"} {
		if _, err := h.Admit(t.Context(), format.Env{}, c,
			format.Write{Op: format.OpDeploy, Path: path}); err == nil {
			t.Errorf("accepted generic or traversal path %q", path)
		}
	}
	// Cache publication and checksum sidecars do not introduce a new client
	// pathname; their existing source/item policy still applies.
	for _, w := range []format.Write{
		{Op: format.OpDeploy, Path: "legacy/cache", Cached: true},
		{Op: format.OpChecksum, Path: "legacy/checksum"},
	} {
		if _, err := h.Admit(t.Context(), format.Env{}, c, w); err != nil {
			t.Fatalf("existing cache/checksum path %q: %v", w.Path, err)
		}
	}
	blacked := c
	black := true
	blacked.BlackedOut = &black
	if _, err := h.Admit(t.Context(), format.Env{}, blacked,
		format.Write{Op: format.OpDeploy, Path: good}); err == nil {
		t.Fatal("blacked-out repository admitted model")
	}
	patterned := c
	include := "datasets--*"
	patterned.IncludesPattern = &include
	if _, err := h.Admit(t.Context(), format.Env{}, patterned,
		format.Write{Op: format.OpDeploy, Path: good}); err == nil {
		t.Fatal("include pattern admitted model outside allowed path")
	}
	if h.Classify(c, good) != format.ClassUnique ||
		h.Classify(c, "models--acme--demo/refs/main") != format.ClassMetadata ||
		!Serves(repo.HuggingFaceML) || Serves(repo.NPM) {
		t.Fatal("Hugging Face identity/classification drift")
	}
}

func TestUnitLifecycleIgnoresNonSnapshotAndFencesReindex(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML})
	env := format.Env{Store: inmem.New()}
	for _, it := range []types.Item{{Folder: true}, {Name: "refs/main"}, {Name: "legacy"}} {
		if err := h.Deployed(t.Context(), env, c, it); err != nil {
			t.Fatalf("non-snapshot deploy: %v", err)
		}
	}
	if err := h.Deleted(t.Context(), env, c, []types.Item{{Name: "legacy"}}); err != nil {
		t.Fatalf("non-snapshot delete: %v", err)
	}
	if err := h.Deleting(t.Context(), env, c, types.Item{}); err != nil {
		t.Fatalf("pre-delete hook: %v", err)
	}
	stop := errors.New("source changed during reindex")
	env.CheckReindex = func(context.Context) error { return stop }
	if err := h.Reindex(t.Context(), env, c); !errors.Is(err, stop) {
		t.Fatalf("source fence did not stop reindex: %v", err)
	}
}

type reindexStore struct {
	store.Store
	items    store.ItemStore
	packages store.PackageStore
}

func (s reindexStore) Items() store.ItemStore {
	if s.items != nil {
		return s.items
	}
	return s.Store.Items()
}

func (s reindexStore) Packages() store.PackageStore {
	if s.packages != nil {
		return s.packages
	}
	return s.Store.Packages()
}

type failingSnapshotList struct {
	store.ItemStore
	err error
}

func (s failingSnapshotList) Descendants(
	context.Context,
	string,
	bool,
	string,
	store.Page,
) ([]types.Item, string, error) {
	return nil, "", s.err
}

type failingPackageList struct {
	store.PackageStore
	err error
}

func (s failingPackageList) List(
	context.Context,
	string,
	store.Page,
) ([]types.Package, string, error) {
	return nil, "", s.err
}

func TestUnitReindexReportsStorageFailures(t *testing.T) {
	base := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML})
	stop := errors.New("metadata store unavailable")
	for _, tc := range []struct {
		name  string
		store store.Store
	}{
		{"snapshot listing", reindexStore{Store: base, items: failingSnapshotList{ItemStore: base.Items(), err: stop}}},
		{"package reconciliation", reindexStore{Store: base, packages: failingPackageList{PackageStore: base.Packages(), err: stop}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := (Handler{}).Reindex(
				t.Context(),
				format.Env{Store: tc.store},
				c,
			); !errors.Is(
				err,
				stop,
			) {
				t.Fatalf("storage failure was lost: %v", err)
			}
		})
	}
}

type pagedSnapshots struct {
	store.ItemStore
	first, second []types.Item
}

func (s pagedSnapshots) Descendants(
	_ context.Context,
	_ string,
	_ bool,
	_ string,
	p store.Page,
) ([]types.Item, string, error) {
	if p.After == "" {
		return s.first, "next", nil
	}
	return s.second, "", nil
}

func TestUnitReindexIncludesEverySnapshotPage(t *testing.T) {
	base := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML})
	revision := "0123456789012345678901234567890123456789"
	files := make([]types.Item, 0, 2)
	for i, name := range []string{"config.json", "weights.bin"} {
		it, err := base.Items().Put(t.Context(), types.Item{
			RepoKey: c.Key,
			Name:    fmt.Sprintf("models--acme--demo/snapshots/%s/%s", revision, name),
			Created: time.Unix(int64(i+1), 0),
		})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, it)
	}
	paged := pagedSnapshots{ItemStore: base.Items(), first: files[:1], second: files[1:]}
	if err := (Handler{}).Reindex(
		t.Context(),
		format.Env{Store: reindexStore{Store: base, items: paged}},
		c,
	); err != nil {
		t.Fatal(err)
	}
	p, err := base.Packages().Find(t.Context(), c.Key, "models/acme/demo")
	if err != nil {
		t.Fatal(err)
	}
	v, err := base.Packages().Version(t.Context(), p.ID, revision)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := base.Packages().Files(t.Context(), v.ID)
	if err != nil || len(linked) != len(files) {
		t.Fatalf("snapshot page lost from package index: %v %v", linked, err)
	}
}
