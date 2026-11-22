package model

import (
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ExecutionOptions is a resolved, immutable manual-run override. Its password
// lives in a separately sealed column, never in execution or queue JSON. Values
// are complete, so an empty password means anonymous rather than inheritance.
type ExecutionOptions struct {
	// Independent is an explicit push target, not a saved configuration.
	// Its identity is bound to this exact execution by ValidateBinding.
	Independent                               bool
	RepositoryKey, TargetKey, TargetRevision  string
	URL, Username, Proxy                      string
	Password                                  string `json:"-"`
	Include, Exclude                          string
	SyncDeletes, SyncProperties, DisableProxy bool
	SocketTimeout                             time.Duration
}

// Validate rejects corrupt private options without including their values in
// errors. Admission additionally applies direction, scope and transport policy.
func (o ExecutionOptions) Validate() error {
	for _, value := range []string{o.URL, o.Username, o.Password, o.Proxy, o.Include, o.Exclude, o.RepositoryKey, o.TargetKey, o.TargetRevision} {
		if !privateText(value) {
			return fmt.Errorf("invalid execution option text: %w", kernel.ErrInvalid)
		}
	}
	if o.SocketTimeout < 0 {
		return fmt.Errorf("invalid execution option timeout: %w", kernel.ErrInvalid)
	}
	if o.Independent && (o.RepositoryKey == "" || o.TargetKey == "" || o.TargetRevision == "" || o.URL == "" || o.SocketTimeout <= 0) {
		return fmt.Errorf("incomplete explicit execution target: %w", kernel.ErrInvalid)
	}
	return nil
}

// ValidateBinding prevents an independent target from borrowing another
// execution's repository, direction or immutable target lifetime.
func (o ExecutionOptions) ValidateBinding(run ExecutionRun) error {
	if err := o.Validate(); err != nil {
		return err
	}
	if !run.Binding.OneShot || o.Independent && (run.Binding.Direction != ReplicationPush ||
		o.RepositoryKey != run.Binding.Repository.Key || o.TargetKey != run.Binding.TargetKey || o.TargetRevision != run.Binding.TargetRevision) {
		return fmt.Errorf("execution options disagree with dispatch: %w", kernel.ErrConflict)
	}
	return nil
}

// Apply changes only per-run transfer values, preserving the current target's
// identity, enablement, schedule author and unsupported capability controls.
func (o ExecutionOptions) Apply(target Replication) Replication {
	target.URL, target.Username, target.Password = o.URL, o.Username, o.Password
	target.Proxy, target.DisableProxy, target.SocketTimeout = o.Proxy, o.DisableProxy, o.SocketTimeout
	target.IncludePathPrefix, target.ExcludePathPrefix = o.Include, o.Exclude
	target.SyncDeletes, target.SyncProperties = o.SyncDeletes, o.SyncProperties
	return target
}
