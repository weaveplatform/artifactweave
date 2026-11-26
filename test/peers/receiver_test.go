package peers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccFederatedReceiverSQLite(t *testing.T) {
	t.Parallel()
	testFederatedReceiver(t, peers.SQLite)
}

func TestAccFederatedReceiverPostgres(t *testing.T) {
	t.Parallel()
	testFederatedReceiver(t, peers.Postgres)
}

func pairReceiver(t *testing.T, issuer, receiver *peers.Instance) {
	t.Helper()
	status, body := issuer.Do(t, http.MethodPost, stPairing+"federated-repo/r", "")
	var tok struct {
		PairingToken string `json:"pairing_token"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(body), &tok) != nil {
		t.Fatalf("receiver token: %d %s", status, body)
	}
	do(t, receiver, http.MethodPut, stPairing+"federated-repo/r", tokenBody(tok.PairingToken), http.StatusOK)
}

// testFederatedReceiver sends changes from two senders to one /r-only
// receiver. An automatic full sync supplies content predating membership;
// local writes stay local and a normal sender full sync reconciles them.
func testFederatedReceiver(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 3, Driver: driver})
	a, b, r := c.Instances[0], c.Instances[1], c.Instances[2]
	all := c.Instances
	pair(t, a, b)
	pairReceiver(t, a, r)
	pairReceiver(t, b, r)
	do(t, a, http.MethodPut, "/artifactory/api/repositories/fed", `{"rclass":"local","packageType":"generic"}`, http.StatusOK)
	do(t, a, http.MethodPut, "/artifactory/fed/before.txt", "predates federation", http.StatusCreated)
	do(t, a, http.MethodPost, "/artifactory/api/federation/migrate/fed", "", http.StatusOK)
	body := `{"members":[{"url":"` + member(a, "fed") + `"},{"url":"` + member(b, "fed") + `"},{"url":"` + member(r, "fed") + `","mode":"receiver"}]}`
	do(t, a, http.MethodPost, "/artifactory/api/repositories/fed", body, http.StatusOK)
	for _, in := range all {
		await(t, in, "fed", "receiver membership", func(cfg federatedRepo) bool { return len(cfg.Members) == 3 })
	}
	awaitFile(t, []*peers.Instance{r}, "fed/before.txt", "predates federation")
	eventually(t, a, "healthy receiving-only connection", func() (bool, string) {
		status, body := a.Do(t, http.MethodGet, "/artifactory/api/federation/status/connectionStates/fed", "")
		var connections struct {
			MemberConnections []struct {
				Mode   string `json:"mode"`
				Status string `json:"status"`
				URL    string `json:"remotePlatformUrl"`
			} `json:"memberConnections"`
		}
		if status != http.StatusOK || json.Unmarshal([]byte(body), &connections) != nil {
			return false, body
		}
		for _, row := range connections.MemberConnections {
			if row.URL == r.URL+"/artifactory" {
				return row.Mode == "Receiving_Only" && row.Status == "HEALTHY", body
			}
		}
		return false, body
	})

	do(t, a, http.MethodPut, "/artifactory/fed/a.txt", "from a", http.StatusCreated)
	do(t, b, http.MethodPut, "/artifactory/fed/b.txt", "from b", http.StatusCreated)
	awaitFile(t, all, "fed/a.txt", "from a")
	awaitFile(t, all, "fed/b.txt", "from b")
	do(t, b, http.MethodPut, "/artifactory/api/storage/fed/a.txt?properties=source=sender", "", http.StatusNoContent)
	for _, in := range all {
		awaitReceiverProperty(t, in, "fed/a.txt", "sender")
	}
	do(t, a, http.MethodDelete, "/artifactory/fed/b.txt", "", http.StatusNoContent)
	awaitFile(t, all, "fed/b.txt", "")
	// A receiver accepts deploy/property/delete with their usual answers.
	do(t, r, http.MethodPut, "/artifactory/fed/local.txt", "only here", http.StatusCreated)
	do(t, r, http.MethodPut, "/artifactory/fed/a.txt", "local divergence", http.StatusCreated)
	do(t, r, http.MethodPut, "/artifactory/api/storage/fed/a.txt?properties=source=local", "", http.StatusNoContent)
	if got := receiverProperties(t, r, "fed/a.txt")["source"]; !slices.Equal(got, []string{"local"}) {
		t.Fatalf("receiver did not store its local property: %v", got)
	}
	awaitFile(t, []*peers.Instance{r}, "fed/a.txt", "local divergence")
	absent(t, []*peers.Instance{a, b}, "fed/local.txt")
	for _, in := range []*peers.Instance{a, b} {
		if got := receiverProperties(t, in, "fed/a.txt")["source"]; !slices.Equal(got, []string{"sender"}) {
			t.Fatalf("%s received the receiver's local property: %v", in.Name, got)
		}
	}
	awaitFile(t, []*peers.Instance{a, b}, "fed/a.txt", "from a")
	do(t, r, http.MethodDelete, "/artifactory/fed/before.txt", "", http.StatusNoContent)
	awaitFile(t, []*peers.Instance{a}, "fed/before.txt", "predates federation")
	for _, path := range []string{"fullSync/fed", "forceFullSync/fed"} {
		status, got := r.Do(t, http.MethodPost, "/artifactory/api/federation/"+path, "")
		if status != http.StatusBadRequest || !strings.Contains(got, "receiver mode") {
			t.Fatalf("receiver %s: %d %s", path, status, got)
		}
	}
	for _, patch := range []string{`{"members":[]}`, `{"members":[{"url":"` + member(r, "fed") + `"}]}`} {
		status, got := r.Do(t, http.MethodPost, "/artifactory/api/repositories/fed", patch)
		if status != http.StatusBadRequest || !strings.Contains(got, "receiver mode") {
			t.Fatalf("receiver member change: %d %s", status, got)
		}
	}
	// A signed event from the /r-only redeemer is forbidden, independently
	// of what a malicious sender might claim for mode in its own database.
	id := r.Identity(t)
	target := a.URL + "/artifactweave/peer/v1/streams/repo:fed/events"
	payload := `{"origin":"` + id.InstanceID + `","from_seq":1,"events":[{"seq":1,"hlc":"0000010000000000.00000000","event_type":"item.delete","payload":{"path":"a.txt"}}]}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := (corepeer.Signer{KeyID: id.InstanceID, Key: id.PrivateKey}).SignRequest(http.MethodPost, target, req.Header, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("/r-only push: %d %s %v", resp.StatusCode, data, err)
	}
	do(t, a, http.MethodPost, "/artifactory/api/federation/fullSync/fed?mirror="+r.URL, "", http.StatusOK)
	awaitFile(t, []*peers.Instance{r}, "fed/a.txt", "from a")
	awaitReceiverProperty(t, r, "fed/a.txt", "sender")
	awaitFile(t, []*peers.Instance{r}, "fed/local.txt", "")
	awaitFile(t, []*peers.Instance{r}, "fed/before.txt", "predates federation")
	// Mode persists in the repository JSON, while default members keep the
	// provider-compatible bidirectional representation.
	status, got := r.Do(t, http.MethodGet, "/artifactory/api/repositories/fed", "")
	if status != http.StatusOK || !strings.Contains(got, `"mode":"receiver"`) {
		t.Fatalf("mode roundtrip: %d %s", status, got)
	}
	// QUA-02 owns the permanent all-resource provider lane. A local
	// qualification can attach the narrow native refresh checker here,
	// while this scenario's isolated processes are still alive.
	if checker := os.Getenv("ARTIFACTWEAVE_RECEIVER_PROVIDER_CHECK"); checker != "" {
		cmd := exec.CommandContext(t.Context(), "python3", checker)
		cmd.Env = append(os.Environ(), "AW_RECEIVER_URL="+a.URL, "AW_RECEIVER_KEY=fed", "AW_RECEIVER_PASSWORD="+a.AdminPassword)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native receiver provider qualification: %v\n%s", err, output)
		} else {
			t.Log(string(output))
		}
	}
}

