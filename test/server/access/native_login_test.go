package access_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

const nativeOrigin = "https://login.example"
const nativeStart = "/artifactory/api/npm/npm-login/-/v1/login"

var nativeCSRF = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

type nativeBrowser struct {
	t          *testing.T
	h          http.Handler
	page, poll string
	cookie     *http.Cookie
	csrf       string
}

func (b *nativeBrowser) request(method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	b.t.Helper()
	r := httptest.NewRequestWithContext(b.t.Context(), method, target, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if b.cookie != nil {
		r.AddCookie(b.cookie)
	}
	w := httptest.NewRecorder()
	b.h.ServeHTTP(w, r)
	return w
}
func (b *nativeBrowser) capture(w *httptest.ResponseRecorder) {
	b.t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == "aw_native_session" {
			b.cookie = c
		}
	}
	match := nativeCSRF.FindStringSubmatch(w.Body.String())
	if len(match) != 2 {
		b.t.Fatalf("approval form missing csrf: %d %s", w.Code, w.Body)
	}
	b.csrf = match[1]
}
func (b *nativeBrowser) open() {
	b.t.Helper()
	w := b.request(http.MethodGet, b.page, "", nil)
	if w.Code != 200 {
		b.t.Fatal(w.Code, w.Body)
	}
	b.capture(w)
}
func (b *nativeBrowser) form(action string) *httptest.ResponseRecorder {
	return b.request(http.MethodPost, b.page, url.Values{"csrf": {b.csrf}, "action": {action}, "username": {"browser-user"}, "password": {"pw"}}.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": nativeOrigin})
}
func (b *nativeBrowser) login() {
	b.t.Helper()
	w := b.form("login")
	if w.Code != 200 {
		b.t.Fatal("sign in", w.Code, w.Body)
	}
	b.capture(w)
}
func (b *nativeBrowser) approve() {
	b.t.Helper()
	w := b.form("approve")
	if w.Code != 200 {
		b.t.Fatal("consent", w.Code, w.Body)
	}
}
func newNativeBrowser(t *testing.T, h http.Handler) *nativeBrowser {
	t.Helper()
	b := &nativeBrowser{t: t, h: h}
	w := b.request(http.MethodPost, nativeStart, `{}`, map[string]string{"Authorization": "Bearer stale", "Content-Type": "application/json"})
	var out struct {
		LoginURL string `json:"loginUrl"`
		DoneURL  string `json:"doneUrl"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || !strings.HasPrefix(out.LoginURL, nativeOrigin+"/ui/native-login?") || !strings.HasPrefix(out.DoneURL, nativeOrigin+"/artifactory/api/npm/npm-login/-/v1/done?") {
		t.Fatal("web bootstrap", w.Code, w.Body)
	}
	b.page, b.poll = out.LoginURL, out.DoneURL
	return b
}

func nativeSQLRouter(t *testing.T, db *appsql.Store) http.Handler {
	t.Helper()
	support.Seed(t, db)
	if err := db.Instance().Init(t.Context(), instance.InstanceIdentity{InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.Users().Create(t.Context(), accesstest.User{Name: "browser-user", PasswordHash: cheapHash, Groups: []string{"readers"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{Key: "npm-login", Class: repo.Local, PackageType: repo.NPM})); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(t.Context(), db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash, Tokens: tokens, APIRestrictions: db.APIRestrictions()})
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the real configuration-to-module settings seam with a nondefault
	// expiry. Passing only a fake service lifetime would miss DI regressions.
	cfg, err := config.Load(config.LoadOptions{Flags: map[string]string{"npm.login_token_expiry": "12h", "native_login.enabled": "true", "native_login.origin": nativeOrigin}})
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Store: db, Blobs: blobs, Auth: a, Tokens: tokens, PasswordHash: testHash, NativeLoginEnabled: cfg.Access().NativeLoginEnabled, NativeLoginOrigin: cfg.Access().NativeLoginOrigin, NPMLoginTokenExpiry: cfg.Access().NativeLoginTokenExpiry})
}

func TestAccNativeLoginSQL(t *testing.T) {
	keys, err := crypt.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sqltest.RunModule(t, appsql.Modules(), func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) }, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		t.Run("required_encryption_browser_only", func(t *testing.T) {
			db := newStore(t)
			h := nativeSQLRouter(t, db)
			if err := db.Settings().Put(t.Context(), settings.SettingPasswordEncryption, accessapi.EncryptionRequired); err != nil {
				t.Fatal(err)
			}
			b := newNativeBrowser(t, h)
			b.open()
			if w := b.request(http.MethodGet, "/artifactory/api/npm/npm-login/-/whoami", "", basic("browser-user", "pw")); w.Code != 401 {
				t.Fatal("ordinary Basic must remain refused", w.Code, w.Body)
			}
			old := b.csrf
			b.csrf = "foreign"
			if w := b.form("login"); w.Code != 400 {
				t.Fatal("CSRF allowed password login", w.Code)
			}
			b.csrf = old
			if w := b.request(http.MethodPost, b.page, url.Values{"action": {"login"}, "csrf": {b.csrf}, "username": {"browser-user"}, "password": {"pw"}}.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "https://evil.example"}); w.Code != 400 {
				t.Fatal("origin allowed password login", w.Code)
			}
			b.login()
			b.approve()
			w := b.request(http.MethodGet, b.poll, "", nil)
			if w.Code != 200 {
				t.Fatal("UI-marked browser flow refused", w.Code, w.Body)
			}
		})
		t.Run("one_winner_policy_and_headers", func(t *testing.T) {
			db := newStore(t)
			h := nativeSQLRouter(t, db)
			b := newNativeBrowser(t, h)
			w := b.request(http.MethodGet, b.poll, "", nil)
			if w.Code != 202 || w.Header().Get("Retry-After") != "5" {
				t.Fatal("pending", w.Code, w.Body)
			}
			if w = b.request(http.MethodHead, b.poll, "", nil); w.Code != 401 {
				t.Fatal("unauthenticated HEAD must not consume", w.Code, w.Body)
			}
			b.open()
			oldCookie, oldCSRF := b.cookie.Value, b.csrf
			if !b.cookie.HttpOnly || !b.cookie.Secure || b.cookie.SameSite != http.SameSiteStrictMode {
				t.Fatal("cookie boundary", b.cookie)
			}
			if w = b.request(http.MethodPost, b.page, url.Values{"action": {"login"}, "csrf": {b.csrf}, "username": {"browser-user"}, "password": {"pw"}}.Encode(), map[string]string{"Origin": "https://attacker.example", "Content-Type": "application/x-www-form-urlencoded"}); w.Code != 400 {
				t.Fatal("foreign origin accepted", w.Code)
			}
			b.login()
			if b.cookie.Value == oldCookie || b.csrf == oldCSRF {
				t.Fatal("session fixation")
			}
			b.approve()
			var wg sync.WaitGroup
			results := make(chan *httptest.ResponseRecorder, 8)
			for range 8 {
				wg.Go(func() { results <- b.request(http.MethodGet, b.poll, "", nil) })
			}
			wg.Wait()
			close(results)
			var raw string
			wins := 0
			for result := range results {
				if result.Code == 200 {
					wins++
					var out struct {
						Token string `json:"token"`
					}
					if json.Unmarshal(result.Body.Bytes(), &out) != nil || out.Token == "" {
						t.Fatal("empty token")
					}
					raw = out.Token
				} else if result.Code != 400 {
					t.Fatal(result.Code, result.Body)
				}
			}
			if wins != 1 {
				t.Fatal("redemption winners", wins)
			}
			rec := b.request(http.MethodGet, "/artifactory/api/npm/npm-login/-/whoami", "", map[string]string{"Authorization": "Bearer " + raw})
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "browser-user") {
				t.Fatal("token unusable", rec.Code, rec.Body)
			}
			verify, err := token.New(t.Context(), db, token.Options{KeyBits: 1024})
			if err != nil {
				t.Fatal(err)
			}
			record, err := verify.Verify(t.Context(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if record.ExpiresAt.Sub(record.IssuedAt) != 12*time.Hour {
				t.Fatal("configured npm expiry ignored", record.ExpiresAt.Sub(record.IssuedAt))
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("secret caching")
			}
		})
		for _, stage := range []string{"login", "consent", "redemption"} {
			for _, change := range []string{"ui_disabled", "mfa", "password", "disabled", "repository_recreated", "permission"} {
				t.Run(stage+"/"+change, func(t *testing.T) {
					db := newStore(t)
					h := nativeSQLRouter(t, db)
					b := newNativeBrowser(t, h)
					b.open()
					if stage != "login" {
						b.login()
					}
					if stage == "redemption" {
						b.approve()
					}
					if err := db.Tx(t.Context(), func(ctx context.Context) error {
						if err := db.Projects().Guard(ctx); err != nil {
							return err
						}
						if change == "repository_recreated" {
							if err := db.Repos().Delete(ctx, "npm-login"); err != nil {
								return err
							}
							return db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "npm-login", Class: repo.Local, PackageType: repo.NPM}))
						}
						u, err := db.Users().Get(ctx, "browser-user")
						if err != nil {
							return err
						}
						switch change {
						case "ui_disabled":
							u.DisableUIAccess = true
						case "mfa":
							u.MFA = "ENABLED"
						case "password":
							u.PasswordHash = accesstest.HashPassword(t, "new-password")
						case "disabled":
							u.Status = accesstest.UserDisabled
						case "permission":
							u.Groups = nil
						}
						return db.Users().Update(ctx, u)
					}); err != nil {
						t.Fatal(err)
					}
					var result *httptest.ResponseRecorder
					if stage == "login" {
						result = b.form("login")
					} else if stage == "consent" {
						result = b.form("approve")
					} else {
						result = b.request(http.MethodGet, b.poll, "", nil)
					}
					if result.Code != 400 {
						t.Fatal("changed policy admitted", result.Code, result.Body)
					}
				})
			}
		}
	}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}

func TestAccNativeLoginTerraformSQL(t *testing.T) {
	keys, err := crypt.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sqltest.RunModule(t, appsql.Modules(), func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) }, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		h := nativeSQLRouter(t, newStore(t))
		b := &nativeBrowser{t: t, h: h}
		doc := b.request(http.MethodGet, "/.well-known/terraform.json", "", nil)
		var discovery map[string]json.RawMessage
		if doc.Code != 200 || json.Unmarshal(doc.Body.Bytes(), &discovery) != nil {
			t.Fatal(doc.Code, doc.Body)
		}
		for _, entry := range []string{"login.v1", "modules.v1", "providers.v1", "state.v2", "tfe.v2", "tfe.v2.1", "tfe.v2.2"} {
			if discovery[entry] == nil {
				t.Fatal("lost discovery", entry)
			}
		}
		query := url.Values{"response_type": {"code"}, "client_id": {"terraform-cli"}, "redirect_uri": {"http://localhost:10000/login"}, "state": {"bound state"}, "code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}, "code_challenge_method": {"S256"}}
		w := b.request(http.MethodGet, "/ui/terraform/oauth2/authorize?"+query.Encode(), "", nil)
		if w.Code != 303 {
			t.Fatal(w.Code, w.Body)
		}
		b.page = w.Header().Get("Location")
		b.open()
		b.login()
		w = b.form("approve")
		if w.Code != 303 {
			t.Fatal(w.Code, w.Body)
		}
		callback, err := url.Parse(w.Header().Get("Location"))
		if err != nil || callback.Host != "localhost:10000" || callback.Query().Get("state") != "bound state" || callback.Query().Get("code") == "" {
			t.Fatal("callback", callback, err)
		}
		exchange := url.Values{"grant_type": {"authorization_code"}, "client_id": {"terraform-cli"}, "redirect_uri": {"http://localhost:10000/login"}, "code": {callback.Query().Get("code")}, "code_verifier": {"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"}}
		w = b.request(http.MethodPost, "/artifactory/api/oauth2/token", exchange.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		var out map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out["token_type"] != "Bearer" || out["refresh_token"] != nil || out["access_token"] == nil {
			t.Fatal(w.Code, w.Body)
		}
		if w = b.request(http.MethodPost, "/artifactory/api/oauth2/token", exchange.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_grant") {
			t.Fatal("code replay", w.Code, w.Body)
		}
	}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}
