// Bridge (P9d): declared in migration/platform/outbox; PRO-02 deletes it.

package types

import "github.com/weaveplatform/artifactweave/migration/platform/outbox"

var ErrInvalidHLC = outbox.ErrInvalidHLC

var ParseHLC = outbox.ParseHLC

type (
	HLC             = outbox.HLC
	OutboxEntry     = outbox.OutboxEntry
	PeerCursor      = outbox.PeerCursor
	AppliedMarker   = outbox.AppliedMarker
	FailedBlobFetch = outbox.FailedBlobFetch
)
