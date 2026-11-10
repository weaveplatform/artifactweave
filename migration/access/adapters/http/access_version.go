package accesshttp

import (
	"context"

	accessgen "github.com/weaveplatform/artifactweave/migration/platform/gen/access"
)

// GetAccessVersion answers with the compatibility version as Access's name
// and version, because clients compare it to decide what Access supports
// (decision 0005).
func (s AccessService) GetAccessVersion(
	context.Context,
	accessgen.GetAccessVersionRequestObject,
) (accessgen.GetAccessVersionResponseObject, error) {
	return accessgen.GetAccessVersion200JSONResponse{
		Name:    s.deps.CompatVersion,
		Version: s.deps.CompatVersion,
	}, nil
}
