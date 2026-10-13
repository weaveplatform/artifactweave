package types

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/repo"
)

// ErrInvalidMemberURL reports a federation member URL that does not name a
// repository on an instance.
var ErrInvalidMemberURL = errors.New("invalid federation member URL")

// FederationMember is one member of a federated repository, normalised from
// the repository's members list. Members are peers paired for federation
// (decision 0022); RemoteRepoKey is the repository's key on that peer.
type FederationMember struct {
	RepoKey        string
	PeerInstanceID string
	BaseURL        string
	RemoteRepoKey  string
	Enabled        bool
}

// ParseMemberURL splits "<base URL>/artifactory/<repoKey>" into the instance
// base URL and the repository key.
func ParseMemberURL(raw string) (baseURL, repoKey string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidMemberURL, raw)
	}
	path := strings.TrimSuffix(u.Path, "/")
	i := strings.LastIndex(path, "/artifactory/")
	if i < 0 {
		return "", "", fmt.Errorf("%w: %q has no /artifactory/<repoKey>", ErrInvalidMemberURL, raw)
	}
	repoKey = path[i+len("/artifactory/"):]
	if repoKey == "" || strings.Contains(repoKey, "/") {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidMemberURL, raw)
	}
	u.Path = path[:i]
	u.RawQuery, u.Fragment = "", ""
	return u.String(), repoKey, nil
}

// MembersOf lists a federated repository's members from its configuration,
// skipping the entry for selfBaseURL (a member may list itself). The peer
// instance id is resolved later against the peer table.
func MembersOf(cfg *repo.Config, selfBaseURL string) ([]FederationMember, error) {
	var out []FederationMember
	self := strings.TrimSuffix(selfBaseURL, "/")
	for _, m := range cfg.Members {
		base, key, err := ParseMemberURL(m.URL)
		if err != nil {
			return nil, err
		}
		if base == self {
			continue
		}
		out = append(out, FederationMember{
			RepoKey:       cfg.Key,
			BaseURL:       base,
			RemoteRepoKey: key,
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
	Status         RunStatus
	ResultPaths    []string
}
