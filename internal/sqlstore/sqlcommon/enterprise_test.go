package sqlcommon_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/weaveplatform-artifact-registry/internal/crypt"
	"github.com/weaveplatform/weaveplatform-artifact-registry/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/weaveplatform-artifact-registry/internal/sqlstore/sqlite"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/types"
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
func entSeed(t *testing.T, s store.Store) (tracker, execution, archived int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	b := types.ReleaseBundle{Name: "app", Version: "1", State: types.BundleOpen, Created: now}
	art := types.BundleArtifact{TargetPath: "a.jar", Props: []types.PropertyValues{{Key: "k", Values: []string{"v"}}}}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Bundles().Create(ctx, b, []types.BundleArtifact{art}))
	must(s.Bundles().AddAudit(ctx, types.BundleRetentionAudit{BundleName: "app", BundleVersion: "1", Created: now}))
	tr, err := s.Distribution().CreateTracker(ctx, types.DistributionTracker{BundleName: "app", BundleVersion: "1"},
		[]types.DistributionJob{{PeerInstanceID: "edge"}})
	must(err)
	must(s.Archive().CreatePolicy(ctx, types.ArchivePolicy{Key: "p", Family: types.ArchiveSmartV2}))
	ex, err := s.Archive().CreateExecution(ctx, types.ArchiveExecution{Kind: types.ExecutionArchive})
	must(err)
	it, err := s.Archive().AddItem(ctx, types.ArchivedItem{
		Item: types.Item{RepoKey: "libs", Path: ".", Name: "a.jar"}, ExpiresAt: now,
	})
	must(err)
	must(s.Workers().Create(ctx, types.Worker{Key: "w", Action: "AFTER_CREATE"}))
	must(s.Workers().PutSecret(ctx, "w", "token", "s3cret"))
	must(s.Workers().AddExecution(ctx, types.WorkerExecution{ID: "e1", WorkerKey: "w", Started: now}))
	return tr.ID, ex.ID, it.ID
}

func TestEnterpriseClosedDatabase(t *testing.T) {
	s := entOpen(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	b, d, a, w := s.Bundles(), s.Distribution(), s.Archive(), s.Workers()
	rb := types.ReleaseBundle{Name: "app", Version: "1"}
	tr := types.DistributionTracker{ID: 1}
	ex := types.ArchiveExecution{ID: 1}
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
		"Distribution.JobsForPeer": entErr(d.JobsForPeer(ctx, "edge", []types.JobStatus{types.JobFailed})),
		"Archive.CreatePolicy":     a.CreatePolicy(ctx, types.ArchivePolicy{Key: "p"}),
		"Archive.Policy":           entErr(a.Policy(ctx, "p")),
		"Archive.UpdatePolicy":     a.UpdatePolicy(ctx, types.ArchivePolicy{Key: "p"}),
		"Archive.DeletePolicy":     a.DeletePolicy(ctx, "p"),
		"Archive.Policies":         entErr(a.Policies(ctx, types.ArchiveSmartV2)),
		"Archive.CreateExecution":  entErr(a.CreateExecution(ctx, ex)),
		"Archive.Execution":        entErr(a.Execution(ctx, 1)),
		"Archive.UpdateExecution":  a.UpdateExecution(ctx, ex),
		"Archive.Executions":       entErr3(a.Executions(ctx, store.ExecutionFilter{}, store.Page{})),
		"Archive.AddItem":          entErr(a.AddItem(ctx, item)),
		"Archive.Item":             entErr(a.Item(ctx, 1)),
		"Archive.FindItems":        entErr(a.FindItems(ctx, "r", "n")),
		"Archive.Items":            entErr3(a.Items(ctx, store.ArchivedFilter{}, store.Page{})),
		"Archive.DeleteItem":       a.DeleteItem(ctx, 1),
		"Archive.ExpiredItems":     entErr(a.ExpiredItems(ctx, time.Now(), 0)),
		"Workers.Create":           w.Create(ctx, types.Worker{Key: "w"}),
		"Workers.Get":              entErr(w.Get(ctx, "w")),
		"Workers.Update":           w.Update(ctx, types.Worker{Key: "w"}),
		"Workers.Delete":           w.Delete(ctx, "w"),
		"Workers.List":             entErr(w.List(ctx, store.WorkerFilter{})),
		"Workers.State":            entErr(w.State(ctx, "w")),
		"Workers.SetState":         w.SetState(ctx, "w", "k", "v"),
		"Workers.DeleteState":      w.DeleteState(ctx, "w", "k"),
		"Workers.PutSecret":        w.PutSecret(ctx, "w", "k", "v"),
		"Workers.Secrets":          entErr(w.Secrets(ctx, "w")),
		"Workers.DeleteSecret":     w.DeleteSecret(ctx, "w", "k"),
		"Workers.AddExecution":     w.AddExecution(ctx, types.WorkerExecution{ID: "e", WorkerKey: "w"}),
		"Workers.Execution":        entErr(w.Execution(ctx, "e")),
		"Workers.Executions":       entErr3(w.Executions(ctx, "", store.Page{})),
		"Workers.PruneExecutions":  entErr(w.PruneExecutions(ctx, time.Now())),
	})
}

