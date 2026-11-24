package federated_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	fedinmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var t0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// Instance A is the one under test; B and C are its peers.
const (
	selfBase = "https://a.example"
	bBase    = "https://b.example/artifactory"
	cBase    = "https://c.example/artifactory"
)

// store is one instance's metadata: the federation and platform stores
// over one in-memory database.
type store struct {
	*fedinmem.Store
	platform *platforminmem.Store
}

func (s store) Configs() configdoc.ConfigStore { return s.platform.Configs() }
func (s store) WorkQueue() work.WorkQueue      { return s.platform.WorkQueue() }

func newStore(t *testing.T) store {
	t.Helper()
	db := memtx.New()
	s := store{Store: fedinmem.New(db), platform: platforminmem.New(db)}
	for _, p := range []model.Peer{
		{InstanceID: "b", BaseURL: "https://b.example/", UseCases: []model.UseCase{model.UseFederatedRepo}},
		{InstanceID: "c", BaseURL: "https://c.example", UseCases: []model.UseCase{model.UseFederatedRepo}},
		{InstanceID: "r", BaseURL: "https://r.example", UseCases: []model.UseCase{model.UseFederatedRepoRead}},
	} {
		if err := s.Peers().Put(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (s store) putScalar(t *testing.T, kind configdoc.ConfigKind, field, value string) {
	t.Helper()
	doc, _ := json.Marshal(map[string]string{field: value})
	if err := s.Configs().Put(t.Context(), configdoc.ConfigDocument{
		Kind: kind, Key: configdoc.BaseURLKey, Enabled: true, Document: doc,
	}); err != nil {
		t.Fatal(err)
	}
}

// queued lists the configSync keys waiting in the queue.
func (s store) queued(t *testing.T) []string {
	t.Helper()
	items, err := s.WorkQueue().Lease(t.Context(), work.WorkLease{
		Kinds: []string{federated.ConfigSyncKind}, Holder: "test", Limit: 100,
		Until: t0.Add(time.Hour), Now: t0.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, it := range items {
		keys = append(keys, it.Key)
	}
	slices.Sort(keys)
	return keys
}

func (s store) members(t *testing.T, key string) []model.FederationMember {
	t.Helper()
	m, err := s.Federation().Members(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// repositories is the artifacts module's Repositories API over a map.
type repositories struct {
	artifactsapi.Repositories
	values  map[string]repo.Config
	getErr  error
	listErr error
	prepErr error
	// mergeErr fails Merge; updErr fails Update.
	mergeErr, updErr error
}

func newRepositories(cs ...repo.Config) *repositories {
	r := &repositories{values: map[string]repo.Config{}}
	for _, c := range cs {
		r.values[c.Key] = c
	}
	return r
}

func (r *repositories) Get(_ context.Context, key string) (repo.Config, error) {
	if r.getErr != nil {
		return repo.Config{}, r.getErr
	}
	c, ok := r.values[key]
	if !ok {
		return repo.Config{}, kernel.ErrNotFound
	}
	return c, nil
}

func (r *repositories) List(context.Context, artifactsapi.RepoFilter) ([]repo.Config, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	keys := slices.Sorted(maps.Keys(r.values))
	out := make([]repo.Config, 0, len(keys))
	for _, k := range keys {
		out = append(out, r.values[k])
	}
	return out, nil
}

func (r *repositories) Create(_ context.Context, c repo.Config) error {
	if _, ok := r.values[c.Key]; ok {
		return kernel.ErrConflict
	}
	r.values[c.Key] = c
	return nil
}

func (r *repositories) Update(_ context.Context, c repo.Config) error {
	if r.updErr != nil {
		return r.updErr
	}
	if _, ok := r.values[c.Key]; !ok {
		return kernel.ErrNotFound
	}
	r.values[c.Key] = c
	return nil
}

func (r *repositories) PrepareCreate(c repo.Config, _ []repo.Config) (repo.Config, error) {
	if r.prepErr != nil {
		return repo.Config{}, r.prepErr
	}
	repo.Defaults(&c)
	return c, nil
}

func (r *repositories) Merge(stored repo.Config, patch map[string]json.RawMessage) (repo.Config, error) {
	if r.mergeErr != nil {
		return repo.Config{}, r.mergeErr
	}
	raw, _ := json.Marshal(patch)
	if err := json.Unmarshal(raw, &stored); err != nil {
		return repo.Config{}, repo.ErrInvalid
	}
	return stored, nil
}

func membership(s store) federated.Membership {
	return federated.Membership{Store: s, BaseURL: selfBase + "/", Now: func() time.Time { return t0 }}
}

func fed(key string, urls ...string) repo.Config {
	c := repo.Config{Key: key, Class: repo.Federated, PackageType: repo.Generic}
	for _, u := range urls {
		c.Members = append(c.Members, repo.Member{URL: u, Enabled: repo.Flex(true)})
	}
	return c
}

func urls(c repo.Config) []string {
	var out []string
	for _, m := range c.Members {
		out = append(out, m.URL)
	}
	return out
}

func refusal(t *testing.T, err error, want string) {
	t.Helper()
	var r *federated.RefusalError
	if !errors.As(err, &r) || !strings.Contains(r.Message, want) || !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("err = %v, want a refusal containing %q", err, want)
	}
}

func TestUnitMembershipAdmitStoresMembersAndSelf(t *testing.T) {
	s := newStore(t)
	ms := membership(s)
	c := fed("fed", "https://a.example/artifactory/fed/", "https://b.example/artifactory/fedb", "https://a.example/fed")
	if err := ms.Admit(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	// This instance's member, found by base and key, is the placeholder once.
	if got, want := urls(c), []string{"{self}/fed", bBase + "/fedb"}; !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	// Without its own member, the list gains one at the end.
	c = fed("fed", "https://c.example/fedc")
	if err := ms.Admit(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	if got, want := urls(c), []string{cBase + "/fedc", "{self}/fed"}; !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	// Another class passes untouched.
	local := repo.Config{Key: "l", Class: repo.Local, Members: []repo.Member{{URL: "x"}}}
	if err := ms.Admit(t.Context(), &local); err != nil || local.Members[0].URL != "x" {
		t.Fatalf("local: %v %v", err, local.Members)
	}
}

func TestUnitMembershipAdmitRefusals(t *testing.T) {
	s := newStore(t)
	ms := membership(s)
	withProxy := fed("fed")
	withProxy.Proxy = "missing"
	for _, tc := range []struct {
		name string
		c    repo.Config
		want string
	}{
		{
			"unpaired", fed("fed", "https://d.example/artifactory/fed"),
			"Federated member 'https://d.example/artifactory/fed' is not a paired peer: pair the instances for federated-repo/* first",
		},
		{
			"read-only trust for a bidirectional member", fed("fed", "https://r.example/artifactory/fed"),
			"pair the instances for federated-repo/* first",
		},
		{"invalid", fed("fed", "ftp://b.example/fed"), "is not a valid member URL"},
		{"another key's placeholder", fed("fed", "{self}/other"), "is not a valid member URL"},
		{"two members on one peer", fed("fed", bBase+"/one", "https://b.example/two"), "names an instance another member already names"},
		{"missing proxy", withProxy, "Proxy 'missing' does not exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.c
			refusal(t, ms.Admit(t.Context(), &c), tc.want)
		})
	}
	// A receiver member accepts receiving-only trust.
	c := fed("fed", "https://r.example/fed")
	c.Members[0].Mode = repo.ModeReceiver
	if err := ms.Admit(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	c = fed("fed", "https://d.example/fed")
	c.Members[0].Mode = repo.ModeReceiver
	refusal(t, ms.Admit(t.Context(), &c), "pair the instances for federated-repo/* or federated-repo/r first")
}

func TestUnitMembershipAdmitCapsMembers(t *testing.T) {
	s := newStore(t)
	for i := range 10 {
		id := string(rune('d' + i))
		if err := s.Peers().Put(t.Context(), model.Peer{
			InstanceID: id, BaseURL: "https://" + id + ".example", UseCases: []model.UseCase{model.UseFederatedRepo},
		}); err != nil {
			t.Fatal(err)
		}
	}
	c := fed("fed")
	for i := range 10 {
		c.Members = append(c.Members, repo.Member{URL: "https://" + string(rune('d'+i)) + ".example/fed"})
	}
	refusal(t, membership(s).Admit(t.Context(), &c), "allows at most 10 members, this instance's included")
	c.Members = c.Members[:9]
	if err := membership(s).Admit(t.Context(), &c); err != nil || len(c.Members) != 10 {
		t.Fatalf("nine others: %v, %d members", err, len(c.Members))
	}
}

func TestUnitMembershipBaseURL(t *testing.T) {
	s := newStore(t)
	ms := federated.Membership{Store: s}
	// No base URL: members are refused, none is fine.
	c := fed("fed", bBase+"/fed")
	refusal(t, ms.Admit(t.Context(), &c), federated.MsgNoBaseURL)
	empty := fed("fed")
	if err := ms.Admit(t.Context(), &empty); err != nil || len(empty.Members) != 0 {
		t.Fatalf("no members: %v %v", err, empty.Members)
	}
	// urlBase, then federatedRepoUrlBase, name this instance.
	s.putScalar(t, configdoc.ConfigURLBase, configdoc.URLBaseField, "https://url.example/")
	if got, err := ms.SelfBase(t.Context()); err != nil || got != "https://url.example" {
		t.Fatalf("urlBase: %q %v", got, err)
	}
	s.putScalar(t, configdoc.ConfigFederatedRepoURLBase, configdoc.FederatedRepoURLBaseField, "https://fed.example")
	c = fed("fed", "https://fed.example/artifactory/fed", "https://url.example/artifactory/fed")
	// The urlBase member is no longer this instance, nor a peer.
	refusal(t, ms.Admit(t.Context(), &c), "'https://url.example/artifactory/fed' is not a paired peer")
	// A broken stored document is a store error, not a refusal.
	if err := s.Configs().Put(t.Context(), configdoc.ConfigDocument{
		Kind: configdoc.ConfigFederatedRepoURLBase, Key: configdoc.BaseURLKey, Document: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := ms.Admit(t.Context(), &c); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("broken base: %v", err)
	}
}

func TestUnitMembershipReconcile(t *testing.T) {
	s := newStore(t)
	ms := membership(s)
	c := fed("fed", bBase+"/fedb", "https://c.example/fedc")
	c.Members[1].Enabled = repo.Flex(false)
	if err := ms.Reconcile(t.Context(), c, true); err != nil {
		t.Fatal(err)
	}
	got := s.members(t, "fed")
	if len(got) != 2 || got[0].PeerInstanceID != "b" || got[0].RemoteRepoKey != "fedb" || !got[0].Enabled ||
		got[1].PeerInstanceID != "c" || got[1].BaseURL != cBase || got[1].Enabled {
		t.Fatalf("members = %+v", got)
	}
	if q := s.queued(t); !slices.Equal(q, []string{"b/fed", "c/fed"}) {
		t.Fatalf("queued = %v", q)
	}
	// A configuration applied from a member queues nothing; any other
	// change, members unchanged or not, is sent to every member.
	s2 := newStore(t)
	ms2 := membership(s2)
	if err := ms2.Reconcile(t.Context(), c, false); err != nil {
		t.Fatal(err)
	}
	if q := s2.queued(t); len(q) != 0 {
		t.Fatalf("sync false queued %v", q)
	}
	if err := ms2.Reconcile(t.Context(), c, true); err != nil {
		t.Fatal(err)
	}
	if q := s2.queued(t); !slices.Equal(q, []string{"b/fed", "c/fed"}) {
		t.Fatalf("unchanged members queued %v", q)
	}
	// A repository no longer federated loses its members.
	c.Class = repo.Local
	if err := ms2.Reconcile(t.Context(), c, true); err != nil || len(s2.members(t, "fed")) != 0 {
		t.Fatalf("local: %v %v", err, s2.members(t, "fed"))
	}
	if err := ms.Clear(t.Context(), "fed"); err != nil || len(s.members(t, "fed")) != 0 {
		t.Fatalf("clear: %v", err)
	}
	refusal(t, ms.Reconcile(t.Context(), fed("fed", "https://d.example/fed"), true), "is not a paired peer")
}

func TestUnitPayloadResolvesThisInstance(t *testing.T) {
	c := fed("fed", "{self}/fed", bBase+"/fedb")
	got, err := federated.Payload(c, selfBase)
	if err != nil {
		t.Fatal(err)
	}
	members, _ := got["members"].([]any)
	first, _ := members[0].(map[string]any)
	if first["url"] != selfBase+"/artifactory/fed" || got["key"] != "fed" || got["rclass"] != "federated" {
		t.Fatalf("payload = %v", got)
	}
	if c.Members[0].URL != "{self}/fed" {
		t.Fatal("Payload changed its argument")
	}
}

// remote records the configurations sent and answers with err.
type remote struct {
	err  error
	sent []string
	cfg  map[string]any
}

func (r *remote) PutConfiguration(_ context.Context, p model.Peer, key, stream string, cfg map[string]any) error {
	r.sent = append(r.sent, p.InstanceID+" "+key+" "+stream)
	r.cfg = cfg
	return r.err
}

func TestUnitSenderSend(t *testing.T) {
	s := newStore(t)
	ms := membership(s)
	c := fed("fed", bBase+"/fedb")
	if err := ms.Admit(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	if err := ms.Reconcile(t.Context(), c, true); err != nil {
		t.Fatal(err)
	}
	repos := newRepositories(c, repo.Config{Key: "local", Class: repo.Local})
	rem := &remote{}
	send := federated.Sender{Membership: ms, Repositories: repos, Remote: rem}
	item := func(k string) work.WorkItem { return work.WorkItem{Kind: federated.ConfigSyncKind, Key: k} }

	if err := send.Send(t.Context(), item("b/fed")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rem.sent, []string{"b fedb repo:fed"}) {
		t.Fatalf("sent = %v", rem.sent)
	}
	members, _ := rem.cfg["members"].([]any)
	if self, _ := members[1].(map[string]any); self["url"] != selfBase+"/artifactory/fed" {
		t.Fatalf("members sent = %v", members)
	}
	// Gone, other-class, unknown member and unknown peer complete unsent.
	if err := s.Peers().Put(t.Context(), model.Peer{InstanceID: "gone"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"b/missing", "b/local", "c/fed"} {
		if err := send.Send(t.Context(), item(k)); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if err := s.Federation().SetMembers(t.Context(), "fed", []model.FederationMember{{
		RepoKey: "fed", PeerInstanceID: "nobody", BaseURL: "https://n.example/artifactory", RemoteRepoKey: "fed", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := send.Send(t.Context(), item("nobody/fed")); err != nil {
		t.Fatalf("unknown peer: %v", err)
	}
	if len(rem.sent) != 1 {
		t.Fatalf("sent = %v", rem.sent)
	}
	if err := send.Send(t.Context(), item("nokey")); !errors.Is(err, work.ErrPermanent) {
		t.Fatalf("bad key: %v", err)
	}
	if err := ms.Reconcile(t.Context(), c, false); err != nil {
		t.Fatal(err)
	}
	rem.err = &federated.RemoteRefusal{Status: http.StatusConflict, Message: "taken"}
	if err := send.Send(t.Context(), item("b/fed")); !errors.Is(err, work.ErrPermanent) ||
		!strings.Contains(err.Error(), "member answered 409: taken") {
		t.Fatalf("refusal: %v", err)
	}
	rem.err = federated.ErrUnavailable
	if err := send.Send(t.Context(), item("b/fed")); !errors.Is(err, work.ErrUnsettled) || errors.Is(err, work.ErrPermanent) {
		t.Fatalf("unavailable: %v", err)
	}
	repos.getErr = kernel.ErrStore
	if err := send.Send(t.Context(), item("b/fed")); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("read failure: %v", err)
	}
}

func caller(uses ...model.UseCase) model.Peer {
	return model.Peer{InstanceID: "b", BaseURL: "https://b.example/", UseCases: uses}
}

// sent is the configuration B sends A for A's repository "fed".
func sent(t *testing.T, mutate func(map[string]any)) map[string]any {
	t.Helper()
	c := fed("fedb", bBase+"/fedb", selfBase+"/artifactory/fed", "https://c.example/fedc")
	c.Description = "from b"
	out, err := federated.Payload(c, "https://b.example")
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(out)
	}
	return out
}

func TestUnitReceiverApply(t *testing.T) {
	s := newStore(t)
	repos := newRepositories()
	rcv := federated.Receiver{Membership: membership(s), Repositories: repos}
	if err := rcv.Apply(t.Context(), caller(model.UseFederatedRepo), "fed", sent(t, nil)); err != nil {
		t.Fatal(err)
	}
	got := repos.values["fed"]
	if got.Key != "fed" || got.Description != "from b" ||
		!slices.Equal(urls(got), []string{bBase + "/fedb", "{self}/fed", cBase + "/fedc"}) {
		t.Fatalf("created = %+v", got)
	}
	if m := s.members(t, "fed"); len(m) != 2 {
		t.Fatalf("members = %+v", m)
	}
	// Applying a member's change never sends it on.
	if q := s.queued(t); len(q) != 0 {
		t.Fatalf("queued = %v", q)
	}
	// An update merges onto this instance's copy and keeps its key.
	if err := rcv.Apply(t.Context(), caller(model.UseFederatedRepo), "fed", sent(t, func(m map[string]any) {
		m["description"] = "changed"
	})); err != nil {
		t.Fatal(err)
	}
	if got := repos.values["fed"]; got.Key != "fed" || got.Description != "changed" {
		t.Fatalf("updated = %+v", got)
	}
}

func TestUnitReceiverRefusals(t *testing.T) {
	s := newStore(t)
	repos := newRepositories(repo.Config{Key: "local", Class: repo.Local, PackageType: repo.Generic})
	rcv := federated.Receiver{Membership: membership(s), Repositories: repos}
	use := caller(model.UseFederatedRepo)
	if err := rcv.Apply(t.Context(), caller(model.UseFederatedRepoRead), "fed", sent(t, nil)); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("read-only caller: %v", err)
	}
	for _, tc := range []struct {
		name, key string
		mutate    func(map[string]any)
		want      string
	}{
		{"not a configuration", "fed", func(m map[string]any) { m["members"] = "x" }, "is not a repository configuration"},
		{"not federated", "fed", func(m map[string]any) { m["rclass"] = "local" }, "is not a federated repository"},
		{"bad member", "fed", func(m map[string]any) { m["members"] = []any{map[string]any{"url": "nope"}} }, "is not a valid member URL"},
		{"not this instance", "other", nil, "No member of the configuration is this instance's repository 'other'"},
		{"not the sender", "fed", func(m map[string]any) {
			m["members"] = []any{map[string]any{"url": selfBase + "/artifactory/fed"}}
		}, "No member of the configuration is the sending instance"},
		{"unpaired member", "fed", func(m map[string]any) {
			m["members"] = append(m["members"].([]any), map[string]any{"url": "https://d.example/artifactory/fed"})
		}, "is not a paired peer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusal(t, rcv.Apply(t.Context(), use, tc.key, sent(t, tc.mutate)), tc.want)
		})
	}
	if _, ok := repos.values["fed"]; ok {
		t.Fatal("a refused configuration was stored")
	}
	local := sent(t, func(m map[string]any) {
		m["members"] = []any{map[string]any{"url": bBase + "/fedb"}, map[string]any{"url": selfBase + "/artifactory/local"}}
	})
	if err := rcv.Apply(t.Context(), use, "local", local); !errors.Is(err, federated.ErrOtherRepository) {
		t.Fatalf("local repository: %v", err)
	}
	repos.prepErr = apierror.New(http.StatusBadRequest, "bad key")
	refusal(t, rcv.Apply(t.Context(), use, "fed", sent(t, nil)), "bad key")
	repos.prepErr = kernel.ErrStore
	if err := rcv.Apply(t.Context(), use, "fed", sent(t, nil)); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("prepare failure: %v", err)
	}
	repos.prepErr = nil
	repos.listErr = kernel.ErrStore
	if err := rcv.Apply(t.Context(), use, "fed", sent(t, nil)); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("list failure: %v", err)
	}
	repos.getErr = kernel.ErrStore
	if err := rcv.Apply(t.Context(), use, "fed", sent(t, nil)); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("get failure: %v", err)
	}
	// An update that does not merge is refused.
	repos.getErr, repos.listErr = nil, nil
	if err := rcv.Apply(t.Context(), use, "fed", sent(t, nil)); err != nil {
		t.Fatal(err)
	}
	repos.mergeErr = repo.ErrInvalid
	refusal(t, rcv.Apply(t.Context(), use, "fed", sent(t, nil)), repo.ErrInvalid.Error())
	repos.mergeErr, repos.updErr = nil, kernel.ErrStore
	if err := rcv.Apply(t.Context(), use, "fed", sent(t, nil)); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("update failure: %v", err)
	}
}

func operations(s store, repos *repositories, deny error) federated.Operations {
	return federated.Operations{
		Membership: membership(s), Repositories: repos,
		Authorize: func(context.Context) error { return deny },
	}
}

func TestUnitOperationsConfigSync(t *testing.T) {
	s := newStore(t)
	ms := membership(s)
	c := fed("fed", bBase+"/fedb", cBase+"/fedc")
	if err := ms.Admit(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	if err := ms.Reconcile(t.Context(), c, false); err != nil {
		t.Fatal(err)
	}
	ops := operations(s, newRepositories(c, repo.Config{Key: "local", Class: repo.Local}), nil)
	if err := ops.ConfigSync(t.Context(), "fed"); err != nil {
		t.Fatal(err)
	}
	if q := s.queued(t); !slices.Equal(q, []string{"b/fed", "c/fed"}) {
		t.Fatalf("queued = %v", q)
	}
	if err := ops.ConfigSync(t.Context(), "missing"); !errors.Is(err, federated.ErrNoRepository) {
		t.Fatalf("missing: %v", err)
	}
	refusal(t, ops.ConfigSync(t.Context(), "local"), "Repository 'local' is not a federated repository")
	if err := operations(s, newRepositories(), kernel.ErrForbidden).ConfigSync(t.Context(), "fed"); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("non-admin: %v", err)
	}
	broken := newRepositories()
	broken.getErr = kernel.ErrStore
	if err := operations(s, broken, nil).ConfigSync(t.Context(), "fed"); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("read failure: %v", err)
	}
}

func TestUnitOperationsMigrate(t *testing.T) {
	s := newStore(t)
	repos := newRepositories(
		repo.Config{Key: "local", Class: repo.Local, PackageType: repo.Generic},
		repo.Config{Key: "tf", Class: repo.Local, PackageType: repo.TerraformBackend},
		repo.Config{Key: "pushed", Class: repo.Local, PackageType: repo.Generic},
		repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Generic},
	)
	if err := s.Replications().Put(t.Context(), model.Replication{RepoKey: "pushed", Key: "r1", URL: "https://x.example/artifactory/t"}); err != nil {
		t.Fatal(err)
	}
	ops := operations(s, repos, nil)
	if err := ops.Migrate(t.Context(), "local"); err != nil {
		t.Fatal(err)
	}
	got := repos.values["local"]
	if got.Class != repo.Federated || !slices.Equal(urls(got), []string{"{self}/local"}) {
		t.Fatalf("migrated = %+v", got)
	}
	refusal(t, ops.Migrate(t.Context(), "missing"), "Repository 'missing' does not exist")
	refusal(t, ops.Migrate(t.Context(), "remote"), "Repository 'remote' is not a local repository")
	refusal(t, ops.Migrate(t.Context(), "pushed"), "has push replication configured")
	refusal(t, ops.Migrate(t.Context(), "tf"), "terraformbackend")
	repos.getErr = kernel.ErrStore
	if err := ops.Migrate(t.Context(), "local"); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("read failure: %v", err)
	}
}

func TestUnitOperationsConvertToLocal(t *testing.T) {
	s := newStore(t)
	ms := membership(s)
	alone := fed("alone")
	disabled := fed("disabled", bBase+"/x")
	disabled.Members[0].Enabled = repo.Flex(false)
	busy := fed("busy", bBase+"/y")
	for _, c := range []*repo.Config{&alone, &disabled, &busy} {
		if err := ms.Admit(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if err := ms.Reconcile(t.Context(), *c, false); err != nil {
			t.Fatal(err)
		}
	}
	repos := newRepositories(alone, disabled, busy, repo.Config{Key: "local", Class: repo.Local})
	ops := operations(s, repos, nil)
	converted, failed, err := ops.ConvertToLocal(t.Context(), []string{"alone", "busy", "disabled", "local", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(converted, []string{"alone", "disabled"}) {
		t.Fatalf("converted = %v", converted)
	}
	if want := map[string]string{
		"busy":    "Repository 'busy' is federated with the enabled member '" + bBase + "/y': remove it first",
		"local":   "Repository 'local' is not a federated repository",
		"missing": "Repository 'missing' does not exist",
	}; !maps.Equal(failed, want) {
		t.Fatalf("failed = %v", failed)
	}
	if c := repos.values["disabled"]; c.Class != repo.Local || len(c.Members) != 0 || len(s.members(t, "disabled")) != 0 {
		t.Fatalf("disabled = %+v", c)
	}
	if _, _, err := operations(s, repos, kernel.ErrForbidden).ConvertToLocal(t.Context(), []string{"busy"}); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("non-admin: %v", err)
	}
	repos.getErr = kernel.ErrStore
	if _, _, err := ops.ConvertToLocal(t.Context(), []string{"busy"}); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("read failure: %v", err)
	}
}

func TestUnitOperationsReplaceURL(t *testing.T) {
	s := newStore(t)
	ms := membership(s)
	one := fed("one", bBase+"/one")
	two := fed("two", "https://c.example/two")
	for _, c := range []*repo.Config{&one, &two} {
		if err := ms.Admit(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	repos := newRepositories(one, two, repo.Config{Key: "local", Class: repo.Local})
	ops := operations(s, repos, nil)
	refusal(t, ops.ReplaceURL(t.Context(), "", cBase), "oldBaseUrl and newBaseUrl are required")
	// The new base must be a paired peer; nothing changes when it is not.
	refusal(t, ops.ReplaceURL(t.Context(), "https://b.example", "https://d.example"), "is not a paired peer")
	if got := urls(repos.values["one"]); got[0] != bBase+"/one" {
		t.Fatalf("refused rewrite stored %v", got)
	}
	if err := s.Peers().Put(t.Context(), model.Peer{
		InstanceID: "b", BaseURL: "https://b2.example", UseCases: []model.UseCase{model.UseFederatedRepo},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ops.ReplaceURL(t.Context(), "https://b.example/", "https://b2.example/artifactory"); err != nil {
		t.Fatal(err)
	}
	if got := urls(repos.values["one"]); got[0] != "https://b2.example/artifactory/one" {
		t.Fatalf("one = %v", got)
	}
	if got := urls(repos.values["two"]); got[0] != cBase+"/two" {
		t.Fatalf("two = %v", got)
	}
	if q := s.queued(t); !slices.Equal(q, []string{"b/one"}) {
		t.Fatalf("queued = %v", q)
	}
	repos.listErr = kernel.ErrStore
	if err := ops.ReplaceURL(t.Context(), bBase, cBase); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("list failure: %v", err)
	}
}

// faulty fails the membership table or the queue with err.
type faulty struct {
	store
	members, queue error
}

type failingMembers struct {
	port.FederationStore
	err error
}

func (f failingMembers) Members(context.Context, string) ([]model.FederationMember, error) {
	return nil, f.err
}

func (f failingMembers) SetMembers(context.Context, string, []model.FederationMember) error {
	return f.err
}

type failingQueue struct {
	work.WorkQueue
	err error
}

func (f failingQueue) Enqueue(context.Context, string, string, []byte, time.Time) error { return f.err }

func (f faulty) Federation() port.FederationStore {
	if f.members != nil {
		return failingMembers{f.store.Federation(), f.members}
	}
	return f.store.Federation()
}

func (f faulty) WorkQueue() work.WorkQueue {
	if f.queue != nil {
		return failingQueue{f.store.WorkQueue(), f.queue}
	}
	return f.store.WorkQueue()
}

func TestUnitMembershipStoreFailures(t *testing.T) {
	c := fed("fed", bBase+"/fedb")
	repos := newRepositories(c)
	for _, f := range []faulty{{store: newStore(t), members: kernel.ErrStore}, {store: newStore(t), queue: kernel.ErrStore}} {
		ms := federated.Membership{Store: f, BaseURL: selfBase}
		if err := ms.Reconcile(t.Context(), c, true); !errors.Is(err, kernel.ErrStore) {
			t.Fatalf("reconcile: %v", err)
		}
		if f.members != nil {
			if err := ms.Clear(t.Context(), "fed"); !errors.Is(err, kernel.ErrStore) {
				t.Fatalf("clear: %v", err)
			}
			if err := (federated.Sender{Membership: ms, Repositories: repos}).Send(t.Context(),
				work.WorkItem{Key: "b/fed"}); !errors.Is(err, kernel.ErrStore) {
				t.Fatalf("send: %v", err)
			}
		}
		if err := operations(f.store, repos, nil).ConfigSync(t.Context(), "fed"); err != nil {
			t.Fatalf("config sync on a healthy store: %v", err)
		}
		ops := federated.Operations{Membership: ms, Repositories: repos, Authorize: func(context.Context) error { return nil }}
		if err := ops.ConfigSync(t.Context(), "fed"); f.members != nil && !errors.Is(err, kernel.ErrStore) {
			t.Fatalf("config sync: %v", err)
		}
	}
	if (&federated.RefusalError{Message: "m"}).Error() != "m" {
		t.Fatal("RefusalError.Error")
	}
}
