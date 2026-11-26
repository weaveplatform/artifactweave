package peers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/test/peers"
)

// TestAccPeerInstance is every instance process's entry point
// (peers.InstanceTest); in this process it returns at once.
func TestAccPeerInstance(t *testing.T) { peers.RunInstance(t) }

func TestAccPeersHarnessSQLite(t *testing.T) {
	t.Parallel()
	qualifyHarness(t, peers.SQLite)
}

func TestAccPeersHarnessPostgres(t *testing.T) {
	t.Parallel()
	qualifyHarness(t, peers.Postgres)
}

// qualifyHarness needs no peer feature: three instances with their own
// identities and base URLs reach each other at those base URLs through
// their egresses, a partition stops one pair's traffic while the third
// instance still reaches and is reached by both, a cut is one way only,
// healing restores the traffic, and the closed cluster leaves nothing
// behind.
func qualifyHarness(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 3, Driver: driver})
	owner := map[string]string{}
	ids := make([]string, len(c.Instances))
	for i, in := range c.Instances {
		status, id := in.Do(t, http.MethodGet, "/artifactory/api/system/service_id", "")
		if status != http.StatusOK || !strings.HasPrefix(id, "jfrt@") {
			t.Fatalf("%s service_id: %d %q", in.Name, status, id)
		}
		if other, taken := owner[id]; taken {
			t.Fatalf("%s and %s share the instance identity %s", other, in.Name, id)
		}
		owner[id], ids[i] = in.Name, id
		expect(t, in, http.MethodPut, "/artifactory/api/repositories/peer-local",
			`{"key":"peer-local","rclass":"local","packageType":"generic"}`, http.StatusOK)
		expect(t, in, http.MethodPut, "/artifactory/peer-local/whoami.txt", in.Name, http.StatusCreated)
	}
	reaches := func(i, j int) error {
		status, id, err := c.DoFrom(i, j, http.MethodGet, "/artifactory/api/system/service_id", "")
		if err != nil {
			return err
		}
		if status != http.StatusOK || id != ids[j] {
			return fmt.Errorf("answered %d %q, want %s", status, id, ids[j])
		}
		return nil
	}
	for i, from := range c.Instances {
		for j, to := range c.Instances {
			if i == j {
				continue
			}
			if err := reaches(i, j); err != nil {
				t.Fatalf("%s->%s: %v", from.Name, to.Name, err)
			}
			status, body, err := c.DoFrom(i, j, http.MethodGet, "/artifactory/api/storage/peer-local/whoami.txt", "")
			var info struct {
				URI string `json:"uri"`
			}
			if err != nil || status != http.StatusOK || json.Unmarshal([]byte(body), &info) != nil ||
				info.URI != to.URL+"/artifactory/api/storage/peer-local/whoami.txt" {
				t.Fatalf("%s->%s storage info: %d %s (%v), want uri under %s", from.Name, to.Name, status, body, err, to.URL)
			}
			// The instance's own server makes the next call: a generic remote
			// whose upstream is the other's base URL, reached through the
			// egress that is its platform default proxy.
			remote := fmt.Sprintf(`{"key":%q,"rclass":"remote","packageType":"generic","url":%q,`+
				`"username":%q,"password":%q,"assumedOfflinePeriodSecs":0,"missedRetrievalCachePeriodSecs":0}`,
				"from-"+to.Name, to.URL+"/artifactory/peer-local", peers.AdminUser, to.AdminPassword)
			expect(t, from, http.MethodPut, "/artifactory/api/repositories/from-"+to.Name, remote, http.StatusOK)
			if body := expect(t, from, http.MethodGet, "/artifactory/from-"+to.Name+"/whoami.txt", "", http.StatusOK); body != to.Name {
				t.Fatalf("%s fetched %q from %s, want %q", from.Name, body, to.Name, to.Name)
			}
		}
	}

	a, b, other := c.Instances[0], c.Instances[1], c.Instances[2]
	for _, in := range c.Instances {
		expect(t, in, http.MethodPut, "/artifactory/peer-local/later.txt", "later on "+in.Name, http.StatusCreated)
	}
	c.Partition(0, 1)
	for _, pair := range [][2]int{{0, 1}, {1, 0}} {
		if err := reaches(pair[0], pair[1]); err == nil {
			t.Fatalf("%s reached %s across the partition", c.Instances[pair[0]].Name, c.Instances[pair[1]].Name)
		}
	}
	// The instance's own server is cut too: its egress is its platform
	// default proxy.
	for _, pair := range [][2]*peers.Instance{{a, b}, {b, a}} {
		status, body := pair[0].Do(t, http.MethodGet, "/artifactory/from-"+pair[1].Name+"/later.txt", "")
		if status == http.StatusOK {
			t.Fatalf("%s fetched %q from %s across the partition", pair[0].Name, body, pair[1].Name)
		}
		t.Logf("%s's fetch from %s across the partition: %d %s", pair[0].Name, pair[1].Name, status, body)
	}
	// The partition is between a and b only: the third instance reaches both
	// and both reach it, and the scenario still reaches a and b at their
	// base URLs.
	for _, pair := range [][2]int{{0, 2}, {2, 0}, {1, 2}, {2, 1}} {
		if err := reaches(pair[0], pair[1]); err != nil {
			t.Fatalf("%s->%s during the partition: %v", c.Instances[pair[0]].Name, c.Instances[pair[1]].Name, err)
		}
	}
	if body := expect(t, a, http.MethodGet, "/artifactory/from-"+other.Name+"/later.txt", "", http.StatusOK); body != "later on "+other.Name {
		t.Fatalf("%s fetched %q from %s during the partition", a.Name, body, other.Name)
	}
	expect(t, b, http.MethodGet, "/artifactory/peer-local/later.txt", "", http.StatusOK)
	c.Heal(0, 1)
	for _, pair := range [][2]*peers.Instance{{a, b}, {b, a}} {
		if body := expect(t, pair[0], http.MethodGet, "/artifactory/from-"+pair[1].Name+"/later.txt", "", http.StatusOK); body != "later on "+pair[1].Name {
			t.Fatalf("%s fetched %q from %s after healing", pair[0].Name, body, pair[1].Name)
		}
	}
	// A cut is one way: a stops reaching b while b still reaches a.
	c.Cut(0, 1)
	if err := reaches(0, 1); err == nil {
		t.Fatalf("%s reached %s across a cut", a.Name, b.Name)
	}
	if err := reaches(1, 0); err != nil {
		t.Fatalf("%s->%s while %s is cut from %s: %v", b.Name, a.Name, a.Name, b.Name, err)
	}
	c.Restore(0, 1)
	if err := reaches(0, 1); err != nil {
		t.Fatalf("%s->%s after the cut was lifted: %v", a.Name, b.Name, err)
	}

	c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if left := c.Leftovers(ctx); len(left) != 0 {
		t.Fatalf("closed cluster left %q", left)
	}
}

