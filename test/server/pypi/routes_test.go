package pypi_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestAccPypiRouteTablesCoverImplemented(t *testing.T) {
	routes := support.PyPIRoutes()
	status, matrix := map[string]bool{}, map[string]bool{}
	for route := range pypiStatusPart.handwritten {
		if !routes[route] {
			t.Errorf("%s is missing from the Pypi route inventory", route)
		}
	}
	for route := range pypiStatusPart.generated {
		if !routes[route] {
			t.Errorf("%s is missing from the PyPI route inventory", route)
		}
	}
	for _, row := range pypiStatusPart.rows() {
		status[row.route] = true
	}
	for _, row := range pypiMatrixPart.rows() {
		matrix[row.route] = true
	}
	for route := range routes {
		if !status[route] {
			t.Errorf("%s has no Pypi status row", route)
		}
		if !matrix[route] {
			t.Errorf("%s has no Pypi principal row", route)
		}
	}
	for route := range status {
		if !routes[route] {
			t.Errorf("%s is an unregistered Pypi status row", route)
		}
	}
	for route := range matrix {
		if !routes[route] {
			t.Errorf("%s is an unregistered Pypi principal row", route)
		}
	}
}
