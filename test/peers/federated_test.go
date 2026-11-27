package peers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccFederatedMembersSQLite(t *testing.T) {
	t.Parallel()
	testFederatedMembers(t, peers.SQLite)
}

func TestAccFederatedMembersPostgres(t *testing.T) {
	t.Parallel()
	testFederatedMembers(t, peers.Postgres)
}

// syncWait bounds how long a configuration takes to reach a member. The
// work queue retries an unreachable member after 1, 2, 4, 8 and 16 seconds,
// so a member back within the scenario's short cut is reached well inside it.
const syncWait = 90 * time.Second

// federatedRepo is the part of a repository configuration the scenario
// reads back.
type federatedRepo struct {
	Key         string `json:"key"`
	Rclass      string `json:"rclass"`
	Description string `json:"description"`
	Members     []struct {
		URL     string `json:"url"`
		Enabled bool   `json:"enabled"`
	} `json:"members"`
}

func (r federatedRepo) urls() []string {
	out := make([]string, 0, len(r.Members))
	for _, m := range r.Members {
		out = append(out, m.URL)
	}
	slices.Sort(out)
	return out
}

// readRepo returns in's configuration of key, ok false while it has none.
func readRepo(t *testing.T, in *peers.Instance, key string) (federatedRepo, bool) {
	t.Helper()
	status, body := in.Do(t, http.MethodGet, "/artifactory/api/repositories/"+key, "")
	if status == http.StatusBadRequest || status == http.StatusNotFound {
		return federatedRepo{}, false
	}
	var r federatedRepo
	if status != http.StatusOK || json.Unmarshal([]byte(body), &r) != nil {
		t.Fatalf("GET %s on %s: %d %s", key, in.Name, status, body)
	}
	return r, true
}

