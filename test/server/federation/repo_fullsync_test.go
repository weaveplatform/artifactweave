package federation_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

// alone deploys path on in while it has no members, so the deploy enters
// no stream and only in holds it, then restores peer as its member.
func (in *streamInstance) alone(peer *streamInstance, path string) {
	in.t.Helper()
	in.members()
	in.call(http.MethodPut, "/artifactory/fed/"+path, "only "+path, http.StatusCreated)
	in.members(model.FederationMember{PeerInstanceID: peer.id, BaseURL: peer.url, RemoteRepoKey: "fed", Enabled: true})
}

// lastRun is the id of in's newest full sync, 0 before any.
func (in *streamInstance) lastRun() int64 {
	in.t.Helper()
	if runs := in.runs(); len(runs) > 0 {
		return runs[0].ID
	}
	return 0
}

// waitRun waits for in's newest full sync of the repository from origin,
// one started after the run since, to complete.
func (in *streamInstance) waitRun(origin *streamInstance, since int64) model.FullSyncRun {
	in.t.Helper()
	var found model.FullSyncRun
	eventually(in.t, func() string { return fmt.Sprintf("runs %+v", in.runs()) }, func() bool {
		for _, r := range in.runs() {
			if r.PeerInstanceID == origin.id && r.RepoKey == "fed" && r.ID > since {
				found = r
				return r.Status == retentionapi.RunCompleted
			}
		}
		return false
	})
	return found
}

func (in *streamInstance) created(path string) time.Time {
	in.t.Helper()
	it, err := in.runtime.Store.Items().Get(in.t.Context(), "fed", false, path)
	if err != nil {
		in.t.Fatal(err)
	}
	return it.Created
}

// testRepositoryFullSync runs the repository snapshot and reconciler
// between two real instances (FED-04): fullSync gives the member what it
// lacks, forceFullSync also discards what the member holds and this
// instance does not, and propagateCreatedTime decides whether an item
// created by a full sync keeps its creation time.
func testRepositoryFullSync(t *testing.T, driver string, dsn func() string) {
	a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
	a.federate(b)
	b.federate(a)
	a.call(http.MethodPut, "/artifactory/fed/shared/f.txt", "both", http.StatusCreated)
	eventually(t, func() string { return "f.txt never reached B" }, func() bool { return b.hasItem("shared/f.txt") })

	a.alone(b, "only-a.txt")
	b.alone(a, "only-b.txt")
	since := b.lastRun()
	if got := a.call(http.MethodPost, "/artifactory/api/federation/fullSync/fed", "", http.StatusOK); got != "{}\n" {
		t.Fatalf("fullSync answered %q", got)
	}
	if run := b.waitRun(a, since); run.Forced {
		t.Fatalf("B's run %+v, want a normal one", run)
	}
	// B takes what it lacks and keeps what A lacks; the full sync runs
	// from A towards its members only.
	for _, p := range []string{"shared/f.txt", "only-a.txt", "only-b.txt"} {
		if !b.hasItem(p) {
			t.Fatalf("B lacks %s after fullSync", p)
		}
	}
	if a.hasItem("only-b.txt") || len(a.runs()) != 0 {
		t.Fatalf("A resynchronised from B: runs %+v", a.runs())
	}
	// A wrote only-a.txt in no change of the stream: B stamps it as A's
	// snapshot did, A's at the time A last modified it.
	it, err := a.runtime.Store.Items().Get(t.Context(), "fed", false, "only-a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if sb, _ := b.stamp("only-a.txt"); sb.Origin != a.id || sb.Time.Wall != it.Modified.UnixMilli() {
		t.Fatalf("only-a.txt stamped %+v on B, want A's at %v", sb, it.Modified)
	}

	// The comparison tool is a registered deviation: isDry is refused, so
	// no comparison file is ever there to read.
	if got := a.call(http.MethodPost, "/artifactory/api/federation/fullSync/fed?isDry=true", "", http.StatusBadRequest); !strings.Contains(got, "isDry is not supported") {
		t.Fatalf("isDry answered %q", got)
	}
	a.call(http.MethodGet, "/artifactory/api/federation/compareResult/fed.json", "", http.StatusNotFound)

	// A forced full sync replaces B's copy with A's.
	b.alone(a, "diverged/x.txt")
	since = b.lastRun()
	a.call(http.MethodPost, "/artifactory/api/federation/forceFullSync/fed?mirror="+b.url, "", http.StatusOK)
	if run := b.waitRun(a, since); !run.Forced {
		t.Fatalf("B's run %+v, want a forced one", run)
	}
	for _, p := range []string{"diverged/x.txt", "diverged", "only-b.txt"} {
		if b.hasItem(p) {
			t.Fatalf("B kept %s, which A lacks, through a forced full sync", p)
		}
		if _, ok := b.stamp(p); ok {
			t.Fatalf("B kept the stamp of %s", p)
		}
	}
	for _, p := range []string{"shared/f.txt", "only-a.txt"} {
		if !b.hasItem(p) {
			t.Fatalf("B lost %s, which A holds", p)
		}
	}

	// Without propagateCreatedTime an item a full sync creates is created
	// when it arrives; with it, when it was created on A.
	for _, on := range []bool{false, true} {
		p := fmt.Sprintf("created-%t.txt", on)
		a.call(http.MethodPut, fmt.Sprintf("/artifactory/api/federation/propagateCreatedTime?enabled=%t", on), "", http.StatusOK)
		a.alone(b, p)
		time.Sleep(20 * time.Millisecond)
		since = b.lastRun()
		a.call(http.MethodPost, "/artifactory/api/federation/fullSync/fed?mirror="+b.url+"/fed", "", http.StatusOK)
		b.waitRun(a, since)
		if same := b.created(p).Equal(a.created(p)); same != on {
			t.Fatalf("propagateCreatedTime %t: created %v on A and %v on B", on, a.created(p), b.created(p))
		}
	}
}

func TestAccRepositoryFullSyncSQLite(t *testing.T) {
	t.Parallel()
	testRepositoryFullSync(t, "", func() string { return "" })
}

func TestAccRepositoryFullSyncPostgres(t *testing.T) {
	t.Parallel()
	dsn := sqltest.PostgresDSN(t)
	testRepositoryFullSync(t, "postgres", func() string { return sqltest.ScratchDSN(t, dsn) })
}
