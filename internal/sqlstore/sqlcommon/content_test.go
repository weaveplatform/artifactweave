package sqlcommon_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The contract suite proves behaviour; these tests prove that a database
// failure part-way through a content write surfaces as an error (and rolls
// the write back) instead of being lost. SQLite triggers make one chosen
// statement fail; dropping a table makes every statement on it fail.

func contentKeys(t *testing.T) *crypt.Keyring {
	t.Helper()
	k, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// openContent opens a migrated SQLite store at path.
func openContent(t *testing.T, path string, k *crypt.Keyring) *sqlcommon.Store {
	t.Helper()
	s, err := sqlite.Open(
		context.Background(),
		path,
		sqlite.Options{Options: sqlcommon.Options{Keyring: k}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func execSQL(t *testing.T, s *sqlcommon.Store, q string) {
	t.Helper()
	if _, err := s.DB().ExecContext(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// failBefore makes every op (INSERT, UPDATE or DELETE) on table fail.
func failBefore(t *testing.T, s *sqlcommon.Store, op, table string) {
	t.Helper()
	execSQL(t, s, "CREATE TRIGGER fail_"+table+" BEFORE "+op+" ON "+table+
		" BEGIN SELECT RAISE(ABORT, 'injected failure'); END")
}

// wantDriverError asserts a failure that is neither success nor a store
// sentinel the caller would mistake for a normal outcome.
func wantDriverError(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalid) {
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
	item := types.Item{RepoKey: "r", Name: "a/b.jar", Actual: types.Checksums{SHA256: "aa"}}

	cases := map[string]struct {
		setup func(*testing.T, *sqlcommon.Store)
		run   func(*sqlcommon.Store) error
	}{
		"create repository row": {
			func(t *testing.T, s *sqlcommon.Store) { failBefore(t, s, "INSERT", "repositories") },
			func(s *sqlcommon.Store) error { return s.Repos().Create(ctx, keyed) },
		},
		"clear repository mirrors": {
			func(t *testing.T, s *sqlcommon.Store) { execSQL(t, s, "DROP TABLE repository_keypairs") },
			func(s *sqlcommon.Store) error { return s.Repos().Create(ctx, keyed) },
		},
		"seal repository password": {
			func(t *testing.T, s *sqlcommon.Store) { failBefore(t, s, "INSERT", "repository_secrets") },
			func(s *sqlcommon.Store) error { return s.Repos().Create(ctx, remote) },
		},
		"mirror virtual members": {
			func(t *testing.T, s *sqlcommon.Store) { failBefore(t, s, "INSERT", "repository_members") },
			func(s *sqlcommon.Store) error { return s.Repos().Create(ctx, virtual) },
		},
		"mirror key pairs": {
			func(t *testing.T, s *sqlcommon.Store) { failBefore(t, s, "INSERT", "repository_keypairs") },
			func(s *sqlcommon.Store) error { return s.Repos().Create(ctx, keyed) },
		},
		"put item": {
			func(t *testing.T, s *sqlcommon.Store) { failBefore(t, s, "INSERT", "items") },
			func(s *sqlcommon.Store) error { _, err := s.Items().Put(ctx, item); return err },
		},
		"list items": {
			func(t *testing.T, s *sqlcommon.Store) { execSQL(t, s, "DROP TABLE items") },
			func(s *sqlcommon.Store) error {
				_, _, err := s.Items().Children(ctx, "r", false, "", store.Page{})
				return err
			},
		},
		"delete a repository's items": {
			func(t *testing.T, s *sqlcommon.Store) { execSQL(t, s, "DROP TABLE items") },
			func(s *sqlcommon.Store) error { _, err := s.Items().Delete(ctx, "r", false, ""); return err },
		},
		"find the item for properties": {
			func(t *testing.T, s *sqlcommon.Store) { execSQL(t, s, "DROP TABLE items") },
			func(s *sqlcommon.Store) error { _, err := s.Items().Properties(ctx, 1); return err },
		},
		"link a file to a missing items table": {
			func(t *testing.T, s *sqlcommon.Store) {
				p, err := s.Packages().Ensure(ctx, types.Package{RepoKey: "r", Name: "p"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Packages().
					PutVersion(ctx, types.PackageVersion{PackageID: p.ID, Version: "1"}); err != nil {
					t.Fatal(err)
				}
				execSQL(t, s, "DROP TABLE package_version_files")
				execSQL(t, s, "DROP TABLE item_properties")
				execSQL(t, s, "DROP TABLE item_stats")
				execSQL(t, s, "DROP TABLE items")
			},
			func(s *sqlcommon.Store) error {
				return s.Packages().LinkFile(ctx, types.VersionFile{VersionID: 1, ItemID: 1})
			},
		},
		"insert a property value": {
			func(t *testing.T, s *sqlcommon.Store) {
				if _, err := s.Items().Put(ctx, item); err != nil {
					t.Fatal(err)
				}
				failBefore(t, s, "INSERT", "item_properties")
			},
			func(s *sqlcommon.Store) error {
				return s.Items().SetProperties(ctx, 1, types.Properties{"a": {"1"}}, time.Now())
			},
		},
		"add trash entry": {
			func(t *testing.T, s *sqlcommon.Store) { failBefore(t, s, "INSERT", "trash") },
			func(s *sqlcommon.Store) error {
				_, err := s.Trash().
					Add(ctx, types.TrashEntry{Record: types.TrashRecord{Item: item}})
				return err
			},
		},
		"list trash": {
			func(t *testing.T, s *sqlcommon.Store) { execSQL(t, s, "DROP TABLE trash") },
			func(s *sqlcommon.Store) error { _, _, err := s.Trash().List(ctx, "", "", store.Page{}); return err },
		},
		"ensure package": {
			func(t *testing.T, s *sqlcommon.Store) { failBefore(t, s, "INSERT", "packages") },
			func(s *sqlcommon.Store) error {
				_, err := s.Packages().Ensure(ctx, types.Package{RepoKey: "r", Name: "p"})
				return err
			},
		},
		"list packages": {
			func(t *testing.T, s *sqlcommon.Store) {
				execSQL(t, s, "DROP TABLE package_version_files")
				execSQL(t, s, "DROP TABLE package_versions")
				execSQL(t, s, "DROP TABLE packages")
			},
			func(s *sqlcommon.Store) error { _, _, err := s.Packages().List(ctx, "r", store.Page{}); return err },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := openContent(t, filepath.Join(t.TempDir(), "aw.db"), contentKeys(t))
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
		run         func(*sqlcommon.Store, int64) error
	}{
		"read properties before a write": {"item_properties", "", func(s *sqlcommon.Store, id int64) error {
			return s.Items().SetProperties(ctx, id, types.Properties{"a": {"1"}}, time.Now())
		}},
		"clear properties": {"item_properties", "DELETE", func(s *sqlcommon.Store, id int64) error {
			return s.Items().SetProperties(ctx, id, types.Properties{"a": {"1"}}, time.Now())
		}},
		"remove a property": {"item_properties", "DELETE", func(s *sqlcommon.Store, id int64) error {
			return s.Items().PatchProperties(ctx, id, nil, []string{"a"}, time.Now())
		}},
		"set a property": {"item_properties", "DELETE", func(s *sqlcommon.Store, id int64) error {
			return s.Items().PatchProperties(ctx, id, types.Properties{"a": {"1"}}, nil, time.Now())
		}},
		"stamp the properties time": {"items", "UPDATE", func(s *sqlcommon.Store, id int64) error {
			return s.Items().PatchProperties(ctx, id, types.Properties{"a": {"1"}}, nil, time.Now())
		}},
		"record a download": {"item_stats", "", func(s *sqlcommon.Store, id int64) error {
			return s.Items().RecordDownload(ctx, id, store.Download{By: "a"})
		}},
	} {
		t.Run(name, func(t *testing.T) {
			s := openContent(t, filepath.Join(t.TempDir(), "aw.db"), contentKeys(t))
			it, err := s.Items().Put(ctx, types.Item{RepoKey: "r", Name: "a.jar"})
			if err != nil {
				t.Fatal(err)
			}
			// A row to delete, as a trigger fires only for the rows a
			// statement touches.
			if err := s.Items().
				SetProperties(ctx, it.ID, types.Properties{"a": {"0"}}, time.Now()); err != nil {
				t.Fatal(err)
			}
			if tc.fail != "" {
				failBefore(t, s, tc.fail, tc.table)
			} else {
				execSQL(t, s, "DROP TABLE "+tc.table)
			}
			wantDriverError(t, name, tc.run(s, it.ID))
		})
	}
}

func TestAccContentVersionFailures(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		op, table string
		run       func(*sqlcommon.Store, int64) error
	}{
		"store a version": {"INSERT", "package_versions", func(s *sqlcommon.Store, pkg int64) error {
			_, err := s.Packages().PutVersion(ctx, types.PackageVersion{PackageID: pkg, Version: "2"})
			return err
		}},
		"reorder versions": {"UPDATE", "package_versions", func(s *sqlcommon.Store, pkg int64) error {
			_, err := s.Packages().PutVersion(ctx, types.PackageVersion{PackageID: pkg, Version: "0.1"})
			return err
		}},
		"delete a version": {"DELETE", "package_versions", func(s *sqlcommon.Store, pkg int64) error {
			v, err := s.Packages().Version(ctx, pkg, "1")
			if err != nil {
				return err
			}
			return s.Packages().DeleteVersion(ctx, v.ID)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			s := openContent(t, filepath.Join(t.TempDir(), "aw.db"), contentKeys(t))
			p, err := s.Packages().
				Ensure(ctx, types.Package{RepoKey: "r", Type: repo.Maven, Name: "p"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Packages().
				PutVersion(ctx, types.PackageVersion{PackageID: p.ID, Version: "1"}); err != nil {
				t.Fatal(err)
			}
			failBefore(t, s, tc.op, tc.table)
			wantDriverError(t, name, tc.run(s, p.ID))
		})
	}
}

// TestAccContentUnreadableRows covers documents that no longer decode and
// sealed passwords read without the key that sealed them.
func TestAccContentUnreadableRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "aw.db")
	sealed := openContent(t, path, contentKeys(t))
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
		Add(ctx, types.TrashEntry{Record: types.TrashRecord{Item: types.Item{RepoKey: "r", Name: "a"}}})
	if err != nil {
		t.Fatal(err)
	}

	// The same database opened without the master key cannot read the
	// password, and says so rather than returning it sealed.
	plain := openContent(t, path, nil)
	if _, err := plain.Repos().Get(ctx, "rm"); !errors.Is(err, sqlcommon.ErrNoKeyring) {
		t.Fatalf("Get without the keyring = %v", err)
	}
	if _, err := plain.Repos().
		List(ctx, store.RepoFilter{Class: repo.Remote}); !errors.Is(
		err,
		sqlcommon.ErrNoKeyring,
	) {
		t.Fatalf("List without the keyring = %v", err)
	}

	execSQL(t, sealed, "UPDATE repositories SET doc = 'not json' WHERE key = 'broken'")
	execSQL(t, sealed, "UPDATE trash SET item_json = 'not json'")
	if _, err := sealed.Repos().Get(ctx, "broken"); err == nil {
		t.Fatal("Get decoded a broken document")
	}
	if _, err := sealed.Repos().List(ctx, store.RepoFilter{}); err == nil {
		t.Fatal("List decoded a broken document")
	}
	if _, err := sealed.Trash().Get(ctx, e.ID); err == nil {
		t.Fatal("Get decoded a broken trash record")
	}
}
