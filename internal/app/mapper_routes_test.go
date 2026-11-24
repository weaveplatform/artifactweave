package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestUnitMapperRoutes: the REST roots whose 7.161.26 resources refuse an
// anonymous caller through the exception mappers are marked, below them
// too, while a root's longer namesake, an operation that builds its own
// 401, a package format's endpoint, a closed resource's root and a path
// outside the REST API are not.
func TestUnitMapperRoutes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/artifactory/api/storage/a-local/x.bin", true},
		{http.MethodGet, "/artifactory/api/storageinfo", true},
		{http.MethodGet, "/artifactory/api/security/users", true},
		{http.MethodGet, "/artifactory/api/security/token", true},
		{http.MethodPost, "/artifactory/api/security/token", false},
		{http.MethodGet, "/artifactory/api/system/metrics", false},
		{http.MethodPost, "/artifactory/api/replications/propagateEvents", false},
		{http.MethodGet, "/artifactory/api/replications/a-local", true},
		{http.MethodGet, "/artifactory/api/build", true},
		{http.MethodGet, "/artifactory/api/builds/buildArtifacts/n/1/r", true},
		{http.MethodGet, "/artifactory/api/buildsx", false},
		{http.MethodGet, "/artifactory/api/v2/security/permissions/pt", true},
		{http.MethodGet, "/artifactory/api/versions/a-local/_any", false},
		{http.MethodGet, "/artifactory/api/archive/download/a-local", false},
		{http.MethodGet, "/artifactory/api/npm/npm-local/pkg", false},
		{http.MethodGet, "/artifactory/api/vcs/tags/a/b/c", false},
		{http.MethodGet, "/artifactory/api/securityconfig", false},
		{http.MethodGet, "/artifactory/a-local/x.bin", false},
		{http.MethodGet, "/access/api/v1/users", false},
	} {
		if got := mapperRoutes(httptest.NewRequest(tc.method, tc.path, nil)); got != tc.want {
			t.Errorf("%s %s: %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
