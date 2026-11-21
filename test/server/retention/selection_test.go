package retention_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	artifactmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

type selectionAuthority string

func (a selectionAuthority) Any() bool                  { return a != "none" }
func (a selectionAuthority) Allows(project string) bool { return a == "*" || string(a) == project }
func (selectionAuthority) Name() string                 { return "selection-admin" }
func (selectionAuthority) Credential() kernel.CredentialReference {
	return kernel.CredentialReference{Name: "selection-admin", TokenID: "verified-test-token"}
}

type selectionWorld struct {
	t         *testing.T
	db        app.RouterStore
	service   cleanup.Service
	authority string
	now       time.Time
	items     []artifactmodel.Item
	foreign   artifactsapi.IndexVersion
}

func newSelectionWorld(t *testing.T, db app.RouterStore) *selectionWorld {
	t.Helper()
	w := &selectionWorld{t: t, db: db, authority: "alpha", now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	for _, key := range []string{"alpha", "beta"} {
		if err := db.Projects().Create(t.Context(), accesstest.Project{Key: key}); err != nil {
			t.Fatal(err)
		}
	}
	formats := ecosystem.Settings{}
	w.service = cleanup.Service{
		Store:         db,
		Schedules:     db.Schedules(),
		Repositories:  db.Repos(),
		PackageIndex:  pkgindex.NewReader(db, formats.For, &item.Service{Store: db}),
		Now:           func() time.Time { return w.now },
		ProjectExists: func(ctx context.Context, key string) error { _, err := db.Projects().Get(ctx, key); return err },
		Authorize: func(ctx context.Context) (cleanup.Policy, error) {
			if err := db.Projects().Guard(ctx); err != nil {
				return nil, err
			}
			return selectionAuthority(w.authority), nil
		},
	}
	return w
}

func (w *selectionWorld) repository(key, project string, kind repo.PackageType) {
	w.t.Helper()
	c := repo.Config{Key: key, Class: repo.Local, PackageType: kind, ProjectKey: project, Environments: []string{"DEV"}}
	if err := w.db.Repos().Create(w.t.Context(), *repo.Defaults(&c)); err != nil {
		w.t.Fatal(err)
	}
}

func (w *selectionWorld) version(repository string, kind repo.PackageType, name, version string, paths []string) artifactsapi.IndexVersion {
	return w.versionIn(w.t.Context(), repository, kind, name, version, paths)
}

func (w *selectionWorld) versionIn(ctx context.Context, repository string, kind repo.PackageType, name, version string, paths []string) artifactsapi.IndexVersion {
	w.t.Helper()
	p, err := w.db.Packages().Ensure(ctx, artifactmodel.Package{RepoKey: repository, Type: kind, Name: name})
	if err != nil {
		w.t.Fatal(err)
	}
	v, err := w.db.Packages().PutVersion(ctx, artifactmodel.PackageVersion{PackageID: p.ID, Version: version, Created: w.now})
	if err != nil {
		w.t.Fatal(err)
	}
	owner := "alpha"
	if repository == "aa-foreign" {
		owner = "beta"
	}
	ref := artifactsapi.IndexVersion{
		Package: artifactsapi.IndexPackage{
			Repository: artifactsapi.IndexRepository{Key: repository, Project: owner, Class: repo.Local, PackageType: kind, Environments: []string{"DEV"}},
			ID:         p.ID,
			Name:       name,
		},
		ID:      v.ID,
		Version: version,
	}
	for _, path := range paths {
		old := w.now.Add(-48 * time.Hour)
		it := artifactmodel.Item{RepoKey: repository, Created: old, Modified: old, Updated: old, Size: 7, Actual: kernel.Checksums{SHA256: "same-fixture-content"}}
		props := artifactmodel.Properties{"release": {"clean"}, "team": {"platform"}, "private": {"not-requested"}}
		if kind == repo.Maven && version == "1.0-SNAPSHOT" && strings.HasSuffix(path, "-9.pom") {
			it.Created = w.now
			props["hold"] = []string{"true"}
		}
		it.Path, it.Name, it.Depth, err = kernel.SplitItemPath(path)
		if err != nil {
			w.t.Fatal(err)
		}
		it, err = w.db.Items().Put(ctx, it)
		if err != nil {
			w.t.Fatal(err)
		}
		if err := w.db.Items().SetProperties(ctx, it.ID, props, old); err != nil {
			w.t.Fatal(err)
		}
		if err := w.db.Packages().LinkFile(ctx, artifactmodel.VersionFile{VersionID: v.ID, ItemID: it.ID}); err != nil {
			w.t.Fatal(err)
		}
		w.items = append(w.items, it)
	}
	return ref
}

func selectionInput(kind repo.PackageType) model.CleanupPolicy {
	stages := false
	return model.CleanupPolicy{Key: "selection-policy", ProjectKey: "alpha", Criteria: model.CleanupCriteria{
		PackageTypes: []string{string(kind)}, Repos: []string{"**"}, ExcludedRepos: []string{"*-safe"}, IncludedPackages: []string{"**"}, ExcludedPackages: []string{"keep"},
		IncludeAllStages: &stages, IncludedStages: []string{"dEv"}, KeepLastNVersions: 1, CreatedBeforeInDays: 1, LastDownloadedBeforeInDays: 1,
		IncludedProperties: map[string][]string{"release": {"clean"}, "team": {"platform"}}, ExcludedProperties: map[string][]string{"hold": {"true"}},
	}}
}

func collectSelection(t *testing.T, selection *cleanup.Selection, limit int) ([]artifactsapi.IndexFile, map[string]cleanup.Reason) {
	t.Helper()
	ctx := t.Context()
	var all []artifactsapi.IndexFile
	reasons := map[string]cleanup.Reason{}
	for rp := (kernel.Page{Limit: limit}); ; {
		repositories, nextRepo, err := selection.Repositories(ctx, rp)
		if err != nil {
			t.Fatal(err)
		}
		for _, repository := range repositories {
			for pp := (kernel.Page{Limit: limit}); ; {
				packages, nextPackage, err := selection.Packages(ctx, repository, pp)
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range packages {
					for vp := (kernel.Page{Limit: limit}); ; {
						versions, nextVersion, err := selection.Versions(ctx, p, vp)
						if err != nil {
							t.Fatal(err)
						}
						for _, v := range versions {
							for fp := (kernel.Page{Limit: limit}); ; {
								page, err := selection.Candidates(ctx, v, fp)
								if err != nil {
									t.Fatal(err)
								}
								if len(page.Items) > limit {
									t.Fatal("candidate page exceeded bound")
								}
								reasons[v.Version] = page.Reason
								all = append(all, page.Items...)
								if page.Next == "" {
									break
								}
								fp.After = page.Next
							}
						}
						if nextVersion == "" {
							break
						}
						vp.After = nextVersion
					}
				}
				if nextPackage == "" {
					break
				}
				pp.After = nextPackage
			}
		}
		if nextRepo == "" {
			break
		}
		rp.After = nextRepo
	}
	return all, reasons
}

func selectionContract(t *testing.T, newStore func(*testing.T) app.RouterStore) {
	t.Helper()
	for _, tc := range []struct {
		kind             repo.PackageType
		name             string
		versions         []string
		excluded, newest string
	}{
		{repo.Maven, "org.example:widget", []string{"1.0-SNAPSHOT", "2", "9", "10"}, "2", "10"},
		{repo.NPM, "@scope/widget", []string{"1.0.0-beta.1", "2.0.0", "9.0.0", "10.0.0"}, "2.0.0", "10.0.0"},
		{repo.Debian, "widget", []string{"1.0~rc1-1", "1.0-9", "1.0-10", "2:0.1-1"}, "1.0-9", "2:0.1-1"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			w := newSelectionWorld(t, newStore(t))
			ctx := t.Context()
			for _, class := range []repo.Class{repo.Remote, repo.Virtual} {
				c := repo.Config{Key: "aa-" + string(class), Class: class, PackageType: tc.kind, ProjectKey: "alpha", Environments: []string{"DEV"}}
				if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
					t.Fatal(err)
				}
			}
			for _, r := range []struct{ key, project string }{{"aa-foreign", "beta"}, {"alpha-main", "alpha"}, {"alpha-safe", "alpha"}} {
				w.repository(r.key, r.project, tc.kind)
			}
			paths := func(version string) []string {
				switch tc.kind {
				case repo.NPM:
					return []string{npm.CanonicalPath(tc.name, version)}
				case repo.Debian:
					return []string{"pool/main/w/widget/widget_" + version + "_amd64.deb", "pool/main/w/widget/widget_" + version + "_arm64.deb"}
				default:
					if version == "1.0-SNAPSHOT" {
						return []string{
							"org/example/widget/1.0-SNAPSHOT/widget-1.0-20261120.120000-9.pom",
							"org/example/widget/1.0-SNAPSHOT/widget-1.0-20261120.120000-10.pom",
							"org/example/widget/1.0-SNAPSHOT/widget-1.0-20261120.120000-10.jar",
						}
					}
					return []string{fmt.Sprintf("org/example/widget/%s/widget-%s.pom", version, version), fmt.Sprintf("org/example/widget/%s/widget-%s.jar", version, version)}
				}
			}
			var newest artifactsapi.IndexVersion
			for _, v := range tc.versions {
				ref := w.version("alpha-main", tc.kind, tc.name, v, paths(v))
				if v == tc.newest {
					newest = ref
				}
			}
			w.foreign = w.version("aa-foreign", tc.kind, tc.name, tc.versions[0], paths(tc.versions[0]))
			w.version("alpha-safe", tc.kind, tc.name, tc.versions[0], paths(tc.versions[0]))
			p := selectionInput(tc.kind)
			p.Criteria.ExcludedPackageVersions = []string{tc.excluded}
			_, err := w.service.Create(ctx, p.Key, p)
			if err != nil {
				t.Fatal(err)
			}
			beforeUsage, err := w.db.Items().Usage(ctx)
			if err != nil {
				t.Fatal(err)
			}
			beforeWork, err := w.db.WorkQueue().Stats(ctx)
			if err != nil {
				t.Fatal(err)
			}
			beforeItems := map[int64]artifactmodel.Item{}
			for _, it := range w.items {
				stored, err := w.db.Items().GetByID(ctx, it.ID)
				if err != nil {
					t.Fatal(err)
				}
				beforeItems[it.ID] = stored
			}
			selection, err := w.service.Select(ctx, p.Key)
			if err != nil {
				t.Fatal(err)
			}
			repositories, nextRepository, err := selection.Repositories(ctx, kernel.Page{Limit: 1})
			if err != nil || len(repositories) != 1 || repositories[0].Key != "alpha-main" || nextRepository != "" {
				t.Fatal("foreign, remote, virtual or excluded repositories consumed a page", repositories, nextRepository, err)
			}
			literal := selectionInput(tc.kind)
			literal.Key, literal.Criteria.Repos = "remote-policy", []string{"aa-remote"}
			if _, err := w.service.Create(ctx, literal.Key, literal); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("a literal remote cleanup policy was admitted", err)
			}
			rows, reasons := collectSelection(t, selection, 1)
			if len(rows) == 0 || reasons[tc.newest] != cleanup.ReasonProtected || reasons[tc.excluded] != cleanup.ReasonVersionExcluded {
				t.Fatalf("selection/reasons missing: %d %+v", len(rows), reasons)
			}
			eligibleVersions := slices.Clone(tc.versions)
			slices.Sort(eligibleVersions)
			var expectedPaths, actualPaths []string
			for _, v := range eligibleVersions {
				if v != tc.newest && v != tc.excluded {
					expectedPaths = append(expectedPaths, paths(v)...)
				}
			}
			for _, file := range rows {
				actualPaths = append(actualPaths, file.Path)
			}
			if !slices.Equal(actualPaths, expectedPaths) {
				t.Fatalf("ordered candidates=%v, want%v", actualPaths, expectedPaths)
			}
			for _, file := range rows {
				if file.Version.Package.Repository.Key != "alpha-main" || file.Version.Version == tc.newest || file.Version.Version == tc.excluded {
					t.Fatal("foreign/excluded/protected file selected", file.Path)
				}
				if _, ok := file.Properties["private"]; ok {
					t.Fatal("unrequested property disclosed")
				}
				original := beforeItems[file.ID]
				if file.Path != original.FullPath() || file.Actual != original.Actual || file.Size != original.Size || !file.Modified.Equal(original.Modified) {
					t.Fatal("candidate lost original item snapshot")
				}
			}
			// A supplied flag is descriptive; the index's current complete set
			// still protects the comparator-newest, even when the request lies.
			newest.Protected = false
			page, err := selection.Candidates(ctx, newest, kernel.Page{Limit: 1})
			if err != nil || page.Reason != cleanup.ReasonProtected || len(page.Items) != 0 {
				t.Fatal("forged protection flag admitted newest", err)
			}
			if _, err := selection.Candidates(ctx, w.foreign, kernel.Page{}); !errors.Is(err, kernel.ErrForbidden) {
				t.Fatal("foreign identity reached candidate explanation", err)
			}
			afterUsage, err := w.db.Items().Usage(ctx)
			if err != nil {
				t.Fatal(err)
			}
			afterWork, err := w.db.WorkQueue().Stats(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeUsage, afterUsage) || !reflect.DeepEqual(beforeWork, afterWork) {
				t.Fatal("selection changed usage or work state")
			}
			for id, before := range beforeItems {
				after, err := w.db.Items().GetByID(ctx, id)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("selection changed item bytes/metadata", err)
				}
			}
			// Returning the same ordered set is independent of page size.
			again, _ := collectSelection(t, selection, 2)
			if !slices.EqualFunc(rows, again, func(a, b artifactsapi.IndexFile) bool { return a.ID == b.ID && a.Path == b.Path }) {
				t.Fatal("ordered candidate set changed without mutation")
			}
		})
	}
}

func TestUnitPackageCleanupCandidateSelection(t *testing.T) {
	selectionContract(t, func(*testing.T) app.RouterStore { return inmem.New() })
	selectionChangesContract(t, func(*testing.T) app.RouterStore { return inmem.New() })
	selectionCredentialsContract(t, func(*testing.T) app.RouterStore { return inmem.New() })
}

func TestAccPackageCleanupCandidateSQLSelection(t *testing.T) {
	keys, err := crypt.New(bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			selectionContract(t, func(t *testing.T) app.RouterStore { return newStore(t) })
			selectionChangesContract(t, func(t *testing.T) app.RouterStore { return newStore(t) })
			selectionCredentialsContract(t, func(t *testing.T) app.RouterStore { return newStore(t) })
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}
