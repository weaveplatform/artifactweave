package types_test

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func TestSplitItemPath(t *testing.T) {
	cases := []struct {
		in           string
		parent, name string
		depth        int
	}{
		{"a.jar", ".", "a.jar", 1},
		{"/org/acme/a.jar", "org/acme", "a.jar", 3},
		{"org/acme/", "org", "acme", 2},
	}
	for _, c := range cases {
		parent, name, depth, err := types.SplitItemPath(c.in)
		if err != nil || parent != c.parent || name != c.name || depth != c.depth {
			t.Errorf("%q = %q %q %d %v", c.in, parent, name, depth, err)
		}
		it := types.Item{Path: parent, Name: name}
		if want := strings.Trim(c.in, "/"); it.FullPath() != want || it.StoragePath() != "/"+want {
			t.Errorf("%q full path %q storage %q", c.in, it.FullPath(), it.StoragePath())
		}
	}
	for _, bad := range []string{"", "/", "a//b", "a/./b", "../x", "a/..", `a\b`, "a\x00b"} {
		if _, _, _, err := types.SplitItemPath(bad); !errors.Is(err, types.ErrInvalidItemPath) {
			t.Errorf("%q: want ErrInvalidItemPath, got %v", bad, err)
		}
	}
	if (types.Item{Name: "x"}).FullPath() != "x" {
		t.Error("empty parent is the root")
	}
	root := types.RootItem("r", true)
	if !root.IsRoot() || !root.Folder || !root.Cached || root.FullPath() != "" ||
		root.StoragePath() != "/" {
		t.Errorf("root record %+v: full path %q", root, root.FullPath())
	}
	if (types.Item{Path: types.RootPath, Name: "x"}).IsRoot() {
		t.Error("an item at the root is not the root")
	}
}

func TestRepoKeyAliasing(t *testing.T) {
	if k, c := types.ResolveRepoKey("maven-remote-cache"); k != "maven-remote" || !c {
		t.Errorf("%q %v", k, c)
	}
	if k, c := types.ResolveRepoKey("libs"); k != "libs" || c {
		t.Errorf("%q %v", k, c)
	}
	if k, c := types.ResolveRepoKey("-cache"); k != "-cache" || c {
		t.Errorf("a bare suffix is not an alias: %q %v", k, c)
	}
	if (types.Item{RepoKey: "r", Cached: true}).ReportedRepoKey() != "r-cache" ||
		(types.Item{RepoKey: "r"}).ReportedRepoKey() != "r" {
		t.Error("ReportedRepoKey")
	}
}

