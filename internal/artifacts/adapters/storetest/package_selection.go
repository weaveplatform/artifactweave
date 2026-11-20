package storetest

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// RunPackageIndexSelection verifies actual bounded index projections, including
// comparator cutoffs and snapshots, on the same fixtures for every adapter.
func RunPackageIndexSelection(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("scope before paging", func(t *testing.T) {
		st := newStore(t)
		ctx := t.Context()
		for _, c := range []repo.Config{
			{Key: "aa-foreign", Class: repo.Local, PackageType: repo.Maven, ProjectKey: "beta", Environments: []string{"DEV"}},
			{Key: "alpha-one", Class: repo.Local, PackageType: repo.Maven, ProjectKey: "alpha", Environments: []string{"DEV"}},
			{Key: "alpha-two", Class: repo.Remote, PackageType: repo.Maven, ProjectKey: "alpha", Environments: []string{"DEV"}},
			{Key: "alpha-secret", Class: repo.Local, PackageType: repo.Maven, ProjectKey: "alpha", Environments: []string{"DEV"}},
			{Key: "alpha-prod", Class: repo.Local, PackageType: repo.Maven, ProjectKey: "alpha", Environments: []string{"PROD"}},
			{Key: "alpha-npm", Class: repo.Local, PackageType: repo.NPM, ProjectKey: "alpha", Environments: []string{"DEV"}},
			{Key: "alpha-virtual", Class: repo.Virtual, PackageType: repo.Maven, ProjectKey: "alpha", Environments: []string{"DEV"}},
		} {
			if err := st.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
		reader := pkgindex.NewReader(st)
		query := api.IndexRepositoryQuery{
			Projects: []string{"alpha"}, Repositories: []string{"**"},
			ExcludedRepositories: []string{"*-secret"}, PackageTypes: []repo.PackageType{repo.Maven}, Environments: []string{"DEV"},
		}
		var keys []string
		page := kernel.Page{Limit: 1}
		for {
			rows, next, err := reader.Repositories(ctx, query, page)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) > 1 {
				t.Fatal("repository page exceeded its bound")
			}
			for _, row := range rows {
				keys = append(keys, row.Key)
			}
			if next == "" {
				break
			}
			page.After = next
		}
		if !slices.Equal(keys, []string{"alpha-one", "alpha-two"}) {
			t.Fatalf("scoped repository pages = %v", keys)
		}
		query.Repositories = []string{"ALPHA*"}
		if rows, _, err := reader.Repositories(ctx, query, kernel.Page{}); err != nil || len(rows) != 0 {
			t.Fatal("repository wildcard folded identity case", err)
		}
		query.Repositories, query.Projects = []string{"**"}, nil
		if rows, _, err := reader.Repositories(ctx, query, kernel.Page{}); err != nil || len(rows) != 0 {
			t.Fatal("empty scope admitted foreign projects", err)
		}
	})

	for _, tc := range []struct {
		name     string
		kind     repo.PackageType
		versions []string
		newest   string
	}{
		{"maven snapshots", repo.Maven, []string{"2", "9", "10", "10.1-20261101.120000-1", "10.1-20261102.120000-2"}, "10.1-20261102.120000-2"},
		{"npm scoped prerelease", repo.NPM, []string{"1.0.0-beta.1", "1.0.0", "2.0.0"}, "2.0.0"},
		{"debian epoch and revision", repo.Debian, []string{"1.0~rc1-1", "1.0-9", "1.0-10", "2:0.1-1"}, "2:0.1-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t)
			ctx := t.Context()
			cfg := repo.Config{Key: "alpha-packages", Class: repo.Local, PackageType: tc.kind, ProjectKey: "alpha", Environments: []string{"DEV"}}
			if err := st.Repos().Create(ctx, *repo.Defaults(&cfg)); err != nil {
				t.Fatal(err)
			}
			p, err := st.Packages().Ensure(ctx, model.Package{RepoKey: cfg.Key, Type: tc.kind, Name: "@scope/name"})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
			for _, version := range tc.versions {
				if _, err := st.Packages().PutVersion(ctx, model.PackageVersion{PackageID: p.ID, Version: version, Created: now}); err != nil {
					t.Fatal(err)
				}
			}
			reader := pkgindex.NewReader(st)
			ref := api.IndexRepository{Key: cfg.Key, Project: "alpha", Class: cfg.Class, PackageType: tc.kind, Environments: []string{"DEV"}}
			packages, _, err := reader.Packages(ctx, ref, kernel.Page{Limit: 1})
			if err != nil || len(packages) != 1 {
				t.Fatal("package identity page missing", err)
			}
			page := kernel.Page{Limit: 1}
			var all []api.IndexVersion
			for {
				rows, next, err := reader.Versions(ctx, packages[0], 0, page)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) > 1 {
					t.Fatal("version page exceeded its bound")
				}
				all = append(all, rows...)
				if next == "" {
					break
				}
				page.After = next
			}
			if len(all) != len(tc.versions) {
				t.Fatal("version pages lost index rows")
			}
			for _, version := range all {
				if version.Protected != (version.Version == tc.newest) {
					t.Fatalf("comparator protection for %s = %t", version.Version, version.Protected)
				}
			}
			selected := all[0]
			var original api.IndexFile
			for _, path := range []string{"one.pkg", "two.pkg", "three.pkg"} {
				item, err := st.Items().
					Put(ctx, model.Item{RepoKey: cfg.Key, Name: path, Size: 7, Actual: kernel.Checksums{SHA256: "old-content"}, Created: now, Modified: now, Updated: now})
				if err != nil {
					t.Fatal(err)
				}
				if err := st.Items().SetProperties(ctx, item.ID, model.Properties{"release": {"candidate", "private-unrequested"}, "unrelated": {"private"}}, now); err != nil {
					t.Fatal(err)
				}
				if err := st.Packages().LinkFile(ctx, model.VersionFile{VersionID: selected.ID, ItemID: item.ID}); err != nil {
					t.Fatal(err)
				}
			}
			files, next, err := reader.Files(ctx, selected, map[string][]string{"release": {"candidate"}}, kernel.Page{Limit: 1})
			if err != nil || len(files) != 1 || next == "" {
				t.Fatal("bounded linked-file page missing", err)
			}
			original = files[0]
			if !reflect.DeepEqual(original.Properties, map[string][]string{"release": {"candidate"}}) {
				t.Fatal("file page hydrated unrequested properties")
			}
			stored, err := st.Items().GetByID(ctx, original.ID)
			if err != nil {
				t.Fatal(err)
			}
			stored.Actual.SHA256, stored.Size, stored.Modified, stored.Updated = "new-content", 8, now.Add(time.Minute), now.Add(time.Minute)
			if _, err := st.Items().Put(ctx, stored); err != nil {
				t.Fatal(err)
			}
			if err := st.Items().PutStats(ctx, original.ID, model.ItemStats{LastDownloaded: now.Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
			updated, _, err := reader.Files(ctx, selected, map[string][]string{"release": {"candidate"}}, kernel.Page{Limit: 1})
			if err != nil || len(updated) != 1 {
				t.Fatal(err)
			}
			if updated[0].ID != original.ID || !updated[0].Created.Equal(original.Created) || updated[0].Actual == original.Actual || updated[0].LastDownloaded.IsZero() {
				t.Fatal("overwrite/download snapshot did not capture mutable evidence")
			}
			if err := st.Packages().DeleteVersion(ctx, selected.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := reader.Files(ctx, selected, nil, kernel.Page{}); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("stale version identity survived deletion", err)
			}
			cfg.ProjectKey = "beta"
			if err := st.Repos().Update(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			if _, _, err := reader.Packages(ctx, ref, kernel.Page{}); !errors.Is(err, kernel.ErrConflict) {
				t.Fatal("stale ownership context disclosed a package page", err)
			}
		})
	}
}
