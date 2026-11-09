package model_test

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/federation/core/model"
	"github.com/weaveplatform/artifactweave/migration/platform/outbox"
)

// t0 is the fixed instant of the domain tests in pkg/types, which keep their
// own copy (domain_shared_test.go).
var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func TestUnitDirectionFor(t *testing.T) {
	for c, want := range map[repo.Class]model.ReplicationDirection{repo.Local: model.ReplicationPush, repo.Remote: model.ReplicationPull} {
		if got, ok := model.DirectionFor(c); !ok || got != want {
			t.Errorf("%s: %s %v", c, got, ok)
		}
	}
	if _, ok := model.DirectionFor(repo.Virtual); ok {
		t.Error("virtual repositories do not replicate")
	}
}

func TestUnitPeer(t *testing.T) {
	for _, u := range []model.UseCase{model.UseFederation, model.UseDistribution, model.UseAccessFederation, model.UsePlatformFederation} {
		if !model.KnownUseCase(u) {
			t.Errorf("%s should be known", u)
		}
	}
	for _, u := range []model.UseCase{"mission-control", "bridge", "artifactory-cold"} {
		if model.KnownUseCase(u) {
			t.Errorf("%s should answer 404", u)
		}
	}
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	p := model.Peer{PublicKey: pub, UseCases: []model.UseCase{model.UseFederation}, LastSeen: t0}
	if !p.Has(model.UseFederation) || p.Has(model.UseDistribution) {
		t.Error("Peer.Has")
	}
	stale := 5 * time.Minute
	cases := []struct {
		now      time.Time
		rejected bool
		failing  int
		want     model.PeerStatus
	}{
		{t0, true, 0, model.PeerUnauthorized},
		{t0.Add(10 * time.Minute), false, 0, model.PeerOffline},
		{t0.Add(time.Minute), false, 1, model.PeerPartial},
		{t0.Add(time.Minute), false, 0, model.PeerOnline},
	}
	for _, c := range cases {
		if got := p.DeriveStatus(c.now, stale, c.rejected, c.failing); got != c.want {
			t.Errorf("%+v = %s", c, got)
		}
	}
	if (model.Peer{}).DeriveStatus(t0, stale, false, 0) != model.PeerOffline {
		t.Error("a never-seen peer is offline")
	}
	n := model.PairingNonce{ExpiresAt: t0.Add(model.PairingTokenTTL)}
	if !n.Usable(t0) || n.Usable(t0.Add(model.PairingTokenTTL)) {
		t.Error("expiry")
	}
	n.RedeemedAt = t0
	if n.Usable(t0) {
		t.Error("a redeemed nonce is single-use")
	}
}

func TestUnitStreams(t *testing.T) {
	if model.RepositoryStream("libs") != "repo:libs" ||
		model.DistributionStream(42, "jfrt@x") != "distribution:42:jfrt@x" {
		t.Error("stream names")
	}
	cases := []struct {
		in   string
		kind model.StreamKind
		rest string
	}{
		{"repo:libs", model.StreamRepository, "libs"},
		{"access", model.StreamAccess, ""},
		{"distribution:42:jfrt@x", model.StreamDistribution, "42:jfrt@x"},
	}
	for _, c := range cases {
		kind, rest, err := model.ParseStream(c.in)
		if err != nil || kind != c.kind || rest != c.rest {
			t.Errorf("%q = %s %q %v", c.in, kind, rest, err)
		}
	}
	for _, bad := range []string{"repo:", "repo", "other:x", ""} {
		if _, _, err := model.ParseStream(bad); !errors.Is(err, model.ErrInvalidStream) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestUnitStamp(t *testing.T) {
	x := model.Stamp{Time: outbox.HLC{Wall: 5}, Origin: "jfrt@a"}
	y := model.Stamp{Time: outbox.HLC{Wall: 5}, Origin: "jfrt@b"}
	z := model.Stamp{Time: outbox.HLC{Wall: 6}, Origin: "jfrt@a"}
	if !y.After(x) || x.After(y) || !z.After(y) || y.After(z) {
		t.Error("last writer wins with origin tie-break")
	}
}

func TestUnitFederation(t *testing.T) {
	base, key, err := model.ParseMemberURL("https://a.example.com/artifactory/generic-fed/")
	if err != nil || base != "https://a.example.com" || key != "generic-fed" {
		t.Errorf("%q %q %v", base, key, err)
	}
	base, _, err = model.ParseMemberURL("https://a.example.com/prefix/artifactory/libs?x=1")
	if err != nil || base != "https://a.example.com/prefix" {
		t.Errorf("prefixed base: %q %v", base, err)
	}
	for _, bad := range []string{"not a url", "/artifactory/x", "https://h/repo/x", "https://h/artifactory/", "https://h/artifactory/a/b", "%zz"} {
		if _, _, err := model.ParseMemberURL(bad); !errors.Is(err, model.ErrInvalidMemberURL) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	cfg := &repo.Config{Key: "generic-fed", Class: repo.Federated, Members: []repo.Member{
		{URL: "https://self.example.com/artifactory/generic-fed", Enabled: repo.Flex(true)},
		{URL: "https://a.example.com/artifactory/generic-fed"},
		{URL: "https://b.example.com/artifactory/other", Enabled: repo.Flex(false)},
	}}
	members, err := model.MembersOf(cfg, "https://self.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	want := []model.FederationMember{
		{
			RepoKey:       "generic-fed",
			BaseURL:       "https://a.example.com",
			RemoteRepoKey: "generic-fed",
			Enabled:       true,
		},
		{
			RepoKey:       "generic-fed",
			BaseURL:       "https://b.example.com",
			RemoteRepoKey: "other",
			Enabled:       false,
		},
	}
	if diff := cmp.Diff(want, members); diff != "" {
		t.Error(diff)
	}
	cfg.Members = append(cfg.Members, repo.Member{URL: "nonsense"})
	if _, err := model.MembersOf(cfg, ""); !errors.Is(err, model.ErrInvalidMemberURL) {
		t.Errorf("want ErrInvalidMemberURL, got %v", err)
	}
	th := time.Minute
	cases := []struct {
		s       model.FederationSettings
		running bool
		lags    []time.Duration
		want    model.SyncStatus
	}{
		{model.FederationSettings{Disabled: true}, true, nil, model.SyncDisabled},
		{model.FederationSettings{}, true, nil, model.SyncFullSyncRunning},
		{
			model.FederationSettings{},
			false,
			[]time.Duration{time.Second, 2 * time.Minute},
			model.SyncOutOfSync,
		},
		{model.FederationSettings{}, false, []time.Duration{time.Second}, model.SyncSyncing},
	}
	for _, c := range cases {
		if got := model.DeriveSyncStatus(c.s, c.running, c.lags, th); got != c.want {
			t.Errorf("%+v = %s", c, got)
		}
	}
}

func TestUnitPlatformFederation(t *testing.T) {
	f := model.PatternFilter{Include: []string{"team-*"}, Exclude: []string{"team-secret"}}
	if !f.Matches("team-a") || f.Matches("team-secret") || f.Matches("other") ||
		!(model.PatternFilter{}).Matches("x") {
		t.Error("PatternFilter")
	}
	target := model.AccessFederationTarget{
		Active:   true,
		Entities: []model.FederatedEntity{model.EntityUsers},
	}
	if !target.Streams(model.EntityUsers) || target.Streams(model.EntityTokens) {
		t.Error("Streams")
	}
	target.Active = false
	if target.Streams(model.EntityUsers) {
		t.Error("an inactive target receives nothing")
	}
}
