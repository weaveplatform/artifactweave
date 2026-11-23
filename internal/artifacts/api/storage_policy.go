package api

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// StoragePolicy is the effective garbage collection schedule and trash can
// policy, with Artifactory's defaults filled in for what was never set: the
// descriptor's gcConfig and trashcanConfig sections describe the same state.
type StoragePolicy struct {
	// GCCron is the Quartz expression, normalized, on which scheduled
	// garbage collection passes run. Fire times are evaluated in UTC.
	GCCron string
	// TrashEnabled says whether a deleted local item waits in the trash can.
	TrashEnabled bool
	// TrashRetentionDays is how long a trashed item waits before a
	// collection pass purges it; zero purges it at the next pass.
	TrashRetentionDays int
}

// StoragePolicyChange is what a descriptor sets. A nil field keeps the
// current value.
type StoragePolicyChange struct {
	GCCron             *string
	TrashEnabled       *bool
	TrashRetentionDays *int
}

// StoragePolicyResult says which halves of the policy a change alters, or
// with Plan would alter.
type StoragePolicyResult struct {
	GCChanged    bool
	TrashChanged bool
}

// The fields a StoragePolicyInvalid names.
const (
	StoragePolicyGCCron             = "gc.cron"
	StoragePolicyTrashRetentionDays = "trash.retention_days"
)

// StoragePolicyInvalid refuses a change. Field is one of the
// StoragePolicy* names, so each surface can name the field in its own
// spelling. It matches kernel.ErrInvalid.
type StoragePolicyInvalid struct {
	Field  string
	Reason string
}

func (e *StoragePolicyInvalid) Error() string { return e.Field + " " + e.Reason }

// Unwrap makes a refusal match kernel.ErrInvalid.
func (e *StoragePolicyInvalid) Unwrap() error { return kernel.ErrInvalid }

// StoragePolicies reads and changes the storage policy through the services
// that own collection and the trash can, so a change takes effect on the
// next deletion and the next scheduled pass. Each call uses ctx for the
// store it reads or writes: inside a store transaction it takes part in it,
// so a refused or failed descriptor changes neither the policy nor the
// schedule. A refusal is a *StoragePolicyInvalid. now bounds the retention
// period and the first fire of a changed schedule.
type StoragePolicies interface {
	Current(ctx context.Context) (StoragePolicy, error)
	Plan(ctx context.Context, change StoragePolicyChange, now time.Time) (StoragePolicyResult, error)
	Apply(ctx context.Context, change StoragePolicyChange, now time.Time) (StoragePolicyResult, error)
}