// receiverProperties requires a successful property read even when checking
// that a local receiver value did not propagate to another member.
func receiverProperties(t *testing.T, in *peers.Instance, path string) map[string][]string {
	t.Helper()
	got, present := readReceiverProperties(t, in, path)
	if !present {
		t.Fatalf("no properties of %s on %s", path, in.Name)
	}
	return got
}

func readReceiverProperties(t *testing.T, in *peers.Instance, path string) (map[string][]string, bool) {
	t.Helper()
	status, body := in.Do(t, http.MethodGet, "/artifactory/api/storage/"+path+"?properties", "")
	// Property replication may lag content. Only the documented no-properties
	// response is pending; an authentication or admission error is a failure.
	if status == http.StatusNotFound {
		var got struct {
			Errors []struct {
				Message string `json:"message"`
				Status  int    `json:"status"`
			} `json:"errors"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil || len(got.Errors) != 1 ||
			got.Errors[0].Status != http.StatusNotFound || got.Errors[0].Message != "No properties could be found." {
			t.Fatalf("unexpected properties response for %s on %s: %d %s", path, in.Name, status, body)
		}
		return nil, false
	}
	if status != http.StatusOK {
		t.Fatalf("properties of %s on %s: %d %s", path, in.Name, status, body)
	}
	var got struct {
		Properties map[string][]string `json:"properties"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Properties == nil {
		t.Fatalf("properties of %s on %s: %v: %s", path, in.Name, err, body)
	}
	return got.Properties, true
}

func awaitReceiverProperty(t *testing.T, in *peers.Instance, path, want string) {
	t.Helper()
	eventually(t, in, "source property "+want, func() (bool, string) {
		properties, present := readReceiverProperties(t, in, path)
		got := properties["source"]
		return present && slices.Equal(got, []string{want}), strings.Join(got, ",")
	})
}

func TestAccFederatedReceiverPullSQLite(t *testing.T) {
	t.Parallel()
	testFederatedReceiverPull(t, peers.SQLite)
}

func TestAccFederatedReceiverPullPostgres(t *testing.T) {
	t.Parallel()
	testFederatedReceiverPull(t, peers.Postgres)
}

// The sender cannot initiate any connection to the receiver after the cut.
// New bytes and properties therefore require signed /r pulls and blob fetches.
func testFederatedReceiverPull(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 2, Driver: driver})
	a, r := c.Instances[0], c.Instances[1]
	pairReceiver(t, a, r)
	body := `{"rclass":"federated","packageType":"generic","members":[{"url":"` +
		member(a, "fed") + `"},{"url":"` + member(r, "fed") + `","mode":"receiver"}]}`
	do(t, a, http.MethodPut, "/artifactory/api/repositories/fed", body, http.StatusOK)
	await(t, r, "fed", "receiver membership", func(cfg federatedRepo) bool { return len(cfg.Members) == 2 })
	do(t, a, http.MethodPut, "/artifactory/fed/before.txt", "before cut", http.StatusCreated)
	awaitFile(t, []*peers.Instance{r}, "fed/before.txt", "before cut")
	a.SetPeerDelivery(t, r.Identity(t).InstanceID, model.DeliveryPulled)
	r.SetPeerDelivery(t, a.Identity(t).InstanceID, model.DeliveryPull)
	c.Cut(0, 1)
	if _, _, err := c.DoFrom(0, 1, http.MethodGet, "/artifactory/api/system/ping", ""); err == nil {
		t.Fatal("sender can still reach receiver across the cut")
	}
	do(t, a, http.MethodPut, "/artifactory/fed/pull.txt", "created after cut", http.StatusCreated)
	awaitFile(t, []*peers.Instance{r}, "fed/pull.txt", "created after cut")
	do(t, a, http.MethodPut, "/artifactory/api/storage/fed/pull.txt?properties=source=pull", "", http.StatusNoContent)
	awaitReceiverProperty(t, r, "fed/pull.txt", "pull")
	do(t, a, http.MethodDelete, "/artifactory/fed/pull.txt", "", http.StatusNoContent)
	eventually(t, r, "pulled deletion", func() (bool, string) {
		status, body := r.Do(t, http.MethodGet, "/artifactory/fed/pull.txt", "")
		if status != http.StatusOK && status != http.StatusNotFound {
			t.Fatalf("pulled deletion read: %d %s", status, body)
		}
		return status == http.StatusNotFound, body
	})
}
