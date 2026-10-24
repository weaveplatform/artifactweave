package server

import "testing"

// These tests exercise path helpers that cannot be called through the
// extracted HTTP suite. In particular, log redaction covers credential
// segments before the request reaches a protocol handler.
func TestAccNPMInternalPaths(t *testing.T) {
	for path, want := range map[string]bool{
		"/artifactory/api/npm/npm-local/-/user/org.couchdb.user:a": true,
		"/artifactory/api/npm/npm-local/-/user/token/x":            true,
		"/artifactory/api/npm//-/user/token/x":                     false,
		"/artifactory/api/npm/npm-local/-/v1/login":                false,
		"/artifactory/api/npm/npm-local":                           false,
		"/artifactory/api/go/x/-/user/token/x":                     false,
	} {
		if got := npmUserPath(path); got != want {
			t.Errorf("npmUserPath(%s) = %v", path, got)
		}
	}
	for path, want := range map[string]bool{
		"/artifactory/api/npm/auth":                true,
		"/artifactory/api/npm/npm-local/auth/acme": true,
		"/artifactory/api/npm/npm-local/auth/":     false,
		"/artifactory/api/npm//auth/acme":          false,
		"/artifactory/api/npm/npm-local/auth":      false,
		"/artifactory/api/npm/npm-local/x/acme":    false,
		"/artifactory/api/npm/npm-local/auth/a/b":  false,
		"/artifactory/api/go/auth":                 false,
	} {
		if got := npmOperation(path); got != want {
			t.Errorf("npmOperation(%s) = %v", path, got)
		}
	}
	for path, want := range map[string]string{
		"/artifactory/api/npm/r/-/user/token/secret":          "/artifactory/api/npm/r/-/user/token/[redacted]",
		"/artifactory/api/npm/r/-/whoami":                     "/artifactory/api/npm/r/-/whoami",
		"/artifactory/r/-/user/token/secret":                  "/artifactory/r/-/user/token/secret",
		"/artifactory/api/luarocks/rocks/api/1/secret/upload": "/artifactory/api/luarocks/rocks/api/1/[redacted]/upload",
	} {
		if got := loggedPath(path); got != want {
			t.Errorf("loggedPath(%s) = %s", path, got)
		}
	}
}
