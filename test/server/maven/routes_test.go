package maven_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestMavenRouteTablesCoverImplemented(t *testing.T) {
	routes := support.MavenRoutes()
	status, matrix := map[string]bool{}, map[string]bool{}
	for route := range mavenStatusPart.handwritten {
		if !routes[route] {
			t.Errorf("%s is missing from the Maven route inventory", route)
		}
	}
	for _, row := range mavenStatusPart.rows() {
		status[row.route] = true
	}
	for _, row := range mavenMatrixPart.rows() {
		matrix[row.route] = true
	}
	for route := range routes {
		if !status[route] {
			t.Errorf("%s has no Maven status row", route)
		}
		if !matrix[route] {
			t.Errorf("%s has no Maven principal row", route)
		}
	}
	for route := range status {
		if !routes[route] {
			t.Errorf("%s is an unregistered Maven status row", route)
		}
	}
	for route := range matrix {
		if !routes[route] {
			t.Errorf("%s is an unregistered Maven principal row", route)
		}
	}
}
