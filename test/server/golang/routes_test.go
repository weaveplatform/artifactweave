package golang_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestGoRouteTablesCoverImplemented(t *testing.T) {
	routes := support.GoRoutes()
	status, matrix := map[string]bool{}, map[string]bool{}
	for route := range goStatusPart.handwritten {
		if !routes[route] {
			t.Errorf("%s is missing from the shared Go route inventory", route)
		}
	}
	for _, row := range goStatusPart.rows() {
		status[row.route] = true
	}
	for _, row := range goMatrixPart.rows() {
		matrix[row.route] = true
	}
	for route := range routes {
		if !goStatusPart.handwritten[route] {
			t.Errorf("%s is missing from the Go status part inventory", route)
		}
		if !status[route] {
			t.Errorf("%s has no Go status row", route)
		}
		if !matrix[route] {
			t.Errorf("%s has no Go principal row", route)
		}
	}
	for route := range status {
		if !routes[route] && route != goSumDB && route != "GET "+goBase+"{repoKey}/*" &&
			route != "POST "+goBase+"{repoKey}/*" && route != "POST "+goBase+"{repoKey}/{module}/@v/list" &&
			route != "DELETE "+goBase+"{repoKey}/{module}/@v/{version}.zip" {
			t.Errorf("%s is an unregistered Go status row", route)
		}
	}
	for route := range matrix {
		if !routes[route] {
			t.Errorf("%s is an unregistered Go principal row", route)
		}
	}
}
