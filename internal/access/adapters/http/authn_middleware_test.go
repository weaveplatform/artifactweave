package accesshttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

func TestAccAuthenticatePrincipal(t *testing.T) {
	db := inmem.New()
	ctx := context.Background()
	cheapSeed(t, db)
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	var got *kernel.Principal
	h := Authenticate(
		testAuthnOptions(a, nil),
	)(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			if p, ok := kernel.FromContext(r.Context()); ok {
				got = &p
			}
		}),
	)
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
	if err := db.Settings().Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	serve(nil)
	if got == nil || !got.Anonymous || got.Name != kernel.AnonymousUser {
		t.Fatalf("anonymous principal %+v", got)
	}
}

// The middleware stores every principal it authorises with the decider that
// authorised it, which handlers reach through authn.DeciderFrom; a request it
// lets through without authorising a principal carries no decider.
func TestAccAuthenticateAttachesDecider(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Instance().Init(ctx, instance.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{
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
		p       kernel.Principal
		stored  bool
		d       accessapi.Decider
	)
	h := Authenticate(
		testAuthnOptions(a, db, skip),
	)(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			reached = true
			p, stored = kernel.FromContext(r.Context())
			d, _ = accessapi.DeciderFrom(r.Context())
		}),
	)
	check := func(name, path string, headers map[string]string, authorised bool) {
		t.Helper()
		reached, p, stored, d = false, kernel.Principal{}, false, nil
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
	if err := db.Settings().Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	check("anonymous on", ping, nil, true)
	if !p.Anonymous {
		t.Errorf("anonymous on: principal %+v", p)
	}
}

type brokenUsers struct{ port.UserStore }

func (brokenUsers) Get(context.Context, string) (model.User, error) {
	return model.User{}, errors.New("database down")
}

type brokenSettings struct{ settings.SettingsStore }

func (brokenSettings) Get(context.Context, string, any) error { return errors.New("database down") }

func TestAccAuthenticateStoreFailures(t *testing.T) {
	db := inmem.New()
	a, err := authn.NewAuthenticator(
		brokenUsers{db.Users()},
		brokenSettings{db.Settings()},
		authn.Options{Hash: testHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	h := Authenticate(
		testAuthnOptions(a, nil),
	)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("the request reached the handler")
		}),
	)
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

// TestUnitEntryPoint: the refusals 7.x's entry point answers 403 for the
// account, a locked account, an expired password and a suspended login,
// carry the challenge as its sendErrorResponse adds it, while an
// authenticated user's lack of a right and an API restriction, which never
// reach the entry point, do not (#1485). A request of the UI's, by its path
// or its Request-Agent, gets no challenge on any refusal
// (UiRequestUtils.isUiRestRequest). The shared sentinels stay as they are.
func TestUnitEntryPoint(t *testing.T) {
	suspended := fmt.Errorf("%w: %w", authn.ErrLoginSuspended, apierror.New(http.StatusForbidden,
		"This request is blocked due to recurrent login failures, please try again in 3 seconds"))
	other := errors.New("database down")
	const (
		api = "/artifactory/api/repositories"
		ui  = "/artifactory/ui/api/v1/ui/auth/current"
	)
	type want struct {
		status              int
		message             string
		challenge, withheld bool
	}
	for _, c := range []struct {
		name, path, agent string
		err               error
		want              want
	}{
		{
			"locked", api, "", kernel.ErrAccountLocked,
			want{403, kernel.ErrAccountLocked.Message, true, false},
		},
		{
			"expired", api, "", kernel.ErrPasswordExpired,
			want{403, "Password has expired", true, false},
		},
		{"suspended", api, "", suspended, want{
			403, "This request is blocked due to recurrent login failures, please try again in 3 seconds",
			true, false,
		}},
		{
			"wrapped lock", api, "", fmt.Errorf("basic: %w", kernel.ErrAccountLocked),
			want{403, kernel.ErrAccountLocked.Message, true, false},
		},
		{"forbidden", api, "", kernel.ErrForbidden, want{403, "Forbidden", false, false}},
		{
			"api restricted", api, "", kernel.ErrAPIRestricted,
			want{403, "API access is temporarily restricted", false, false},
		},
		{"bad credentials", api, "", kernel.ErrBadCredentials, want{401, "Bad Credentials", false, false}},
		{
			"ui path, locked", ui, "", kernel.ErrAccountLocked,
			want{403, kernel.ErrAccountLocked.Message, false, true},
		},
		{
			"ui agent, expired", api, "artifactoryUI", kernel.ErrPasswordExpired,
			want{403, "Password has expired", false, true},
		},
		{
			"ui agent, bad credentials", api, "artifactoryUI", kernel.ErrBadCredentials,
			want{401, "Bad Credentials", false, true},
		},
		{
			"ui agent, refused key", api, "artifactoryUI", challengedForbidden("Bad Credentials"),
			want{403, "Bad Credentials", false, true},
		},
		{
			"ui agent, forbidden", api, "artifactoryUI", kernel.ErrForbidden,
			want{403, "Forbidden", false, false},
		},
		// isUiRestRequest compares the header with equals and the
		// undecoded request URI with startsWith.
		{
			"agent in another case", api, "ArtifactoryUI", kernel.ErrAccountLocked,
			want{403, kernel.ErrAccountLocked.Message, true, false},
		},
		{
			"escaped ui path", "/artifactory/%75i/x", "", kernel.ErrAccountLocked,
			want{403, kernel.ErrAccountLocked.Message, true, false},
		},
		{
			"ui without its slash", "/artifactory/uix", "", kernel.ErrBadCredentials,
			want{401, "Bad Credentials", false, false},
		},
	} {
		r := httptest.NewRequest(http.MethodGet, c.path, nil)
		if c.agent != "" {
			r.Header.Set("Request-Agent", c.agent)
		}
		var e *apierror.Error
		if got := entryPoint(r, c.err); !errors.As(got, &e) ||
			e.Status != c.want.status || e.Message != c.want.message ||
			e.Challenge != c.want.challenge || e.NoChallenge != c.want.withheld {
			t.Errorf("%s: %+v, want %+v", c.name, e, c.want)
		}
	}
	if got := entryPoint(httptest.NewRequest(http.MethodGet, ui, nil), other); got != other {
		t.Errorf("an error that is no refusal: %v", got)
	}
	for _, e := range []*apierror.Error{
		kernel.ErrAccountLocked, kernel.ErrPasswordExpired, kernel.ErrBadCredentials,
	} {
		if e.Challenge || e.NoChallenge {
			t.Errorf("the shared %q changed: %+v", e.Message, e)
		}
	}
}
