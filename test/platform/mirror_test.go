package platform

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The multi-region scenarios run artifactweave's mirror over two regional
// stores (decision 0028). After the generic checks they show, on the real
// clouds, that every member holds a copy, that reads prefer the member in
// this instance's zone, that a drained member stops taking copies while
// reads go on, and that repair brings it back up to the redundancy.
func init() {
	for _, s := range []string{"gcp/multi-region", "azure/multi-region", "aws/multi-region"} {
		scenarioChecks[s] = []check{
			{"every member holds the blob", everyMemberHolds},
			{"redirect prefers the member in this instance's zone", zoneFirst},
			{"uploads and reads go on with a member drained", drainedMember},
			{"repair copies to the member once it is re-enabled", repairBackfills},
		}
	}
}

const drainedPath = "scenario/drained.bin"

// drainedFile is deployed while the zone's member is drained, so it starts
// with a copy on the other member only.
var drainedFile payload

// mirrorStatus is the part of GET /api/storageinfo's blobMirrors the checks
// read: the blob tier's members and its under-replicated count.
type mirrorStatus struct {
	UnderReplicated int64
	// Present is each member's count of present copies, by store name.
	Present      map[string]int64
	WriteEnabled map[string]bool
}

func storageStatus(t *testing.T, h *harness) mirrorStatus {
	t.Helper()
	resp, err := h.do(t, http.MethodGet, "/artifactory/api/storageinfo", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := drain(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("storageinfo: %d %s", resp.StatusCode, b)
	}
	var info struct {
		BlobMirrors []struct {
			Tier                 string `json:"tier"`
			UnderReplicatedBlobs int64  `json:"underReplicatedBlobs"`
			Members              []struct {
				Name         string `json:"name"`
				WriteEnabled bool   `json:"writeEnabled"`
				Replicas     map[string]struct {
					Count int64 `json:"count"`
				} `json:"replicas"`
			} `json:"members"`
		} `json:"blobMirrors"`
	}
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	for _, m := range info.BlobMirrors {
		if m.Tier != "blob" {
			continue
		}
		st := mirrorStatus{
			UnderReplicated: m.UnderReplicatedBlobs,
			Present:         map[string]int64{},
			WriteEnabled:    map[string]bool{},
		}
		for _, mem := range m.Members {
			st.Present[mem.Name] = mem.Replicas["present"].Count
			st.WriteEnabled[mem.Name] = mem.WriteEnabled
		}
		return st
	}
	t.Fatalf("storageinfo has no blob mirror: %s", b)
	return mirrorStatus{}
}

// optimize starts a repair pass (POST /api/system/storage/optimize).
func optimize(t *testing.T, h *harness) {
	t.Helper()
	resp, err := h.do(t, http.MethodPost, "/artifactory/api/system/storage/optimize", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := drain(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("optimize: %d %s", resp.StatusCode, b)
	}
}

// waitFor polls the mirror's status until ok holds, starting a repair pass
// every 15 seconds, for up to five minutes: a copy that missed the upload's
// quorum is finished by the member or by repair.
func waitFor(t *testing.T, h *harness, what string, ok func(mirrorStatus) bool) mirrorStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	nextRepair := time.Now()
	for {
		st := storageStatus(t, h)
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not after five minutes; present copies %v, under-replicated %d",
				what, st.Present, st.UnderReplicated)
		}
		if time.Now().After(nextRepair) {
			optimize(t, h)
			nextRepair = time.Now().Add(15 * time.Second)
		}
		time.Sleep(3 * time.Second)
	}
}

func mustMembers(t *testing.T, h *harness) []mirrorMember {
	t.Helper()
	ms := h.members(t)
	if len(ms) != 2 {
		t.Fatalf("the scenario's mirror_members output lists %d members, want 2", len(ms))
	}
	return ms
}

func everyMemberHolds(t *testing.T, h *harness) {
	ms := mustMembers(t, h)
	st := waitFor(t, h, "a copy on every member", func(st mirrorStatus) bool {
		return st.UnderReplicated == 0 && st.Present[ms[0].Store] >= 1 &&
			st.Present[ms[1].Store] >= 1
	})
	t.Logf("present copies %v", st.Present)
}

