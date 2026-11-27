package api

import (
	"context"
	"errors"
)

// ErrCleanupExcluded is temporary refusal of a hosted operation while a
// coordinated cleanup reservation owns this exact repository lifetime.
var ErrCleanupExcluded = errors.New("repository cleanup exclusion is active")

// CleanupVersionIdentity names one existing version, including its immutable
// member-local index identity. A same-name replacement is not the same version.
type CleanupVersionIdentity struct {
	DownloadVersion
	ID int64 `json:"id"`
}

// CleanupExclusion is a durable participant reservation, not a timed lease.
// Owner is the coordinator instance and ID is its immutable operation identity.
// Repository, Incarnation and Generation bind the exact repository context.
type CleanupExclusion struct {
	ID          string                   `json:"id"`
	Owner       string                   `json:"owner"`
	Repository  string                   `json:"repository"`
	Incarnation string                   `json:"incarnation"`
	Generation  string                   `json:"generation"`
	Versions    []CleanupVersionIdentity `json:"versions"`
}

// CleanupAdmission is private state for a hosted response admitted before an
// exclusion closes the repository. It is independent of public download counts.
// A zero ID means that this response needs no federated admission tracking.
type CleanupAdmission struct {
	ID, Repository, Incarnation string
}

type cleanupExclusionKey struct{}

// WithCleanupExclusion carries a proposed local destructive permit. Stores must
// still verify its complete identity against the durable reservation before
// allowing a mutation; a context value alone never bypasses exclusion.
func WithCleanupExclusion(ctx context.Context, exclusion CleanupExclusion) context.Context {
	return context.WithValue(ctx, cleanupExclusionKey{}, exclusion)
}

// CleanupExclusionFromContext returns a proposed permit for local publication.
func CleanupExclusionFromContext(ctx context.Context) (CleanupExclusion, bool) {
	exclusion, ok := ctx.Value(cleanupExclusionKey{}).(CleanupExclusion)
	return exclusion, ok
}

// CleanupExclusions serializes hosted download admission and participant
// exclusion through short metadata transactions. No method keeps a transaction
// open over client or peer network I/O.
//
// Admit must finish before a hosted response writes bytes or issues its member
// redirect. Complete is called after that response ends, including cancellation;
// a missing completion stays ambiguous and prevents affected cleanup. HEAD and
// metadata-only responses do not acquire a download admission. Public statistics
// and their skip/range rules remain unchanged.
// Private admissions include hosted local responses so migration to federated
// cannot erase an active response or let a stale local snapshot evade a barrier.
//
// Prepare atomically closes new admissions, binds every requested version ID
// and refuses every active or ambiguous admission in that repository, including
// other versions whose files a native deletion hook could remove. Once acknowledged, the reservation
// persists until Release; neither time nor a serving-process restart expires it.
// Guard joins the destructive publication transaction and verifies the same
// reservation. Ordinary writes/lifetime changes cannot evade a held reservation;
// trusted ordered federation application remains able to apply its tombstones.
//
// Release invokes authorized inside its short transaction for a new terminal
// binding. An exact already-terminal binding acknowledges idempotently before
// current repository or callback restrictions and never clears a replacement. The federation owner
// must already have obtained the coordinator's durable, irreversible outcome
// outside a transaction. The callback verifies its exact binding and either a
// fenced abort or the local applied cleanup watermark. A network error, elapsed
// deadline, missing coordinator or ambiguous outcome never authorizes release.
//
// Release retains a terminal identity marker so a delayed Prepare cannot
// resurrect an operation after its irreversible outcome has released the gate.
type CleanupExclusions interface {
	Admit(context.Context, string, int64) (CleanupAdmission, error)
	Complete(context.Context, CleanupAdmission) error
	Prepare(context.Context, CleanupExclusion) error
	Guard(context.Context, CleanupExclusion) error
	Release(context.Context, CleanupExclusion, func(context.Context) error) error
}
