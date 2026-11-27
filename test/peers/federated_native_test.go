package peers_test

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccFederatedNativeSQLite(t *testing.T) {
	t.Parallel()
	testFederatedNative(t, peers.SQLite)
}

func TestAccFederatedNativePostgres(t *testing.T) {
	t.Parallel()
	testFederatedNative(t, peers.Postgres)
}

func awaitNativeArtifact(t *testing.T, all []*peers.Instance, key, format string, a peerNativeArtifact) {
	t.Helper()
	if format == "maven" {
		awaitFile(t, all, a.path(key, format), string(a.jar(t)))
		return
	}
	// Package metadata and the tarball are both necessary for npm install.
	for _, in := range all {
		eventually(t, in, a.name+" packument and tarball", func() (bool, string) {
			status, body := in.Do(t, http.MethodGet, "/artifactory/api/npm/"+key+"/"+a.name, "")
			if status != http.StatusOK && status != http.StatusNotFound {
				t.Fatalf("npm packument on %s: %d %s", in.Name, status, body)
			}
			_, present := file(t, in, a.path(key, format))
			return status == http.StatusOK && present, body
		})
	}
}

// signedPruned reads as the isolated member, without healing that member's
// server egress. These repositories have no pre-partition item events, so 410
// proves a change missed in this partition was pruned, rather than old history.
func signedPruned(t *testing.T, source, isolated *peers.Instance, key string) bool {
	t.Helper()
	target := source.URL + "/artifactweave/peer/v1/streams/repo:" + key + "/events?after=0&limit=1"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := isolated.Identity(t)
	if err := (corepeer.Signer{KeyID: id.InstanceID, Key: id.PrivateKey}).SignRequest(http.MethodGet, target, req.Header, nil); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		t.Fatal(err)
	}
	senderID := source.Identity(t)
	request := corepeer.Message{Method: http.MethodGet, TargetURI: target, Header: req.Header}
	if err := corepeer.VerifyResponse(corepeer.Message{Status: resp.StatusCode, Header: resp.Header, Request: &request}, body,
		senderID.InstanceID, senderID.PublicKey, time.Now()); err != nil {
		t.Fatalf("pruning response is not authenticated: %v", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusGone {
		t.Fatalf("signed pruning observation: %d %s", resp.StatusCode, body)
	}
	if resp.StatusCode == http.StatusGone {
		t.Logf("signed pruning proof %s -> %s %s: HTTP 410 %s", isolated.Name, source.Name, key, body)
		return true
	}
	return false
}

func testFederatedNative(t *testing.T, driver peers.Driver) {
	clients := newPeerNativeClients(t)
	c := peers.Start(t, peers.Options{Instances: 3, Driver: driver, Retention: retention})
	all := c.Instances
	pair(t, all[0], all[1])
	pair(t, all[0], all[2])
	pair(t, all[1], all[2])
	private := peerNativeArtifact{"p13-divergent", "local content predating federation\n"}
	for _, format := range []string{"maven", "npm"} {
		key := "native-" + format
		federate(t, all, key, format)
		for i, origin := range all {
			live := peerNativeArtifact{fmt.Sprintf("p13-live-%d", i), fmt.Sprintf("native %s origin %d\n", format, i)}
			gone := peerNativeArtifact{fmt.Sprintf("p13-gone-%d", i), fmt.Sprintf("deleted %s origin %d\n", format, i)}
			clients.publish(origin, key, format, live)
			clients.publish(origin, key, format, gone)
			awaitNativeArtifact(t, all, key, format, live)
			awaitNativeArtifact(t, all, key, format, gone)
			clients.consume(all, key, format, live, true)
			clients.property(origin, live.path(key, format), origin.Name)
			for _, in := range all {
				awaitReceiverProperty(t, in, live.path(key, format), origin.Name)
			}
			clients.remove(origin, gone.path(key, format))
			awaitFile(t, all, gone.path(key, format), "")
			clients.consume(all, key, format, gone, false)
		}
		// A pre-existing local addition has no federated event to escape after
		// healing. Normal sync keeps it; forced sync must discard it.
		do(t, all[2], http.MethodPut, "/artifactory/api/repositories/repair-"+format,
			`{"rclass":"local","packageType":"`+format+`"}`, http.StatusOK)
		clients.publish(all[2], "repair-"+format, format, private)
		do(t, all[2], http.MethodPost, "/artifactory/api/federation/migrate/repair-"+format, "", http.StatusOK)
		// A's repair stream has no item event before isolation.
		federate(t, all, "repair-"+format, format)
	}

	// No third member can relay A's events around the partition.
	c.Partition(0, 2)
	c.Partition(1, 2)
	late := peerNativeArtifact{"p13-late", "published during the partition\n"}
	for _, format := range []string{"maven", "npm"} {
		key := "repair-" + format
		if signedPruned(t, all[0], all[2], key) {
			t.Fatal("empty repair stream was already pruned")
		}
		clients.publish(all[0], key, format, late)
		clients.property(all[0], late.path(key, format), "partition")
		awaitNativeArtifact(t, all[:2], key, format, late)
		absent(t, all[2:], late.path(key, format))
		absent(t, all[:2], private.path(key, format))
	}
	// The minimum supported retention is one minute; wait past it without
	// mutating fixture clocks or depending on a fixed pruning scheduler tick.
	select {
	case <-time.After(retention + time.Second):
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	for _, format := range []string{"maven", "npm"} {
		key := "repair-" + format
		eventually(t, all[0], key+" signed HTTP 410 after retention", func() (bool, string) {
			return signedPruned(t, all[0], all[2], key), "events have not yet been pruned"
		})
		absent(t, all[2:], late.path(key, format))
		// Queue the operator's full sync while the target is still isolated.
		do(t, all[0], http.MethodPost, "/artifactory/api/federation/fullSync/"+key+"?mirror="+url.QueryEscape(all[2].URL), "", http.StatusOK)
	}
	c.Heal(0, 2)
	c.Heal(1, 2)
	for _, format := range []string{"maven", "npm"} {
		key := "repair-" + format
		awaitNativeArtifact(t, all, key, format, late)
		for _, in := range all {
			awaitReceiverProperty(t, in, late.path(key, format), "partition")
		}
		clients.consume(all, key, format, late, true)
		// A normal full sync keeps divergent paths; a forced one removes them.
		awaitNativeArtifact(t, all[2:], key, format, private)
		do(t, all[0], http.MethodPost, "/artifactory/api/federation/forceFullSync/"+key+"?mirror="+url.QueryEscape(all[2].URL), "", http.StatusOK)
		awaitFile(t, all[2:], private.path(key, format), "")
		clients.consume(all[2:], key, format, private, false)
		after := peerNativeArtifact{"p13-after", "native publication from repaired member\n"}
		clients.publish(all[2], key, format, after)
		awaitNativeArtifact(t, all, key, format, after)
		clients.consume(all, key, format, after, true)
	}
	c.Close()
	testNativeReceiver(t, driver, clients)
}

func testNativeReceiver(t *testing.T, driver peers.Driver, clients *peerNativeClients) {
	t.Helper()
	c := peers.Start(t, peers.Options{Instances: 3, Driver: driver})
	all := c.Instances
	a, b, receiver := all[0], all[1], all[2]
	pair(t, a, b)
	pairReceiver(t, a, receiver)
	pairReceiver(t, b, receiver)
	for _, format := range []string{"maven", "npm"} {
		key := "receiver-" + format
		before := peerNativeArtifact{"p13-before", "native bytes predating receiver membership\n"}
		sent := peerNativeArtifact{"p13-sent", "native bytes from second sender\n"}
		private := peerNativeArtifact{"p13-private", "receiver native publication stays local\n"}
		do(t, a, http.MethodPut, "/artifactory/api/repositories/"+key, `{"rclass":"local","packageType":"`+format+`"}`, http.StatusOK)
		clients.publish(a, key, format, before)
		clients.property(a, before.path(key, format), "sender")
		do(t, a, http.MethodPost, "/artifactory/api/federation/migrate/"+key, "", http.StatusOK)
		do(
			t,
			a,
			http.MethodPost,
			"/artifactory/api/repositories/"+key,
			`{"members":[{"url":"`+member(a, key)+`"},{"url":"`+member(b, key)+`"},{"url":"`+member(receiver, key)+`","mode":"receiver"}]}`,
			http.StatusOK,
		)
		for _, in := range all {
			await(t, in, key, "receiving-only membership", func(cfg federatedRepo) bool { return len(cfg.Members) == 3 })
		}
		// Adding a bidirectional member does not snapshot pre-membership
		// content automatically; that automatic contract belongs to receivers.
		do(t, a, http.MethodPost, "/artifactory/api/federation/fullSync/"+key+"?mirror="+url.QueryEscape(b.URL), "", http.StatusOK)
		// Initial receiver full sync must deliver bytes written before membership.
		awaitNativeArtifact(t, all, key, format, before)
		clients.publish(b, key, format, sent)
		awaitNativeArtifact(t, all, key, format, sent)
		clients.consume(all, key, format, before, true)
		clients.consume(all, key, format, sent, true)
		clients.publish(receiver, key, format, private)
		clients.consume(all[2:], key, format, private, true)
		clients.property(receiver, before.path(key, format), "receiver")
		awaitReceiverProperty(t, receiver, before.path(key, format), "receiver")
		clients.remove(receiver, sent.path(key, format))
		awaitFile(t, all[2:], sent.path(key, format), "")
		absent(t, all[:2], private.path(key, format))
		for _, sender := range all[:2] {
			awaitReceiverProperty(t, sender, before.path(key, format), "sender")
		}
		clients.consume(all[:2], key, format, private, false)
		clients.consume(all[:2], key, format, sent, true)
		do(t, a, http.MethodPost, "/artifactory/api/federation/fullSync/"+key+"?mirror="+url.QueryEscape(receiver.URL), "", http.StatusOK)
		awaitNativeArtifact(t, all[2:], key, format, sent)
		awaitReceiverProperty(t, receiver, before.path(key, format), "sender")
		awaitFile(t, all[2:], private.path(key, format), "")
		clients.consume(all[2:], key, format, sent, true)
		clients.consume(all[2:], key, format, private, false)
	}
}
