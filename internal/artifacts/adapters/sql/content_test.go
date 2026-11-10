package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// The contract suite proves behaviour; these tests prove that a database
// failure part-way through a content write surfaces as an error (and rolls
// the write back) instead of being lost. SQLite triggers make one chosen
// statement fail; dropping a table makes every statement on it fail.

// wantDriverError asserts a failure that is neither success nor a store
// sentinel the caller would mistake for a normal outcome.
func wantDriverError(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil || errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("%s: want a database error, got %v", what, err)
	}
}

func TestAccContentWriteFailures(t *testing.T) {
	ctx := context.Background()
	virtual := repo.Config{
		Key:          "v",
		Class:        repo.Virtual,
		PackageType:  repo.Generic,
		Repositories: []string{"l"},
	}
	remote := repo.Config{
		Key:         "rm",
		Class:       repo.Remote,
		PackageType: repo.Generic,
		URL:         "https://x",
		Password:    "p",
	}
	keyed := repo.Config{
		Key:               "k",
		Class:             repo.Local,
		PackageType:       repo.Generic,
		PrimaryKeyPairRef: "kp",
	}
	item := model.Item{RepoKey: "r", Name: "a/b.jar", Actual: kernel.Checksums{SHA256: "aa"}}

	cases := map[string]struct {
		setup func(*testing.T, *artsql.Store)
		run   func(*artsql.Store) error
	}{
		"create repository row": {
			func(t *testing.T, s *artsql.Store) {
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "repositories"))
			},
			func(s *artsql.Store) error { return s.Repos().Create(ctx, keyed) },
		},
		"clear repository mirrors": {
			func(t *testing.T, s *artsql.Store) { sqltest.Exec(t, s.DB(), "DROP TABLE repository_keypairs") },
			func(s *artsql.Store) error { return s.Repos().Create(ctx, keyed) },
		},
		"seal repository password": {
			func(t *testing.T, s *artsql.Store) {
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "repository_secrets"))
			},
			func(s *artsql.Store) error { return s.Repos().Create(ctx, remote) },
		},
		"mirror virtual members": {
			func(t *testing.T, s *artsql.Store) {
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "repository_members"))
			},
			func(s *artsql.Store) error { return s.Repos().Create(ctx, virtual) },
		},
		"mirror key pairs": {
			func(t *testing.T, s *artsql.Store) {
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "repository_keypairs"))
			},
			func(s *artsql.Store) error { return s.Repos().Create(ctx, keyed) },
		},
		"put item": {
			func(t *testing.T, s *artsql.Store) { sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "items")) },
			func(s *artsql.Store) error { _, err := s.Items().Put(ctx, item); return err },
		},
		"list items": {
			func(t *testing.T, s *artsql.Store) { sqltest.Exec(t, s.DB(), "DROP TABLE items") },
			func(s *artsql.Store) error {
				_, _, err := s.Items().Children(ctx, "r", false, "", kernel.Page{})
				return err
			},
		},
		"delete a repository's items": {
			func(t *testing.T, s *artsql.Store) { sqltest.Exec(t, s.DB(), "DROP TABLE items") },
			func(s *artsql.Store) error { _, err := s.Items().Delete(ctx, "r", false, ""); return err },
		},
		"find the item for properties": {
			func(t *testing.T, s *artsql.Store) { sqltest.Exec(t, s.DB(), "DROP TABLE items") },
			func(s *artsql.Store) error { _, err := s.Items().Properties(ctx, 1); return err },
		},
		"link a file to a missing items table": {
			func(t *testing.T, s *artsql.Store) {
				p, err := s.Packages().Ensure(ctx, model.Package{RepoKey: "r", Name: "p"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Packages().
					PutVersion(ctx, model.PackageVersion{PackageID: p.ID, Version: "1"}); err != nil {
					t.Fatal(err)
				}
				sqltest.Exec(t, s.DB(), "DROP TABLE package_version_files")
				sqltest.Exec(t, s.DB(), "DROP TABLE item_properties")
				sqltest.Exec(t, s.DB(), "DROP TABLE item_stats")
				sqltest.Exec(t, s.DB(), "DROP TABLE items")
			},
			func(s *artsql.Store) error {
				return s.Packages().LinkFile(ctx, model.VersionFile{VersionID: 1, ItemID: 1})
			},
		},
		"insert a property value": {
			func(t *testing.T, s *artsql.Store) {
				if _, err := s.Items().Put(ctx, item); err != nil {
					t.Fatal(err)
				}
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "item_properties"))
			},
			func(s *artsql.Store) error {
				return s.Items().SetProperties(ctx, 1, model.Properties{"a": {"1"}}, time.Now())
			},
		},
		"add trash entry": {
			func(t *testing.T, s *artsql.Store) { sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "trash")) },
			func(s *artsql.Store) error {
				_, err := s.Trash().
					Add(ctx, model.TrashEntry{Record: model.TrashRecord{Item: item}})
				return err
			},
		},
		"list trash": {
			func(t *testing.T, s *artsql.Store) { sqltest.Exec(t, s.DB(), "DROP TABLE trash") },
			func(s *artsql.Store) error { _, _, err := s.Trash().List(ctx, "", "", kernel.Page{}); return err },
		},
		"ensure package": {
			func(t *testing.T, s *artsql.Store) {
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "packages"))
			},
			func(s *artsql.Store) error {
				_, err := s.Packages().Ensure(ctx, model.Package{RepoKey: "r", Name: "p"})
				return err
			},
		},
		"list packages": {
			func(t *testing.T, s *artsql.Store) {
				sqltest.Exec(t, s.DB(), "DROP TABLE package_version_files")
				sqltest.Exec(t, s.DB(), "DROP TABLE package_versions")
				sqltest.Exec(t, s.DB(), "DROP TABLE packages")
			},
			func(s *artsql.Store) error { _, _, err := s.Packages().List(ctx, "r", kernel.Page{}); return err },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := mustOpenFile(t, filepath.Join(t.TempDir(), "aw.db"), testKeys(t))
			tc.setup(t, s)
			wantDriverError(t, name, tc.run(s))
		})
	}
}

