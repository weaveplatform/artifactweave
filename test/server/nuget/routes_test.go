package nuget_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestAccNuGetRouteTablesCoverImplemented(t *testing.T) {
	routes := support.NuGetRoutes()
	status, matrix := map[string]bool{}, map[string]bool{}
	for _, row := range nugetStatusRows() {
		status[row.route] = true
	}
	for _, row := range nugetMatrixPart.rows() {
		matrix[row.route] = true
	}
	for route := range routes {
		if !status[route] {
			t.Errorf("%s has no NuGet status row", route)
		}
		if !matrix[route] {
			t.Errorf("%s has no NuGet principal row", route)
		}
	}
	for route := range status {
		if !routes[route] {
			t.Errorf("%s is an unregistered NuGet status row", route)
		}
	}
	for route := range matrix {
		if !routes[route] {
			t.Errorf("%s is an unregistered NuGet principal row", route)
		}
	}
}
