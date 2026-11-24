package federated

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// MsgNoBaseURL is 7.x's refusal of a federated repository with members on an
// instance with neither federatedRepoUrlBase nor urlBase
// (FederatedMemberUtils.java:140-143). The status that reaches a client is
// [I] (research 40, section 11, item 2); artifactweave answers 400.
const MsgNoBaseURL = "Custom base URL must be configured for federated repositories' functionality"

// ConfigSyncKind is the work that sends a federated repository's
// configuration to one member (configSync, decision 0022). Its key is
// "<peer instance id>/<repository key>", so one unreachable member retries
// on its own while the others are served. The kind string is persisted in
// work items, so it never changes.
const ConfigSyncKind = "federation.config-sync"

// Store is what membership reads and writes, every call in the unit of work
// its context carries.
type Store interface {
	tx.Runner
	Peers() port.PeerStore
	Federation() port.FederationStore
	Stamps() port.ItemStampStore
	Replications() port.ReplicationStore
	Configs() configdoc.ConfigStore
	Settings() settings.SettingsStore
	WorkQueue() work.WorkQueue
}

// RefusalError is a federated repository configuration this instance
// refuses; the repositories API answers it 400 in its envelope with
// Message.
type RefusalError struct{ Message string }

func (e *RefusalError) Error() string { return e.Message }

// Unwrap lets errors.Is(err, kernel.ErrInvalid) find a refusal.
func (e *RefusalError) Unwrap() error { return kernel.ErrInvalid }

func refuse(format string, a ...any) error {
	return &RefusalError{Message: fmt.Sprintf(format, a...)}
}

// Membership checks federated repository members against this instance's
// paired peers and base URL (decision 0022, Members) and keeps the
// federation_members table and the configSync work in step with the
// configurations stored.
type Membership struct {
	Store Store
	// BaseURL is the installation's configured base URL, which stands in
	// for urlBase when the descriptor sets none (configdoc.FederatedBase).
	BaseURL string
	// Now defaults to time.Now.
	Now func() time.Time
	// Changed, when set, runs in the transaction of every Reconcile, so
	// stream delivery starts at once for a member added or enabled, or for
	// this instance's own member enabled again, rather than at its next
	// sweep.
	Changed func(ctx context.Context, key string) error
	// Dropped, when set, runs in the transaction for each peer that stops
	// being a member of repository key, so the stream delivery engine
	// forgets its cursor and any full sync with it on the repository's
	// stream (streamsync.Engine.Forget): a removed member is gone from
	// every member that hears of the change, not only from the stream's
	// subscribers.
	Dropped func(ctx context.Context, key, peer string) error
}

// SelfBase is this instance's own member base URL: federatedRepoUrlBase,
// or urlBase, or the installation's base URL; "" when none is set.
func (m Membership) SelfBase(ctx context.Context) (string, error) {
	base, err := configdoc.FederatedBase(ctx, m.Store.Configs(), m.BaseURL)
	if err != nil {
		return "", fmt.Errorf("federation base URL: %w", err)
	}
	return base, nil
}

func (m Membership) now() time.Time {
	if m.Now == nil {
		return time.Now()
	}
	return m.Now()
}

// Admit applies the member rules to c, about to be stored, and rewrites its
// members to their stored form: each URL with its base normalized as 7.x
// does, and this instance's own member as repo.SelfMemberURL, added at the
// end when the list lacks it (FederatedMemberUtils.membersWithSelfMember).
// A configuration of another class passes untouched. Refusals are
// *RefusalError:
//   - a proxy that does not exist;
//   - members with neither federatedRepoUrlBase nor urlBase set (MsgNoBaseURL);
//   - a member whose base URL is not a peer paired for the use case its
//     mode needs, with research 40 section 10's message;
//   - two members on one peer, which the membership table cannot hold;
//   - more than repo.MaxMembers once this instance's own member is added.
func (m Membership) Admit(ctx context.Context, c *repo.Config) error {
	_, err := m.resolve(ctx, c)
	return err
}

