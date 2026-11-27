package peers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/test/peers"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestAccFederatedCleanupNPMSQLite(t *testing.T)   { testFederatedCleanupNPM(t, peers.SQLite) }
func TestAccFederatedCleanupNPMPostgres(t *testing.T) { testFederatedCleanupNPM(t, peers.Postgres) }

// Native reads on two different members protect different old versions. The
// third member must be available before an eligible version can be removed.
func TestAccFederatedCleanupReceiverNPMSQLite(t *testing.T) {
	testFederatedCleanupNPMTopology(t, peers.SQLite, true)
}

func TestAccFederatedCleanupReceiverNPMPostgres(t *testing.T) {
	testFederatedCleanupNPMTopology(t, peers.Postgres, true)
}

func testFederatedCleanupNPM(t *testing.T, driver peers.Driver) {
	t.Helper()
	testFederatedCleanupNPMTopology(t, driver, false)
}

func testFederatedCleanupNPMTopology(t *testing.T, driver peers.Driver, receiver bool) {
	var c *peers.Cluster
	var all []*peers.Instance
	if receiver {
		c = peers.Start(t, peers.Options{Instances: 3, Driver: driver})
		all = c.Instances
		pair(t, all[0], all[1])
		pairReceiver(t, all[0], all[2])
		pairReceiver(t, all[1], all[2])
	} else {
		c, all = federation(t, driver)
	}
	a, b := all[0], all[1]
	const key = "fed-cleanup-npm"
	const registry = "/artifactory/api/npm/" + key + "/widget"
	versions := []string{"1.0.0", "2.0.0", "3.0.0", "4.0.0"}
	tarball := func(version string) string { return registry + "/-/widget-" + version + ".tgz" }
	stored := func(version string) string { return key + "/widget/-/widget-" + version + ".tgz" }
	if receiver {
		body := `{"rclass":"federated","packageType":"npm","members":[{"url":"` + member(b, key) + `"},{"url":"` + member(all[2], key) + `","mode":"receiver"}]}`
		do(t, a, http.MethodPut, "/artifactory/api/repositories/"+key, body, http.StatusOK)
		for _, in := range all {
			await(t, in, key, "receiver cleanup membership", func(cfg federatedRepo) bool { return len(cfg.Members) == 3 })
		}
	} else {
		federate(t, all, key, "npm")
	}
	for _, version := range versions {
		do(t, a, http.MethodPut, registry, support.NPMPublishBody("widget", version), http.StatusCreated)
		awaitFile(t, all, stored(version), string(support.NPMTarballOf("widget", version)))
	}
	old := time.Now().Add(-72 * time.Hour)
	for _, in := range all {
		for _, version := range versions {
			in.AgeCleanupVersion(t, key, "widget", version, old)
		}
	}
	read := func(in *peers.Instance, version string) {
		t.Helper()
		if got := expect(t, in, http.MethodGet, tarball(version), "", http.StatusOK); got != string(support.NPMTarballOf("widget", version)) {
			t.Fatal("native npm tarball changed")
		}
	}
	reader := b
	if receiver {
		reader = all[2]
	}
	read(reader, "1.0.0")
	read(a, "2.0.0")
	stat := func(in *peers.Instance, version string) int64 {
		t.Helper()
		body := expect(t, in, http.MethodGet, "/artifactory/api/storage/"+stored(version)+"?stats", "", http.StatusOK)
		var out struct {
			LastDownloaded int64 `json:"lastDownloaded"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return out.LastDownloaded
	}
	for _, control := range []struct {
		reader, other *peers.Instance
		version       string
	}{{reader, a, "1.0.0"}, {a, b, "2.0.0"}} {
		eventually(t, control.reader, "recorded native npm download", func() (bool, string) {
			at := stat(control.reader, control.version)
			return at > old.Add(48*time.Hour).UnixMilli(), fmt.Sprint(at)
		})
		if at := stat(control.other, control.version); at != old.UnixMilli() {
			t.Fatalf("other member's download=%d, want old=%d", at, old.UnixMilli())
		}
	}
	const policy = "/artifactory/api/cleanup/packages/policies/federated-npm"
	do(
		t,
		a,
		http.MethodPost,
		policy,
		`{"key":"federated-npm","searchCriteria":{"packageTypes":["npm"],"repos":["fed-cleanup-npm"],"includedPackages":["**"],"includedProjects":["default"],"keepLastNVersions":1,"lastDownloadedBeforeInDays":1}}`,
		http.StatusCreated,
	)
	do(t, a, http.MethodPost, policy+"/enablement", `{"enabled":true}`, http.StatusOK)
	run := func(in *peers.Instance, want int64) {
		t.Helper()
		status, body := in.Do(t, http.MethodPut, policy+"/run", "")
		var trigger struct {
			ID int64 `json:"triggerId"`
		}
		if status != http.StatusAccepted || json.Unmarshal([]byte(body), &trigger) != nil || trigger.ID <= 0 {
			t.Fatalf("native npm cleanup admission: %d %s", status, body)
		}
		eventually(t, in, "terminal native npm cleanup", func() (bool, string) {
			out := in.CleanupRun(t, trigger.ID)
			return out.Status.Terminal(), string(out.Status)
		})
		out := in.CleanupRun(t, trigger.ID)
		if out.Status != "COMPLETED" || out.CleanedItems != want {
			t.Fatalf("native npm cleanup status=%s selected=%d cleaned=%d want=%d", out.Status, out.TotalItems, out.CleanedItems, want)
		}
	}
	if receiver {
		r := all[2]
		before := r.CleanupSequence(t, key)
		do(
			t,
			r,
			http.MethodPost,
			policy,
			`{"key":"federated-npm","searchCriteria":{"packageTypes":["npm"],"repos":["fed-cleanup-npm"],"includedPackages":["**"],"includedProjects":["default"],"keepLastNVersions":1,"lastDownloadedBeforeInDays":1}}`,
			http.StatusCreated,
		)
		do(t, r, http.MethodPost, policy+"/enablement", `{"enabled":true}`, http.StatusOK)
		run(r, 0)
		if after := r.CleanupSequence(t, key); after != before {
			t.Fatalf("receiver cleanup originated events: before=%d after=%d", before, after)
		}
		for _, in := range all {
			for _, version := range versions {
				expect(t, in, http.MethodHead, tarball(version), "", http.StatusOK)
			}
		}
	}
	c.Cut(0, 2)
	run(a, 0)
	for _, in := range all {
		expect(t, in, http.MethodHead, tarball("3.0.0"), "", http.StatusOK)
	}
	c.Heal(0, 2)
	run(a, 1)
	for _, in := range all {
		eventually(t, in, "native npm tombstone", func() (bool, string) {
			status, body := in.Do(t, http.MethodHead, tarball("3.0.0"), "")
			if status != http.StatusOK && status != http.StatusNotFound {
				t.Fatalf("cleanup tombstone probe: HTTP %d: %s", status, body)
			}
			return status == http.StatusNotFound, fmt.Sprintf("%d %s", status, body)
		})
		for _, version := range []string{"1.0.0", "2.0.0", "4.0.0"} {
			read(in, version)
		}
		eventually(t, in, "npm packument after cleanup", func() (bool, string) {
			status, body := in.Do(t, http.MethodGet, registry, "")
			var doc struct {
				DistTags map[string]string          `json:"dist-tags"`
				Versions map[string]json.RawMessage `json:"versions"`
			}
			if status != http.StatusOK || json.Unmarshal([]byte(body), &doc) != nil {
				return false, fmt.Sprintf("%d %s", status, body)
			}
			return len(doc.Versions) == 3 && doc.Versions["1.0.0"] != nil && doc.Versions["2.0.0"] != nil && doc.Versions["4.0.0"] != nil && doc.Versions["3.0.0"] == nil &&
				doc.DistTags["latest"] == "4.0.0", body
		})
	}
}
