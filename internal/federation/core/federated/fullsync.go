package federated

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// FullSyncRequestKind is the work that asks one member to resynchronise a
// federated repository from this instance with a full sync (the peer
// API's POST /streams/{stream}/resync). Its key is
// "<peer instance id>/<repository key>/<mode>", the mode "normal" or
// "forced", so an unreachable member is asked again on its own, and a
// normal request queued after a forced one does not replace it. The kind
// string is persisted in work items, so it never changes.
const FullSyncRequestKind = "federation.full-sync-request"

// MaxHighPriority is how many federated repositories may have high
// priority at once, as 7.x documents for prioritization.
const MaxHighPriority = 10

// ErrNoStreams reports an operation that needs the stream delivery engine
// on a server that runs without one.
// on a server that runs without one; the API answers it 501, as the
// operation's stub did.
var ErrNoStreams = errors.New("federated repository streams are not running")

func requestKey(peer, key string, forced bool) string {
	mode := "normal"
	if forced {
		mode = "forced"
	}
	return peer + "/" + key + "/" + mode
}

// parseRequestKey splits a FullSyncRequestKind key. Neither instance ids
// nor repository keys hold a slash.
func parseRequestKey(k string) (peer, key string, forced, ok bool) {
	parts := strings.Split(k, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] != "normal" && parts[2] != "forced" {
		return "", "", false, false
	}
	return parts[0], parts[1], parts[2] == "forced", true
}

// QueueFullSync queues, in the transaction ctx carries, the request that
// member peer resynchronise repository key from this instance with a full
// sync, forced or not; it is sent after the transaction commits
// (FullSyncRequestKind). fullSync and forceFullSync call it for each member
// they name, and adding a receiver member (FED-03) for that member.
func (m Membership) QueueFullSync(ctx context.Context, key, peer string, forced bool) error {
	if err := m.Store.WorkQueue().Enqueue(ctx, FullSyncRequestKind, requestKey(peer, key, forced), nil, m.now()); err != nil {
		return fmt.Errorf("queue a full sync of %s to %s: %w", key, peer, err)
	}
	return nil
}

// Resyncer runs FullSyncRequestKind: it asks one member for a full sync of
// a repository from this instance. A repository or member gone by the time
// it runs completes with nothing asked.
type Resyncer struct {
	Membership Membership
	Remote     Remote
	// Repositories lets a receiver's initial full sync establish its configuration first.
	Repositories interface {
		Get(context.Context, string) (repo.Config, error)
	}
	Log *slog.Logger
}

func (r Resyncer) log() *slog.Logger {
	if r.Log == nil {
		return slog.Default()
	}
	return r.Log
}

// Send is the work handler. As configSync's, an unreachable member is
// retried for as long as it stays a member (work.ErrUnsettled), and a
// member's refusal is logged and parked (work.ErrPermanent). A member's
// 5xx is a failure on its side, not a refusal: parked, the operator's full
// sync would silently never run there, so it is asked again.
func (r Resyncer) Send(ctx context.Context, item work.WorkItem) error {
	peerID, key, forced, ok := parseRequestKey(item.Key)
	if !ok {
		return fmt.Errorf("%w: full-sync request key %q", work.ErrPermanent, item.Key)
	}
	st := r.Membership.Store
	members, err := st.Federation().Members(ctx, key)
	if err != nil {
		return fmt.Errorf("full sync of %s: %w", key, err)
	}
	i := slices.IndexFunc(members, func(m model.FederationMember) bool { return m.PeerInstanceID == peerID })
	if i < 0 {
		return nil
	}
	member := members[i]
	peer, err := st.Peers().Get(ctx, peerID)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("full sync of %s: %w", key, err)
	}
	if member.Mode == repo.ModeReceiver && r.Repositories != nil {
		sender := Sender{Membership: r.Membership, Repositories: r.Repositories, Remote: r.Remote, Log: r.Log}
		if err := sender.Send(ctx, work.WorkItem{Key: syncKey(peerID, key)}); err != nil {
			return err
		}
	}
	err = r.Remote.RequestResync(ctx, peer, model.RepositoryStream(key), forced)
	var refusal *RemoteRefusal
	switch {
	case err == nil:
		return nil
	case errors.As(err, &refusal) && refusal.Status < http.StatusInternalServerError:
		r.log().ErrorContext(ctx, "federated member refused a full sync",
			"repository", key, "member", repo.MemberURL(member.BaseURL, member.RemoteRepoKey), "forced", forced,
			"status", refusal.Status, "message", refusal.Message)
		return fmt.Errorf("%w: full sync of %s: %w", work.ErrPermanent, key, err)
	default:
		r.log().WarnContext(ctx, "federated member is unavailable; its full sync is asked again",
			"repository", key, "member", repo.MemberURL(member.BaseURL, member.RemoteRepoKey), "error", err)
		return fmt.Errorf("%w: full sync of %s: %w", work.ErrUnsettled, key, err)
	}
}

