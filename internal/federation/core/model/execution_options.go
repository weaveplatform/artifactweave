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
	URL, Username, Proxy                      string
	Password                                  string `json:"-"`
	Include, Exclude                          string
	SyncDeletes, SyncProperties, DisableProxy bool
	SocketTimeout                             time.Duration
}

// Validate rejects corrupt private options without including their values in
// errors. Admission additionally applies direction, scope and transport policy.
func (o ExecutionOptions) Validate() error {
	for _, value := range []string{o.URL, o.Username, o.Password, o.Proxy, o.Include, o.Exclude} {
		if !privateText(value) {
			return fmt.Errorf("invalid execution option text: %w", kernel.ErrInvalid)
		}
	}
	if o.SocketTimeout < 0 {
		return fmt.Errorf("invalid execution option timeout: %w", kernel.ErrInvalid)
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