// resolve is Admit, returning the members other than this instance with
// their peers.
func (m Membership) resolve(ctx context.Context, c *repo.Config) ([]model.FederationMember, error) {
	if c.Class != repo.Federated {
		return nil, nil
	}
	if c.Proxy != "" {
		if _, err := m.Store.Configs().Get(ctx, configdoc.ConfigProxy, c.Proxy); errors.Is(err, kernel.ErrNotFound) {
			return nil, refuse("Proxy '%s' does not exist", c.Proxy)
		} else if err != nil {
			return nil, fmt.Errorf("federated repository proxy: %w", err)
		}
	}
	self, err := m.SelfBase(ctx)
	if err != nil {
		return nil, err
	}
	if self == "" {
		if len(c.Members) > 0 {
			return nil, refuse("%s", MsgNoBaseURL)
		}
		return nil, nil
	}
	peers, err := m.Store.Peers().List(ctx, port.PeerFilter{})
	if err != nil {
		return nil, fmt.Errorf("federated repository members: %w", err)
	}
	stored := make([]repo.Member, 0, len(c.Members)+1)
	var out []model.FederationMember
	seenSelf := false
	for _, mem := range c.Members {
		if model.IsSelfMember(mem.URL, c.Key, self) {
			if seenSelf {
				continue
			}
			seenSelf = true
			mem.URL = repo.SelfMemberURL(c.Key)
			stored = append(stored, mem)
			continue
		}
		base, key, err := model.ParseMemberURL(mem.URL)
		if err != nil || base == repo.SelfMemberBase {
			return nil, refuse("Federated member '%s' is not a valid member URL", mem.URL)
		}
		peer, ok := pairedPeer(peers, base, mem.Mode)
		if !ok {
			return nil, refuse("Federated member '%s' is not a paired peer: pair the instances for %s first",
				mem.URL, useCasesFor(mem.Mode))
		}
		if slices.ContainsFunc(out, func(o model.FederationMember) bool { return o.PeerInstanceID == peer.InstanceID }) {
			return nil, refuse("Federated member '%s' names an instance another member already names", mem.URL)
		}
		mem.URL = repo.MemberURL(base, key)
		stored = append(stored, mem)
		out = append(out, model.FederationMember{
			RepoKey: c.Key, PeerInstanceID: peer.InstanceID, BaseURL: base, RemoteRepoKey: key,
			Mode: mem.Mode, Enabled: mem.Enabled == nil || mem.Enabled.Bool(),
		})
	}
	if !seenSelf {
		stored = append(stored, repo.Member{URL: repo.SelfMemberURL(c.Key), Enabled: repo.Flex(true)})
	}
	if len(stored) > repo.MaxMembers {
		return nil, refuse("%s", repo.ValidationError{{
			Field:   "members",
			Message: fmt.Sprintf("allows at most %d members, this instance's included", repo.MaxMembers),
		}}.Error())
	}
	c.Members = stored
	return out, nil
}

// useCasesFor names the pairing use cases a member of mode needs: a
// bidirectional member sends events both ways, so only two-way trust
// serves it; a receiver also accepts receiving-only trust.
func useCasesFor(mode repo.MemberMode) string {
	if mode == repo.ModeReceiver {
		return string(model.UseFederatedRepo) + " or " + string(model.UseFederatedRepoRead)
	}
	return string(model.UseFederatedRepo)
}

// pairedPeer finds the peer whose recorded base URL, normalized as member
// URLs are, is base and which holds a use case mode accepts.
func pairedPeer(peers []model.Peer, base string, mode repo.MemberMode) (model.Peer, bool) {
	for _, p := range peers {
		if repo.NormalizeMemberBase(p.BaseURL) != base {
			continue
		}
		if p.Has(model.UseFederatedRepo) || mode == repo.ModeReceiver && p.Has(model.UseFederatedRepoRead) {
			return p, true
		}
	}
	return model.Peer{}, false
}

