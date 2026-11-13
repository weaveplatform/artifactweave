package access_test

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// TestAccKeyEncryption: POST /api/system/encrypt and /api/system/decrypt
// answer an administrator 200 text/plain "DONE", as 7.x's SystemResource
// does, and change nothing, since secrets stay sealed (decision 0017). A
// user who is not an administrator gets 403 and a caller without
// credentials 401 with the Basic challenge, whether anonymous access is on
// or off (#1385).
func TestAccKeyEncryption(t *testing.T) {
	const (
		forbidden = `{"errors":[{"message":"Forbidden","status":403}]}` + "\n"
		required  = `{"errors":[{"message":"Authentication is required","status":401}]}` + "\n"
	)
	for _, anonymous := range []bool{false, true} {
		h := statusFixture(t, anonymous)
		for _, path := range []string{"/artifactory/api/system/encrypt", "/artifactory/api/system/decrypt"} {
			for _, tc := range []struct {
				who                     string
				headers                 map[string]string
				status                  int
				contentType, body, auth string
			}{
				{"admin", basic("admin", "pw"), http.StatusOK, "text/plain", "DONE", ""},
				{"user", basic("user", "pw"), http.StatusForbidden, "application/json", forbidden, ""},
				{"no credentials", nil, http.StatusUnauthorized, "application/json", required, apierror.BasicChallenge},
			} {
				rec := do(t, h, http.MethodPost, path, tc.headers, "")
				if rec.Code != tc.status || rec.Body.String() != tc.body ||
					rec.Header().Get("Content-Type") != tc.contentType ||
					rec.Header().Get("WWW-Authenticate") != tc.auth {
					t.Errorf("%s as %s (anonymous access %v): %d %q %q %q, want %d %q %q %q",
						path, tc.who, anonymous, rec.Code, rec.Header().Get("Content-Type"), rec.Body,
						rec.Header().Get("WWW-Authenticate"), tc.status, tc.contentType, tc.body, tc.auth)
				}
			}
		}
	}
}
