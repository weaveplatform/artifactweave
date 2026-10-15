package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

// implemented lists the generated operations that have real handlers. Every
// other generated operation must reach the UnimplementedStrict stub and answer
// 501 in its service's envelope; this list grows as phases land.
var implemented = map[string]bool{
	"GET /artifactory/api/system/version":      true,
	"GET /artifactory/api/system/license":      true,
	"GET /artifactory/api/v1/system/readiness": true,
	"GET /artifactory/api/v1/system/liveness":  true,
	"GET /access/api/v1/system/version":        true,

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
	"DELETE /access/api/v2/groups/{name}":                                            true,
	"PATCH /access/api/v2/groups/{name}/members":                                     true,
	"GET /access/api/v1/config/security/authentication/basic_authentication_enabled": true,
	"PUT /access/api/v1/config/security/authentication/basic_authentication_enabled": true,
	"PUT /access/api/v1/config/security/password_policy":                             true,
	"PATCH /access/api/v1/config/security/password_policy":                           true,

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
}

// TestUnimplementedOperationsAnswer501 proves the generated stubs reach the
// client: a specified but unimplemented operation answers 501 rather than 404
// (which would tell clients the endpoint does not exist) or 500.
func TestUnimplementedOperationsAnswer501(t *testing.T) {
	h := testHandler()
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
				if v, ok := op.Extensions["x-handwritten"]; ok && v == true {
					continue
				}
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
				for _, p := range params {
					if p.Value.Required && p.Value.In == "header" {
						req.Header.Set(p.Value.Name, plausibleValue(p.Value))
					}
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				checked++
				if rec.Code != http.StatusNotImplemented {
					t.Errorf("%s: status %d, body %s", key, rec.Code, rec.Body.String())
					continue
				}
				// The body must be exactly what the envelope table renders for
				// this path, so each product keeps its documented error shape.
				want := httptest.NewRecorder()
				apierror.Write(want, surfaceFor(req.URL.Path), apierror.ErrNotImplemented)
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
