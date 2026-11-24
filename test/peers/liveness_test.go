package peers_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccPeerLivenessSQLite(t *testing.T) {
	t.Parallel()
	testPeerLiveness(t, peers.SQLite)
}

func TestAccPeerLivenessPostgres(t *testing.T) {
	t.Parallel()
	testPeerLiveness(t, peers.Postgres)
}

const (
	// probeInterval is the scenario's federation.peer_probe_interval: each
	// instance reads its peers' status every second, so a peer unseen for
	// two seconds is OFFLINE.
	probeInterval = time.Second
	// livenessWait bounds how long a change takes to show on a record: a
	// few intervals for the read itself, its retries against an unreachable
	// peer (two back-offs, 1.5s) and the stale window, with room for a
	// loaded CI runner under the race detector.
	livenessWait = 30 * time.Second
)

// awaitPeer polls in's record of the peer id until ok accepts it, and
// returns it.
func awaitPeer(t *testing.T, in *peers.Instance, id, what string, ok func(model.Peer) bool) model.Peer {
	t.Helper()
	deadline := time.Now().Add(livenessWait)
	for {
		p, found := in.Peer(t, id)
		if found && ok(p) {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s's record of %s never showed %s: last %s, seen %s", in.Name, id, what, p.Status, p.LastSeen)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// seenSince accepts a record that is ONLINE with a status read after since.
func seenSince(since time.Time) func(model.Peer) bool {
	return func(p model.Peer) bool { return p.Status == model.PeerOnline && p.LastSeen.After(since) }
}

func hasStatus(s model.PeerStatus) func(model.Peer) bool {
	return func(p model.Peer) bool { return p.Status == s }
}

// testPeerLiveness pairs two instances that read each other's signed
// status every second (#1949). Each records the other ONLINE from a read
// after pairing, with no clock skew to speak of on one host; a partition
// turns both OFFLINE once two reads have gone unanswered, and healing it
// turns both ONLINE again. When A's record holds a key B does not sign
// with, B's answers stop verifying on A and A's requests are refused by B,
// and each shows the other UNAUTHORIZED until the key is restored. A
// stopped instance turns OFFLINE on the other.
func testPeerLiveness(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 2, Driver: driver, PeerProbeInterval: probeInterval})
	a, b := c.Instances[0], c.Instances[1]
	aID, bID := a.Identity(t), b.Identity(t)
	pair(t, a, b)
	paired := map[*peers.Instance]model.Peer{}
	for _, side := range []struct {
		in    *peers.Instance
		other string
	}{{a, bID.InstanceID}, {b, aID.InstanceID}} {
		p, found := side.in.Peer(t, side.other)
		if !found {
			t.Fatalf("%s has no record of its peer", side.in.Name)
		}
		paired[side.in] = p
	}

	// Each reads the other within an interval or so of pairing.
	for in, p := range paired {
		got := awaitPeer(t, in, p.InstanceID, "a read after pairing", seenSince(p.LastSeen))
		if got.ClockSkew > time.Second || got.ClockSkew < -time.Second {
			t.Errorf("%s measured a skew of %s on one host", in.Name, got.ClockSkew)
		}
	}

	// A partition: neither answers the other, and both go OFFLINE.
	c.Partition(0, 1)
	cut := time.Now()
	awaitPeer(t, a, bID.InstanceID, "OFFLINE across a partition", hasStatus(model.PeerOffline))
	awaitPeer(t, b, aID.InstanceID, "OFFLINE across a partition", hasStatus(model.PeerOffline))
	for in, id := range map[*peers.Instance]string{a: bID.InstanceID, b: aID.InstanceID} {
		if p, _ := in.Peer(t, id); p.LastSeen.After(cut) {
			t.Errorf("%s recorded its peer seen across the partition at %s", in.Name, p.LastSeen)
		}
	}
	c.Heal(0, 1)
	healed := time.Now()
	awaitPeer(t, a, bID.InstanceID, "ONLINE once healed", seenSince(healed))
	awaitPeer(t, b, aID.InstanceID, "ONLINE once healed", seenSince(healed))

	// A key B does not sign with: the trust no longer holds either way.
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a.SetPeerKey(t, bID.InstanceID, other)
	awaitPeer(t, a, bID.InstanceID, "UNAUTHORIZED for answers that do not verify", hasStatus(model.PeerUnauthorized))
	awaitPeer(t, b, aID.InstanceID, "UNAUTHORIZED once refused", hasStatus(model.PeerUnauthorized))
	a.SetPeerKey(t, bID.InstanceID, bID.PublicKey)
	restored := time.Now()
	awaitPeer(t, a, bID.InstanceID, "ONLINE with the key restored", seenSince(restored))
	awaitPeer(t, b, aID.InstanceID, "ONLINE with the key restored", seenSince(restored))

	// A stopped instance answers nothing.
	c.Stop(1)
	awaitPeer(t, a, bID.InstanceID, "OFFLINE once stopped", hasStatus(model.PeerOffline))
}
