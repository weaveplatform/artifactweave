package federated

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// ErrUnavailable reports a member the configuration could not reach: the
// network, a timeout, TLS, or an answer that does not verify.
var ErrUnavailable = errors.New("federated member unavailable")

// RemoteRefusal is a member's refusal of the configuration: the status and
// message it answered with. Sending it again would get the same answer.
type RemoteRefusal struct {
	Status  int
	Message string
}

func (e *RemoteRefusal) Error() string {
	return "member answered " + strconv.Itoa(e.Status) + ": " + e.Message
}

// Remote sends a federated repository's configuration to a member: the peer
// API's PUT /repositories/{repoKey} through the signed peer client
// (decision 0021), and asks it for a full sync. It wraps ErrUnavailable
// when no verified answer arrived and returns *RemoteRefusal for any
// status but 204.
type Remote interface {
	PutConfiguration(ctx context.Context, peer model.Peer, remoteKey string, stream string, cfg map[string]any) error
	// RequestResync asks the member to resynchronise stream from this
	// instance with a full sync (the peer API's POST
	// /streams/{stream}/resync), with the same errors.
	RequestResync(ctx context.Context, peer model.Peer, stream string, forced bool) error
}

// Sender runs ConfigSyncKind: it sends a repository's current configuration
// to one member. It reads what it sends when it runs, so a run that
// follows several changes sends the latest once, and a repository or
// member gone by then completes with nothing sent.
type Sender struct {
	Membership   Membership
	Repositories interface {
		Get(ctx context.Context, key string) (repo.Config, error)
	}
	Remote Remote
	Log    *slog.Logger
}

func (s Sender) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// Send is the work handler. An unreachable member is logged as unavailable
// and retried for as long as it stays a member: the error wraps
// work.ErrUnsettled, which the queue retries with its capped backoff and
// never parks, so a member that returns after any outage receives the
// configuration. A member's refusal is logged and parked (work.ErrPermanent)
// until the next change or configSync queues the item again; a 5xx is a
// failure on the member's side, not a refusal, and is retried, or a
// passing fault there would leave the member's copy stale with only a log
// line to show it.
func (s Sender) Send(ctx context.Context, item work.WorkItem) error {
	peerID, key, ok := parseSyncKey(item.Key)
	if !ok {
		return fmt.Errorf("%w: configSync key %q", work.ErrPermanent, item.Key)
	}
	c, err := s.Repositories.Get(ctx, key)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("configSync %s: %w", key, err)
	}
	if c.Class != repo.Federated {
		return nil
	}
	st := s.Membership.Store
	members, err := st.Federation().Members(ctx, key)
	if err != nil {
		return fmt.Errorf("configSync %s: %w", key, err)
	}
	var member model.FederationMember
	for _, m := range members {
		if m.PeerInstanceID == peerID {
			member = m
		}
	}
	if member.PeerInstanceID == "" {
		return nil
	}
	peer, err := st.Peers().Get(ctx, peerID)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("configSync %s: %w", key, err)
	}
	self, err := s.Membership.SelfBase(ctx)
	if err != nil {
		return err
	}
	body, err := Payload(c, self)
	if err != nil {
		return err
	}
	err = s.Remote.PutConfiguration(ctx, peer, member.RemoteRepoKey, model.RepositoryStream(key), body)
	var refusal *RemoteRefusal
	switch {
	case err == nil:
		return nil
	case errors.As(err, &refusal) && refusal.Status < http.StatusInternalServerError:
		s.log().ErrorContext(ctx, "federated member refused the repository configuration",
			"repository", key, "member", repo.MemberURL(member.BaseURL, member.RemoteRepoKey),
			"status", refusal.Status, "message", refusal.Message)
		return fmt.Errorf("%w: configSync %s: %w", work.ErrPermanent, key, err)
	default:
		s.log().WarnContext(ctx, "federated member is unavailable; its configuration sync is retried",
			"repository", key, "member", repo.MemberURL(member.BaseURL, member.RemoteRepoKey), "error", err)
		return fmt.Errorf("%w: configSync %s: %w", work.ErrUnsettled, key, err)
	}
}

// Receiver applies a configuration a member sends (the peer API's PUT
// /repositories/{repoKey}): it creates or updates this instance's copy,
// checked against this instance's own peers.
type Receiver struct {
	Membership Membership
	// Repositories writes without consumer lifecycles: Apply reconciles
	// membership itself, without queueing configSync back to the sender.
	Repositories artifactsapi.Repositories
}

