package terraform_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestTerraformRouteTablesCoverImplemented(t *testing.T) {
	routes := support.TerraformRoutes()
	status, matrix := map[string]bool{}, map[string]bool{}
	for _, part := range []statusPart{tfRegistryStatusPart, tfProviderStatusPart} {
		for route := range part.handwritten {
			if !routes[route] {
				t.Errorf("%s is missing from Terraform route inventory", route)
			}
		}
		for _, row := range part.rows() {
			status[row.route] = true
		}
	}
	for _, part := range []matrixPart{terraformMatrixPart, tfProviderMatrixPart} {
		for _, row := range part.rows() {
			matrix[row.route] = true
		}
	}
	for route := range routes {
		if !status[route] {
			t.Errorf("%s has no Terraform status row", route)
		}
		if !matrix[route] {
			t.Errorf("%s has no Terraform principal row", route)
		}
	}
	for route := range status {
		if !routes[route] {
			t.Errorf("%s is an unregistered Terraform status row", route)
		}
	}
	for route := range matrix {
		if !routes[route] {
			t.Errorf("%s is an unregistered Terraform principal row", route)
		}
	}
}
