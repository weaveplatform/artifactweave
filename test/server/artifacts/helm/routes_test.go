package helm_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestAccHelmRouteTablesCoverImplemented(t *testing.T) {
	routes := support.HelmRoutes()
	status, matrix := map[string]bool{}, map[string]bool{}
	for route := range helmProtocolStatusPart.handwritten {
		if !routes[route] {
			t.Errorf("%s is missing from the Helm route inventory", route)
		}
	}
	for _, row := range helmProtocolStatusPart.rows() {
		status[row.route] = true
	}
	for _, row := range helmProtocolMatrixPart.rows() {
		matrix[row.route] = true
	}
	for route := range routes {
		if !status[route] {
			t.Errorf("%s has no Helm status row", route)
		}
		if !matrix[route] {
			t.Errorf("%s has no Helm principal row", route)
		}
	}
	for route := range status {
		if !routes[route] {
			t.Errorf("%s is an unregistered Helm status row", route)
		}
	}
	for route := range matrix {
		if !routes[route] {
			t.Errorf("%s is an unregistered Helm principal row", route)
		}
	}
}
