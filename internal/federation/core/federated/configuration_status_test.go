package federated_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
)

func TestUnitConfigurationStatusRechecksCurrentMemberAndMasksSecrets(t *testing.T) {
	f := newStreamFixture(t)
	peer := must(f.s.Peers().Get(t.Context(), "b"))
	peer.ProtocolVersion = instance.ProtocolVersion
	peer.PublicKey = bytes.Repeat([]byte{1}, 32)
	if err := f.s.Peers().Put(t.Context(), peer); err != nil {
		t.Fatal(err)
	}
	cfg := f.repos.values["fed"]
	cfg.Password = "private-password"
	f.repos.values["fed"] = cfg
	reader := federated.Receiver{Membership: membership(f.s), Repositories: f.repos}
	result, err := reader.Configuration(t.Context(), peer, "fed")
	if err != nil || result["rclass"] != "federated" || result["key"] != "fed" {
		t.Fatalf("configuration: %v %v", result, err)
	}
	if result["password"] == "private-password" {
		t.Fatal("secret disclosed")
	}
	for _, member := range result["members"].([]any) {
		if strings.Contains(member.(map[string]any)["url"].(string), repo.SelfMemberBase) {
			t.Fatal("unresolved self member")
		}
	}
	for _, key := range []string{"unknown", "local"} {
		if _, err := reader.Configuration(t.Context(), peer, key); !errors.Is(err, kernel.ErrForbidden) {
			t.Fatalf("%s disclosed: %v", key, err)
		}
	}
	// The signed caller snapshot still has its old grant and key. Each current
	// record change must refuse that snapshot without exposing repository state.
	for _, change := range []func(*model.Peer){
		func(p *model.Peer) { p.UseCases = nil },
		func(p *model.Peer) { p.ProtocolVersion++ },
		func(p *model.Peer) { p.PublicKey = bytes.Repeat([]byte{2}, 32) },
	} {
		current := peer
		change(&current)
		if err := f.s.Peers().Put(t.Context(), current); err != nil {
			t.Fatal(err)
		}
		if _, err := reader.Configuration(t.Context(), peer, "fed"); !errors.Is(err, kernel.ErrForbidden) {
			t.Fatalf("stale authority disclosed: %v", err)
		}
	}
	if err := f.s.Peers().Put(t.Context(), peer); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Federation().SetMembers(t.Context(), "fed", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Configuration(t.Context(), peer, "fed"); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("removed member disclosed: %v", err)
	}
	if err := f.s.Peers().Delete(t.Context(), peer.InstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Configuration(t.Context(), peer, "fed"); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("revoked peer disclosed: %v", err)
	}
}

type configurationRemoteFunc func(context.Context, model.Peer, string) (map[string]any, error)

func (f configurationRemoteFunc) Configuration(ctx context.Context, p model.Peer, key string) (map[string]any, error) {
	return f(ctx, p, key)
}

func TestUnitConfigurationDriftComparesReachableSharedConfiguration(t *testing.T) {
	f, m, _ := monitorFixture(t)
	cfg := f.repos.values["fed"]
	expected := must(federated.Payload(cfg, selfBase+"/artifactory"))
	calls := 0
	remote := configurationRemoteFunc(func(ctx context.Context, _ model.Peer, key string) (map[string]any, error) {
		calls++
		if key != "fed" {
			t.Fatalf("remote key %s", key)
		}
		if f.s.platform.InTx(ctx) {
			t.Fatal("network call inside metadata transaction")
		}
		raw, _ := json.Marshal(expected)
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		// Reordering members, spelling defaults explicitly, and a different local
		// key do not constitute shared configuration drift.
		result["key"] = "other-local-key"
		members := result["members"].([]any)
		slices.Reverse(members)
		for _, member := range members {
			member.(map[string]any)["mode"] = "bidirectional"
		}
		return result, nil
	})
	m.Remote = func() federated.ConfigurationRemote { return remote }
	result, err := m.Inconsistent(t.Context())
	if err != nil || len(result) != 0 || calls != 2 {
		t.Fatalf("unchanged: %v %v, calls %d", result, err, calls)
	}
	m.Remote = func() federated.ConfigurationRemote {
		return configurationRemoteFunc(func(ctx context.Context, p model.Peer, key string) (map[string]any, error) {
			result, err := remote.Configuration(ctx, p, key)
			result["notes"] = "changed on one member"
			return result, err
		})
	}
	result, err = m.Inconsistent(t.Context())
	if err != nil || !slices.Equal(result, []string{"fed"}) {
		t.Fatalf("drift: %v %v", result, err)
	}
	m.Remote = func() federated.ConfigurationRemote {
		return configurationRemoteFunc(func(context.Context, model.Peer, string) (map[string]any, error) {
			return nil, federated.ErrUnavailable
		})
	}
	result, err = m.Inconsistent(t.Context())
	if err != nil || len(result) != 0 {
		t.Fatalf("unavailability reported as drift: %v %v", result, err)
	}
}

func TestUnitConfigurationDriftRechecksAuthorityAfterPeerCalls(t *testing.T) {
	f, m, _ := monitorFixture(t)
	body := must(federated.Payload(f.repos.values["fed"], selfBase+"/artifactory"))
	allowed := true
	m.Authorize = func(context.Context) error {
		if !allowed {
			return kernel.ErrForbidden
		}
		return nil
	}
	m.Remote = func() federated.ConfigurationRemote {
		return configurationRemoteFunc(func(context.Context, model.Peer, string) (map[string]any, error) { allowed = false; return body, nil })
	}
	if _, err := m.Inconsistent(t.Context()); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("revoked administrator received report: %v", err)
	}
	m.Remote = nil
	allowed = true
	if _, err := m.Inconsistent(t.Context()); !errors.Is(err, federated.ErrNoStreams) {
		t.Fatalf("no transport: %v", err)
	}
}
