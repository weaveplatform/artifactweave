package sql_test

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
	relsql "github.com/weaveplatform/artifactweave/migration/releases/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/releases/core/model"
	"github.com/weaveplatform/artifactweave/migration/releases/core/port"
)

// The contract suite covers the bundle and distribution stores' behaviour;
// these drive their driver-failure paths, which a healthy database never
// takes: a closed pool, rows corrupted behind the store's back, and
// statements a trigger makes fail halfway through a transaction. They use
// SQLite because its dynamic typing lets a test store a value the column
// type forbids.

// bundleStore opens a SQLite store, sealed with the test keyring, that holds
// the releases module's tables alone and the bundle app 1 with the artifact
// a.jar, a retention audit and a tracker with a job for the peer edge. It
// returns the store and the tracker's ID.
func bundleStore(t *testing.T) (*relsql.Store, int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	s := relsql.New(sqltest.OpenSQLite(t, relsql.Module))
	b := model.ReleaseBundle{Name: "app", Version: "1", State: model.BundleOpen, Created: now}
	art := model.BundleArtifact{
		TargetPath: "a.jar",
		Props:      []model.PropertyValues{{Key: "k", Values: []string{"v"}}},
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Bundles().Create(ctx, b, []model.BundleArtifact{art}))
	must(
		s.Bundles().
			AddAudit(ctx, model.BundleRetentionAudit{BundleName: "app", BundleVersion: "1", Created: now}),
	)
	tr, err := s.Distribution().
		CreateTracker(ctx, model.DistributionTracker{BundleName: "app", BundleVersion: "1"},
			[]model.DistributionJob{{PeerInstanceID: "edge"}})
	must(err)
	return s, tr.ID
}

func TestAccReleasesClosedDatabase(t *testing.T) {
	s := relsql.New(sqltest.OpenSQLite(t, relsql.Module))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	b, d := s.Bundles(), s.Distribution()
	rb := model.ReleaseBundle{Name: "app", Version: "1"}
	tr := model.DistributionTracker{ID: 1}
	sqltest.Fails(t, map[string]error{
		"Bundles.Create":           b.Create(ctx, rb, nil),
		"Bundles.Get":              sqltest.Err(b.Get(ctx, "app", "1")),
		"Bundles.Update":           b.Update(ctx, rb),
		"Bundles.ReplaceArtifacts": b.ReplaceArtifacts(ctx, "app", "1", nil),
		"Bundles.Delete":           b.Delete(ctx, "app", "1"),
		"Bundles.List":             sqltest.Err3(b.List(ctx, port.BundleFilter{}, kernel.Page{})),
		"Bundles.Artifacts":        sqltest.Err(b.Artifacts(ctx, "app", "1")),
		"Bundles.SetKeep":          b.SetKeep(ctx, "app", "1", true),
		"Bundles.Kept":             sqltest.Err(b.Kept(ctx, "app", "1")),
		"Bundles.AddAudit":         b.AddAudit(ctx, model.BundleRetentionAudit{}),
		"Bundles.Audits":           sqltest.Err(b.Audits(ctx, "app")),
		"Bundles.PutExport":        b.PutExport(ctx, model.BundleExport{}),
		"Bundles.Export":           sqltest.Err(b.Export(ctx, "app", "1")),
		"Bundles.DeleteExport":     b.DeleteExport(ctx, "app", "1"),
		"Distribution.Create":      sqltest.Err(d.CreateTracker(ctx, tr, nil)),
		"Distribution.Tracker":     sqltest.Err(d.Tracker(ctx, 1)),
		"Distribution.Update":      d.UpdateTracker(ctx, tr),
		"Distribution.Trackers":    sqltest.Err(d.Trackers(ctx, "app", "1")),
		"Distribution.PutJob":      d.PutJob(ctx, model.DistributionJob{TrackerID: 1}),
		"Distribution.Jobs":        sqltest.Err(d.Jobs(ctx, 1)),
		"Distribution.JobsForPeer": sqltest.Err(
			d.JobsForPeer(ctx, "edge", []model.JobStatus{model.JobFailed}),
		),
	})
}

func TestAccReleasesCorruptRows(t *testing.T) {
	s, tracker := bundleStore(t)
	ctx := context.Background()
	b, d := s.Bundles(), s.Distribution()
	// Text where a number, boolean or JSON document belongs.
	sqltest.Exec(t, s.DB(),
		"UPDATE release_bundles SET notes = '{'",
		"UPDATE bundle_artifacts SET props = '{'",
		"UPDATE bundle_audits SET created_at = 'x'",
		"UPDATE distribution_trackers SET rules = '{'",
		"UPDATE distribution_jobs SET started_at = 'x'",
	)
	sqltest.Fails(t, map[string]error{
		"Bundles.Get":           sqltest.Err(b.Get(ctx, "app", "1")),
		"Bundles.List":          sqltest.Err3(b.List(ctx, port.BundleFilter{}, kernel.Page{})),
		"Bundles.Artifacts":     sqltest.Err(b.Artifacts(ctx, "app", "1")),
		"Bundles.Audits":        sqltest.Err(b.Audits(ctx, "")),
		"Distribution.Tracker":  sqltest.Err(d.Tracker(ctx, tracker)),
		"Distribution.Trackers": sqltest.Err(d.Trackers(ctx, "", "")),
		"Distribution.Jobs":     sqltest.Err(d.Jobs(ctx, tracker)),
	})
	// A second round reaches the checks the first round's failures stop
	// short of.
	sqltest.Exec(t, s.DB(), "UPDATE bundle_artifacts SET props = NULL, size = 'x'")
	sqltest.Fails(t, map[string]error{
		"Bundles.Artifacts": sqltest.Err(b.Artifacts(ctx, "app", "1")),
	})
}

func TestAccReleasesFailingStatements(t *testing.T) {
	s, tracker := bundleStore(t)
	ctx := context.Background()
	b, d := s.Bundles(), s.Distribution()
	for _, table := range []string{
		"bundle_keeps", "bundle_exports", "distribution_trackers", "distribution_jobs",
	} {
		sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", table))
	}
	sqltest.Exec(t, s.DB(), sqltest.FailTrigger("DELETE", "bundle_artifacts"))
	sqltest.Fails(t, map[string]error{
		"Bundles.ReplaceArtifacts": b.ReplaceArtifacts(ctx, "app", "1", nil),
		"Bundles.SetKeep":          b.SetKeep(ctx, "app", "1", true),
		"Bundles.PutExport": b.PutExport(
			ctx,
			model.BundleExport{BundleName: "app", BundleVersion: "1"},
		),
		"Distribution.PutJob": d.PutJob(
			ctx,
			model.DistributionJob{TrackerID: tracker, PeerInstanceID: "x"},
		),
		"Distribution.Create": sqltest.Err(d.CreateTracker(ctx, model.DistributionTracker{}, nil)),
	})
	// The failed replace left the artifact list as it was.
	got, err := b.Artifacts(ctx, "app", "1")
	if err != nil || len(got) != 1 {
		t.Fatalf("Artifacts after a failed replace = %v, %v", got, err)
	}
}
