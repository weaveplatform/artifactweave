package port

import (
	"context"
	"io"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// PushTargetItem is secret-free observed destination state. Paths remain
// canonical repository-relative names, never URLs or recursive delete grants.
type PushTargetItem struct {
	Path       string
	Snapshot   model.PushTargetSnapshot
	Properties map[string][]string
}

// PushMutationFence refreshes local source/configuration/ownership immediately
// before a remote mutation, after any blocking metadata reads or body rewind.
// It opens only a short local transaction and returns on the external context;
// no metadata transaction or local lock may span the following HTTP exchange.
type PushMutationFence func(context.Context) error

// PushConnection performs normal authenticated destination repository APIs.
// Every method except Revision performs network I/O and must refuse an enclosing
// metadata transaction. A successful response transfers no local authority.
type PushConnection interface {
	// Revision binds the current target credentials, proxy and transport policy.
	// Connection construction resolves local settings only; DNS/HTTP wait until
	// an operation is called outside the metadata transaction.
	Revision() string
	Inspect(context.Context, string) (PushTargetItem, error)
	// Put uses normal item admission and hooks, never a physical blob path or
	// checksum-only shortcut. A folder has no body; a file body stays caller-owned.
	Put(context.Context, artifactsapi.TransferItem, io.ReadSeeker, PushMutationFence) error
	// Properties replaces the selected item's user property set through normal
	// metadata APIs, explicitly removing destination-only keys without recursion.
	Properties(context.Context, string, map[string][]string, PushMutationFence) error
	// Files returns a bounded deterministic page. Empty pages may continue;
	// filtering never turns an unreadable response into an authorized absence.
	Files(context.Context, string, kernel.Page) ([]PushTargetItem, string, error)
	// Delete refreshes the exact observed file before normal DELETE. A changed
	// file or folder is refused. Cross-server atomicity is not implied: the
	// driver separately checks current source absence before and after I/O.
	Delete(context.Context, string, model.PushTargetSnapshot, PushMutationFence) error
}

// PushConnections refreshes current local connection policy for the given
// unsealed target. It performs no DNS/HTTP and joins the caller's transaction.
// The application owns pooled connection lifetimes and closes them at shutdown.
type PushConnections interface {
	Connection(context.Context, model.Replication) (PushConnection, error)
}

// PushTargetFailure carries only a closed classification, never a response body,
// URL, header or password. Unknown failures remain retryable and are not stored.
type PushTargetFailure string

// Permanent destination refusals. Transient transport failures use their
// sanitized transport/context error and retain the durable unverified intent.
const (
	PushTargetUnauthorized PushTargetFailure = "target_unauthorized"
	PushTargetForbidden    PushTargetFailure = "target_forbidden"
	PushTargetChecksum     PushTargetFailure = "target_checksum_mismatch"
	PushTargetMetadata     PushTargetFailure = "target_metadata_mismatch"
	PushTargetInvalid      PushTargetFailure = "target_invalid_response"
)

func (f PushTargetFailure) Error() string { return "replication: " + f.Reason() }

// Reason maps even an invalid implementation-supplied value to a fixed safe
// code; arbitrary upstream text can never become a persisted per-item reason.
func (f PushTargetFailure) Reason() string {
	switch f {
	case PushTargetUnauthorized, PushTargetForbidden, PushTargetChecksum, PushTargetMetadata, PushTargetInvalid:
		return string(f)
	default:
		return string(PushTargetInvalid)
	}
}
