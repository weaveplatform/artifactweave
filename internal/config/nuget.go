package config

import (
	"fmt"
	"time"
)

// DefaultNuGetSnapshotCleanupInterval is how often Artifactory 7.x's
// background process deletes the unique snapshots over a repository's
// maxUniqueSnapshots, NuGet's among them: "every 5 minutes" (JFrog KB
// 000005403; decision 0038).
const DefaultNuGetSnapshotCleanupInterval = 5 * time.Minute

// NuGetConfig holds the NuGet repositories' options (decision 0038).
type NuGetConfig struct {
	// SnapshotCleanupInterval is how often each node runs the
	// maxUniqueSnapshots cleanups that pre-release pushes have marked in
	// the work queue (nuget.snapshot_cleanup_interval).
	SnapshotCleanupInterval time.Duration `yaml:"snapshot_cleanup_interval"`
}

// setNuGet assigns one key under nuget; sub is the key without that
// prefix.
func setNuGet(n *NuGetConfig, sub, key, value string) error {
	if sub != "snapshot_cleanup_interval" {
		return fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	return setDuration(&n.SnapshotCleanupInterval, key, value)
}

// validate refuses an interval shorter than a second, which would keep a
// node leasing for cleanups it has just run.
func (n NuGetConfig) validate() []error {
	if n.SnapshotCleanupInterval < time.Second {
		return []error{fmt.Errorf("%w: nuget.snapshot_cleanup_interval %s must be at least 1s",
			ErrInvalidValue, n.SnapshotCleanupInterval)}
	}
	return nil
}
