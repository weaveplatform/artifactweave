package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var errIndexRevisionRollback = errors.New("refuse atomic content publication")

type indexRevisionFixture struct {
	store    port.Store
	reader   api.PackageIndex
	item     model.Item
	versions []api.IndexVersion
	at       time.Time
}

func newIndexRevisionFixture(t *testing.T, st port.Store) indexRevisionFixture {
	t.Helper()
	ctx := t.Context()
	cfg := *repo.Defaults(&repo.Config{Key: "revisions", Class: repo.Local, PackageType: repo.Generic})
	if err := st.Repos().Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	f := indexRevisionFixture{store: st, reader: pkgindex.NewReader(st, nil), at: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)}
	p, err := st.Packages().Ensure(ctx, model.Package{RepoKey: cfg.Key, Type: cfg.PackageType, Name: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	f.item, err = st.Items().Put(ctx, model.Item{RepoKey: cfg.Key, Path: "folder", Name: "file.bin", Size: 3, Updated: f.at})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"1", "2"} {
		v, err := st.Packages().PutVersion(ctx, model.PackageVersion{PackageID: p.ID, Version: name, LastDownloaded: f.at})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Packages().LinkFile(ctx, model.VersionFile{VersionID: v.ID, ItemID: f.item.ID}); err != nil {
			t.Fatal(err)
		}
		f.versions = append(
			f.versions,
			api.IndexVersion{
				ID:      v.ID,
				Version: name,
				Package: api.IndexPackage{ID: p.ID, Name: p.Name, Repository: api.IndexRepository{Key: cfg.Key, Class: cfg.Class, PackageType: cfg.PackageType}},
			},
		)
	}
	if err := st.Items().SetProperties(ctx, f.item.ID, model.Properties{"quality": {"eligible"}}, f.at); err != nil {
		t.Fatal(err)
	}
	if err := st.Items().PutStats(ctx, f.item.ID, model.ItemStats{DownloadCount: 1, LastDownloaded: f.at}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f indexRevisionFixture) snapshots(t *testing.T) []api.IndexVersion {
	t.Helper()
	out := make([]api.IndexVersion, 0, len(f.versions))
	for _, ref := range f.versions {
		v, err := f.reader.Version(t.Context(), ref, 0)
		if err != nil || v.MutationRevision == "" || v.LeadRevision == "" {
			t.Fatal("missing version mutation/classifier binding", err)
		}
		out = append(out, v)
	}
	return out
}

func indexMutationRevisions(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("invalid retention precedes a missing version guard", func(t *testing.T) {
		pages := newStore(t).Packages().(port.PackageIndexPages)
		if _, err := pages.IndexVersion(t.Context(), api.IndexVersion{}, -1); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("invalid retention became an identity miss", err)
		}
	})
	for _, tc := range []struct {
		name      string
		all, noOp bool
		mutate    func(context.Context, indexRevisionFixture) error
	}{
		{"overwrite without a new timestamp", true, false, func(ctx context.Context, f indexRevisionFixture) error {
			it := f.item
			it.Size++
			_, err := f.store.Items().Put(ctx, it)
			if err != nil {
				return fmt.Errorf("mutate version facts: %w", err)
			}
			return nil
		}},
		{"properties with the same timestamp", true, false, func(ctx context.Context, f indexRevisionFixture) error {
			return f.store.Items().SetProperties(ctx, f.item.ID, model.Properties{"quality": {"eligible", "other"}}, f.at)
		}},
		{"remove properties", true, false, func(ctx context.Context, f indexRevisionFixture) error {
			return f.store.Items().PatchProperties(ctx, f.item.ID, nil, []string{"quality"}, f.at)
		}},
		{"unchanged properties", true, true, func(ctx context.Context, f indexRevisionFixture) error {
			return f.store.Items().SetProperties(ctx, f.item.ID, model.Properties{"quality": {"eligible"}}, f.at)
		}},
		{"local download with the same timestamp", true, false, func(ctx context.Context, f indexRevisionFixture) error {
			return f.store.Items().RecordDownload(ctx, f.item.ID, port.Download{At: f.at, By: "reader"})
		}},
		{"remote download", true, false, func(ctx context.Context, f indexRevisionFixture) error {
			return f.store.Items().RecordDownload(ctx, f.item.ID, port.Download{At: f.at, Remote: true})
		}},
		{"replace statistics", true, false, func(ctx context.Context, f indexRevisionFixture) error {
			return f.store.Items().PutStats(ctx, f.item.ID, model.ItemStats{DownloadCount: 2, LastDownloaded: f.at})
		}},
		{"version metadata", false, false, func(ctx context.Context, f indexRevisionFixture) error {
			v, err := f.store.Packages().Version(ctx, f.versions[0].Package.ID, "1")
			if err != nil {
				return fmt.Errorf("mutate version facts: %w", err)
			}
			v.Metadata = []byte(`{"descriptor":"changed"}`)
			_, err = f.store.Packages().PutVersion(ctx, v)
			if err != nil {
				return fmt.Errorf("mutate version facts: %w", err)
			}
			return nil
		}},
		{"version download with the same timestamp", false, false, func(ctx context.Context, f indexRevisionFixture) error {
			return f.store.Packages().RecordVersionDownload(ctx, f.versions[0].ID, f.at)
		}},
		{"new companion link", false, false, func(ctx context.Context, f indexRevisionFixture) error {
			it, err := f.store.Items().Create(ctx, model.Item{RepoKey: f.item.RepoKey, Name: "new.bin"})
			if err != nil {
				return fmt.Errorf("mutate version facts: %w", err)
			}
			return f.store.Packages().LinkFile(ctx, model.VersionFile{VersionID: f.versions[0].ID, ItemID: it.ID})
		}},
		{"duplicate link", false, true, func(ctx context.Context, f indexRevisionFixture) error {
			return f.store.Packages().LinkFile(ctx, model.VersionFile{VersionID: f.versions[0].ID, ItemID: f.item.ID})
		}},
		{"item deletion before link cascade", true, false, func(ctx context.Context, f indexRevisionFixture) error {
			_, err := f.store.Items().Delete(ctx, f.item.RepoKey, false, f.item.FullPath())
			if err != nil {
				return fmt.Errorf("mutate version facts: %w", err)
			}
			return nil
		}},
		{"repository content deletion", true, false, func(ctx context.Context, f indexRevisionFixture) error {
			_, err := f.store.Items().Delete(ctx, f.item.RepoKey, false, ".")
			if err != nil {
				return fmt.Errorf("mutate version facts: %w", err)
			}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIndexRevisionFixture(t, newStore(t))
			before := f.snapshots(t)
			if err := tc.mutate(t.Context(), f); err != nil {
				t.Fatal(err)
			}
			after := f.snapshots(t)
			for i := range before {
				wantChanged := !tc.noOp && (i == 0 || tc.all)
				if changed := before[i].MutationRevision != after[i].MutationRevision; changed != wantChanged {
					t.Fatalf("version %s mutation fence changed=%t, want %t", before[i].Version, changed, wantChanged)
				}
			}
		})
	}
	t.Run("rollback restores content and all shared markers", func(t *testing.T) {
		f := newIndexRevisionFixture(t, newStore(t))
		before := f.snapshots(t)
		err := f.store.Tx(t.Context(), func(ctx context.Context) error {
			it := f.item
			it.Size++
			if _, err := f.store.Items().Put(ctx, it); err != nil {
				return fmt.Errorf("mutate version facts: %w", err)
			}
			if err := f.store.Items().SetProperties(ctx, it.ID, model.Properties{"quality": {"changed"}}, f.at); err != nil {
				return fmt.Errorf("mutate version facts: %w", err)
			}
			return errIndexRevisionRollback
		})
		if !errors.Is(err, errIndexRevisionRollback) || !reflect.DeepEqual(before, f.snapshots(t)) {
			t.Fatal("rolled-back content retained a new version marker", err)
		}
		it, err := f.store.Items().GetByID(t.Context(), f.item.ID)
		if err != nil || it.Size != f.item.Size {
			t.Fatal("rollback changed content", err)
		}
	})
}