// FullSync runs a full sync of federated repository key from this
// instance to its members (POST federation/fullSync and
// forceFullSync/{repoKey}), or to the one mirror names, by its base URL or
// its member URL: each is asked to resynchronise the repository from this
// instance's snapshot (QueueFullSync), as 7.x's full sync runs from the
// sender towards each member. A normal run gives a member what it lacks
// and settles what both hold by the conflict rule; a forced one also
// discards what the member holds that this instance does not. A run
// already going on a member is continued, a forced request making it
// forced. Each member records its run in its full_sync_runs.
func (o Operations) FullSync(ctx context.Context, key, mirror string, forced bool) error {
	return o.tx(ctx, func(ctx context.Context) error {
		c, err := o.federated(ctx, key)
		if err != nil {
			return err
		}
		self, err := o.Membership.SelfBase(ctx)
		if err != nil {
			return err
		}
		if selfReceiver(c, self) {
			return refuse("Repository '%s' is in receiver mode: full sync cannot be initiated locally", key)
		}
		members, err := o.Membership.Store.Federation().Members(ctx, key)
		if err != nil {
			return fmt.Errorf("federated repository members: %w", err)
		}
		if mirror != "" {
			members = slices.DeleteFunc(members, func(m model.FederationMember) bool { return !names(mirror, m) })
			if len(members) == 0 {
				return refuse("Federated member '%s' is not a member of repository '%s'", mirror, key)
			}
		}
		for _, m := range members {
			if err := o.Membership.QueueFullSync(ctx, key, m.PeerInstanceID, forced); err != nil {
				return err
			}
		}
		return nil
	})
}

// names reports whether raw, a base URL or a member URL, names member m.
func names(raw string, m model.FederationMember) bool {
	stored := repo.NormalizeMemberBase(m.BaseURL)
	if repo.NormalizeMemberBase(raw) == stored {
		return true
	}
	base, key, err := model.ParseMemberURL(raw)
	return err == nil && repo.NormalizeMemberBase(base) == stored && key == m.RemoteRepoKey
}

// scope is the federated repositories an operation acts on: key, which
// must be one, or every federated repository when key is empty.
func (o Operations) scope(ctx context.Context, key string) ([]string, error) {
	if key != "" {
		if _, err := o.federated(ctx, key); err != nil {
			return nil, err
		}
		return []string{key}, nil
	}
	all, err := o.Repositories.List(ctx, artifactsapi.RepoFilter{})
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	var out []string
	for _, c := range all {
		if c.Class == repo.Federated {
			out = append(out, c.Key)
		}
	}
	return out, nil
}

func (o Operations) engine() (*streamsync.Engine, error) {
	if o.Streams == nil {
		return nil, ErrNoStreams
	}
	e := o.Streams()
	if e == nil {
		return nil, ErrNoStreams
	}
	return e, nil
}

// Recovery retries at once the delivery of repository key's events to its
// members, or of every federated repository's when key is empty, that is
// backing off after failures; with force, every member's, failing or not
// (POST federation/recovery). Delivery resumes from each member's
// acknowledged cursor, so nothing applies twice. The retries run from the
// queue after the call returns (streamsync.RecoverKind).
func (o Operations) Recovery(ctx context.Context, key string, force bool) error {
	return o.tx(ctx, func(ctx context.Context) error {
		keys, err := o.scope(ctx, key)
		if err != nil {
			return err
		}
		e, err := o.engine()
		if err != nil {
			return err
		}
		for _, k := range keys {
			if err := e.QueueRecovery(ctx, model.RepositoryStream(k), force); err != nil {
				return err //nolint:wrapcheck // the engine names the stream
			}
		}
		return nil
	})
}

// Reconnect re-establishes delivery to every member of every federated
// repository (POST federation/reconnect): 7.x reconnects to its
// federation service; artifactweave's peer client holds no session, so
// what there is to re-establish is delivery, which is Recovery forced for
// every repository.
func (o Operations) Reconnect(ctx context.Context) error {
	return o.Recovery(ctx, "", true)
}

