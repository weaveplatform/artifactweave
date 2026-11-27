package federated

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Connection is one member connection in connectionStates. Mode's JSON
// name is inferred: receiving-only documentation distinguishes target mode
// Receiving_Only from connection status HEALTHY (research 40 section 6).
type Connection struct {
	LocalRepoKey      string
	LocalPlatformURL  string
	RemoteRepoKey     string
	RemotePlatformURL string
	Status            string
	Mode              string
	LagInMS           int64
	QueuedEvents      int64
	Disabled          bool
	Supported         bool
}

// ErrNoConnectionBaseURL selects the source-backed empty 400 response when
// a federated repository exists but its instance has no configured base URL.
var ErrNoConnectionBaseURL = errors.New("federation connection base URL is not configured")

// connectionConfig classifies the repository before reading its base URL,
// preserving the empty-response order at the source resource boundary.
func (o Operations) connectionConfig(ctx context.Context, key string) (repo.Config, string, error) {
	c, err := o.Repositories.Get(ctx, key)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return repo.Config{}, "", nil
	case err != nil:
		return repo.Config{}, "", fmt.Errorf("read repository %s: %w", key, err)
	case c.Class != repo.Federated:
		return c, "", nil
	}
	self, err := o.Membership.SelfBase(ctx)
	if err != nil {
		return c, "", err
	}
	if self == "" {
		return c, "", ErrNoConnectionBaseURL
	}
	return c, self, nil
}

// ConnectionLagThreshold is the same monitoring threshold used by the
// repository, lag and state projections.
const ConnectionLagThreshold = StatusLagThreshold

// Connections retains FED-03's public projection over the common bounded
// member read. Receiver role remains separate from connection health.
func (o Operations) Connections(ctx context.Context, key string) ([]Connection, error) {
	rows, err := (Monitoring{Operations: o}).ConnectionStates(ctx, key)
	if err != nil {
		return nil, err
	}
	result := make([]Connection, 0, len(rows))
	for _, row := range rows {
		result = append(result, Connection{
			LocalRepoKey: key, LocalPlatformURL: row.LocalPlatformURL,
			RemoteRepoKey: row.Member.RemoteRepoKey, RemotePlatformURL: row.Member.BaseURL,
			Status: row.Status, Mode: row.Mode, LagInMS: row.LagInMS, QueuedEvents: row.QueuedEvents,
			Disabled: row.Disabled, Supported: row.Supported,
		})
	}
	return result, nil
}
