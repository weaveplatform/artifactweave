package federationhttp

import (
	"context"

	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// GetFederationConnectionDetails reports member mode separately from
// connection health; a healthy receiver reports Receiving_Only and HEALTHY.
func (a FederatedAPI) GetFederationConnectionDetails(
	ctx context.Context,
	req artgen.GetFederationConnectionDetailsRequestObject,
) (artgen.GetFederationConnectionDetailsResponseObject, error) {
	if a.Connections == nil {
		return nil, apierror.ErrNotImplemented
	}
	rows, err := a.Connections.Connections(ctx, req.RepoKey)
	if err != nil {
		return nil, federatedError(ctx, req.RepoKey, err)
	}
	result := make([]artgen.FederationConnection, 0, len(rows))
	for _, row := range rows {
		status := artgen.FederationConnectionStatus(row.Status)
		conn := artgen.FederationConnection{
			LocalRepoKey: &row.LocalRepoKey, LocalPlatformUrl: &row.LocalPlatformURL,
			RemoteRepoKey: &row.RemoteRepoKey, RemotePlatformUrl: &row.RemotePlatformURL,
			Status: &status, LagInMs: &row.LagInMS, QueuedEvents: &row.QueuedEvents,
			Disabled: &row.Disabled, Supported: &row.Supported,
		}
		if row.Mode != "" {
			conn.Mode = &row.Mode
		}
		result = append(result, conn)
	}
	return artgen.GetFederationConnectionDetails200JSONResponse{MemberConnections: &result}, nil
}