// TestAccContentPropertyAndStatsFailures covers the writes that run after the
// item is found. A property write reads the properties before and after it,
// so its own statements are failed by a trigger, which leaves the reads
// working; dropping the table would fail the first read instead.
func TestAccContentPropertyAndStatsFailures(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		table, fail string
		run         func(*artsql.Store, int64) error
	}{
		"read properties before a write": {"item_properties", "", func(s *artsql.Store, id int64) error {
			return s.Items().SetProperties(ctx, id, model.Properties{"a": {"1"}}, time.Now())
		}},
		"clear properties": {"item_properties", "DELETE", func(s *artsql.Store, id int64) error {
			return s.Items().SetProperties(ctx, id, model.Properties{"a": {"1"}}, time.Now())
		}},
		"remove a property": {"item_properties", "DELETE", func(s *artsql.Store, id int64) error {
			return s.Items().PatchProperties(ctx, id, nil, []string{"a"}, time.Now())
		}},
		"set a property": {"item_properties", "DELETE", func(s *artsql.Store, id int64) error {
			return s.Items().PatchProperties(ctx, id, model.Properties{"a": {"1"}}, nil, time.Now())
		}},
		"stamp the properties time": {"items", "UPDATE", func(s *artsql.Store, id int64) error {
			return s.Items().PatchProperties(ctx, id, model.Properties{"a": {"1"}}, nil, time.Now())
		}},
		"record a download": {"item_stats", "", func(s *artsql.Store, id int64) error {
			return s.Items().RecordDownload(ctx, id, port.Download{By: "a"})
		}},
	} {
		t.Run(name, func(t *testing.T) {
			s := mustOpenFile(t, filepath.Join(t.TempDir(), "aw.db"), testKeys(t))
			it, err := s.Items().Put(ctx, model.Item{RepoKey: "r", Name: "a.jar"})
			if err != nil {
				t.Fatal(err)
			}
			// A row to delete, as a trigger fires only for the rows a
			// statement touches.
			if err := s.Items().
				SetProperties(ctx, it.ID, model.Properties{"a": {"0"}}, time.Now()); err != nil {
				t.Fatal(err)
			}
			if tc.fail != "" {
				sqltest.Exec(t, s.DB(), sqltest.FailTrigger(tc.fail, tc.table))
			} else {
				sqltest.Exec(t, s.DB(), "DROP TABLE "+tc.table)
			}
			wantDriverError(t, name, tc.run(s, it.ID))
		})
	}
}

