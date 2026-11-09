package sqlcommon_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The contract suite covers behaviour; these tests drive the enterprise
// stores' driver-failure paths, which a healthy database never takes:
// a closed pool, rows corrupted behind the store's back, and statements a
// trigger makes fail halfway through a transaction. They use SQLite because
// its dynamic typing lets a test store a value the column type forbids.

// entOpen opens a migrated SQLite store with a keyring.
func entOpen(t *testing.T) *sqlcommon.Store {
	t.Helper()
	k, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "aw.db"),
		sqlite.Options{Options: sqlcommon.Options{Keyring: k}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// entExec runs raw SQL against the store's database.
func entExec(t *testing.T, s *sqlcommon.Store, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := s.DB().ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// entFails asserts that every call returned an error.
func entFails(t *testing.T, calls map[string]error) {
	t.Helper()
	for name, err := range calls {
		if err == nil {
			t.Errorf("%s succeeded", name)
		}
	}
}

func entErr[V any](_ V, err error) error { return err }

func entErr3[V any](_ V, _ string, err error) error { return err }

// entSeed stores one record of every enterprise kind and returns the IDs
// the store assigned.
func entSeed(t *testing.T, s store.Store) (tracker, archived int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	b := types.ReleaseBundle{Name: "app", Version: "1", State: types.BundleOpen, Created: now}
	art := types.BundleArtifact{
		TargetPath: "a.jar",
		Props:      []types.PropertyValues{{Key: "k", Values: []string{"v"}}},
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Bundles().Create(ctx, b, []types.BundleArtifact{art}))
	must(
		s.Bundles().
			AddAudit(ctx, types.BundleRetentionAudit{BundleName: "app", BundleVersion: "1", Created: now}),
	)
	tr, err := s.Distribution().
		CreateTracker(ctx, types.DistributionTracker{BundleName: "app", BundleVersion: "1"},
			[]types.DistributionJob{{PeerInstanceID: "edge"}})
	must(err)
	it, err := s.ArchivedItems().AddItem(ctx, types.ArchivedItem{
		Item: types.Item{RepoKey: "libs", Path: ".", Name: "a.jar"}, ExpiresAt: now,
	})
	must(err)
	return tr.ID, it.ID
}

func TestAccEnterpriseClosedDatabase(t *testing.T) {
	s := entOpen(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	b, d := s.Bundles(), s.Distribution()
	ai := s.ArchivedItems()
	rb := types.ReleaseBundle{Name: "app", Version: "1"}
	tr := types.DistributionTracker{ID: 1}
	item := types.ArchivedItem{Item: types.Item{RepoKey: "r", Path: ".", Name: "n"}}
	entFails(t, map[string]error{
		"Bundles.Create":           b.Create(ctx, rb, nil),
		"Bundles.Get":              entErr(b.Get(ctx, "app", "1")),
		"Bundles.Update":           b.Update(ctx, rb),
		"Bundles.ReplaceArtifacts": b.ReplaceArtifacts(ctx, "app", "1", nil),
		"Bundles.Delete":           b.Delete(ctx, "app", "1"),
		"Bundles.List":             entErr3(b.List(ctx, store.BundleFilter{}, store.Page{})),
		"Bundles.Artifacts":        entErr(b.Artifacts(ctx, "app", "1")),
		"Bundles.SetKeep":          b.SetKeep(ctx, "app", "1", true),
		"Bundles.Kept":             entErr(b.Kept(ctx, "app", "1")),
		"Bundles.AddAudit":         b.AddAudit(ctx, types.BundleRetentionAudit{}),
		"Bundles.Audits":           entErr(b.Audits(ctx, "app")),
		"Bundles.PutExport":        b.PutExport(ctx, types.BundleExport{}),
		"Bundles.Export":           entErr(b.Export(ctx, "app", "1")),
		"Bundles.DeleteExport":     b.DeleteExport(ctx, "app", "1"),
		"Distribution.Create":      entErr(d.CreateTracker(ctx, tr, nil)),
		"Distribution.Tracker":     entErr(d.Tracker(ctx, 1)),
		"Distribution.Update":      d.UpdateTracker(ctx, tr),
		"Distribution.Trackers":    entErr(d.Trackers(ctx, "app", "1")),
		"Distribution.PutJob":      d.PutJob(ctx, types.DistributionJob{TrackerID: 1}),
		"Distribution.Jobs":        entErr(d.Jobs(ctx, 1)),
		"Distribution.JobsForPeer": entErr(
			d.JobsForPeer(ctx, "edge", []types.JobStatus{types.JobFailed}),
		),
		"Archive.AddItem":      entErr(ai.AddItem(ctx, item)),
		"Archive.Item":         entErr(ai.Item(ctx, 1)),
		"Archive.FindItems":    entErr(ai.FindItems(ctx, "r", "n")),
		"Archive.Items":        entErr3(ai.Items(ctx, store.ArchivedFilter{}, store.Page{})),
		"Archive.DeleteItem":   ai.DeleteItem(ctx, 1),
		"Archive.ExpiredItems": entErr(ai.ExpiredItems(ctx, time.Now(), 0)),
	})
}

func TestAccEnterpriseCorruptRows(t *testing.T) {
	s := entOpen(t)
	ctx := context.Background()
	tracker, archived := entSeed(t, s)
	b, d := s.Bundles(), s.Distribution()
	ai := s.ArchivedItems()
	// Text where a number, boolean or JSON document belongs.
	entExec(t, s,
		"UPDATE release_bundles SET notes = '{'",
		"UPDATE bundle_artifacts SET props = '{'",
		"UPDATE bundle_audits SET created_at = 'x'",
		"UPDATE distribution_trackers SET rules = '{'",
		"UPDATE distribution_jobs SET started_at = 'x'",
		"UPDATE archived_items SET item = '{'",
	)
	later := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	entFails(t, map[string]error{
		"Bundles.Get":           entErr(b.Get(ctx, "app", "1")),
		"Bundles.List":          entErr3(b.List(ctx, store.BundleFilter{}, store.Page{})),
		"Bundles.Artifacts":     entErr(b.Artifacts(ctx, "app", "1")),
		"Bundles.Audits":        entErr(b.Audits(ctx, "")),
		"Distribution.Tracker":  entErr(d.Tracker(ctx, tracker)),
		"Distribution.Trackers": entErr(d.Trackers(ctx, "", "")),
		"Distribution.Jobs":     entErr(d.Jobs(ctx, tracker)),
		"Archive.Item":          entErr(ai.Item(ctx, archived)),
		"Archive.Items":         entErr3(ai.Items(ctx, store.ArchivedFilter{}, store.Page{})),
		"Archive.ExpiredItems":  entErr(ai.ExpiredItems(ctx, later, 0)),
	})
	// A second round reaches the checks the first round's failures stop
	// short of.
	entExec(t, s,
		"UPDATE archived_items SET item = '{}', properties = '{'",
		"UPDATE bundle_artifacts SET props = NULL, size = 'x'",
	)
	entFails(t, map[string]error{
		"Archive.FindItems": entErr(ai.FindItems(ctx, "libs", "a.jar")),
		"Bundles.Artifacts": entErr(b.Artifacts(ctx, "app", "1")),
	})
}

func TestAccEnterpriseFailingStatements(t *testing.T) {
	s := entOpen(t)
	ctx := context.Background()
	tracker, _ := entSeed(t, s)
	b, d := s.Bundles(), s.Distribution()
	for _, table := range []string{
		"bundle_keeps", "bundle_exports", "distribution_trackers", "distribution_jobs",
	} {
		entExec(t, s, "CREATE TRIGGER fail_"+table+" BEFORE INSERT ON "+table+
			" BEGIN SELECT RAISE(ABORT, 'injected'); END")
	}
	entExec(t, s, "CREATE TRIGGER fail_artifacts BEFORE DELETE ON bundle_artifacts"+
		" BEGIN SELECT RAISE(ABORT, 'injected'); END")
	entFails(t, map[string]error{
		"Bundles.ReplaceArtifacts": b.ReplaceArtifacts(ctx, "app", "1", nil),
		"Bundles.SetKeep":          b.SetKeep(ctx, "app", "1", true),
		"Bundles.PutExport": b.PutExport(
			ctx,
			types.BundleExport{BundleName: "app", BundleVersion: "1"},
		),
		"Distribution.PutJob": d.PutJob(
			ctx,
			types.DistributionJob{TrackerID: tracker, PeerInstanceID: "x"},
		),
		"Distribution.Create": entErr(d.CreateTracker(ctx, types.DistributionTracker{}, nil)),
	})
	// The failed replace left the artifact list as it was.
	got, err := b.Artifacts(ctx, "app", "1")
	if err != nil || len(got) != 1 {
		t.Fatalf("Artifacts after a failed replace = %v, %v", got, err)
	}
}