func TestProperties(t *testing.T) {
	p := types.Properties{}
	p.Set("build.name", "b", "a", "b")
	p.Add("build.name", "c", "a")
	p.Set("release", "1")
	if diff := cmp.Diff([]string{"a", "b", "c"}, p["build.name"]); diff != "" {
		t.Error(diff)
	}
	if !p.Has("release", "1") || !p.Has("release", "") || p.Has("release", "2") ||
		p.Has("missing", "") {
		t.Error("Has")
	}
	if diff := cmp.Diff([]string{"build.name", "release"}, p.Keys()); diff != "" {
		t.Error(diff)
	}
	c := p.Clone()
	c["release"][0] = "changed"
	if p["release"][0] != "1" {
		t.Error("Clone is shallow")
	}
	if err := types.ValidatePropertyKey("build.number"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"", "a=b", "a;b", "a,b", "a|b", "a b", "a\tb", "a/b", `a"b`} {
		if err := types.ValidatePropertyKey(bad); !errors.Is(err, types.ErrInvalidPropertyKey) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// The key limit counts characters: "é" is two bytes.
	if err := types.ValidatePropertyKey(strings.Repeat("é", types.PropertyKeyMax)); err != nil {
		t.Error(err)
	}
	if err := types.ValidatePropertyKey(
		strings.Repeat("é", types.PropertyKeyMax+1),
	); !errors.Is(
		err,
		types.ErrPropertyKeyTooLong,
	) ||
		!errors.Is(err, types.ErrInvalidPropertyKey) {
		t.Errorf("a long key: %v", err)
	}
}

func TestTruncatePropertyValue(t *testing.T) {
	// "é" is two UTF-8 bytes and one UTF-16 code unit; "😀" is four bytes
	// and a surrogate pair.
	cases := []struct {
		name, in, want string
		limit          int
	}{
		{"short", "abc", "abc", 3},
		{"bytes over, units within", strings.Repeat("é", 3), strings.Repeat("é", 3), 3},
		{"cut by code units", strings.Repeat("é", 5), strings.Repeat("é", 3), 3},
		{"pair fits", "a😀", "a😀", 3},
		{"pair would straddle", "ab😀", "ab", 3},
		{"pair after the limit", "abc😀", "abc", 3},
	}
	for _, c := range cases {
		if got := types.TruncatePropertyValue(c.in, c.limit); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	long := strings.Repeat("é", types.PropertyValueMax+1)
	if got := types.TruncatePropertyValue(
		long,
		types.PropertyValueMax,
	); got != long[:2*types.PropertyValueMax] {
		t.Errorf("PropertyValueMax: kept %d bytes", len(got))
	}
}

func TestTrashRecordDocument(t *testing.T) {
	rec := types.TrashRecord{
		Item: types.Item{
			ID:      7,
			RepoKey: "libs",
			Path:    ".",
			Name:    "a.jar",
			Depth:   1,
			Size:    3,
			Actual: types.Checksums{
				SHA256: "s",
				SHA1:   "h",
				MD5:    "m",
			},
			Created:  t0,
			Modified: t0,
			Updated:  t0,
		},
		Properties: types.Properties{"k": {"v"}},
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var back types.TrashRecord
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(rec, back); diff != "" {
		t.Error(diff)
	}
	e := types.TrashEntry{DeletedAt: t0}
	if e.Expired(t0.Add(time.Hour), 2*time.Hour) || !e.Expired(t0.Add(2*time.Hour), 2*time.Hour) {
		t.Error("TrashEntry.Expired")
	}
}

func TestToken(t *testing.T) {
	tok := types.Token{ExpiresAt: t0}
	if tok.Expired(t0.Add(-time.Second)) || !tok.Expired(t0) ||
		(types.Token{}).Expired(t0.AddDate(100, 0, 0)) {
		t.Error("Expired")
	}
	cases := []struct {
		in    time.Duration
		force bool
		want  bool
	}{
		{
			0,
			false,
			true,
		},
		{time.Hour, false, false},
		{time.Hour, true, true},
		{6 * time.Hour, false, true},
	}
	for _, c := range cases {
		if got := types.Revocable(c.in, types.DefaultRevocableThreshold, c.force); got != c.want {
			t.Errorf("Revocable(%v, %v) = %v", c.in, c.force, got)
		}
	}
	s, err := types.ParseSubject("jfac@01abc/users/bob")
	if err != nil || s != (types.Subject{ServiceID: "jfac@01abc", Kind: "users", Name: "bob"}) ||
		s.String() != "jfac@01abc/users/bob" {
		t.Errorf("%+v %v", s, err)
	}
	for _, bad := range []string{"bob", "jfac@x", "noat/users/bob", "jfac@x/users", "jfac@x//bob"} {
		if _, err := types.ParseSubject(bad); !errors.Is(err, types.ErrInvalidSubject) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	ref := "cmVmd" + strings.Repeat("A", 59)
	if !types.IsReferenceToken(ref) || types.IsReferenceToken(ref+"A") ||
		types.IsReferenceToken("eyJhbGciOi") {
		t.Error("IsReferenceToken")
	}
}

func TestProject(t *testing.T) {
	for _, ok := range []string{"ab", "my-proj", "a" + strings.Repeat("b", 31)} {
		if err := types.ValidateProjectKey(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"a", "1ab", "Ab", "a_b", "a" + strings.Repeat("b", 32)} {
		if err := types.ValidateProjectKey(bad); !errors.Is(err, types.ErrInvalidProjectKey) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if types.ProjectRepoKey("p", "docker") != "p-docker" ||
		types.ProjectRepoKey("p", "p-docker") != "p-docker" {
		t.Error("ProjectRepoKey")
	}
	if !(types.Role{Type: types.RoleCustom}).Editable() ||
		(types.Role{Type: types.RolePredefined}).Editable() {
		t.Error("Role.Editable")
	}
}

func TestWebhookEventTypes(t *testing.T) {
	ok := []struct {
		d     types.WebhookDomain
		types []string
	}{
		{types.DomainArtifact, []string{"deployed", "cached"}},
		{types.DomainReleaseBundleV2, []string{"release_bundle_v2_started"}},
		{types.DomainAppTrust, []string{"anything"}},
	}
	for _, c := range ok {
		if err := types.ValidateEventTypes(c.d, c.types); err != nil {
			t.Errorf("%s %v: %v", c.d, c.types, err)
		}
	}
	bad := []struct {
		d     types.WebhookDomain
		types []string
	}{
		{"unknown", []string{"x"}},
		{types.DomainArtifact, nil},
		{types.DomainArtifact, []string{"pushed"}},
		{types.DomainReleaseBundleV2, []string{"started"}},
		{types.DomainCuration, []string{""}},
	}
	for _, c := range bad {
		if err := types.ValidateEventTypes(
			c.d,
			c.types,
		); !errors.Is(
			err,
			types.ErrInvalidEventType,
		) {
			t.Errorf("%s %v: %v", c.d, c.types, err)
		}
	}
	h := types.WebhookHandler{
		Type: types.HandlerWebhook, URL: "u", Secret: "s",
		Secrets: []types.NamedSecret{{Name: "n", Value: "v"}},
	}
	raw, err := json.Marshal(h)
	if err != nil || strings.Contains(string(raw), `"s"`) || strings.Contains(string(raw), `"v"`) {
		t.Errorf("secrets leaked into the document: %s %v", raw, err)
	}
}

func TestOperations(t *testing.T) {
	for s, want := range map[types.RunStatus]bool{
		types.RunCompleted: true, types.RunCompletedWithErrors: true, types.RunStopped: true, types.RunFailed: true,
		types.RunInProgress: false, types.RunStopping: false, types.RunWarmed: false,
	} {
		if s.Terminal() != want {
			t.Errorf("%s.Terminal() != %v", s, want)
		}
	}
	for c, want := range map[repo.Class]types.ReplicationDirection{repo.Local: types.ReplicationPush, repo.Remote: types.ReplicationPull} {
		if got, ok := types.DirectionFor(c); !ok || got != want {
			t.Errorf("%s: %s %v", c, got, ok)
		}
	}
	if _, ok := types.DirectionFor(repo.Virtual); ok {
		t.Error("virtual repositories do not replicate")
	}
	l := types.Lease{Expires: t0}
	if !l.Held(t0.Add(-time.Second)) || l.Held(t0) {
		t.Error("Lease.Held")
	}
}

func TestInstanceAndPeer(t *testing.T) {
	if err := types.ValidateInstanceID("jfrt@0f8fad5b-d9cb-469f-a165-70867728950e"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"jfrt@x", "jfac@0f8fad5b-d9cb-469f-a165-70867728950e", ""} {
		if err := types.ValidateInstanceID(bad); !errors.Is(err, types.ErrInvalidInstanceID) {
			t.Errorf("%q: %v", bad, err)
		}
	}
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

func TestHLC(t *testing.T) {
	a := types.HLC{Wall: 100}
	if got := a.Next(time.UnixMilli(200)); got != (types.HLC{Wall: 200}) {
		t.Errorf("physical time ahead: %+v", got)
	}
	if got := a.Next(time.UnixMilli(50)); got != (types.HLC{Wall: 100, Logical: 1}) {
		t.Errorf("physical time behind: %+v", got)
	}
	merges := []struct {
		local, remote types.HLC
		now           int64
		want          types.HLC
	}{
		{
			types.HLC{Wall: 100, Logical: 2},
			types.HLC{Wall: 100, Logical: 5},
			50,
			types.HLC{Wall: 100, Logical: 6},
		},
		{
			types.HLC{Wall: 200, Logical: 2},
			types.HLC{Wall: 100, Logical: 5},
			50,
			types.HLC{Wall: 200, Logical: 3},
		},
		{
			types.HLC{Wall: 100, Logical: 2},
			types.HLC{Wall: 300, Logical: 5},
			50,
			types.HLC{Wall: 300, Logical: 6},
		},
		{types.HLC{Wall: 100}, types.HLC{Wall: 100}, 400, types.HLC{Wall: 400}},
	}
	for _, m := range merges {
		if got := m.local.Merge(m.remote, time.UnixMilli(m.now)); got != m.want {
			t.Errorf("%+v merge %+v at %d = %+v, want %+v", m.local, m.remote, m.now, got, m.want)
		}
	}
	ordered := []types.HLC{
		{Wall: 1},
		{Wall: 1, Logical: 1},
		{Wall: 1, Logical: 0xffffffff},
		{Wall: 2},
	}
	for i := range ordered {
		for j := range ordered {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := ordered[i].Compare(ordered[j]); got != want {
				t.Errorf("%+v vs %+v = %d", ordered[i], ordered[j], got)
			}
			if got := strings.Compare(ordered[i].String(), ordered[j].String()); got != want {
				t.Errorf("text order of %+v vs %+v = %d", ordered[i], ordered[j], got)
			}
		}
		back, err := types.ParseHLC(ordered[i].String())
		if err != nil || back != ordered[i] {
			t.Errorf("%s: %+v %v", ordered[i], back, err)
		}
	}
	for _, bad := range []string{"", "1.2", "000000000000000g.00000000", "0000000000000001.0000000g", "0000000000000001-00000000"} {
		if _, err := types.ParseHLC(bad); !errors.Is(err, types.ErrInvalidHLC) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	x := types.Stamp{Time: types.HLC{Wall: 5}, Origin: "jfrt@a"}
	y := types.Stamp{Time: types.HLC{Wall: 5}, Origin: "jfrt@b"}
	z := types.Stamp{Time: types.HLC{Wall: 6}, Origin: "jfrt@a"}
	if !y.After(x) || x.After(y) || !z.After(y) || y.After(z) {
		t.Error("last writer wins with origin tie-break")
	}
}

func TestStreamsAndCursors(t *testing.T) {
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
	c := types.PeerCursor{AckedSeq: 10}
	if c.Behind(15) != 5 || c.Behind(5) != 0 {
		t.Error("Behind")
	}
	if (types.AppliedMarker{LastSeq: 9}).Expect() != 10 {
		t.Error("Expect")
	}
}

func TestFederation(t *testing.T) {
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

func TestDistribution(t *testing.T) {
	if !types.BundleOpen.Editable() || types.BundleSigned.Editable() {
		t.Error("Editable")
	}
	targets := []types.DistributionTarget{
		{PeerInstanceID: "a", SiteName: "edge-london", CityName: "London", CountryCode: "GB"},
		{PeerInstanceID: "b", SiteName: "edge-paris", CityName: "Paris", CountryCode: "FR"},
		{PeerInstanceID: "c", SiteName: "core", CityName: "London", CountryCode: "GB"},
	}
	cases := []struct {
		rules []types.DistributionRule
		want  []string
	}{
		{[]types.DistributionRule{{SiteName: "edge-*"}}, []string{"a", "b"}},
		{[]types.DistributionRule{{CityName: "london"}}, []string{"a", "c"}},
		{[]types.DistributionRule{{CountryCodes: []string{"fr"}}}, []string{"b"}},
		{
			[]types.DistributionRule{{SiteName: "edge-*", CountryCodes: []string{"GB"}}},
			[]string{"a"},
		},
		{[]types.DistributionRule{{SiteName: "["}}, nil},
		{[]types.DistributionRule{{}}, []string{"a", "b", "c"}},
		{nil, nil},
	}
	for _, c := range cases {
		var got []string
		for _, tg := range types.SelectTargets(c.rules, targets) {
			got = append(got, tg.PeerInstanceID)
		}
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%+v (-want +got):\n%s", c.rules, diff)
		}
	}
	job := func(s types.JobStatus) types.DistributionJob { return types.DistributionJob{Status: s} }
	statuses := []struct {
		jobs []types.DistributionJob
		want types.JobStatus
	}{
		{nil, types.JobNotDistributed},
		{
			[]types.DistributionJob{job(types.JobCompleted), job(types.JobCompleted)},
			types.JobCompleted,
		},
		{
			[]types.DistributionJob{job(types.JobCompleted), job(types.JobInProgress)},
			types.JobInProgress,
		},
		{
			[]types.DistributionJob{job(types.JobAborted), job(types.JobInProgress)},
			types.JobAborted,
		},
		{
			[]types.DistributionJob{job(types.JobInProgress), job(types.JobAborted)},
			types.JobAborted,
		},
		{[]types.DistributionJob{job(types.JobAborted), job(types.JobFailed)}, types.JobFailed},
		{
			[]types.DistributionJob{job(types.JobCompleted), job(types.JobInQueue)},
			types.JobInProgress,
		},
	}
	for _, s := range statuses {
		if got := types.TrackerStatus(s.jobs); got != s.want {
			t.Errorf("%+v = %s, want %s", s.jobs, got, s.want)
		}
	}
	raw, err := json.Marshal(types.SigningKey{Alias: "k", PrivateKey: "secret", Passphrase: "pass"})
	if err != nil || strings.Contains(string(raw), "secret") ||
		strings.Contains(string(raw), "pass") {
		t.Errorf("signing key secrets leaked: %s %v", raw, err)
	}
}

func TestArchive(t *testing.T) {
	e := types.ArchiveExecution{Total: 200, Completed: 50, Failed: 50}
	if e.Progress() != 50 || (types.ArchiveExecution{}).Progress() != 0 {
		t.Error("Progress")
	}
	a := types.ArchivedItem{ExpiresAt: t0}
	if a.Expired(t0.Add(-time.Second)) || !a.Expired(t0) || (types.ArchivedItem{}).Expired(t0) {
		t.Error("Expired")
	}
	ok := types.RestoreRequest{
		Type:       types.RestoreArtifact,
		Items:      []string{"libs/a.jar"},
		ToOriginal: true,
	}
	if err := ok.Validate(); err != nil {
		t.Error(err)
	}
	manifest := types.RestoreRequest{
		Type:          types.RestorePackage,
		ManifestPaths: []string{"m"},
		NewPath:       "restored/",
	}
	if err := manifest.Validate(); err != nil {
		t.Error(err)
	}
	bad := []types.RestoreRequest{
		{Type: "X", Items: []string{"a"}, ToOriginal: true},
		{Type: types.RestoreArtifact, ToOriginal: true},
		{
			Type:       types.RestoreArtifact,
			Items:      make([]string, types.MaxRestoreItems+1),
			ToOriginal: true,
		},
		{Type: types.RestoreArtifact, Items: []string{"a"}},
	}
	for _, r := range bad {
		if err := r.Validate(); !errors.Is(err, types.ErrInvalidRestore) {
			t.Errorf("%+v: %v", r.Type, err)
		}
	}
}

func TestPlatform(t *testing.T) {
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
	if !types.WorkerAction("BEFORE_DOWNLOAD").Before() ||
		types.WorkerAction("AFTER_CREATE").Before() {
		t.Error("WorkerAction.Before")
	}
	wf := types.WorkerFilter{
		RepoKeys:        []string{"libs"},
		AnyRemote:       true,
		ExcludePatterns: []string{"*.tmp"},
	}
	cases := []struct {
		key  string
		c    repo.Class
		path string
		want bool
	}{
		{"libs", repo.Local, "a.jar", true},
		{"libs", repo.Local, "a.tmp", false},
		{"cache", repo.Remote, "a.jar", true},
		{"other", repo.Local, "a.jar", false},
		{"fed", repo.Federated, "a.jar", false},
	}
	for _, c := range cases {
		if got := wf.Matches(c.key, c.c, c.path); got != c.want {
			t.Errorf("%+v = %v", c, got)
		}
	}
	if !(types.WorkerFilter{AnyLocal: true, AnyFederated: true}).Matches(
		"fed",
		repo.Federated,
		"x",
	) {
		t.Error("AnyFederated")
	}
	w := types.Worker{BlockedUntil: t0}
	if !w.Blocked(t0.Add(-time.Second)) || w.Blocked(t0) {
		t.Error("Blocked")
	}
	if err := types.ValidateWorkerSource("export default async () => ({status: 1})"); err != nil {
		t.Error(err)
	}
	for _, src := range []string{"", strings.Repeat("x", types.MaxWorkerSource+1)} {
		if err := types.ValidateWorkerSource(src); !errors.Is(err, types.ErrInvalidWorker) {
			t.Errorf("len %d: %v", len(src), err)
		}
	}
	if err := types.ValidateWorkerState(
		map[string]string{"k": strings.Repeat("é", types.MaxWorkerStateValue)},
	); err != nil {
		t.Errorf("limits count characters, not bytes: %v", err)
	}
	tooMany := map[string]string{}
	for i := range types.MaxWorkerStateItems + 1 {
		tooMany[strings.Repeat("k", i+1)] = "v"
	}
	for _, bad := range []map[string]string{
		tooMany,
		{"": "v"},
		{strings.Repeat("k", types.MaxWorkerStateKey+1): "v"},
		{"k": strings.Repeat("v", types.MaxWorkerStateValue+1)},
	} {
		if err := types.ValidateWorkerState(bad); !errors.Is(err, types.ErrInvalidWorker) {
			t.Errorf("%d items: %v", len(bad), err)
		}
	}
}
