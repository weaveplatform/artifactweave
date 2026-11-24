package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

// ErrInvalidMemberURL reports a federation member URL that does not name a
// repository on an instance.
var ErrInvalidMemberURL = errors.New("invalid federation member URL")

// FederationMember is one member of a federated repository, normalised from
// the repository's members list. Members are peers paired for federation
// (decision 0022); BaseURL is the member's base URL as 7.x normalizes it,
// ending in /artifactory, and RemoteRepoKey the repository's key on that
// peer.
type FederationMember struct {
	RepoKey        string
	PeerInstanceID string
	BaseURL        string
	RemoteRepoKey  string
	Mode           repo.MemberMode
	Enabled        bool
}

// Receiver reports whether the member only receives (decision 0022).
func (m FederationMember) Receiver() bool { return m.Mode == repo.ModeReceiver }

// ParseMemberURL splits a member URL into its base URL, normalized by 7.x's
// rule (trailing slashes trimmed, /artifactory appended when missing), and
// the repository key, its last path element (research 40, section 3).
func ParseMemberURL(raw string) (baseURL, repoKey string, err error) {
	base, key, err := repo.SplitMemberURL(raw)
	if err != nil {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidMemberURL, raw)
	}
	return base, key, nil
}

// IsSelfMember reports whether a member URL names this instance's own
// member of repository key: the stored placeholder, or selfBase (this
// instance's federatedRepoUrlBase, or urlBase when that is empty) with the
// repository's key (FederatedMemberUtils.java:126-129).
func IsSelfMember(raw, key, selfBase string) bool {
	base, memberKey, err := repo.SplitMemberURL(raw)
	if err != nil || memberKey != key {
		return false
	}
	return base == repo.SelfMemberBase ||
		strings.TrimSpace(selfBase) != "" && base == repo.NormalizeMemberBase(selfBase)
}

// MembersOf lists a federated repository's members from its configuration,
// skipping this instance's own (IsSelfMember). The peer instance id is
// resolved later against the peer table.
func MembersOf(cfg *repo.Config, selfBase string) ([]FederationMember, error) {
	var out []FederationMember
	for _, m := range cfg.Members {
		base, key, err := ParseMemberURL(m.URL)
		if err != nil {
			return nil, err
		}
		if IsSelfMember(m.URL, cfg.Key, selfBase) {
			continue
		}
		out = append(out, FederationMember{
			RepoKey:       cfg.Key,
			BaseURL:       base,
			RemoteRepoKey: key,
			Mode:          m.Mode,
			Enabled:       m.Enabled == nil || m.Enabled.Bool(),
		})
	}
	return out, nil
}

// FederationPriority orders the dispatch of a repository's outbox batches.
type FederationPriority string

// Federation priorities.
const (
	PriorityHigh     FederationPriority = "HIGH"
	PriorityStandard FederationPriority = "STANDARD"
)

// FederationSettings are artifactweave's per-repository federation switches.
// They have no field in JFrog's repository JSON, so they are stored as their
// own document beside the repository configuration.
type FederationSettings struct {
	Priority             FederationPriority `json:"priority,omitempty"`
	PropagateCreatedTime bool               `json:"propagate_created_time,omitempty"`
	Disabled             bool               `json:"disabled,omitempty"`
}

// SyncStatus is a federated repository's reported state.
type SyncStatus string

// Sync statuses.
const (
	SyncSyncing         SyncStatus = "SYNCING"
	SyncFullSyncRunning SyncStatus = "FULL_SYNC_RUNNING"
	SyncDisabled        SyncStatus = "DISABLED"
	SyncOutOfSync       SyncStatus = "OUT_OF_SYNC"
)

// DeriveSyncStatus applies decision 0022: disabled wins, then a running
// snapshot, then out of sync when any member lags beyond the threshold.
func DeriveSyncStatus(
	s FederationSettings,
	fullSyncRunning bool,
	lags []time.Duration,
	threshold time.Duration,
) SyncStatus {
	switch {
	case s.Disabled:
		return SyncDisabled
	case fullSyncRunning:
		return SyncFullSyncRunning
	}
	for _, l := range lags {
		if l > threshold {
			return SyncOutOfSync
		}
	}
	return SyncSyncing
}

// FullSyncRun is one snapshot resynchronisation of a repository with a peer.
type FullSyncRun struct {
	ID             int64
	RepoKey        string
	PeerInstanceID string
	Forced         bool
	SnapshotSeq    int64
	Started        time.Time
	Finished       time.Time
	Status         retentionapi.RunStatus
	ResultPaths    []string
}