// Errors Apply reports besides *RefusalError, which the peer API answers
// 403 and 409.
var (
	// ErrNotFederatedPeer: the caller holds no two-way federated
	// repository trust with this instance.
	ErrNotFederatedPeer = fmt.Errorf("caller is not paired for %s: %w", model.UseFederatedRepo, kernel.ErrForbidden)
	// ErrOtherRepository: the key names a repository here that is not a
	// federated repository of the same package type.
	ErrOtherRepository = fmt.Errorf("repository exists and is not the same federated repository: %w", kernel.ErrConflict)
)

// Apply stores the configuration caller sent for repoKey, all or nothing.
// The caller must hold federated-repo/* with this instance: a member it
// paired with only federated-repo/r receives and never sends (decision
// 0022). The configuration must be a federated repository whose members
// name this instance at its own base URL and repoKey and the caller among
// the others; every other member must be a peer of this instance too.
func (r Receiver) Apply(ctx context.Context, caller model.Peer, repoKey string, cfg map[string]any) error {
	if !caller.Has(model.UseFederatedRepo) {
		return ErrNotFederatedPeer
	}
	var c repo.Config
	raw, err := json.Marshal(cfg)
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	if err != nil {
		return refuse("The configuration is not a repository configuration: %v", err)
	}
	if c.Class != repo.Federated {
		return refuse("The configuration is not a federated repository")
	}
	st := r.Membership.Store
	//nolint:wrapcheck // each step names what failed, and refusals keep their type
	return st.Tx(ctx, func(ctx context.Context) error {
		self, err := r.Membership.SelfBase(ctx)
		if err != nil {
			return err
		}
		if err := namesBoth(c, repoKey, self, caller); err != nil {
			return err
		}
		// From here the configuration is this instance's: its own key, and
		// its own member found as Admit finds it.
		c.Key = repoKey
		stored, err := r.Repositories.Get(ctx, repoKey)
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			return r.create(ctx, c)
		case err != nil:
			return fmt.Errorf("read repository %s: %w", repoKey, err)
		case stored.Class != repo.Federated || stored.PackageType != c.PackageType:
			return ErrOtherRepository
		}
		return r.update(ctx, stored, raw)
	})
}

// namesBoth checks the members name this instance (at self with repoKey)
// and the caller.
func namesBoth(c repo.Config, repoKey, self string, caller model.Peer) error {
	var me, sender bool
	for _, m := range c.Members {
		base, key, err := model.ParseMemberURL(m.URL)
		if err != nil {
			return refuse("Federated member '%s' is not a valid member URL", m.URL)
		}
		me = me || self != "" && key == repoKey && base == repo.NormalizeMemberBase(self)
		sender = sender || base == repo.NormalizeMemberBase(caller.BaseURL)
	}
	if !me {
		return refuse("No member of the configuration is this instance's repository '%s'", repoKey)
	}
	if !sender {
		return refuse("No member of the configuration is the sending instance")
	}
	return nil
}

func (r Receiver) create(ctx context.Context, c repo.Config) error {
	existing, err := r.Repositories.List(ctx, artifactsapi.RepoFilter{})
	if err != nil {
		return fmt.Errorf("list repositories: %w", err)
	}
	if c, err = r.Repositories.PrepareCreate(c, existing); err != nil {
		return asRefusal(err)
	}
	if err := r.Membership.Admit(ctx, &c); err != nil {
		return err
	}
	if err := r.Repositories.Create(ctx, c); err != nil {
		return fmt.Errorf("create repository %s: %w", c.Key, err)
	}
	return r.Membership.Reconcile(ctx, c, false)
}

func (r Receiver) update(ctx context.Context, stored repo.Config, raw []byte) error {
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(raw, &patch); err != nil {
		return refuse("The configuration is not a repository configuration: %v", err)
	}
	// The key is this instance's, which Merge refuses to change.
	delete(patch, "key")
	c, err := r.Repositories.Merge(stored, patch)
	if err != nil {
		return asRefusal(err)
	}
	if err := r.Membership.Admit(ctx, &c); err != nil {
		return err
	}
	if err := r.Repositories.Update(ctx, c); err != nil {
		return fmt.Errorf("update repository %s: %w", c.Key, err)
	}
	return r.Membership.Reconcile(ctx, c, false)
}

// asRefusal keeps a validation refusal's message as a *RefusalError and
// passes anything else on.
func asRefusal(err error) error {
	if errors.Is(err, repo.ErrInvalid) || errors.Is(err, kernel.ErrInvalid) || isBadRequest(err) {
		return &RefusalError{Message: err.Error()}
	}
	return err
}

// isBadRequest reports the artifacts module's 400 refusals (PrepareCreate's
// key and validation answers), which are apierrors.
func isBadRequest(err error) bool {
	var e *apierror.Error
	return errors.As(err, &e) && e.Status == http.StatusBadRequest
}
