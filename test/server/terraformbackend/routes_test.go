package terraformbackend_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestTerraformBackendRouteTablesCoverImplemented(t *testing.T) {
	routes := support.TerraformBackendRoutes()
	status, matrix := map[string]bool{}, map[string]bool{}
	for _, row := range tfbeStatusPart.rows() {
		status[row.route] = true
	}
	for _, row := range tfbeMatrixPart.rows() {
		matrix[row.route] = true
	}
	for route := range routes {
		if !status[route] {
			t.Errorf("%s has no backend status row", route)
		}
		if !matrix[route] {
			t.Errorf("%s has no backend principal row", route)
		}
	}
}
