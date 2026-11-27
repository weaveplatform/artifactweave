package peers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccFederatedCleanupSQLite(t *testing.T)   { testFederatedCleanup(t, peers.SQLite) }
func TestAccFederatedCleanupPostgres(t *testing.T) { testFederatedCleanup(t, peers.Postgres) }

// Three independent processes record a native read only on the second member.
// An unavailable third member first prevents cleanup. Once reachable, ordinary
// native cleanup deletes only the other old version and propagates tombstones;
// every member serves the retained bytes and recomputed Maven metadata.
func testFederatedCleanup(t *testing.T, driver peers.Driver) {
	c, all := federation(t, driver)
	a, b := all[0], all[1]
	const key = "fed-cleanup"
	federate(t, all, key, "maven")
	for _, version := range []string{"1.0", "2.0", "3.0"} {
		base := "/artifactory/" + key + "/com/example/lib/" + version + "/lib-" + version
		pom := fmt.Sprintf(`<project><modelVersion>4.0.0</modelVersion><groupId>com.example</groupId><artifactId>lib</artifactId><version>%s</version></project>`, version)
		do(t, a, http.MethodPut, base+".pom", pom, http.StatusCreated)
		do(t, a, http.MethodPut, base+".jar", "jar-"+version, http.StatusCreated)
		awaitFile(t, all, key+"/com/example/lib/"+version+"/lib-"+version+".jar", "jar-"+version)
		awaitFile(t, all, key+"/com/example/lib/"+version+"/lib-"+version+".pom", pom)
	}
	old := time.Now().Add(-72 * time.Hour)
	for _, in := range all {
		for _, version := range []string{"1.0", "2.0", "3.0"} {
			in.AgeCleanupVersion(t, key, "com.example:lib", version, old)
		}
	}
	// This successful GET is not repeated on the initiator: its local old
	// statistics alone would wrongly permit deleting version 1.0.
	if body := expect(t, b, http.MethodGet, "/artifactory/"+key+"/com/example/lib/1.0/lib-1.0.jar", "", http.StatusOK); body != "jar-1.0" {
		t.Fatal("native member download returned different bytes")
	}
	readDownload := func(in *peers.Instance) int64 {
		t.Helper()
		body := expect(t, in, http.MethodGet, "/artifactory/api/storage/"+key+"/com/example/lib/1.0/lib-1.0.jar?stats", "", http.StatusOK)
		var stats struct {
			LastDownloaded int64 `json:"lastDownloaded"`
		}
		if err := json.Unmarshal([]byte(body), &stats); err != nil {
			t.Fatal("native download statistics are malformed", err)
		}
		return stats.LastDownloaded
	}
	eventually(t, b, "its recorded native download", func() (bool, string) {
		at := readDownload(b)
		return at > old.Add(48*time.Hour).UnixMilli(), fmt.Sprint(at)
	})
	if at := readDownload(a); at != old.UnixMilli() {
		t.Fatalf("initiator no longer has the old local statistic: got=%d want=%d", at, old.UnixMilli())
	}
	const policy = "/artifactory/api/cleanup/packages/policies/federated-native"
	body := `{"key":"federated-native","searchCriteria":{"packageTypes":["maven"],"repos":["fed-cleanup"],"includedPackages":["**"],"includedProjects":["default"],"keepLastNVersions":1,"lastDownloadedBeforeInDays":1}}`
	do(t, a, http.MethodPost, policy, body, http.StatusCreated)
	do(t, a, http.MethodPost, policy+"/enablement", `{"enabled":true}`, http.StatusOK)
	run := func(want int64) {
		t.Helper()
		status, body := a.Do(t, http.MethodPut, policy+"/run", "")
		var trigger struct {
			ID int64 `json:"triggerId"`
		}
		if status != http.StatusAccepted || json.Unmarshal([]byte(body), &trigger) != nil || trigger.ID <= 0 {
			t.Fatalf("native cleanup admission: %d %s", status, body)
		}
		eventually(t, a, "terminal native cleanup", func() (bool, string) {
			out := a.CleanupRun(t, trigger.ID)
			return out.Status.Terminal(), string(out.Status)
		})
		out := a.CleanupRun(t, trigger.ID)
		if out.Status != "COMPLETED" || out.CleanedItems != want {
			t.Fatalf("native cleanup status=%s selected=%d cleaned=%d want=%d", out.Status, out.TotalItems, out.CleanedItems, want)
		}
	}
	c.Cut(0, 2)
	run(0)
	// Presence is checked without a GET, preserving the old eligible version's
	// download precondition for the positive control after the cut heals.
	for _, in := range all {
		expect(t, in, http.MethodHead, "/artifactory/"+key+"/com/example/lib/2.0/lib-2.0.jar", "", http.StatusOK)
	}
	c.Heal(0, 2)
	run(2)
	for _, in := range all {
		eventually(t, in, "propagated cleanup tombstone", func() (bool, string) {
			status, body := in.Do(t, http.MethodHead, "/artifactory/"+key+"/com/example/lib/2.0/lib-2.0.jar", "")
			if status != http.StatusOK && status != http.StatusNotFound {
				t.Fatalf("cleanup tombstone probe: HTTP %d: %s", status, body)
			}
			return status == http.StatusNotFound, fmt.Sprintf("%d %s", status, body)
		})
		for _, version := range []string{"1.0", "3.0"} {
			if body := expect(t, in, http.MethodGet, "/artifactory/"+key+"/com/example/lib/"+version+"/lib-"+version+".jar", "", http.StatusOK); body != "jar-"+version {
				t.Fatal("retained native content changed")
			}
		}
		eventually(t, in, "recomputed Maven version metadata", func() (bool, string) {
			status, body := in.Do(t, http.MethodGet, "/artifactory/"+key+"/com/example/lib/maven-metadata.xml", "")
			return status == http.StatusOK && strings.Contains(body, "<version>1.0</version>") && strings.Contains(body, "<version>3.0</version>") &&
				!strings.Contains(body, "<version>2.0</version>"), body
		})
	}
}
