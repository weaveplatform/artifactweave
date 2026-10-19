package config

import (
	"fmt"
	"time"
)

// DefaultMavenSnapshotCleanupInterval is how often Artifactory 7.x's
// background process deletes the unique snapshots over a repository's
// maxUniqueSnapshots: "every 5 minutes" (JFrog KB, "How does the max unique
// snapshots parameter work", 2025-05-15; decision 0036).
const DefaultMavenSnapshotCleanupInterval = 5 * time.Minute

// MavenConfig holds the Maven, Gradle, Ivy and SBT repositories' options
// (decision 0036).
type MavenConfig struct {
	// SnapshotCleanupInterval is how often each node runs the
	// maxUniqueSnapshots cleanups that deploys have marked in the work
	// queue (maven.snapshot_cleanup_interval).
	SnapshotCleanupInterval time.Duration `yaml:"snapshot_cleanup_interval"`
}

// setMaven assigns one key under maven; sub is the key without that
// prefix.
func setMaven(m *MavenConfig, sub, key, value string) error {
	if sub != "snapshot_cleanup_interval" {
		return fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	return setDuration(&m.SnapshotCleanupInterval, key, value)
}

// validate refuses an interval shorter than a second, which would keep a
// node leasing for cleanups it has just run.
func (m MavenConfig) validate() []error {
	if m.SnapshotCleanupInterval < time.Second {
		return []error{fmt.Errorf("%w: maven.snapshot_cleanup_interval %s must be at least 1s",
			ErrInvalidValue, m.SnapshotCleanupInterval)}
	}
	return nil
}
