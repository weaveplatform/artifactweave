package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob/mirror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type fakeMirrors struct {
	optimized int
	status    []MirrorStatus
	err       error
}

func (f *fakeMirrors) Optimize() { f.optimized++ }

func (f *fakeMirrors) Status(context.Context) ([]MirrorStatus, error) { return f.status, f.err }

func withMirrors(m Mirrors) func(*Config) { return func(c *Config) { c.Mirrors = m } }

func TestAccStorageInfoReportsMirrors(t *testing.T) {
	fm := &fakeMirrors{status: []MirrorStatus{{Tier: "blob", Status: mirror.Status{
		Redundancy: 2, WriteQuorum: 1, Zone: "home", UnderReplicated: 1,
		Members: []mirror.MemberStatus{
			{
				Name: "near", Zone: "home", Kind: "s3", WriteEnabled: true, Available: true,
				Latency: 12 * time.Millisecond,
				Replicas: []store.ReplicaCount{
					{Member: "near", State: types.ReplicaPresent, Count: 2, Bytes: 2048},
				},
				SoleCopies: 1,
			},
			{Name: "far", Kind: "gcs", Replicas: []store.ReplicaCount{
				{Member: "far", State: types.ReplicaPending, Count: 1, Bytes: 10},
			}},
		},
		Removed: []mirror.RemovedMember{{
			Name: "retired", SoleCopies: 3,
			Replicas: []store.ReplicaCount{
				{Member: "retired", State: types.ReplicaPresent, Count: 3, Bytes: 30},
			},
		}},
	}}}}
	h := statusFixture(t, false, withMirrors(fm))
	rec := do(t, h, http.MethodGet, "/artifactory/api/storageinfo", basic("admin", "pw"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body struct {
		BinariesSummary map[string]any `json:"binariesSummary"`
		BlobMirrors     []struct {
			Tier, Zone              string
			Redundancy, WriteQuorum int
			UnderReplicatedBlobs    int64
			Members                 []struct {
				Name, Zone, Backend     string
				WriteEnabled, Available bool
				AverageLatencyMs        int64
				SoleCopies              int64
				Replicas                map[string]struct {
					Count, Bytes int64
					Size         string
				}
			}
			RemovedMembers []struct {
				Name       string
				SoleCopies int64
				Replicas   map[string]struct{ Count int64 }
			}
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.BinariesSummary == nil || len(body.BlobMirrors) != 1 {
		t.Fatalf("the JFrog fields stay, the mirrors are added: %s", rec.Body)
	}
	m := body.BlobMirrors[0]
	near, far := m.Members[0], m.Members[1]
	if m.Tier != "blob" || m.Zone != "home" || m.Redundancy != 2 || m.WriteQuorum != 1 ||
		m.UnderReplicatedBlobs != 1 || len(m.Members) != 2 ||
		near.Name != "near" || near.Backend != "s3" || !near.WriteEnabled || !near.Available ||
		near.AverageLatencyMs != 12 || near.Replicas["present"].Count != 2 ||
		near.Replicas["present"].Size != "2.00 KB" || len(near.Replicas) != 4 ||
		far.Available || far.Replicas["pending"].Count != 1 || far.Replicas["failed"].Size != "0 bytes" ||
		near.SoleCopies != 1 || far.SoleCopies != 0 || len(m.RemovedMembers) != 1 ||
		m.RemovedMembers[0].Name != "retired" || m.RemovedMembers[0].SoleCopies != 3 ||
		m.RemovedMembers[0].Replicas["present"].Count != 3 || len(m.RemovedMembers[0].Replicas) != 4 {
		t.Fatalf("summary %s", rec.Body)
	}
	fm.err = errors.New("replica index down")
	rec = do(t, h, http.MethodGet, "/artifactory/api/storageinfo", basic("admin", "pw"), "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status error: %d %s", rec.Code, rec.Body)
	}
}

func TestAccStorageInfoWithoutMirrors(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(t, h, http.MethodGet, "/artifactory/api/storageinfo", basic("admin", "pw"), "")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["blobMirrors"]; ok || rec.Code != http.StatusOK {
		t.Fatalf("a summary without mirrors is exactly Artifactory's: %s", rec.Body)
	}
}

func TestAccOptimize(t *testing.T) {
	const path = "/artifactory/api/system/storage/optimize"
	rec := do(t, statusFixture(t, false), http.MethodPost, path, basic("admin", "pw"), "")
	if rec.Code != http.StatusOK ||
		rec.Body.String() != "No blob mirror is configured; nothing to optimize" {
		t.Fatalf("without mirrors: %d %q", rec.Code, rec.Body)
	}
	fm := &fakeMirrors{}
	h := statusFixture(t, false, withMirrors(fm))
	rec = do(t, h, http.MethodPost, path, basic("admin", "pw"), "")
	if rec.Code != http.StatusOK || rec.Body.String() != "Blob mirror repair started" ||
		fm.optimized != 1 {
		t.Fatalf("with mirrors: %d %q, optimized %d", rec.Code, rec.Body, fm.optimized)
	}
	if rec := do(
		t,
		h,
		http.MethodPost,
		path,
		basic("user", "pw"),
		"",
	); rec.Code != http.StatusForbidden ||
		fm.optimized != 1 {
		t.Fatalf("non-admin: %d, optimized %d", rec.Code, fm.optimized)
	}
}
