package peers

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

// AgeCleanupVersion establishes an old-data precondition for a native cleanup
// scenario without changing server clocks. It only dates already published
// indexed files and their public/private completed-read evidence; the scenario performs real HTTP
// downloads after this preparation, and verifies real cleanup/tombstones.
func (in *Instance) AgeCleanupVersion(t testing.TB, key, pkg, version string, at time.Time) {
	t.Helper()
	st := in.openStore(t)
	err := st.Tx(t.Context(), func(ctx context.Context) error {
		p, err := st.Packages().Find(ctx, key, pkg)
		if err != nil {
			return err
		}
		v, err := st.Packages().Version(ctx, p.ID, version)
		if err != nil {
			return err
		}
		files, err := st.Packages().Files(ctx, v.ID)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			t.Fatal("peers: native cleanup version has no indexed files")
		}
		for _, id := range files {
			item, err := st.Items().GetByID(ctx, id)
			if err != nil {
				return err
			}
			item.Created = at
			if _, err := st.Items().Put(ctx, item); err != nil {
				return err
			}
			stats, err := st.Items().Stats(ctx, id)
			if err != nil {
				return err
			}
			stats.LastDownloaded = at
			if !stats.RemoteLastDownloaded.IsZero() {
				stats.RemoteLastDownloaded = at
			}
			if err := st.Items().PutStats(ctx, id, stats); err != nil {
				return err
			}
		}
		// Fixture-only dating preserves admission identities and every active pin.
		// Public statistics are not a proxy for the hidden admission timestamp.
		if _, err := st.Exec(ctx, "date completed cleanup fixture admissions", "UPDATE package_versions SET admitted_at = ? WHERE id = ?", dialect.Micros(at), v.ID); err != nil {
			return err
		}
		if _, err := st.Exec(
			ctx,
			"date cleanup fixture repository admission fallback",
			"UPDATE artifact_cleanup_state SET admitted_at = ? WHERE repo_key = ?",
			dialect.Micros(at),
			key,
		); err != nil {
			return err
		}
		v.Created, v.LastDownloaded = at, at
		_, err = st.Packages().PutVersion(ctx, v)
		return err
	})
	if err != nil {
		t.Fatalf("peers: date %s's existing %s/%s/%s fixture: %v", in.Name, key, pkg, version, err)
	}
}

// CleanupRun reads only the persisted outcome of a run admitted through the
// native API. It neither invokes an executor nor changes the queue's lifecycle.
func (in *Instance) CleanupRun(t testing.TB, id int64) model.CleanupRun {
	t.Helper()
	run, err := in.openStore(t).Cleanup().Run(t.Context(), id)
	if err != nil {
		t.Fatalf("peers: read %s's native cleanup outcome: %v", in.Name, err)
	}
	return run
}

// CleanupSequence observes only this process's existing repository stream.
// A refused receiver run must not allocate outward deletion events.
func (in *Instance) CleanupSequence(t testing.TB, key string) int64 {
	t.Helper()
	st := in.openStore(t)
	id, err := st.Instance().ID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	seq, err := st.Outbox().LastSeq(t.Context(), id, "repo:"+key)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}