// await polls in's configuration of key until ok accepts it.
func await(t *testing.T, in *peers.Instance, key, what string, ok func(federatedRepo) bool) federatedRepo {
	t.Helper()
	deadline := time.Now().Add(syncWait)
	for {
		r, found := readRepo(t, in, key)
		if found && ok(r) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never had %s %s: last %+v", in.Name, key, what, r)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// pair gives from and to two-way federated repository trust: a token from
// from, redeemed on to.
func pair(t *testing.T, from, to *peers.Instance) {
	t.Helper()
	status, body := from.Do(t, http.MethodPost, stPairing+"federated-repo/*", "")
	var tok struct {
		PairingToken string `json:"pairing_token"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(body), &tok) != nil {
		t.Fatalf("token from %s: %d %s", from.Name, status, body)
	}
	if status, body := to.Do(t, http.MethodPut, stPairing+"federated-repo/*", tokenBody(tok.PairingToken)); status != http.StatusOK {
		t.Fatalf("redeem on %s: %d %s", to.Name, status, body)
	}
}

func member(in *peers.Instance, key string) string { return in.URL + "/artifactory/" + key }

// testFederatedMembers federates one repository across three paired
// instances (#1930): a create on one reaches the others as their own
// copies, each listing every member with itself resolved; an update reaches
// a member cut off from the sender once the cut heals; configSync repairs
// what a member changed; and a member that is not a paired peer is refused.
func testFederatedMembers(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 3, Driver: driver})
	a, b, d := c.Instances[0], c.Instances[1], c.Instances[2]
	pair(t, a, b)
	pair(t, a, d)
	pair(t, b, d)
	refuseInvalidFederatedMembership(t, c.Instances)

	// A member that is not a paired peer is refused before anything is stored.
	unpaired := `{"rclass":"federated","packageType":"generic","members":[{"url":"http://127.0.0.1:9/artifactory/fed"}]}`
	if status, body := a.Do(t, http.MethodPut, "/artifactory/api/repositories/fed", unpaired); status != http.StatusBadRequest ||
		!strings.Contains(body, "Federated member 'http://127.0.0.1:9/artifactory/fed' is not a paired peer: pair the instances for federated-repo/* first") {
		t.Fatalf("unpaired member: %d %s", status, body)
	}
	if _, found := readRepo(t, a, "fed"); found {
		t.Fatal("a refused federated repository was stored")
	}

	create := `{"rclass":"federated","packageType":"generic","description":"one","members":[` +
		`{"url":"` + member(b, "fed") + `","enabled":true},{"url":"` + member(d, "fed") + `/","enabled":true}]}`
	if status, body := a.Do(t, http.MethodPut, "/artifactory/api/repositories/fed", create); status != http.StatusOK {
		t.Fatalf("create on %s: %d %s", a.Name, status, body)
	}
	all := []string{member(a, "fed"), member(b, "fed"), member(d, "fed")}
	slices.Sort(all)
	for _, in := range []*peers.Instance{a, b, d} {
		r := await(t, in, "fed", "every member", func(r federatedRepo) bool {
			return r.Rclass == "federated" && r.Description == "one" && slices.Equal(r.urls(), all)
		})
		if r.Key != "fed" {
			t.Fatalf("%s's copy is %+v", in.Name, r)
		}
	}

	// A member the sender cannot reach gets the update once it can.
	c.Cut(0, 2)
	if status, body := a.Do(t, http.MethodPost, "/artifactory/api/repositories/fed", `{"description":"two"}`); status != http.StatusOK {
		t.Fatalf("update on %s: %d %s", a.Name, status, body)
	}
	await(t, b, "fed", "the update", func(r federatedRepo) bool { return r.Description == "two" })
	time.Sleep(3 * time.Second)
	if r, _ := readRepo(t, d, "fed"); r.Description != "one" {
		t.Fatalf("%s received across the cut: %+v", d.Name, r)
	}
	c.Restore(0, 2)
	await(t, d, "fed", "the update after the cut", func(r federatedRepo) bool { return r.Description == "two" })

	// A change on any member reaches the others, and configSync sends the
	// sender's configuration again.
	if status, body := d.Do(t, http.MethodPost, "/artifactory/api/repositories/fed", `{"description":"three"}`); status != http.StatusOK {
		t.Fatalf("update on %s: %d %s", d.Name, status, body)
	}
	for _, in := range []*peers.Instance{a, b} {
		await(t, in, "fed", "the member's update", func(r federatedRepo) bool { return r.Description == "three" })
	}
	if status, body := a.Do(t, http.MethodPost, "/artifactory/api/federation/configSync/fed", ""); status != http.StatusOK ||
		strings.TrimSpace(body) != "Configuration sync executed successfully" {
		t.Fatalf("configSync: %d %s", status, body)
	}
	if status, body := a.DoAs(t, "", "", http.MethodPost, "/artifactory/api/federation/configSync/fed", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous configSync: %d %s", status, body)
	}
}

// These refusals must survive the real HTTP, SQL and process boundary and
// leave repository configuration unchanged, not merely fail model validation.
func refuseInvalidFederatedMembership(t *testing.T, all []*peers.Instance) {
	t.Helper()
	a := all[0]
	const repositories = "/artifactory/api/repositories/"
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = fmt.Sprintf(`{"url":%q}`, member(all[1], fmt.Sprintf("limit-%d", i)))
	}
	for _, tc := range []struct{ key, body, message string }{
		{
			"member-limit",
			`{"rclass":"federated","packageType":"generic","members":[` + strings.Join(eleven, ",") + `]}`,
			"allows at most 10 members, this instance's included",
		},
		{
			"unknown-mode",
			fmt.Sprintf(`{"rclass":"federated","packageType":"generic","members":[{"url":%q,"mode":"sideways"}]}`, member(all[1], "unknown-mode")),
			"is not one of bidirectional, receiver",
		},
		{
			"unsupported-type",
			`{"rclass":"federated","packageType":"terraformbackend"}`,
			"packageType",
		},
	} {
		t.Run(tc.key, func(t *testing.T) {
			status, body := a.Do(t, http.MethodPut, repositories+tc.key, tc.body)
			if status != http.StatusBadRequest || !strings.Contains(body, tc.message) {
				t.Fatalf("invalid membership: %d %s", status, body)
			}
			for _, in := range all {
				do(t, in, http.MethodGet, repositories+tc.key, "", http.StatusNotFound)
			}
		})
	}
	t.Run("terraformbackend-migrate", func(t *testing.T) {
		const key = "backend-local"
		do(t, a, http.MethodPut, repositories+key,
			`{"rclass":"local","packageType":"terraformbackend","description":"migration refusal sentinel"}`, http.StatusOK)
		status, body := a.Do(t, http.MethodPost, "/artifactory/api/federation/migrate/"+key, "")
		if status != http.StatusBadRequest || !strings.Contains(body, "terraformbackend") {
			t.Fatalf("unsupported migration: %d %s", status, body)
		}
		config, found := readRepo(t, a, key)
		if !found || config.Rclass != "local" || config.Description != "migration refusal sentinel" || len(config.Members) != 0 {
			t.Fatalf("refused migration changed the local repository: %+v", config)
		}
		for _, in := range all[1:] {
			do(t, in, http.MethodGet, repositories+key, "", http.StatusNotFound)
		}
	})
}
