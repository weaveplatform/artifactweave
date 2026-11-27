package peers_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/test/peers"
)

func federationState(t *testing.T, in *peers.Instance, key string) string {
	t.Helper()
	status, body := in.Do(t, http.MethodGet, "/artifactory/api/federation/state", "")
	var rows []struct{ Key, Status string }
	if status != http.StatusOK || json.Unmarshal([]byte(body), &rows) != nil {
		t.Fatalf("state: %d %s", status, body)
	}
	for _, row := range rows {
		if row.Key == key {
			return row.Status
		}
	}
	return "missing"
}

func awaitFederationState(t *testing.T, in *peers.Instance, key, want string) {
	t.Helper()
	eventually(t, in, key+" state "+want, func() (bool, string) { state := federationState(t, in, key); return state == want, state })
}

func testFederatedMonitoring(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 3, Driver: driver, PeerProbeInterval: probeInterval})
	all := c.Instances
	a, b, d := all[0], all[1], all[2]
	pair(t, a, b)
	pair(t, a, d)
	pair(t, b, d)
	federate(t, all, "fed", "generic")
	const base = "/artifactory/api/federation/"
	for _, in := range all {
		awaitFederationState(t, in, "fed", "SYNCING")
		for _, answer := range []struct{ path, body string }{{"rtfs", "true"}, {"migration/status", "MIGRATION_COMPLETED"}} {
			status, header, body := in.DoHeader(t, http.MethodGet, base+answer.path, "", nil)
			if status != http.StatusOK || body != answer.body || !strings.HasPrefix(header.Get("Content-Type"), "text/plain") {
				t.Fatalf("%s on %s: %d %q %v", answer.path, in.Name, status, body, header)
			}
		}
	}
	// D can answer A's signed reads but cannot send its changed configuration
	// to either other member. This creates genuine drift without mutating a lab DB.
	c.Cut(2, 0)
	c.Cut(2, 1)
	do(t, d, http.MethodPost, "/artifactory/api/repositories/fed", `{"description":"drifted member"}`, http.StatusOK)
	await(t, d, "fed", "deliberately different configuration", func(r federatedRepo) bool { return r.Description == "drifted member" })
	eventually(t, a, "configuration drift", func() (bool, string) {
		status, body := a.Do(t, http.MethodGet, base+"inconsistentRepos", "")
		var keys []string
		if status != http.StatusOK || json.Unmarshal([]byte(body), &keys) != nil {
			return false, body
		}
		for _, key := range keys {
			if key == "fed" {
				return true, body
			}
		}
		return false, body
	})
	do(t, a, http.MethodPost, base+"configSync/fed", "", http.StatusOK)
	eventually(t, a, "configuration repaired", func() (bool, string) {
		status, body := a.Do(t, http.MethodGet, base+"inconsistentRepos", "")
		var keys []string
		return status == http.StatusOK && json.Unmarshal([]byte(body), &keys) == nil && len(keys) == 0, body
	})
	c.Restore(2, 0)
	c.Restore(2, 1)
	// Isolate D from both other members, so B cannot relay A's missing entry.
	c.Partition(0, 2)
	c.Partition(1, 2)
	do(t, a, http.MethodPut, "/artifactory/fed/partitioned.bin", "partitioned content", http.StatusCreated)
	awaitFederationState(t, a, "fed", "OUT_OF_SYNC")
	eventually(t, a, "partitioned member unavailable", func() (bool, string) {
		status, body := a.Do(t, http.MethodGet, base+"status/unavailableMirrors", "")
		var rows []struct{ LocalRepoKey, RemoteUrl string }
		if status != http.StatusOK || json.Unmarshal([]byte(body), &rows) != nil {
			return false, body
		}
		for _, row := range rows {
			if row.LocalRepoKey == "fed" && strings.HasPrefix(row.RemoteUrl, d.URL) {
				return true, body
			}
		}
		return false, body
	})
	eventually(t, a, "partitioned member lag", func() (bool, string) {
		status, body := a.Do(t, http.MethodGet, base+"status/mirrorsLag", "")
		var rows []struct {
			LocalRepoKey, RemoteUrl string
			LagInMS                 int64
		}
		if status != http.StatusOK || json.Unmarshal([]byte(body), &rows) != nil {
			return false, body
		}
		for _, row := range rows {
			if row.LocalRepoKey == "fed" && strings.HasPrefix(row.RemoteUrl, d.URL) && row.LagInMS > 0 {
				return true, body
			}
		}
		return false, body
	})
	c.Heal(0, 2)
	c.Heal(1, 2)
	do(t, a, http.MethodPost, base+"recovery?repoKey=fed", "", http.StatusOK)
	awaitFile(t, all, "fed/partitioned.bin", "partitioned content")
	for _, in := range all {
		awaitFederationState(t, in, "fed", "SYNCING")
	}
	// The full-sync request travels A -> B. Blocking B -> A holds its real
	// incoming snapshot open, so the durable running state can be observed.
	c.Cut(1, 0)
	do(t, a, http.MethodPost, base+"fullSync/fed?mirror="+member(b, "fed"), "", http.StatusOK)
	awaitFederationState(t, b, "fed", "FULL_SYNC_RUNNING")
	c.Restore(1, 0)
	do(t, b, http.MethodPost, base+"recovery?repoKey=fed", "", http.StatusOK)
	awaitFederationState(t, b, "fed", "SYNCING")
	do(t, a, http.MethodPost, "/artifactory/api/repositories/fed", membersBody(all, "fed", a), http.StatusOK)
	awaitFederationState(t, a, "fed", "DISABLED")
	do(t, a, http.MethodPost, "/artifactory/api/repositories/fed", membersBody(all, "fed", nil), http.StatusOK)
	awaitFederationState(t, a, "fed", "SYNCING")

	for _, in := range all {
		awaitFederationState(t, in, "fed", "SYNCING")
	}

	// Consume the receiver delivery route through the same status scenario.
	// Role is independent of health after the added receiver's full sync settles.
	members := make([]map[string]any, 0, len(all))
	for _, in := range all {
		row := map[string]any{"url": member(in, "fed"), "enabled": true}
		if in == d {
			row["mode"] = "receiver"
		}
		members = append(members, row)
	}
	body, err := json.Marshal(map[string]any{"members": members})
	if err != nil {
		t.Fatal(err)
	}
	do(t, a, http.MethodPost, "/artifactory/api/repositories/fed", string(body), http.StatusOK)
	eventually(t, a, "healthy receiver connection", func() (bool, string) {
		status, body := a.Do(t, http.MethodGet, base+"status/connectionStates/fed", "")
		var result struct {
			MemberConnections []struct {
				RemotePlatformURL string
				Mode              string
				Status            string
			}
		}
		if status != http.StatusOK || json.Unmarshal([]byte(body), &result) != nil || len(result.MemberConnections) != 2 {
			return false, body
		}
		for _, row := range result.MemberConnections {
			if strings.HasPrefix(row.RemotePlatformURL, d.URL) {
				return row.Mode == "Receiving_Only" && row.Status == "HEALTHY", body
			}
		}
		return false, body
	})
	// 7.161.26 returns a concrete empty list for missing and local keys.
	do(t, a, http.MethodPut, "/artifactory/api/repositories/local-monitor", `{"rclass":"local","packageType":"generic"}`, http.StatusOK)
	for _, key := range []string{"missing-monitor", "local-monitor"} {
		status, body := a.Do(t, http.MethodGet, base+"status/connectionStates/"+key, "")
		var result struct {
			MemberConnections []json.RawMessage
		}
		if status != http.StatusOK || json.Unmarshal([]byte(body), &result) != nil || result.MemberConnections == nil || len(result.MemberConnections) != 0 {
			t.Fatalf("connectionStates %s: %d %s", key, status, body)
		}
		status, body = a.DoAs(t, "", "", http.MethodGet, base+"status/connectionStates/"+key, "")
		if status != http.StatusUnauthorized {
			t.Fatalf("anonymous connectionStates %s: %d %s", key, status, body)
		}
	}
}

func TestAccFederatedMonitoringSQLite(t *testing.T) {
	t.Parallel()
	testFederatedMonitoring(t, peers.SQLite)
}

func TestAccFederatedMonitoringPostgres(t *testing.T) {
	t.Parallel()
	testFederatedMonitoring(t, peers.Postgres)
}
