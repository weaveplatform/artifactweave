package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// implemented lists the generated operations that have real handlers:
// generatedRoutes, and each format's (statusParts). Every other generated
// operation must reach the UnimplementedStrict stub and answer 501 in its
// service's envelope; this list grows as phases land.
var implemented = routeUnion(append(
	[]map[string]bool{generatedRoutes, support.NPMGeneratedRoutes()},
	partRoutes(func(p statusPart) map[string]bool { return p.generated })...,
)...)

// generatedRoutes are the generated operations with real handlers outside
// the formats' parts.
var generatedRoutes = map[string]bool{
	"GET /artifactory/api/system/version":          true,
	"GET /artifactory/api/system/license":          true,
	"GET /artifactory/api/system/service_id":       true,
	"GET /artifactory/api/v1/system/readiness":     true,
	"GET /artifactory/api/v1/system/liveness":      true,
	"POST /artifactory/api/deb/{repoKey}/snapshot": true,
	"GET /access/api/v1/system/version":            true,

	"GET /artifactory/api/repositories":                                true,
	"GET /artifactory/api/repositories/configurations":                 true,
	"GET /artifactory/api/repositories/existence":                      true,
	"GET /artifactory/api/repositories/{remoteRepoName}/configuration": true,
	"GET /artifactory/api/repositories/{repoKey}":                      true,
	"PUT /artifactory/api/repositories/{repoKey}":                      true,
	"POST /artifactory/api/repositories/{repoKey}":                     true,
	"DELETE /artifactory/api/repositories/{repoKey}":                   true,
	"GET /artifactory/api/v2/repositories/{repoKey}":                   true,
	"GET /artifactory/api/v2/repositories/batch":                       true,
	"PUT /artifactory/api/v2/repositories/batch":                       true,
	"POST /artifactory/api/v2/repositories/batch":                      true,
	"DELETE /artifactory/api/v2/repositories/batch":                    true,

	"GET /artifactory/api/v1/uploads/config": true,

	// VCS remote REST handlers (#786). The selected-file routes validate their
	// selector before loading a repository, so they must not be tested as stubs.
	"GET /artifactory/api/vcs/branches/{repoKey}/{userOrg}/{repo}":                                     true,
	"GET /artifactory/api/vcs/downloadBranch/{repoKey}/{userOrg}/{branchName}":                         true,
	"GET /artifactory/api/vcs/downloadBranch/{repoKey}/{userOrg}/{repo}/archive/refs/{branchName}":     true,
	"GET /artifactory/api/vcs/downloadBranch/{repoKey}/{userOrg}/{repo}/{branchName}":                  true,
	"GET /artifactory/api/vcs/downloadBranchFile/{repoKey}/{userOrg}/{branchName}":                     true,
	"GET /artifactory/api/vcs/downloadBranchFile/{repoKey}/{userOrg}/{repo}/archive/refs/{branchName}": true,
	"GET /artifactory/api/vcs/downloadBranchFile/{repoKey}/{userOrg}/{repo}/{branchName}":              true,
	"GET /artifactory/api/vcs/downloadCommit/{repoKey}/{userOrg}/{repo}/{commitId}":                    true,
	"GET /artifactory/api/vcs/downloadRelease/{repoKey}/{userOrg}/{repo}/{releaseName}":                true,
	"GET /artifactory/api/vcs/downloadTag/{repoKey}/{userOrg}/{repo}/archive/refs/{tagName}":           true,
	"GET /artifactory/api/vcs/downloadTag/{repoKey}/{userOrg}/{repo}/{tagName}":                        true,
	"GET /artifactory/api/vcs/downloadTag/{repoKey}/{userOrg}/{tagName}":                               true,
	"GET /artifactory/api/vcs/downloadTagFile/{repoKey}/{userOrg}/{repo}/archive/refs/{tagName}":       true,
	"GET /artifactory/api/vcs/downloadTagFile/{repoKey}/{userOrg}/{repo}/{tagName}":                    true,
	"GET /artifactory/api/vcs/downloadTagFile/{repoKey}/{userOrg}/{tagName}":                           true,
	"GET /artifactory/api/vcs/refs/{repoKey}/{userOrg}/{repo}":                                         true,
	"GET /artifactory/api/vcs/tags/{repoKey}/{userOrg}/{repo}":                                         true,

	// The reverse proxy read, for the Docker access method (#220).
	"GET /artifactory/api/system/configuration/webServer": true,

	// The GET searches (#169).
	"GET /artifactory/api/search/artifact":    true,
	"GET /artifactory/api/search/badChecksum": true,
	"GET /artifactory/api/search/checksum":    true,
	"GET /artifactory/api/search/creation":    true,
	"GET /artifactory/api/search/dates":       true,
	"GET /artifactory/api/search/pattern":     true,
	"GET /artifactory/api/search/prop":        true,
	"GET /artifactory/api/search/usage":       true,
	// The layout and build searches (#170).
	"GET /artifactory/api/search/gavc":            true,
	"GET /artifactory/api/search/versions":        true,
	"GET /artifactory/api/search/latestVersion":   true,
	"GET /artifactory/api/search/dependency":      true,
	"POST /artifactory/api/search/buildArtifacts": true,

	"GET /artifactory/api/security/users":                                            true,
	"GET /artifactory/api/security/users/{userName}":                                 true,
	"PUT /artifactory/api/security/users/{userName}":                                 true,
	"POST /artifactory/api/security/users/{userName}":                                true,
	"DELETE /artifactory/api/security/users/{userName}":                              true,
	"GET /artifactory/api/security/groups":                                           true,
	"GET /artifactory/api/security/groups/{groupName}":                               true,
	"PUT /artifactory/api/security/groups/{groupName}":                               true,
	"POST /artifactory/api/security/groups/{groupName}":                              true,
	"DELETE /artifactory/api/security/groups/{groupName}":                            true,
	"GET /artifactory/api/security/lockedUsers":                                      true,
	"POST /artifactory/api/security/unlockAllUsers":                                  true,
	"POST /artifactory/api/security/unlockUsers":                                     true,
	"POST /artifactory/api/security/unlockUsers/{userName}":                          true,
	"GET /artifactory/api/security/userLockPolicy":                                   true,
	"PUT /artifactory/api/security/userLockPolicy":                                   true,
	"GET /artifactory/api/security/configuration/passwordExpirationPolicy":           true,
	"PUT /artifactory/api/security/configuration/passwordExpirationPolicy":           true,
	"POST /artifactory/api/security/users/authorization/expirePassword":              true,
	"POST /artifactory/api/security/users/authorization/unexpirePassword/{userName}": true,
	"POST /artifactory/api/security/users/authorization/expirePassword/{userName}":   true,
	"POST /artifactory/api/security/users/authorization/expirePasswordForAllUsers":   true,
	"POST /artifactory/api/security/users/authorization/changePassword":              true,
	"POST /artifactory/api/security/apiKey":                                          true,
	"PUT /artifactory/api/security/apiKey":                                           true,
	"GET /artifactory/api/security/apiKey":                                           true,
	"DELETE /artifactory/api/security/apiKey":                                        true,
	"DELETE /artifactory/api/security/apiKey/{username}":                             true,

	"POST /artifactory/api/system/storage/gc":        true,
	"DELETE /artifactory/api/trash/clean/{repoPath}": true,
	"POST /artifactory/api/trash/empty":              true,
	"POST /artifactory/api/trash/restore/{fromPath}": true,
	"GET /artifactory/api/storageinfo":               true,
	"POST /artifactory/api/storageinfo/calculate":    true,
	"POST /artifactory/api/system/storage/optimize":  true,

	// Key encryption, which answers DONE and changes nothing (#1385).
	"POST /artifactory/api/system/encrypt": true,
	"POST /artifactory/api/system/decrypt": true,

	"GET /access/api/v1/environments":                                         true,
	"POST /access/api/v1/environments":                                        true,
	"DELETE /access/api/v1/environments/{environmentName}":                    true,
	"POST /access/api/v1/environments/{environmentName}/rename":               true,
	"GET /access/api/v1/roles":                                                true,
	"POST /access/api/v1/roles":                                               true,
	"GET /access/api/v1/roles/{role}":                                         true,
	"PUT /access/api/v1/roles/{role}":                                         true,
	"DELETE /access/api/v1/roles/{role}":                                      true,
	"GET /artifactory/api/security/permissions":                               true,
	"GET /artifactory/api/security/permissions/{permissionTargetName}":        true,
	"PUT /artifactory/api/security/permissions/{permissionTargetName}":        true,
	"DELETE /artifactory/api/security/permissions/{permissionTargetName}":     true,
	"GET /artifactory/api/v2/security/permissions":                            true,
	"GET /artifactory/api/v2/security/permissions/groups/{groupName}":         true,
	"GET /artifactory/api/v2/security/permissions/users/{userName}":           true,
	"GET /artifactory/api/v2/security/permissions/{permissionTargetName}":     true,
	"HEAD /artifactory/api/v2/security/permissions/{permissionTargetName}":    true,
	"POST /artifactory/api/v2/security/permissions/{permissionTargetName}":    true,
	"PUT /artifactory/api/v2/security/permissions/{permissionTargetName}":     true,
	"DELETE /artifactory/api/v2/security/permissions/{permissionTargetName}":  true,
	"GET /access/api/v2/permissions":                                          true,
	"POST /access/api/v2/permissions":                                         true,
	"GET /access/api/v2/permissions/{permissionName}":                         true,
	"DELETE /access/api/v2/permissions/{permissionName}":                      true,
	"GET /access/api/v2/permissions/{permissionName}/{resourceType}":          true,
	"PUT /access/api/v2/permissions/{permissionName}/{resourceType}":          true,
	"PATCH /access/api/v2/permissions/{permissionName}/{resourceType}":        true,
	"DELETE /access/api/v2/permissions/{permissionName}/{resourceType}":       true,
	"GET /artifactory/api/security/keypair":                                   true,
	"POST /artifactory/api/security/keypair":                                  true,
	"PUT /artifactory/api/security/keypair":                                   true,
	"POST /artifactory/api/security/keypair/verify":                           true,
	"GET /artifactory/api/security/keypair/{keyPairName}":                     true,
	"DELETE /artifactory/api/security/keypair/{keyPairName}":                  true,
	"GET /artifactory/api/security/keypair/public/repositories/{repoKey}":     true,
	"GET /artifactory/api/system/security/certificates":                       true,
	"POST /artifactory/api/system/security/certificates/{certificateAlias}":   true,
	"DELETE /artifactory/api/system/security/certificates/{certificateAlias}": true,
	"GET /access/api/v1/tokens":                                               true,
	"POST /access/api/v1/tokens":                                              true,
	"DELETE /access/api/v1/tokens/revoke":                                     true,
	"GET /access/api/v1/tokens/{tokenId}":                                     true,
	"DELETE /access/api/v1/tokens/{tokenId}":                                  true,
	"GET /access/api/v1/config/token/default_expiry":                          true,
	"PUT /access/api/v1/config/token/default_expiry":                          true,
	"GET /artifactory/api/security/token":                                     true,
	"GET /artifactory/api/security/encryptedPassword":                         true,

	// Access v2 users and groups and the identity configuration (#81).
	"GET /access/api/v2/users":                                                       true,
	"POST /access/api/v2/users":                                                      true,
	"POST /access/api/v2/users/expire_password_for_all_users":                        true,
	"POST /access/api/v2/users/unexpire_password_for_all_users":                      true,
	"GET /access/api/v2/users/{username}":                                            true,
	"PATCH /access/api/v2/users/{username}":                                          true,
	"DELETE /access/api/v2/users/{username}":                                         true,
	"PATCH /access/api/v2/users/{username}/groups":                                   true,
	"PUT /access/api/v2/users/{username}/password":                                   true,
	"POST /access/api/v2/users/{username}/password/expire":                           true,
	"POST /access/api/v2/users/{username}/unlock":                                    true,
	"GET /access/api/v2/groups":                                                      true,
	"POST /access/api/v2/groups":                                                     true,
	"GET /access/api/v2/groups/{name}":                                               true,
	"PATCH /access/api/v2/groups/{name}":                                             true,
	"PUT /access/api/v2/groups/{name}":                                               true,
	"DELETE /access/api/v2/groups/{name}":                                            true,
	"PATCH /access/api/v2/groups/{name}/members":                                     true,
	"GET /access/api/v1/config/security/authentication/basic_authentication_enabled": true,
	"PUT /access/api/v1/config/security/authentication/basic_authentication_enabled": true,
	"PUT /access/api/v1/config/security/password_policy":                             true,
	"PATCH /access/api/v1/config/security/password_policy":                           true,
	"GET /access/api/v1/config/security/user_lock_policy":                            true,
	"PUT /access/api/v1/config/security/user_lock_policy":                            true,

	// The Builds API (#165), with promotion and retention (#166).
	"GET /artifactory/api/build":                                                       true,
	"PUT /artifactory/api/build":                                                       true,
	"POST /artifactory/api/build/append/{buildName}/{buildNumber}":                     true,
	"POST /artifactory/api/build/delete":                                               true,
	"POST /artifactory/api/build/promote/{buildName}/{buildNumber}":                    true,
	"POST /artifactory/api/build/rename/{buildName}":                                   true,
	"POST /artifactory/api/build/retention/{buildName}":                                true,
	"DELETE /artifactory/api/build/{buildName}":                                        true,
	"GET /artifactory/api/build/{buildName}":                                           true,
	"GET /artifactory/api/build/{buildName}/{buildNumber}":                             true,
	"GET /artifactory/api/builds/buildArtifacts/{buildName}/{buildNumber}/{buildRepo}": true,

	// The OIDC integration (#83).
	"GET /access/api/v1/oidc":                                                     true,
	"POST /access/api/v1/oidc":                                                    true,
	"POST /access/api/v1/oidc/token":                                              true,
	"GET /access/api/v1/oidc/{name}":                                              true,
	"PUT /access/api/v1/oidc/{name}":                                              true,
	"DELETE /access/api/v1/oidc/{name}":                                           true,
	"GET /access/api/v1/oidc/{provider_name}/identity_mappings":                   true,
	"POST /access/api/v1/oidc/{provider_name}/identity_mappings":                  true,
	"GET /access/api/v1/oidc/{provider_name}/identity_mappings/{mapping_name}":    true,
	"PUT /access/api/v1/oidc/{provider_name}/identity_mappings/{mapping_name}":    true,
	"DELETE /access/api/v1/oidc/{provider_name}/identity_mappings/{mapping_name}": true,

	// The multi-architecture tag delete (#226), the promotion and the parent
	// manifest lookup. test/server/artifacts/docker holds their rows
	// (support.DockerRoutes).
	"DELETE /artifactory/api/docker/{repoKey}/v2/delete":         true,
	"POST /artifactory/api/docker/{repo-key}/v2/promote":         true,
	"POST /artifactory/api/docker/{repo-key}/v2/parentManifests": true,

	// PyPI's JSON API, which RegisterPypiMetadataRoutes mounts over the
	// generated routes, as Puppet's v3 reads are; test/server/artifacts/pypi
	// holds their rows (support.PyPIRoutes).
	"GET /artifactory/api/pypi/{repository}/pypi/{package}/json":           true,
	"GET /artifactory/api/pypi/{repository}/pypi/{package}/{version}/json": true,
}