// RemoveMember removes the member that raw names, by its base URL or its
// member URL, from every federated repository on this instance (POST
// federation/removeMember). Each changed configuration is sent to the
// remaining members, which drop the member too, and every instance that
// drops it stops exchanging the repository's stream with it: its cursor
// and any full sync between the two go (Membership.Dropped). The removed
// instance keeps its copies, as 7.x documents, and its events are refused
// as a non-member's. It returns how many repositories it changed; none is
// a refusal.
func (o Operations) RemoveMember(ctx context.Context, raw string) (int, error) {
	var n int
	err := o.tx(ctx, func(ctx context.Context) error {
		if strings.TrimSpace(raw) == "" {
			return refuse("The base URL of the member to remove is required")
		}
		keys, err := o.scope(ctx, "")
		if err != nil {
			return err
		}
		for _, key := range keys {
			done, err := o.removeFrom(ctx, key, raw)
			if err != nil {
				return err
			}
			if done {
				n++
			}
		}
		if n == 0 {
			return refuse("Federated member '%s' is not a member of any federated repository", raw)
		}
		return nil
	})
	return n, err
}

func (o Operations) removeFrom(ctx context.Context, key, raw string) (bool, error) {
	members, err := o.Membership.Store.Federation().Members(ctx, key)
	if err != nil {
		return false, fmt.Errorf("federated repository members: %w", err)
	}
	gone := slices.DeleteFunc(members, func(m model.FederationMember) bool { return !names(raw, m) })
	if len(gone) == 0 {
		return false, nil
	}
	c, err := o.Repositories.Get(ctx, key)
	if err != nil {
		return false, fmt.Errorf("read repository %s: %w", key, err)
	}
	c.Members = slices.DeleteFunc(c.Members, func(m repo.Member) bool {
		base, k, err := model.ParseMemberURL(m.URL)
		return err == nil && slices.ContainsFunc(gone, func(g model.FederationMember) bool {
			return repo.NormalizeMemberBase(g.BaseURL) == repo.NormalizeMemberBase(base) && g.RemoteRepoKey == k
		})
	})
	if err := o.write(ctx, c); err != nil {
		return false, err
	}
	return true, nil
}

// Prioritize sets repository key's priority, HIGH or STANDARD in any case
// (PUT federation/prioritization/{repoKey}): a HIGH repository's events
// are dispatched to its members before STANDARD ones' when both wait
// (streamsync.PriorityLead). At most MaxHighPriority repositories are HIGH
// at once.
func (o Operations) Prioritize(ctx context.Context, key, level string) error {
	return o.tx(ctx, func(ctx context.Context) error {
		if _, err := o.federated(ctx, key); err != nil {
			return err
		}
		p := model.FederationPriority(strings.ToUpper(level))
		if p != model.PriorityHigh && p != model.PriorityStandard {
			return refuse("priorityLevel must be HIGH or STANDARD, not '%s'", level)
		}
		st := o.Membership.Store.Settings()
		high, err := HighPriority(ctx, st)
		if err != nil {
			return err
		}
		// Repositories deleted or converted since they were prioritized
		// hold no place.
		high = slices.DeleteFunc(high, func(k string) bool {
			c, err := o.Repositories.Get(ctx, k)
			return k == key || err != nil || c.Class != repo.Federated
		})
		if p == model.PriorityHigh {
			if len(high) >= MaxHighPriority {
				return refuse("At most %d federated repositories can have high priority at once", MaxHighPriority)
			}
			high = append(high, key)
			slices.Sort(high)
		}
		if err := st.Put(ctx, settings.SettingFederationPriority, high); err != nil {
			return fmt.Errorf("store %s: %w", settings.SettingFederationPriority, err)
		}
		return nil
	})
}

// HighPriority lists the repositories prioritization made HIGH.
func HighPriority(ctx context.Context, st settings.SettingsStore) ([]string, error) {
	var high []string
	if err := st.Get(ctx, settings.SettingFederationPriority, &high); err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return nil, fmt.Errorf("read %s: %w", settings.SettingFederationPriority, err)
	}
	return high, nil
}

// Priority is the delivery engine's dispatch order for stream
// (streamsync.Engine.Priority): 1 for a HIGH repository's stream, 0 for
// any other. A setting that cannot be read orders nothing first.
func Priority(st settings.SettingsStore) func(ctx context.Context, stream string) int {
	return func(ctx context.Context, stream string) int {
		k, ok := key(stream)
		if !ok {
			return 0
		}
		high, err := HighPriority(ctx, st)
		if err != nil || !slices.Contains(high, k) {
			return 0
		}
		return 1
	}
}

// SetPropagateCreatedTime turns on or off, instance-wide, whether full
// sync gives each item its original creation time (PUT
// federation/propagateCreatedTime). It is this instance's setting as the
// sender: the snapshots it serves carry creation times while it is on.
func (o Operations) SetPropagateCreatedTime(ctx context.Context, enabled bool) error {
	return o.tx(ctx, func(ctx context.Context) error {
		if err := o.Membership.Store.Settings().Put(ctx, settings.SettingFederationPropagateCreatedTime, enabled); err != nil {
			return fmt.Errorf("store %s: %w", settings.SettingFederationPropagateCreatedTime, err)
		}
		return nil
	})
}
