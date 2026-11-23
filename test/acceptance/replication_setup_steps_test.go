package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type replicationFixture struct {
	upstream, consumer                 *world
	owningRepo, sourceKey, consumerKey string
}

func (w *world) configureReplicationPair(direction, kind string) (replicationFixture, error) {
	if w.replicationPeer == nil || direction != "push" && direction != "pull" {
		return replicationFixture{}, fmt.Errorf("ordinary replication requires two fixture-owned instances and a supported direction")
	}
	peer := w.replicationPeer
	peerRepo, owningRepo := "p11-"+kind+"-peer", "p11-"+kind+"-owner"
	put := func(instance *world, path, body string, status int) error {
		_, err := replicationFixtureRequest(instance, http.MethodPut, path, body, status)
		return err
	}
	if err := put(peer, "/artifactory/api/repositories/"+peerRepo, fmt.Sprintf(`{"rclass":"local","packageType":%q}`, kind), http.StatusOK); err != nil {
		return replicationFixture{}, err
	}
	// Fixture administration sets up a separate, nonadministrator account.
	// Only its repository-scoped credentials enter the actual peer transport.
	if err := put(peer, "/artifactory/api/security/users/replication-transport",
		`{"name":"replication-transport","email":"replication@example.invalid","password":"`+replicationTransportPassword+`","admin":false}`, http.StatusCreated); err != nil {
		return replicationFixture{}, err
	}
	if err := put(
		peer,
		"/artifactory/api/security/permissions/replication-transport",
		`{"name":"replication-transport","repositories":["`+peerRepo+`"],"includesPattern":"**","principals":{"users":{"replication-transport":["r","w","n","d"]}}}`,
		http.StatusOK,
	); err != nil {
		return replicationFixture{}, err
	}
	configuration := map[string]any{"rclass": "local", "packageType": kind}
	if direction == "pull" {
		configuration["rclass"], configuration["url"] = "remote", peer.url+"/artifactory/"+peerRepo
		configuration["username"], configuration["password"] = "replication-transport", replicationTransportPassword
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		return replicationFixture{}, fmt.Errorf("encode owned replication repository: %w", err)
	}
	if err := put(w, "/artifactory/api/repositories/"+owningRepo, string(data), http.StatusOK); err != nil {
		return replicationFixture{}, err
	}
	target := map[string]any{
		"enabled": true, "cronExp": "0 0 2 * * ?", "syncProperties": true, "syncDeletes": true,
		"includePathPrefixPattern": "selected/**", "excludePathPrefixPattern": "selected/omitted/**",
	}
	if direction == "push" {
		target["url"], target["username"], target["password"] = peer.url+"/artifactory/"+peerRepo, "replication-transport", replicationTransportPassword
		if w.replicationNTLM == "explicit" {
			target["proxy"] = replicationNTLMKey
		}
	}
	data, err = json.Marshal(target)
	if err != nil {
		return replicationFixture{}, fmt.Errorf("encode owned replication target: %w", err)
	}
	if err := put(w, "/artifactory/api/replications/"+owningRepo, string(data), http.StatusCreated); err != nil {
		return replicationFixture{}, err
	}
	upstream, consumer, sourceKey, consumerKey := w, peer, owningRepo, peerRepo
	if direction == "pull" {
		upstream, consumer, sourceKey, consumerKey = peer, w, peerRepo, owningRepo+"-cache"
	}
	return replicationFixture{upstream: upstream, consumer: consumer, owningRepo: owningRepo, sourceKey: sourceKey, consumerKey: consumerKey}, nil
}