func expect(t *testing.T, in *peers.Instance, method, path, body string, want int) string {
	t.Helper()
	status, data := in.Do(t, method, path, body)
	if status != want {
		t.Fatalf("%s %s on %s: %d %s, want %d", method, path, in.Name, status, data, want)
	}
	return data
}

// failingScenario makes TestAccPeersDeliberateFailure fail, in the process
// TestAccPeersFailurePrintsEachServerLog starts for it.
const failingScenario = "ARTIFACTWEAVE_PEERS_DELIBERATE_FAILURE"

// TestAccPeersFailurePrintsEachServerLog runs a scenario that fails after its
// instances started, in a process of its own so its failure is not this
// test's, and requires its output to carry each instance's server log.
func TestAccPeersFailurePrintsEachServerLog(t *testing.T) {
	t.Parallel()
	cmd, collect, err := peers.TestCommand(t.Context(), "TestAccPeersDeliberateFailure", failingScenario+"=1")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if collectErr := collect(); collectErr != nil {
		t.Fatalf("collect failing scenario coverage: %v", collectErr)
	}
	if err == nil {
		t.Fatalf("the deliberately failing scenario passed:\n%s", out)
	}
	for _, name := range []string{"peer1", "peer2"} {
		// Each log is printed under its own header, so the next header ends
		// it; the line serve logs once it listens proves it is the server's.
		_, log, found := strings.Cut(string(out), "peers: "+name+" server log (")
		log, _, _ = strings.Cut(log, "peers: peer")
		if !found || !strings.Contains(log, "artifactweave listening") {
			t.Fatalf("failing scenario's output lacks %s's server log:\n%s", name, out)
		}
	}
}

// TestAccPeersDeliberateFailure fails once its two instances are serving,
// only when TestAccPeersFailurePrintsEachServerLog runs it.
func TestAccPeersDeliberateFailure(t *testing.T) {
	if os.Getenv(failingScenario) == "" {
		return
	}
	peers.Start(t, peers.Options{Instances: 2})
	t.Error("deliberate failure: the harness must now print each instance's server log")
}