// zoneFirst: with both copies in place, the redirect signs on the member in
// this instance's zone (blob.mirror.zone), the first in read order.
func zoneFirst(t *testing.T, h *harness) {
	ms := mustMembers(t, h)
	loc := redirectLocation(t, h, nil)
	if !strings.HasPrefix(loc.Host+loc.Path, ms[0].SignedURLPrefix) {
		t.Fatalf(
			"redirected to %s, want the %s member (%s)",
			redact(loc),
			ms[0].Zone,
			ms[0].SignedURLPrefix,
		)
	}
}

// drainedMember restarts artifactweave with the zone's member drained
// (write_enabled false): a new upload succeeds on the other member alone,
// every file still reads, and the new file's redirect signs on the member
// that holds it.
func drainedMember(t *testing.T, h *harness) {
	ms := mustMembers(t, h)
	h.restart(t, "ARTIFACTWEAVE_BLOB_STORES_"+strings.ToUpper(ms[0].Store)+"_WRITE_ENABLED=false")
	if st := storageStatus(t, h); st.WriteEnabled[ms[0].Store] || !st.WriteEnabled[ms[1].Store] {
		t.Fatalf(
			"after the restart write_enabled is %v, want only %s drained",
			st.WriteEnabled,
			ms[0].Store,
		)
	}

	drainedFile.data = make([]byte, 6<<20)
	if _, err := rand.Read(drainedFile.data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(drainedFile.data)
	drainedFile.sha256 = hex.EncodeToString(sum[:])
	resp, err := h.do(
		t,
		http.MethodPut,
		"/artifactory/"+redirectRepo+"/"+drainedPath,
		drainedFile.data,
		http.Header{"X-Checksum-Sha256": {drainedFile.sha256}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if b := drain(t, resp); resp.StatusCode != http.StatusCreated {
		t.Fatalf("deploy with %s drained: %d %s", ms[0].Store, resp.StatusCode, b)
	}

	// The first file, with copies on both, still streams.
	resp, err = h.do(t, http.MethodGet, "/artifactory/"+streamRepo+"/"+artifactPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, resp); resp.StatusCode != http.StatusOK || !bytes.Equal(got, file.data) {
		t.Fatalf(
			"reading the first file with %s drained: %d, %d bytes",
			ms[0].Store,
			resp.StatusCode,
			len(got),
		)
	}

	// The new file is only on the other member, and its redirect says so.
	loc := redirectFor(t, h, drainedPath, drainedFile.sha256, nil)
	if !strings.HasPrefix(loc.Host+loc.Path, ms[1].SignedURLPrefix) {
		t.Fatalf("redirected to %s, want the %s member that holds the file (%s)",
			redact(loc), ms[1].Zone, ms[1].SignedURLPrefix)
	}
	resp = fetch(t, h, loc, nil)
	if got := drain(
		t,
		resp,
	); resp.StatusCode != http.StatusOK ||
		!bytes.Equal(got, drainedFile.data) {
		t.Fatalf("signed URL on %s: %d, %d bytes", ms[1].Store, resp.StatusCode, len(got))
	}

	st := storageStatus(t, h)
	if st.Present[ms[0].Store] != 1 {
		t.Errorf(
			"the drained %s holds %d copies, want 1: it must take no new copy",
			ms[0].Store,
			st.Present[ms[0].Store],
		)
	}
	t.Logf(
		"with %s drained: present copies %v, under-replicated %d",
		ms[0].Store,
		st.Present,
		st.UnderReplicated,
	)
}

// repairBackfills restarts artifactweave with both members writable: repair
// copies the file that only the other member holds back to the zone's
// member, after which its redirect signs there again.
func repairBackfills(t *testing.T, h *harness) {
	ms := mustMembers(t, h)
	h.restart(t)
	st := waitFor(t, h, "repair onto the re-enabled member", func(st mirrorStatus) bool {
		return st.UnderReplicated == 0 && st.Present[ms[0].Store] >= 2
	})
	t.Logf("after repair: present copies %v", st.Present)

	loc := redirectFor(t, h, drainedPath, drainedFile.sha256, nil)
	if !strings.HasPrefix(loc.Host+loc.Path, ms[0].SignedURLPrefix) {
		t.Fatalf("redirected to %s, want the %s member now that it holds the file (%s)",
			redact(loc), ms[0].Zone, ms[0].SignedURLPrefix)
	}
	resp := fetch(t, h, loc, nil)
	if got := drain(
		t,
		resp,
	); resp.StatusCode != http.StatusOK ||
		!bytes.Equal(got, drainedFile.data) {
		t.Fatalf(
			"signed URL on %s after repair: %d, %d bytes",
			ms[0].Store,
			resp.StatusCode,
			len(got),
		)
	}
}
