package docker_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support"
)

func dockerMatrixRows() []matrixRow {
	rows := registryMatrix()
	rows = append(rows, registryBlobMatrix()...)
	rows = append(rows, registryManifestMatrix()...)
	rows = append(rows, registryReferrersMatrix()...)
	rows = append(rows, registryListingMatrix()...)
	return rows
}

func TestAccDockerRouteTablesCoverImplemented(t *testing.T) {
	routes := support.DockerRoutes()
	status, matrix := map[string]bool{}, map[string]bool{}
	for _, row := range dockerStatusRows() {
		status[row.route] = true
	}
	for _, row := range dockerMatrixRows() {
		matrix[row.route] = true
	}
	for route := range routes {
		if !status[route] {
			t.Errorf("%s has no Docker status row", route)
		}
		if !matrix[route] {
			t.Errorf("%s has no Docker principal row", route)
		}
	}
	for route := range status {
		// Method-not-allowed coverage is intentionally outside the implemented route inventory.
		if route == "POST /v2/token" {
			continue
		}
		if !routes[route] {
			t.Errorf("%s is an unregistered Docker status row", route)
		}
	}
	for route := range matrix {
		if !routes[route] {
			t.Errorf("%s is an unregistered Docker principal row", route)
		}
	}
}
