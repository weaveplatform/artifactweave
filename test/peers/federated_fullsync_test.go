package peers_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccFederatedFullSyncSQLite(t *testing.T) {
	t.Parallel()
	testFederatedFullSync(t, peers.SQLite)
}

func TestAccFederatedFullSyncPostgres(t *testing.T) {
	t.Parallel()
	testFederatedFullSync(t, peers.Postgres)
}

// retention is the scenario's federation.outbox_retention, the least the
// configuration accepts; the instances prune every ten seconds.
const retention = time.Minute

// created reads the creation time in's storage API gives p.
func created(t *testing.T, in *peers.Instance, p string) string {
	t.Helper()
	status, body := in.Do(t, http.MethodGet, "/artifactory/api/storage/"+p, "")
	var info struct {
		Created string `json:"created"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(body), &info) != nil || info.Created == "" {
		t.Fatalf("storage of %s on %s: %d %s", p, in.Name, status, body)
	}
	return info.Created
}

// migrated creates the generic repository key on all[0] holding file, then
// converts it to a federated one with the other instances as members. They
// hold no file until a full sync gives it them.
func migrated(t *testing.T, all []*peers.Instance, key, file string) {
	t.Helper()
	a := all[0]
	do(t, a, http.MethodPut, "/artifactory/api/repositories/"+key, `{"rclass":"local","packageType":"generic"}`, http.StatusOK)
	do(t, a, http.MethodPut, "/artifactory/"+key+"/"+file, "migrated", http.StatusCreated)
	time.Sleep(20 * time.Millisecond)
	do(t, a, http.MethodPost, "/artifactory/api/federation/migrate/"+key, "", http.StatusOK)
	do(t, a, http.MethodPost, "/artifactory/api/repositories/"+key, membersBody(all, key, nil), http.StatusOK)
	for _, in := range all {
		await(t, in, key, "every member", func(r federatedRepo) bool { return len(r.Members) == len(all) })
	}
}

// testFederatedFullSync runs FED-04's operations across three instances
// (#1932): members partitioned from the sender past the retention window,
// which therefore pruned what they never received and its tombstones,
// are repaired by a full sync and follow the stream again; a forced full
// sync discards what the sender lacks, on the one member ?mirror names or
// on every member; recovery and reconnect resume delivery; full sync keeps
// creation times only with propagateCreatedTime; and a removed member
// neither receives from nor sends to the others.
func testFederatedFullSync(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 3, Driver: driver, Retention: retention})
	all := c.Instances
	a, b, d := all[0], all[1], all[2]
	pair(t, a, b)
	pair(t, a, d)
	pair(t, b, d)
	federate(t, all, "fed", "generic")
	do(t, a, http.MethodPut, "/artifactory/fed/y.txt", "y", http.StatusCreated)
	awaitFile(t, all, "fed/y.txt", "y")

	// B and D are cut off from A while A deletes y and writes late.txt,
	// past the retention window: A prunes both entries and y's tombstone.
	c.Partition(0, 1)
	c.Partition(0, 2)
	do(t, a, http.MethodDelete, "/artifactory/fed/y.txt", "", http.StatusNoContent)
	do(t, a, http.MethodPut, "/artifactory/fed/late.txt", "late", http.StatusCreated)
	time.Sleep(retention + 25*time.Second)
	c.Heal(0, 1)
	c.Heal(0, 2)

	// A full sync gives D what it missed. A holds nothing of y any more,
	// so a normal full sync leaves D's copy.
	mirror := "?mirror=" + member(d, "fed")
	do(t, a, http.MethodPost, "/artifactory/api/federation/fullSync/fed"+mirror, "", http.StatusOK)
	awaitFile(t, []*peers.Instance{d}, "fed/late.txt", "late")
	for _, in := range []*peers.Instance{b, d} {
		if _, ok := file(t, in, "fed/y.txt"); !ok {
			t.Fatalf("%s lost y.txt without a forced full sync", in.Name)
		}
	}

	// A forced full sync discards it, on the member ?mirror names only,
	// then on every member.
	do(t, a, http.MethodPost, "/artifactory/api/federation/forceFullSync/fed?mirror="+b.URL, "", http.StatusOK)
	awaitFile(t, []*peers.Instance{b}, "fed/late.txt", "late")
	awaitFile(t, []*peers.Instance{b}, "fed/y.txt", "")
	time.Sleep(quiet)
	if _, ok := file(t, d, "fed/y.txt"); !ok {
		t.Fatalf("%s lost y.txt to a forced full sync of %s", d.Name, b.Name)
	}
	do(t, a, http.MethodPost, "/artifactory/api/federation/forceFullSync/fed", "", http.StatusOK)
	awaitFile(t, []*peers.Instance{d}, "fed/y.txt", "")
	for _, in := range all {
		if got, ok := file(t, in, "fed/late.txt"); !ok || got != "late" {
			t.Fatalf("%s lost late.txt: %q", in.Name, got)
		}
	}

	// The repaired members follow the stream both ways.
	do(t, a, http.MethodPut, "/artifactory/fed/after/a.txt", "after a", http.StatusCreated)
	do(t, d, http.MethodPut, "/artifactory/fed/after/d.txt", "after d", http.StatusCreated)
	awaitFile(t, all, "fed/after/a.txt", "after a")
	awaitFile(t, all, "fed/after/d.txt", "after d")

	// Recovery retries at once what A could not deliver to D; reconnect
	// does so for every repository.
	c.Partition(0, 2)
	do(t, a, http.MethodPut, "/artifactory/fed/recovered.txt", "recovered", http.StatusCreated)
	awaitFile(t, []*peers.Instance{b}, "fed/recovered.txt", "recovered")
	time.Sleep(quiet)
	c.Heal(0, 2)
	if status, body := a.Do(t, http.MethodPost, "/artifactory/api/federation/recovery?repoKey=fed", ""); status != http.StatusOK ||
		!strings.Contains(body, "fed") {
		t.Fatalf("recovery: %d %s", status, body)
	}
	awaitFile(t, []*peers.Instance{d}, "fed/recovered.txt", "recovered")
	if status, body := a.Do(t, http.MethodPost, "/artifactory/api/federation/reconnect", ""); status != http.StatusOK || body != "true" {
		t.Fatalf("reconnect: %d %q", status, body)
	}
	do(t, a, http.MethodPut, "/artifactory/api/federation/prioritization/fed?priorityLevel=HIGH", "", http.StatusOK)

	// Full sync carries creation times only with propagateCreatedTime.
	for _, on := range []bool{false, true} {
		key := map[bool]string{false: "mig-off", true: "mig-on"}[on]
		do(t, a, http.MethodPut, "/artifactory/api/federation/propagateCreatedTime?enabled="+map[bool]string{false: "false", true: "true"}[on], "", http.StatusOK)
		migrated(t, all, key, "m.txt")
		do(t, a, http.MethodPost, "/artifactory/api/federation/fullSync/"+key, "", http.StatusOK)
		awaitFile(t, all, key+"/m.txt", "migrated")
		want := created(t, a, key+"/m.txt")
		for _, in := range all[1:] {
			if got := created(t, in, key+"/m.txt"); (got == want) != on {
				t.Fatalf("propagateCreatedTime %t: %s created %s, %s %s", on, a.Name, want, in.Name, got)
			}
		}
	}

	// A removed member is gone from every member's configuration, and
	// nothing passes between it and them any more.
	if status, body := a.Do(t, http.MethodPost, "/artifactory/api/federation/removeMember", d.URL); status != http.StatusOK {
		t.Fatalf("removeMember: %d %s", status, body)
	}
	for _, in := range []*peers.Instance{a, b} {
		await(t, in, "fed", "no "+d.Name, func(r federatedRepo) bool {
			return !slices.Contains(r.urls(), member(d, "fed")) && len(r.Members) == 2
		})
	}
	do(t, a, http.MethodPut, "/artifactory/fed/removed/a.txt", "removed a", http.StatusCreated)
	do(t, d, http.MethodPut, "/artifactory/fed/removed/d.txt", "removed d", http.StatusCreated)
	awaitFile(t, []*peers.Instance{b}, "fed/removed/a.txt", "removed a")
	absent(t, []*peers.Instance{d}, "fed/removed/a.txt")
	absent(t, []*peers.Instance{a, b}, "fed/removed/d.txt")
}
