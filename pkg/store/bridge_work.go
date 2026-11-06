// Bridge (P9d): declared in migration/platform/work; PRO-02 deletes it.

package store

import "github.com/weaveplatform/artifactweave/migration/platform/work"

type (
	WorkQueue   = work.WorkQueue
	WorkLease   = work.WorkLease
	WorkFailure = work.WorkFailure
	LeaseStore  = work.LeaseStore
)

const (
	MaxWorkKind    = work.MaxWorkKind
	MaxWorkKey     = work.MaxWorkKey
	MaxWorkPayload = work.MaxWorkPayload
)

var (
	ValidateWork  = work.ValidateWork
	ValidateLease = work.ValidateLease
)
