package shared_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func do(
	t *testing.T,
	h http.Handler,
	method, path string,
	headers map[string]string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return support.Do(t, h, method, path, headers, body)
}

// seededHandler is the router over a seeded in-memory store whose admin
// password is "pw".
func seededHandler(t *testing.T) (http.Handler, *inmem.Store) {
	t.Helper()
	db := inmem.New()
	support.Seed(t, db)
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: support.TestHash})
	if err != nil {
		t.Fatal(err)
	}
	return server.New(server.Config{CompatVersion: "7.155.0", Store: db, Auth: a}), db
}

func basic(user, password string) map[string]string {
	return support.Basic(user, password)
}

func TestAccAuthentication(t *testing.T) {
	h, db := seededHandler(t)
	const (
		protected = "/artifactory/api/repositories"
		version   = "/artifactory/api/system/version"
	)
	type want struct {
		status    int
		challenge bool
		message   string
	}
	for name, tc := range map[string]struct {
		method, path string
		headers      map[string]string
		want         want
	}{
		"protected without credentials": {
			"GET", protected, nil,
			want{http.StatusUnauthorized, true, "Authentication is required"},
		},
		"public without credentials": {"GET", "/artifactory/api/system/ping", nil, want{status: http.StatusOK}},
		// Version is not public: Artifactory requires a valid user for it
		// (RTFACT-26414).
		"version without credentials": {
			"GET", version, nil,
			want{http.StatusUnauthorized, true, "Authentication is required"},
		},
		"version as admin": {"GET", version, basic("admin", "pw"), want{status: http.StatusOK}},
		"usage sink without credentials": {
			"POST", "/artifactory/api/system/usage", nil, want{status: http.StatusOK},
		},
		"wrong password on a public route": {
			"GET", "/artifactory/api/system/ping", basic("admin", "nope"),
			want{http.StatusUnauthorized, true, "Bad credentials"},
		},
		"unknown user": {
			"GET", protected, basic("ghost", "pw"),
			want{http.StatusUnauthorized, true, "Bad credentials"},
		},
		"admin": {"GET", protected, basic("admin", "pw"), want{status: http.StatusOK}},
		"bearer before tokens exist": {
			"GET", protected,
			map[string]string{"Authorization": "Bearer abc"},
			want{http.StatusUnauthorized, true, "Bad credentials"},
		},
		"api key header": {
			"GET", protected,
			map[string]string{"X-JFrog-Art-Api": "cmVmd123"},
			want{http.StatusUnauthorized, true, "Bad credentials"},
		},
	} {
		rec := do(t, h, tc.method, tc.path, tc.headers, "")
		if rec.Code != tc.want.status {
			t.Errorf("%s: status %d, want %d: %s", name, rec.Code, tc.want.status, rec.Body)
			continue
		}
		if got := rec.Header().
			Get("WWW-Authenticate"); (got == apierror.BasicChallenge) != tc.want.challenge {
			t.Errorf("%s: challenge %q", name, got)
		}
		if tc.want.message != "" {
			var body struct {
				Errors []struct {
					Status  int    `json:"status"`
					Message string `json:"message"`
				} `json:"errors"`
			}
			if err := json.Unmarshal(
				rec.Body.Bytes(),
				&body,
			); err != nil || len(body.Errors) != 1 ||
				body.Errors[0].Message != tc.want.message ||
				body.Errors[0].Status != tc.want.status {
				t.Errorf("%s: body %s", name, rec.Body)
			}
		}
	}

	// /access answers in its own envelope.
	rec := do(t, h, "GET", "/access/api/v1/tokens", nil, "")
	if rec.Code != http.StatusUnauthorized ||
		rec.Body.String() != `{"errors":[{"code":"UNAUTHORIZED","message":"Authentication is required"}]}`+"\n" {
		t.Fatalf("access: %d %s", rec.Code, rec.Body)
	}

	// With anonymous access on, a request without credentials goes on as
	// anonymous; handlers decide what anonymous may do. The seeded Any
	// Remote grant covers no repository here, so the list is empty.
	if err := db.Settings().
		Put(context.Background(), types.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, h, "GET", protected, nil, ""); rec.Code != http.StatusOK ||
		rec.Body.String() != "[]\n" {
		t.Fatalf("anonymous on: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", version, nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("version, anonymous on: %d %s", rec.Code, rec.Body)
	}
}
