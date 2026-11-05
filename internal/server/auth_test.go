package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func basic(user, password string) map[string]string {
	return support.Basic(user, password)
}

func TestAccAuthenticatePrincipal(t *testing.T) {
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

// The middleware stores every principal it authorises with the decider that
// authorised it, which handlers reach through auth.DeciderFrom; a request it
// lets through without authorising a principal carries no decider.
func TestAccAuthenticateAttachesDecider(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := tokens.Issue(ctx, token.Request{Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	const ping, skipped = "/artifactory/api/system/ping", "/skipped"
	skip := func(r *http.Request) bool { return r.URL.Path == skipped }
	var (
		reached bool
		p       auth.Principal
		stored  bool
		d       *authz.Decider
	)
	h := authenticate(a, db, skip)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached = true
		p, stored = auth.FromContext(r.Context())
		d = auth.DeciderFrom(r.Context())
	}))
	check := func(name, path string, headers map[string]string, authorised bool) {
		t.Helper()
		reached, p, stored, d = false, auth.Principal{}, false, nil
		do(t, h, http.MethodGet, path, headers, "")
		switch {
		case !reached:
			t.Errorf("%s: the request did not reach the handler", name)
		case stored != authorised || (d != nil) != authorised:
			t.Errorf(
				"%s: principal %v, decider %v; want both %v",
				name,
				stored,
				d != nil,
				authorised,
			)
		case authorised && d.IsAdmin() != p.Admin:
			t.Errorf("%s: the decider does not answer for %+v", name, p)
		}
	}
	check("basic", ping, basic("admin", "pw"), true)
	check("bearer", ping, map[string]string{"Authorization": "Bearer " + issued.AccessToken}, true)
	check("public route without credentials", ping, nil, false)
	check("unauthenticated route", skipped, basic("admin", "pw"), false)
	if err := db.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	check("anonymous on", ping, nil, true)
	if !p.Anonymous {
		t.Errorf("anonymous on: principal %+v", p)
	}
}

type brokenUsers struct{ store.UserStore }

func (brokenUsers) Get(context.Context, string) (types.User, error) {
	return types.User{}, errors.New("database down")
}

type brokenSettings struct{ store.SettingsStore }

func (brokenSettings) Get(context.Context, string, any) error { return errors.New("database down") }

func TestAccAuthenticateStoreFailures(t *testing.T) {
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
