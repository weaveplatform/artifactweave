package model

import (
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// Replication is one replication target of a repository. Key is the
// server-assigned replicationKey; Password is sealed.
type Replication struct {
	RepoKey              string
	Key                  string
	URL                  string
	Username             string
	Password             string `json:"-"`
	Enabled              bool
	CronExp              string
	EventReplication     bool
	SyncDeletes          bool
	SyncProperties       bool
	SyncStatistics       bool
	IncludePathPrefix    string
	ExcludePathPrefix    string
	Proxy                string
	DisableProxy         bool
	CheckBinaryExistence bool
	SocketTimeout        time.Duration
}

// ReplicationDirection is derived from the repository class.
type ReplicationDirection string

// Replication directions.
const (
	ReplicationPush ReplicationDirection = "push"
	ReplicationPull ReplicationDirection = "pull"
)

// DirectionFor returns how a repository of class c replicates: locals push,
// remotes pull; other classes do not replicate.
func DirectionFor(c repo.Class) (ReplicationDirection, bool) {
	switch c {
	case repo.Local:
		return ReplicationPush, true
	case repo.Remote:
		return ReplicationPull, true
	default:
		return "", false
	}
}

// ReplicationStatus is the reported state of a repository's replication.
type ReplicationStatus string

// Replication statuses.
const (
	ReplicationUnknown        ReplicationStatus = "unknown"
	ReplicationNeverRun       ReplicationStatus = "never_run"
	ReplicationIncomplete     ReplicationStatus = "incomplete"
	ReplicationError          ReplicationStatus = "error"
	ReplicationWarn           ReplicationStatus = "warn"
	ReplicationOK             ReplicationStatus = "ok"
	ReplicationInconsistent   ReplicationStatus = "inconsistent"
	ReplicationPartialFailure ReplicationStatus = "partial_failure"
)

// ReplicationRun records one execution of a replication, for the status
// endpoint. A zero Completed means the run is still going.
type ReplicationRun struct {
	ReplicationKey string
	Started        time.Time
	Completed      time.Time
	Status         ReplicationStatus
}
