package api

import (
	"context"
	"errors"
	"time"
)

// ErrCleanupUnavailable retains content without inventing stale member facts.
var ErrCleanupUnavailable = errors.New("federated cleanup evidence unavailable")

// CleanupBatchSize is the maximum number of logical versions in one signed
// observation request. Files within a version do not cause additional calls.
const CleanupBatchSize = 64

// CleanupMaximumClockAllowance is conservative uncertainty in a peer's stored
// timestamps, matching the transport's accepted current clock skew. It never
// extends the independent local observation/publication expiry.
const CleanupMaximumClockAllowance = 5 * time.Minute

// CleanupVersion identifies a logical version, without a blob or credential.
type CleanupVersion struct {
	Package string `json:"package"`
	Version string `json:"version"`
}

// CleanupMember pins a currently resolved member and its private trust epoch.
// Failure never returns to an earlier value when a peer recovers.
type CleanupMember struct {
	Instance     string `json:"instance"`
	Base         string `json:"base"`
	Repository   string `json:"repository"`
	Mode         string `json:"mode"`
	Grant        string `json:"grant"`
	Failure      string `json:"failure"`
	LocalFailure uint64 `json:"local_failure"`
}

// CleanupScope binds observations to the repository lifetime, configuration,
// this instance's identity/base and every configured member. It grants no delete.
type CleanupScope struct {
	Repository  string          `json:"repository"`
	Incarnation string          `json:"incarnation"`
	Generation  string          `json:"generation"`
	Self        string          `json:"self"`
	Base        string          `json:"base"`
	Project     string          `json:"project"`
	Class       string          `json:"class"`
	PackageType string          `json:"package_type"`
	Members     []CleanupMember `json:"members"`
	Receiver    bool            `json:"receiver"`
}

// CleanupDownload is a member's coherent metadata observation. An absent
// version is unknown, rather than a claim that it has never been downloaded.
type CleanupDownload struct {
	CleanupVersion
	ID                int64     `json:"id"`
	Revision          string    `json:"revision"`
	Created           time.Time `json:"created"`
	LastDownloaded    time.Time `json:"last_downloaded"`
	AdmittedAt        time.Time `json:"admitted_at"`
	AdmissionRevision string    `json:"admission_revision"`
}

// CleanupPin identifies exactly one signed observation of one member version.
// It contains neither a URL credential nor a signing key.
type CleanupPin struct {
	Member         string        `json:"member"`
	Incarnation    string        `json:"incarnation"`
	Generation     string        `json:"generation"`
	Grant          string        `json:"grant"`
	ClockAllowance time.Duration `json:"clock_allowance"`
	CleanupDownload
}

// CleanupEvidence contains individually signed observations collected within a
// bounded local window. It is not an atomic view of all federation members.
type CleanupEvidence struct {
	Scope   CleanupScope `json:"scope"`
	Started time.Time    `json:"started"`
	Expires time.Time    `json:"expires"`
	Pins    []CleanupPin `json:"pins"`
}

// Cleanup supplies fresh read-only observations and local publication fences.
// Collect performs network I/O only outside transactions. Guard requires the
// caller's metadata transaction; Fresh must also run after publication waits.
type Cleanup interface {
	Scope(context.Context, string) (CleanupScope, error)
	Collect(context.Context, CleanupScope, []CleanupVersion) (CleanupEvidence, error)
	Guard(context.Context, CleanupEvidence) error
	Fresh(context.Context, CleanupEvidence) error
}