func TestAccContentVersionFailures(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		op, table string
		run       func(*artsql.Store, int64) error
	}{
		"store a version": {"INSERT", "package_versions", func(s *artsql.Store, pkg int64) error {
			_, err := s.Packages().PutVersion(ctx, model.PackageVersion{PackageID: pkg, Version: "2"})
			return err
		}},
		"reorder versions": {"UPDATE", "package_versions", func(s *artsql.Store, pkg int64) error {
			_, err := s.Packages().PutVersion(ctx, model.PackageVersion{PackageID: pkg, Version: "0.1"})
			return err
		}},
		"delete a version": {"DELETE", "package_versions", func(s *artsql.Store, pkg int64) error {
			v, err := s.Packages().Version(ctx, pkg, "1")
			if err != nil {
				return err
			}
			return s.Packages().DeleteVersion(ctx, v.ID)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			s := mustOpenFile(t, filepath.Join(t.TempDir(), "aw.db"), testKeys(t))
			p, err := s.Packages().
				Ensure(ctx, model.Package{RepoKey: "r", Type: repo.Maven, Name: "p"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Packages().
				PutVersion(ctx, model.PackageVersion{PackageID: p.ID, Version: "1"}); err != nil {
				t.Fatal(err)
			}
			sqltest.Exec(t, s.DB(), sqltest.FailTrigger(tc.op, tc.table))
			wantDriverError(t, name, tc.run(s, p.ID))
		})
	}
}

// TestAccContentUnreadableRows covers documents that no longer decode and
// sealed passwords read without the key that sealed them.
func TestAccContentUnreadableRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "aw.db")
	sealed := mustOpenFile(t, path, testKeys(t))
	remote := repo.Config{
		Key:         "rm",
		Class:       repo.Remote,
		PackageType: repo.Generic,
		URL:         "https://x",
		Password:    "p",
	}
	if err := sealed.Repos().Create(ctx, remote); err != nil {
		t.Fatal(err)
	}
	if err := sealed.Repos().
		Create(ctx, repo.Config{Key: "broken", Class: repo.Local}); err != nil {
		t.Fatal(err)
	}
	e, err := sealed.Trash().
		Add(ctx, model.TrashEntry{Record: model.TrashRecord{Item: model.Item{RepoKey: "r", Name: "a"}}})
	if err != nil {
		t.Fatal(err)
	}

	// The same database opened without the master key cannot read the
	// password, and says so rather than returning it sealed.
	plain := mustOpenFile(t, path, nil)
	if _, err := plain.Repos().Get(ctx, "rm"); !errors.Is(err, dialect.ErrNoKeyring) {
		t.Fatalf("Get without the keyring = %v", err)
	}
	if _, err := plain.Repos().
		List(ctx, port.RepoFilter{Class: repo.Remote}); !errors.Is(
		err,
		dialect.ErrNoKeyring,
	) {
		t.Fatalf("List without the keyring = %v", err)
	}

	sqltest.Exec(t, sealed.DB(), "UPDATE repositories SET doc = 'not json' WHERE key = 'broken'")
	sqltest.Exec(t, sealed.DB(), "UPDATE trash SET item_json = 'not json'")
	if _, err := sealed.Repos().Get(ctx, "broken"); err == nil {
		t.Fatal("Get decoded a broken document")
	}
	if _, err := sealed.Repos().List(ctx, port.RepoFilter{}); err == nil {
		t.Fatal("List decoded a broken document")
	}
	if _, err := sealed.Trash().Get(ctx, e.ID); err == nil {
		t.Fatal("Get decoded a broken trash record")
	}
}