// Reconcile stores c's resolved members, c being the configuration just
// stored, and, when sync is set, queues configSync to every member in the
// same transaction: any change to a federated repository's configuration
// travels, not only one to its members. A configuration of another class
// clears them. A configuration applied from a peer passes sync false: the
// member that changed it tells the others, and answering it would send the
// change straight back.
func (m Membership) Reconcile(ctx context.Context, c repo.Config, sync bool) error {
	members, err := m.resolve(ctx, &c)
	if err != nil {
		return err
	}
	before, err := m.Store.Federation().Members(ctx, c.Key)
	if err != nil {
		return fmt.Errorf("federated repository members: %w", err)
	}
	slices.SortFunc(members, func(a, b model.FederationMember) int {
		return strings.Compare(a.PeerInstanceID, b.PeerInstanceID)
	})
	if !sameMembers(before, members) {
		if err := m.Store.Federation().SetMembers(ctx, c.Key, members); err != nil {
			return fmt.Errorf("store federated repository members: %w", err)
		}
		if err := m.drop(ctx, c.Key, before, members); err != nil {
			return err
		}
	}
	if c.Class != repo.Federated {
		// The stamps settle a federated repository's changes; a local one
		// has none to settle, and should it be federated again, its
		// changes start from what it holds then.
		if err := m.Store.Stamps().Clear(ctx, c.Key); err != nil {
			return fmt.Errorf("clear federated repository stamps: %w", err)
		}
	}
	if m.Changed != nil {
		if err := m.Changed(ctx, c.Key); err != nil {
			return err
		}
	}
	if !sync {
		return nil
	}
	return m.Enqueue(ctx, c.Key, members)
}

// drop runs Dropped for each peer of before that after does not list.
func (m Membership) drop(ctx context.Context, key string, before, after []model.FederationMember) error {
	if m.Dropped == nil {
		return nil
	}
	for _, b := range before {
		if slices.ContainsFunc(after, func(a model.FederationMember) bool { return a.PeerInstanceID == b.PeerInstanceID }) {
			continue
		}
		if err := m.Dropped(ctx, key, b.PeerInstanceID); err != nil {
			return err
		}
	}
	return nil
}

func sameMembers(a, b []model.FederationMember) bool {
	return slices.EqualFunc(a, b, func(x, y model.FederationMember) bool {
		return x.PeerInstanceID == y.PeerInstanceID && x.BaseURL == y.BaseURL &&
			x.RemoteRepoKey == y.RemoteRepoKey && x.Mode == y.Mode && x.Enabled == y.Enabled
	})
}

// Clear drops a repository's members and its conflict-rule stamps, when
// it is deleted, created anew or converted to a local repository.
func (m Membership) Clear(ctx context.Context, key string) error {
	before, err := m.Store.Federation().Members(ctx, key)
	if err != nil {
		return fmt.Errorf("federated repository members: %w", err)
	}
	if err := m.Store.Federation().SetMembers(ctx, key, nil); err != nil {
		return fmt.Errorf("clear federated repository members: %w", err)
	}
	if err := m.drop(ctx, key, before, nil); err != nil {
		return err
	}
	if err := m.Store.Stamps().Clear(ctx, key); err != nil {
		return fmt.Errorf("clear federated repository stamps: %w", err)
	}
	return nil
}

// Enqueue queues configSync of repository key to each of members, in the
// transaction ctx carries; the send runs after it commits (ConfigSyncKind).
func (m Membership) Enqueue(ctx context.Context, key string, members []model.FederationMember) error {
	for _, mem := range members {
		if err := m.Store.WorkQueue().Enqueue(ctx, ConfigSyncKind, syncKey(mem.PeerInstanceID, key), nil, m.now()); err != nil {
			return fmt.Errorf("queue configSync of %s: %w", key, err)
		}
	}
	return nil
}

func syncKey(peer, key string) string { return peer + "/" + key }

// parseSyncKey splits a ConfigSyncKind key. Repository keys and instance
// ids hold no slash, so the first one separates them.
func parseSyncKey(k string) (peer, key string, ok bool) {
	peer, key, ok = strings.Cut(k, "/")
	return peer, key, ok && peer != "" && key != ""
}

// Payload is the configuration sent to members (the peer API's
// RepositoryConfiguration): c as GET /api/repositories/{key} answers an
// administrator, secrets masked, with this instance's own member resolved
// to selfBase, so each receiver finds itself and the sender among the
// members.
func Payload(c repo.Config, selfBase string) (map[string]any, error) {
	view := repo.Masked(repo.Full(c))
	repo.ResolveSelfMember(&view, selfBase)
	raw, err := json.Marshal(view)
	if err != nil {
		return nil, fmt.Errorf("encode federated repository configuration: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("encode federated repository configuration: %w", err)
	}
	return out, nil
}