// TestAccUnimplementedOperationsAnswer501 proves the generated stubs reach the
// client: a specified but unimplemented operation answers 501 rather than 404
// (which would tell clients the endpoint does not exist) or 500. It also
// guards implemented, which the API reliability ledger reads: an operation
// left out of it is recorded there as "stub: 501". So the requests go to the
// status fixture as an administrator. A router without a store would not
// do: a real handler answers 501 without one (itemkit.Kit.Content), and 16
// served operations passed here as stubs that way (#1202).
//
// Every hand-written operation is called too, and must answer handwrittenStub's
// 501 exactly when stubbedHandwritten, which the ledger also reads, lists it:
// ten that answer it were recorded there as served (#1386).
func TestAccUnimplementedOperationsAnswer501(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")["Authorization"]
	stubbed := stubbedHandwritten(t)
	checked := 0
	for _, name := range specFiles {
		doc := loadSpec(t, name)
		prefix := doc.Servers[0].URL
		for path, item := range doc.Paths.Map() {
			for method, op := range item.Operations() {
				key := method + " " + prefix + path
				if implemented[key] {
					continue
				}
				handwritten, _ := op.Extensions["x-handwritten"].(bool)
				params := append(openapi3.Parameters{}, item.Parameters...)
				params = append(params, op.Parameters...)
				url := prefix + fillParams(path)
				var query []string
				for _, p := range params {
					if p.Value.Required && p.Value.In == "query" {
						query = append(query, p.Value.Name+"="+plausibleValue(p.Value))
					}
				}
				if len(query) > 0 {
					url += "?" + strings.Join(query, "&")
				}
				body, contentType := plausibleBody(op.RequestBody)
				req := httptest.NewRequest(method, url, body)
				if contentType != "" {
					req.Header.Set("Content-Type", contentType)
				}
				req.Header.Set("Authorization", admin)
				for _, p := range params {
					if p.Value.Required && p.Value.In == "header" {
						req.Header.Set(p.Value.Name, plausibleValue(p.Value))
					}
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if handwritten {
					// handwrittenStub names the operation in its message, so
					// no other handler's 501 can pass for it.
					want := httptest.NewRecorder()
					apierror.Write(want, errorProfileFor(req.URL.Path), apierror.New(
						http.StatusNotImplemented,
						fmt.Sprintf(
							"%s %s is specified but not implemented yet",
							method,
							prefix+path,
						),
					))
					answered := rec.Code == http.StatusNotImplemented &&
						sameBody(want.Body.Bytes(), rec.Body.Bytes())
					switch {
					case stubbed[key] && !answered:
						t.Errorf("%s: mounted as handwrittenStub, but status %d, body %s; "+
							"list it in implementedHandwritten with its status rows",
							key, rec.Code, rec.Body.String())
					case answered && !stubbed[key]:
						t.Errorf(
							"%s answers handwrittenStub's 501, but the ledger records it as served",
							key,
						)
					}
					continue
				}
				checked++
				if rec.Code != http.StatusNotImplemented {
					t.Errorf("%s: status %d, body %s", key, rec.Code, rec.Body.String())
					continue
				}
				// The body must be exactly what the envelope table renders for
				// this path, so each product keeps its documented error shape.
				want := httptest.NewRecorder()
				apierror.Write(want, errorProfileFor(req.URL.Path), apierror.ErrNotImplemented)
				if !sameBody(want.Body.Bytes(), rec.Body.Bytes()) {
					t.Errorf("%s: body %s, want %s", key, rec.Body.String(), want.Body.String())
				}
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d unimplemented operations checked; the specs look truncated", checked)
	}
}

// stubbedHandwritten lists, as "METHOD pattern", the routes the router mounts
// with handwrittenStub, which the API reliability ledger records as
// "stub: 501". It leaves out implementedHandwritten's: HelmPartialReindex
// serves its route before routing, so the stub chi walks there never answers.
func stubbedHandwritten(t *testing.T) map[string]bool {
	t.Helper()
	stub := runtime.FuncForPC(reflect.ValueOf(handwrittenStub("", "")).Pointer()).Name()
	routes := map[string]bool{}
	err := chi.Walk(
		NewRouter(RouterConfig{}).(chi.Routes),
		func(method, route string, h http.Handler, _ ...func(http.Handler) http.Handler) error {
			key := method + " " + route
			if runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name() == stub &&
				!implementedHandwritten[key] {
				routes[key] = true
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

// fillParams substitutes a plausible value for every path parameter so the
// request passes the generated binder; the stub reads none of them.
func fillParams(path string) string {
	var b strings.Builder
	for {
		i := strings.Index(path, "{")
		if i < 0 {
			b.WriteString(path)
			return b.String()
		}
		j := strings.Index(path[i:], "}")
		b.WriteString(path[:i])
		b.WriteString("1")
		path = path[i+j+1:]
	}
}

// plausibleValue satisfies the generated binder for a parameter's type.
func plausibleValue(p *openapi3.Parameter) string {
	if p.Schema != nil && p.Schema.Value != nil && p.Schema.Value.Type != nil {
		switch {
		case p.Schema.Value.Type.Is("integer"), p.Schema.Value.Type.Is("number"):
			return "1"
		case p.Schema.Value.Type.Is("boolean"):
			return "true"
		}
		if len(p.Schema.Value.Enum) > 0 {
			if s, ok := p.Schema.Value.Enum[0].(string); ok {
				return s
			}
		}
	}
	return "x"
}

// plausibleBody returns a body the generated binder accepts for the first
// declared media type: an empty JSON object or array, or a short text body.
func plausibleBody(rb *openapi3.RequestBodyRef) (*strings.Reader, string) {
	if rb == nil || rb.Value == nil {
		return strings.NewReader(""), ""
	}
	types := make([]string, 0, len(rb.Value.Content))
	for mt := range rb.Value.Content {
		types = append(types, mt)
	}
	sort.Strings(types)
	for _, mt := range types {
		if mt == "application/json" || strings.HasSuffix(mt, "+json") {
			schema := rb.Value.Content[mt].Schema
			if schema != nil && schema.Value != nil && schema.Value.Type != nil &&
				schema.Value.Type.Is("array") {
				return strings.NewReader("[]"), mt
			}
			return strings.NewReader("{}"), mt
		}
	}
	if len(types) == 0 {
		return strings.NewReader(""), ""
	}
	return strings.NewReader("x"), types[0]
}

// sameBody compares JSON bodies structurally and anything else byte for byte.
func sameBody(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}
