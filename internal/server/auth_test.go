package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// seededHandler is the router over a seeded in-memory store whose admin
// password is "pw".
func seededHandler(t *testing.T) (http.Handler, *inmem.Store) {
	t.Helper()
	db := inmem.New()
	cheapSeed(t, db)
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{CompatVersion: "7.155.0", Store: db, Auth: a}), db
}

func basic(user, password string) map[string]string {
	return map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password)),
	}
}

func TestAuthentication(t *testing.T) {
	h, db := seededHandler(t)
	const protected = "/artifactory/api/repositories"
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
}

func TestAuthenticatePrincipal(t *testing.T) {
	db := inmem.New()
	ctx := context.Background()
	cheapSeed(t, db)
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	var got *auth.Principal
	h := authenticate(a, nil, nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if p, ok := auth.FromContext(r.Context()); ok {
			got = &p
		}
	}))
	serve := func(headers map[string]string) {
		got = nil
		do(t, h, "GET", "/artifactory/api/system/ping", headers, "")
	}
	serve(basic("admin", "pw"))
	if got == nil || got.Name != "admin" || !got.Admin {
		t.Fatalf("admin principal %+v", got)
	}
	serve(nil)
	if got != nil {
		t.Fatalf("a public route without credentials got principal %+v", got)
	}
	if err := db.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	serve(nil)
	if got == nil || !got.Anonymous || got.Name != auth.AnonymousUser {
		t.Fatalf("anonymous principal %+v", got)
	}
}

type brokenUsers struct{ store.UserStore }

func (brokenUsers) Get(context.Context, string) (types.User, error) {
	return types.User{}, errors.New("database down")
}

type brokenSettings struct{ store.SettingsStore }

func (brokenSettings) Get(context.Context, string, any) error { return errors.New("database down") }

func TestAuthenticateStoreFailures(t *testing.T) {
	db := inmem.New()
	a, err := auth.NewAuthenticator(
		brokenUsers{db.Users()},
		brokenSettings{db.Settings()},
		auth.Options{Hash: testHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	h := authenticate(a, nil, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the request reached the handler")
	}))
	for name, headers := range map[string]map[string]string{
		"credentials": basic("admin", "pw"),
		"none":        nil,
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/artifactory/api/system/ping", nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
