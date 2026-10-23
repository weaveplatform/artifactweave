package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func basic(user, password string) map[string]string {
	return support.Basic(user, password)
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
