package pubhttp_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestUnitPubRefusalsCarryTheChallenge: the kit's 401 and 403 on the pub
// routes carry WWW-Authenticate: Bearer realm="pub", message="<message>",
// which the hosted pub repository specification v2 requires on both
// ("Missing Authentication or Invalid Token", "Insufficient Permissions",
// doc/repository-spec-v2.md in dart-lang/pub) and from which dart pub
// prints the server's reason (_throwAuthException in
// lib/src/authentication/client.dart). The middleware's own 401 and 403
// are app's TestAccProtocolMiddlewareEnvelopes (#1279).
func TestUnitPubRefusalsCarryTheChallenge(t *testing.T) {
	f := newFixture(t, repo.Local)
	body, contentType := upload(t, archive(t, "widget_pub", "1.2.3"))
	if rec := f.do(
		http.MethodPost,
		base+"/api/packages/versions/new",
		"admin",
		contentType,
		body,
	); rec.Code != http.StatusNoContent {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		name, method, path, user, contentType string
		status                                int
		body, challenge                       string
	}{
		{
			"anonymous publish", http.MethodGet, base + "/api/packages/versions/new", "", "",
			http.StatusUnauthorized,
			`{"error":{"code":"UNAUTHORIZED","message":"Authentication is required"}}`,
			`Bearer realm="pub", message="Authentication is required"`,
		},
		{
			"a reader without permission", http.MethodGet, base + "/api/packages/widget_pub", "nothing", "",
			http.StatusForbidden,
			`{"error":{"code":"FORBIDDEN","message":"Forbidden"}}`,
			`Bearer realm="pub", message="Forbidden"`,
		},
		{
			"a publisher without permission", http.MethodPost, base + "/api/packages/versions/new", "viewer",
			"multipart/form-data", http.StatusForbidden,
			`{"error":{"code":"FORBIDDEN","message":"Forbidden"}}`,
			`Bearer realm="pub", message="Forbidden"`,
		},
	} {
		rec := f.do(c.method, c.path, c.user, c.contentType, strings.NewReader(""))
		got := rec.Header().Get("WWW-Authenticate")
		if rec.Code != c.status || rec.Body.String() != c.body+"\n" || got != c.challenge ||
			rec.Header().Get("Content-Type") != "application/vnd.pub.v2+json" {
			t.Errorf("%s: %d %q %q, want %d %q %q", c.name, rec.Code, rec.Body, got,
				c.status, c.body, c.challenge)
		}
	}
}
