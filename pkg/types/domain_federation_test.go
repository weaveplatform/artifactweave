package types_test

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitDirectionFor(t *testing.T) {
	for c, want := range map[repo.Class]types.ReplicationDirection{repo.Local: types.ReplicationPush, repo.Remote: types.ReplicationPull} {
		if got, ok := types.DirectionFor(c); !ok || got != want {
			t.Errorf("%s: %s %v", c, got, ok)
		}
	}
	if _, ok := types.DirectionFor(repo.Virtual); ok {
		t.Error("virtual repositories do not replicate")
	}
}

func TestUnitPeer(t *testing.T) {
	for _, u := range []types.UseCase{types.UseFederation, types.UseDistribution, types.UseAccessFederation, types.UsePlatformFederation} {
		if !types.KnownUseCase(u) {
			t.Errorf("%s should be known", u)
		}
	}
	for _, u := range []types.UseCase{"mission-control", "bridge", "artifactory-cold"} {
		if types.KnownUseCase(u) {
			t.Errorf("%s should answer 404", u)
		}
	}
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	p := types.Peer{PublicKey: pub, UseCases: []types.UseCase{types.UseFederation}, LastSeen: t0}
	if !p.Has(types.UseFederation) || p.Has(types.UseDistribution) {
		t.Error("Peer.Has")
	}
	stale := 5 * time.Minute
	cases := []struct {
		now      time.Time
		rejected bool
		failing  int
		want     types.PeerStatus
	}{
		{t0, true, 0, types.PeerUnauthorized},
		{t0.Add(10 * time.Minute), false, 0, types.PeerOffline},
		{t0.Add(time.Minute), false, 1, types.PeerPartial},
		{t0.Add(time.Minute), false, 0, types.PeerOnline},
	}
	for _, c := range cases {
		if got := p.DeriveStatus(c.now, stale, c.rejected, c.failing); got != c.want {
			t.Errorf("%+v = %s", c, got)
		}
	}
	if (types.Peer{}).DeriveStatus(t0, stale, false, 0) != types.PeerOffline {
		t.Error("a never-seen peer is offline")
	}
	n := types.PairingNonce{ExpiresAt: t0.Add(types.PairingTokenTTL)}
	if !n.Usable(t0) || n.Usable(t0.Add(types.PairingTokenTTL)) {
		t.Error("expiry")
	}
	n.RedeemedAt = t0
	if n.Usable(t0) {
		t.Error("a redeemed nonce is single-use")
	}
}

func TestUnitStreams(t *testing.T) {
	if types.RepositoryStream("libs") != "repo:libs" ||
		types.DistributionStream(42, "jfrt@x") != "distribution:42:jfrt@x" {
		t.Error("stream names")
	}
	cases := []struct {
		in   string
		kind types.StreamKind
		rest string
	}{
		{"repo:libs", types.StreamRepository, "libs"},
		{"access", types.StreamAccess, ""},
		{"distribution:42:jfrt@x", types.StreamDistribution, "42:jfrt@x"},
	}
	for _, c := range cases {
		kind, rest, err := types.ParseStream(c.in)
		if err != nil || kind != c.kind || rest != c.rest {
			t.Errorf("%q = %s %q %v", c.in, kind, rest, err)
		}
	}
	for _, bad := range []string{"repo:", "repo", "other:x", ""} {
		if _, _, err := types.ParseStream(bad); !errors.Is(err, types.ErrInvalidStream) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestUnitStamp(t *testing.T) {
	x := types.Stamp{Time: types.HLC{Wall: 5}, Origin: "jfrt@a"}
	y := types.Stamp{Time: types.HLC{Wall: 5}, Origin: "jfrt@b"}
	z := types.Stamp{Time: types.HLC{Wall: 6}, Origin: "jfrt@a"}
	if !y.After(x) || x.After(y) || !z.After(y) || y.After(z) {
		t.Error("last writer wins with origin tie-break")
	}
}

func TestUnitFederation(t *testing.T) {
	base, key, err := types.ParseMemberURL("https://a.example.com/artifactory/generic-fed/")
	if err != nil || base != "https://a.example.com" || key != "generic-fed" {
		t.Errorf("%q %q %v", base, key, err)
	}
	base, _, err = types.ParseMemberURL("https://a.example.com/prefix/artifactory/libs?x=1")
	if err != nil || base != "https://a.example.com/prefix" {
		t.Errorf("prefixed base: %q %v", base, err)
	}
	for _, bad := range []string{"not a url", "/artifactory/x", "https://h/repo/x", "https://h/artifactory/", "https://h/artifactory/a/b", "%zz"} {
		if _, _, err := types.ParseMemberURL(bad); !errors.Is(err, types.ErrInvalidMemberURL) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	cfg := &repo.Config{Key: "generic-fed", Class: repo.Federated, Members: []repo.Member{
		{URL: "https://self.example.com/artifactory/generic-fed", Enabled: repo.Flex(true)},
		{URL: "https://a.example.com/artifactory/generic-fed"},
		{URL: "https://b.example.com/artifactory/other", Enabled: repo.Flex(false)},
	}}
	members, err := types.MembersOf(cfg, "https://self.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	want := []types.FederationMember{
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
	if _, err := types.MembersOf(cfg, ""); !errors.Is(err, types.ErrInvalidMemberURL) {
		t.Errorf("want ErrInvalidMemberURL, got %v", err)
	}
	th := time.Minute
	cases := []struct {
		s       types.FederationSettings
		running bool
		lags    []time.Duration
		want    types.SyncStatus
	}{
		{types.FederationSettings{Disabled: true}, true, nil, types.SyncDisabled},
		{types.FederationSettings{}, true, nil, types.SyncFullSyncRunning},
		{
			types.FederationSettings{},
			false,
			[]time.Duration{time.Second, 2 * time.Minute},
			types.SyncOutOfSync,
		},
		{types.FederationSettings{}, false, []time.Duration{time.Second}, types.SyncSyncing},
	}
	for _, c := range cases {
		if got := types.DeriveSyncStatus(c.s, c.running, c.lags, th); got != c.want {
			t.Errorf("%+v = %s", c, got)
		}
	}
}

func TestUnitPlatformFederation(t *testing.T) {
	f := types.PatternFilter{Include: []string{"team-*"}, Exclude: []string{"team-secret"}}
	if !f.Matches("team-a") || f.Matches("team-secret") || f.Matches("other") ||
		!(types.PatternFilter{}).Matches("x") {
		t.Error("PatternFilter")
	}
	target := types.AccessFederationTarget{
		Active:   true,
		Entities: []types.FederatedEntity{types.EntityUsers},
	}
	if !target.Streams(types.EntityUsers) || target.Streams(types.EntityTokens) {
		t.Error("Streams")
	}
	target.Active = false
	if target.Streams(types.EntityUsers) {
		t.Error("an inactive target receives nothing")
	}
}