func TestEnterpriseCorruptRows(t *testing.T) {
	s := entOpen(t)
	ctx := context.Background()
	tracker, execution, archived := entSeed(t, s)
	b, d, a, w := s.Bundles(), s.Distribution(), s.Archive(), s.Workers()
	if err := w.PutSecret(ctx, "w", "other", "x"); err != nil {
		t.Fatal(err)
	}
	// Text where a number, boolean or JSON document belongs.
	entExec(t, s,
		"UPDATE release_bundles SET notes = '{'",
		"UPDATE bundle_artifacts SET props = '{'",
		"UPDATE bundle_audits SET created_at = 'x'",
		"UPDATE distribution_trackers SET rules = '{'",
		"UPDATE distribution_jobs SET started_at = 'x'",
		"UPDATE archive_policies SET enabled = 'x'",
		"UPDATE archive_executions SET total = 'x'",
		"UPDATE archived_items SET item = '{'",
		"UPDATE workers SET filter_doc = '{'",
		"UPDATE worker_executions SET duration_ms = 'x'",
		// A value sealed for another row fails to open.
		"UPDATE worker_secrets SET value = (SELECT value FROM worker_secrets WHERE name = 'other')"+
			" WHERE name = 'token'",
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
		"Archive.Policy":        entErr(a.Policy(ctx, "p")),
		"Archive.Policies":      entErr(a.Policies(ctx, "")),
		"Archive.Execution":     entErr(a.Execution(ctx, execution)),
		"Archive.Executions":    entErr3(a.Executions(ctx, store.ExecutionFilter{}, store.Page{})),
		"Archive.Item":          entErr(a.Item(ctx, archived)),
		"Archive.Items":         entErr3(a.Items(ctx, store.ArchivedFilter{}, store.Page{})),
		"Archive.ExpiredItems":  entErr(a.ExpiredItems(ctx, later, 0)),
		"Workers.Get":           entErr(w.Get(ctx, "w")),
		"Workers.List":          entErr(w.List(ctx, store.WorkerFilter{})),
		"Workers.Secrets":       entErr(w.Secrets(ctx, "w")),
		"Workers.Execution":     entErr(w.Execution(ctx, "e1")),
		"Workers.Executions":    entErr3(w.Executions(ctx, "", store.Page{})),
	})
	// A second round reaches the checks the first round's failures stop
	// short of.
	entExec(t, s,
		"UPDATE archived_items SET item = '{}', properties = '{'",
		"UPDATE bundle_artifacts SET props = NULL, size = 'x'",
	)
	entFails(t, map[string]error{
		"Archive.FindItems": entErr(a.FindItems(ctx, "libs", "a.jar")),
		"Bundles.Artifacts": entErr(b.Artifacts(ctx, "app", "1")),
	})
}

func TestEnterpriseFailingStatements(t *testing.T) {
	s := entOpen(t)
	ctx := context.Background()
	tracker, _, _ := entSeed(t, s)
	b, d, w := s.Bundles(), s.Distribution(), s.Workers()
	for _, table := range []string{"bundle_keeps", "bundle_exports", "distribution_trackers",
		"distribution_jobs", "worker_state", "worker_secrets", "worker_executions"} {
		entExec(t, s, "CREATE TRIGGER fail_"+table+" BEFORE INSERT ON "+table+
			" BEGIN SELECT RAISE(ABORT, 'injected'); END")
	}
	entExec(t, s, "CREATE TRIGGER fail_artifacts BEFORE DELETE ON bundle_artifacts"+
		" BEGIN SELECT RAISE(ABORT, 'injected'); END")
	entFails(t, map[string]error{
		"Bundles.ReplaceArtifacts": b.ReplaceArtifacts(ctx, "app", "1", nil),
		"Bundles.SetKeep":          b.SetKeep(ctx, "app", "1", true),
		"Bundles.PutExport":        b.PutExport(ctx, types.BundleExport{BundleName: "app", BundleVersion: "1"}),
		"Distribution.PutJob":      d.PutJob(ctx, types.DistributionJob{TrackerID: tracker, PeerInstanceID: "x"}),
		"Distribution.Create":      entErr(d.CreateTracker(ctx, types.DistributionTracker{}, nil)),
		"Workers.SetState":         w.SetState(ctx, "w", "k", "v"),
		"Workers.PutSecret":        w.PutSecret(ctx, "w", "k", "v"),
		"Workers.AddExecution":     w.AddExecution(ctx, types.WorkerExecution{ID: "e2", WorkerKey: "w"}),
	})
	// The failed replace left the artifact list as it was.
	got, err := b.Artifacts(ctx, "app", "1")
	if err != nil || len(got) != 1 {
		t.Fatalf("Artifacts after a failed replace = %v, %v", got, err)
	}
}
