package model_test

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

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
	for _, u := range []model.UseCase{model.UseFederatedRepo, model.UseFederatedRepoRead, model.UseDistribution, model.UseAccessFederation, model.UsePlatformFederation} {
		if !model.KnownUseCase(u) {
			t.Errorf("%s should be known", u)
		}
	}
	for _, u := range []model.UseCase{"mission-control", "bridge", "artifactory-cold", "federation"} {
		if model.KnownUseCase(u) {
			t.Errorf("%s should answer 404", u)
		}
	}
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	p := model.Peer{
		PublicKey: pub, UseCases: []model.UseCase{model.UseFederatedRepo}, LastSeen: t0,
		ProtocolVersion: instance.ProtocolVersion,
	}
	if !p.Has(model.UseFederatedRepo) || p.Has(model.UseDistribution) {
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
	// A recorded major version mismatch makes a peer seen a minute ago
	// unavailable (decision 0021, Versioning), and unauthorized still wins.
	other := p
	other.ProtocolVersion = instance.ProtocolVersion + 1
	if other.Compatible() || !p.Compatible() {
		t.Error("Peer.Compatible")
	}
	if got := other.DeriveStatus(t0.Add(time.Minute), stale, false, 0); got != model.PeerOffline {
		t.Errorf("a peer speaking another major version is %s, want OFFLINE", got)
	}
	if got := other.DeriveStatus(t0, stale, true, 0); got != model.PeerUnauthorized {
		t.Errorf("a rejected peer of another major version is %s, want UNAUTHORIZED", got)
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

func TestUnitUseCasePairing(t *testing.T) {
	for path, want := range map[string]model.Pairing{
		"federated-repo/*":            model.PairingServed,
		"federated-repo/r":            model.PairingServed,
		"platform-federation":         model.PairingPlanned,
		"access-federation":           model.PairingPlanned,
		"distribution":                model.PairingPlanned,
		"mission-control":             model.PairingPlanned,
		"bridge":                      model.PairingPlanned,
		"artifactory-cold":            model.PairingUnknown,
		"artifactory-cold/ns1":        model.PairingUnknown,
		"federated-repo":              model.PairingUnknown,
		"federated-repo/libs-local:w": model.PairingUnknown,
		"federation":                  model.PairingUnknown,
		"":                            model.PairingUnknown,
		"federated-repo/*/":           model.PairingUnknown,
		"FEDERATED-REPO/*":            model.PairingUnknown,
	} {
		if got := model.PairingFor(path); got != want {
			t.Errorf("PairingFor(%q) = %d, want %d", path, got, want)
		}
	}
	for u, want := range map[model.UseCase]string{
		model.UseFederatedRepo:      "federated-repo",
		model.UseFederatedRepoRead:  "federated-repo",
		model.UsePlatformFederation: "platform-federation",
	} {
		if got := u.Name(); got != want {
			t.Errorf("%s.Name() = %q, want %q", u, got, want)
		}
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
	if err != nil || base != "https://a.example.com/artifactory" || key != "generic-fed" {
		t.Errorf("%q %q %v", base, key, err)
	}
	// 7.x appends /artifactory to a base without it, after trimming slashes.
	base, key, err = model.ParseMemberURL("https://a.example.com/prefix//libs")
	if err != nil || base != "https://a.example.com/prefix/artifactory" || key != "libs" {
		t.Errorf("normalized base: %q %q %v", base, key, err)
	}
	for _, bad := range []string{
		"not a url", "/artifactory/x", "https://h/", "https://h", "https://h/a?x=1",
		"https://h/a#f", "https://u:p@h/a", "ftp://h/a", "%zz",
	} {
		if _, _, err := model.ParseMemberURL(bad); !errors.Is(err, model.ErrInvalidMemberURL) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	cfg := &repo.Config{Key: "generic-fed", Class: repo.Federated, Members: []repo.Member{
		{URL: "https://self.example.com/artifactory/generic-fed", Enabled: repo.Flex(true)},
		{URL: repo.SelfMemberURL("generic-fed")},
		{URL: "https://a.example.com/generic-fed", Mode: repo.ModeBidirectional},
		{URL: "https://b.example.com/artifactory/other", Enabled: repo.Flex(false)},
	}}
	members, err := model.MembersOf(cfg, "https://self.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	want := []model.FederationMember{
		{
			RepoKey:       "generic-fed",
			BaseURL:       "https://a.example.com/artifactory",
			RemoteRepoKey: "generic-fed",
			Mode:          repo.ModeBidirectional,
			Enabled:       true,
		},
		{
			RepoKey:       "generic-fed",
			BaseURL:       "https://b.example.com/artifactory",
			RemoteRepoKey: "other",
			Enabled:       false,
		},
	}
	if diff := cmp.Diff(want, members); diff != "" {
		t.Error(diff)
	}
	if members[0].Receiver() || !(model.FederationMember{Mode: repo.ModeReceiver}).Receiver() {
		t.Error("Receiver")
	}
	// The self base with another key is another repository, not this one.
	if model.IsSelfMember("https://self.example.com/artifactory/other", "generic-fed", "https://self.example.com") ||
		model.IsSelfMember("https://self.example.com/artifactory/generic-fed", "generic-fed", "") ||
		model.IsSelfMember("nonsense", "generic-fed", "https://self.example.com") {
		t.Error("IsSelfMember")
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
