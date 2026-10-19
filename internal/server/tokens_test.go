package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
)

func bearerAuth(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func jsonAs(headers map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/json"}
	for k, v := range headers {
		out[k] = v
	}
	return out
}

func formAs(headers map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for k, v := range headers {
		out[k] = v
	}
	return out
}

// allowBasicTokens turns token.allow_basic_auth on, for a fixture whose
// tests mint their tokens with the admin's password.
func allowBasicTokens(c *Config) { c.AllowBasicAuthTokens = true }

// mintAdmin creates a token through the Access API as admin, with the
// admin's password: the fixture needs allowBasicTokens.
func mintAdmin(t *testing.T, h http.Handler, body string) map[string]any {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/access/api/v1/tokens", jsonAs(basic("admin", "pw")), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create %s: %d %s", body, rec.Code, rec.Body)
	}
	return decodeObject(t, rec.Body.String())
}

// TestScopedTokenLifecycle is the artifactory_scoped_token resource's flow:
// create answers 200 with token_id, the id reads back, revocation answers
// 200, and the read then answers 404 so the resource leaves state.
func TestScopedTokenLifecycle(t *testing.T) {
	h := statusFixture(t, false, allowBasicTokens)
	created := mintAdmin(t, h, `{"username":"svc","scope":"applied-permissions/user",`+
		`"expires_in":86400,"description":"terraform","include_reference_token":true,"project_key":""}`)
	id, _ := created["token_id"].(string)
	if id == "" || created["token_type"] != "Bearer" || created["expires_in"] != float64(86400) ||
		!strings.HasPrefix(
			created["reference_token"].(string),
			"cmVmd",
		) { //nolint:forcetypeassert // fixture
		t.Fatalf("created %v", created)
	}
	rec := do(t, h, http.MethodGet, "/access/api/v1/tokens/"+id, basic("admin", "pw"), "")
	got := decodeObject(t, rec.Body.String())
	if rec.Code != http.StatusOK || got["description"] != "terraform" ||
		!strings.HasSuffix(
			got["subject"].(string),
			"/users/svc",
		) { //nolint:forcetypeassert // fixture
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodDelete,
		"/access/api/v1/tokens/"+id,
		basic("admin", "pw"),
		"",
	); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/access/api/v1/tokens/"+id,
		basic("admin", "pw"),
		"",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("read after revoke: %d", rec.Code)
	}
}

// TestTokenListOmitsNonRevocable: Artifactory keeps no record of tokens
// shorter than the revocable threshold, so the list leaves them out, while
// a forced-revocable or refreshable one of the same lifetime is listed.
func TestTokenListOmitsNonRevocable(t *testing.T) {
	h := statusFixture(t, false, allowBasicTokens)
	short := mintAdmin(t, h, `{"expires_in":600}`)
	forced := mintAdmin(t, h, `{"expires_in":600,"force_revocable":true}`)
	refreshable := mintAdmin(t, h, `{"expires_in":600,"refreshable":true}`)
	rec := do(t, h, http.MethodGet, "/access/api/v1/tokens", basic("admin", "pw"), "")
	body := rec.Body.String()
	listed := func(tok map[string]any) bool {
		id, _ := tok["token_id"].(string)
		return strings.Contains(body, id)
	}
	if rec.Code != http.StatusOK || listed(short) || !listed(forced) || !listed(refreshable) {
		t.Fatalf("list: %s", body)
	}
	// user sees only their own seeded token.
	rec = do(t, h, http.MethodGet, "/access/api/v1/tokens", basic("user", "pw"), "")
	if !strings.Contains(rec.Body.String(), "user-token") ||
		strings.Contains(rec.Body.String(), "admin-token") {
		t.Fatalf("user's list: %s", rec.Body)
	}
	legacy := do(t, h, http.MethodGet, "/artifactory/api/security/token", basic("admin", "pw"), "")
	if !strings.Contains(
		legacy.Body.String(),
		forced["token_id"].(string),
	) { //nolint:forcetypeassert // fixture
		t.Fatalf("legacy list: %s", legacy.Body)
	}
}

// TestLegacyTokenListAndRevokeAdminOnly: the legacy list and revoke are
// admin only (#382). A user is refused even for their own token, which
// survives and stays revocable through the Access API; an administrator
// lists every user's tokens and revokes any of them.
func TestLegacyTokenListAndRevokeAdminOnly(t *testing.T) {
	h := statusFixture(t, false)
	const legacy = "/artifactory/api/security/token"
	user, admin := basic("user", "pw"), basic("admin", "pw")

	mustStatus(t, do(t, h, http.MethodGet, legacy, user, ""), 403, "user's legacy list")
	rec := do(t, h, http.MethodGet, legacy, admin, "")
	mustStatus(t, rec, 200, "admin's legacy list")
	if body := rec.Body.String(); !strings.Contains(body, `"user-token"`) ||
		!strings.Contains(body, `"admin-token"`) {
		t.Fatalf("admin's legacy list lacks a user's token: %s", body)
	}

	revoke := func(who map[string]string) *httptest.ResponseRecorder {
		return do(t, h, http.MethodPost, legacy+"/revoke", formAs(who), "token_id=user-token")
	}
	mustStatus(t, revoke(user), 403, "user's legacy revoke of their own token")
	mustStatus(t, do(t, h, http.MethodGet, "/access/api/v1/tokens/user-token", user, ""), 200,
		"the token after a refused revoke")
	rec = revoke(admin)
	if rec.Code != http.StatusOK || rec.Body.String() != "Token revoked" {
		t.Fatalf("admin's legacy revoke: %d %s", rec.Code, rec.Body)
	}
	mustStatus(t, do(t, h, http.MethodGet, "/access/api/v1/tokens/user-token", admin, ""), 404,
		"the token after the admin's revoke")
}

// TestTokenRefreshAndRevokeByValue: a refresh grant needs no credentials but
// the refresh token, and revokes the token it replaces; revocation by value
// answers the id and the token stops working.
func TestTokenRefreshAndRevokeByValue(t *testing.T) {
	h := statusFixture(t, false, allowBasicTokens)
	first := mintAdmin(t, h, `{"refreshable":true,"expires_in":86400}`)
	access, _ := first["access_token"].(string)
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/access/api/v1/tokens/me",
		bearerAuth(access),
		"",
	); rec.Code != http.StatusOK {
		t.Fatalf("me with the token: %d %s", rec.Code, rec.Body)
	}
	rec := do(
		t,
		h,
		http.MethodPost,
		"/access/api/v1/tokens",
		formAs(nil),
		url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {first["refresh_token"].(string)},
		}.Encode(),
	) //nolint:forcetypeassert // fixture
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh without credentials: %d %s", rec.Code, rec.Body)
	}
	second := decodeObject(t, rec.Body.String())
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/access/api/v1/tokens/me",
		bearerAuth(access),
		"",
	); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the refreshed token still works: %d", rec.Code)
	}
	next, _ := second["access_token"].(string)
	rec = do(t, h, http.MethodDelete, "/access/api/v1/tokens/revoke", jsonAs(basic("admin", "pw")),
		`{"token":"`+next+`"}`)
	if rec.Code != http.StatusOK ||
		decodeObject(t, rec.Body.String())["revoked_token_id"] != second["token_id"] {
		t.Fatalf("revoke by value: %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/access/api/v1/tokens/me",
		bearerAuth(next),
		"",
	); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked token still works: %d", rec.Code)
	}
}

// TestAccessTokenWithPassword: a password creates no token through the
// Access API until token.allow_basic_auth is on (#385), and is answered as
// Access answers Basic credentials it does not take. A token, even sent as
// the Basic password, and the legacy route are not affected, and neither is
// a refresh, whose credential is its refresh token.
func TestAccessTokenWithPassword(t *testing.T) {
	const legacy = "/artifactory/api/security/token"
	off := statusFixture(t, false)
	create := func(h http.Handler, hdr map[string]string, body string) *httptest.ResponseRecorder {
		return do(t, h, http.MethodPost, "/access/api/v1/tokens", jsonAs(hdr), body)
	}
	password := basic("user", "pw")
	for _, user := range []string{"admin", "user"} {
		rec := create(off, basic(user, "pw"), `{}`)
		if rec.Code != http.StatusUnauthorized ||
			!strings.Contains(rec.Body.String(), "Unsupported authentication method Basic") {
			t.Fatalf("%s's password: %d %s", user, rec.Code, rec.Body)
		}
	}
	rec := do(t, off, http.MethodPost, legacy, formAs(password), "username=user&refreshable=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("the legacy route with a password: %d %s", rec.Code, rec.Body)
	}
	issued := decodeObject(t, rec.Body.String())
	access, _ := issued["access_token"].(string)
	refresh, _ := issued["refresh_token"].(string)
	for name, headers := range map[string]map[string]string{
		"Bearer":             bearerAuth(access),
		"the Basic password": basic("user", access),
	} {
		if rec := create(off, headers, `{}`); rec.Code != http.StatusOK {
			t.Fatalf("a token as %s: %d %s", name, rec.Code, rec.Body)
		}
	}
	grant := `{"grant_type":"refresh_token","refresh_token":"` + refresh + `"}`
	if rec := create(off, password, grant); rec.Code != http.StatusOK {
		t.Fatalf("a refresh beside a password: %d %s", rec.Code, rec.Body)
	}
	on := statusFixture(t, false, allowBasicTokens)
	if rec := create(on, password, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("a password with token.allow_basic_auth on: %d %s", rec.Code, rec.Body)
	}
}

// TestTokenCreateRules: the JFrog CLI's form body is accepted; a token whose
// scope is narrower than its user's identity cannot mint a broader one.
func TestTokenCreateRules(t *testing.T) {
	h := statusFixture(t, false, allowBasicTokens)
	rec := do(t, h, http.MethodPost, "/access/api/v1/tokens", formAs(basic("user", "pw")),
		"scope=applied-permissions/user&expires_in=3600")
	if rec.Code != http.StatusOK {
		t.Fatalf("form create: %d %s", rec.Code, rec.Body)
	}
	groups := mintAdmin(t, h, `{"scope":"applied-permissions/groups:readers"}`)
	rec = do(
		t,
		h,
		http.MethodPost,
		"/access/api/v1/tokens",
		jsonAs(
			bearerAuth(groups["access_token"].(string)),
		),
		`{}`,
	) //nolint:forcetypeassert // fixture
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a group-scoped token minted an identity token: %d %s", rec.Code, rec.Body)
	}
	identity := mintAdmin(t, h, `{}`)
	rec = do(
		t,
		h,
		http.MethodPost,
		"/access/api/v1/tokens",
		jsonAs(
			bearerAuth(identity["access_token"].(string)),
		),
		`{"username":"svc"}`,
	) //nolint:forcetypeassert // fixture
	if rec.Code != http.StatusOK {
		t.Fatalf("an admin identity token mints for others: %d %s", rec.Code, rec.Body)
	}
	// Only an administrator may mint a token that never expires; anyone
	// else is refused 403 in createtoken's words, on either route.
	rec = do(t, h, http.MethodPost, "/access/api/v1/tokens", jsonAs(basic("user", "pw")),
		`{"expires_in":0}`)
	if rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "The requested token details are forbidden") {
		t.Fatalf("a user's never-expiring token: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/artifactory/api/security/token", formAs(basic("user", "pw")),
		"expires_in=0")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a user's never-expiring legacy token: %d %s", rec.Code, rec.Body)
	}
	if never := mintAdmin(t, h, `{"expires_in":0}`); never["expires_in"] != float64(0) {
		t.Fatalf("an administrator's never-expiring token: %v", never)
	}
	// applied-permissions/admin is an administrator's whatever its subject,
	// so only an administrator may mint it, in either scope form.
	for path, body := range map[string]string{
		"/access/api/v1/tokens":           `scope=applied-permissions/admin`,
		"/artifactory/api/security/token": `scope=jfrt@*:admin`,
	} {
		rec = do(t, h, http.MethodPost, path, formAs(basic("user", "pw")), body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("a user's admin-scoped token from %s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

// TestRevokeNonRevocable: a token below the revocable threshold that is
// neither refreshable nor forced revocable answers 400 by id and 404 by
// value, "Token not found or not revocable"; a refreshable one of the same
// lifetime is revoked both ways (#406).
func TestRevokeNonRevocable(t *testing.T) {
	h := statusFixture(t, false, allowBasicTokens)
	admin := basic("admin", "pw")
	short := mintAdmin(t, h, `{"expires_in":600}`)
	id, _ := short["token_id"].(string)
	rec := do(t, h, http.MethodDelete, "/access/api/v1/tokens/"+id, admin, "")
	mustStatus(t, rec, http.StatusBadRequest, "revoke a non-revocable token by id")
	byValue := func(tok any) *httptest.ResponseRecorder {
		raw, _ := tok.(string)
		return do(t, h, http.MethodDelete, "/access/api/v1/tokens/revoke", jsonAs(admin),
			`{"token":"`+raw+`"}`)
	}
	rec = byValue(short["access_token"])
	if rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "Token not found or not revocable") {
		t.Fatalf("revoke a non-revocable token by value: %d %s", rec.Code, rec.Body)
	}
	shortAccess, _ := short["access_token"].(string)
	rec = do(t, h, http.MethodGet, "/access/api/v1/tokens/me", bearerAuth(shortAccess), "")
	mustStatus(t, rec, http.StatusOK, "a refused revocation leaves the token working")
	first := mintAdmin(t, h, `{"expires_in":600,"refreshable":true}`)
	id, _ = first["token_id"].(string)
	mustStatus(t, do(t, h, http.MethodDelete, "/access/api/v1/tokens/"+id, admin, ""),
		http.StatusOK, "revoke a refreshable token by id")
	second := mintAdmin(t, h, `{"expires_in":600,"refreshable":true}`)
	mustStatus(t, byValue(second["access_token"]), http.StatusOK,
		"revoke a refreshable token by value")
	for _, tok := range []any{first["access_token"], second["access_token"]} {
		raw, _ := tok.(string)
		mustStatus(t, do(t, h, http.MethodGet, "/access/api/v1/tokens/me", bearerAuth(raw), ""),
			http.StatusUnauthorized, "a revoked refreshable token")
	}
}

// withAutoJoin gives the fixture's authenticator the group store, so a
// transient user joins the auto-join groups (readers, which Anything lets
// read every repository) as it does in the running server.
func withAutoJoin(c *Config) {
	a, err := auth.NewAuthenticator(c.Store.Users(), c.Store.Settings(), auth.Options{
		Hash: testHash, Tokens: c.Tokens, Groups: c.Store.Groups(),
	})
	if err != nil {
		panic(err)
	}
	c.Auth = a
}

// TestTransientUserToken: a token an administrator mints for a name that is
// no stored user authenticates as a transient user, with what its scope
// gives (JFrog's access-tokens page): applied-permissions/user the
// auto-join groups, a groups scope its groups, and applied-permissions/admin
// an administrator. Nothing is stored for the user, so the user API answers
// 404 for it; its tokens are listed and revoked as any other (#406).
func TestTransientUserToken(t *testing.T) {
	h := statusFixture(t, false, allowBasicTokens, withAutoJoin)
	admin := basic("admin", "pw")
	mint := func(body string) string {
		t.Helper()
		tok, _ := mintAdmin(t, h, body)["access_token"].(string)
		return tok
	}
	user := mint(`{"username":"ghost"}`)
	groups := mint(`{"username":"ghost","scope":"applied-permissions/groups:readers"}`)
	root := mint(`{"username":"ghost-admin","scope":"applied-permissions/admin"}`)
	const file = "/artifactory/libs-local/fixture/get.txt"
	const users = "/artifactory/api/security/users"
	for name, c := range map[string]struct {
		tok, method, path string
		want              int
	}{
		"user reads through auto-join":    {user, http.MethodGet, file, http.StatusOK},
		"user deploys nothing":            {user, http.MethodPut, "/artifactory/libs-local/g.txt", http.StatusForbidden},
		"groups reads through its group":  {groups, http.MethodGet, file, http.StatusOK},
		"groups is no administrator":      {groups, http.MethodGet, users, http.StatusForbidden},
		"admin scope is an administrator": {root, http.MethodGet, users, http.StatusOK},
		"admin scope deploys":             {root, http.MethodPut, "/artifactory/libs-local/g.txt", http.StatusCreated},
	} {
		rec := do(t, h, c.method, c.path, bearerAuth(c.tok), "")
		mustStatus(t, rec, c.want, name)
	}
	// No record: the user API does not know the name, and deleting it
	// revokes nothing.
	mustStatus(t, do(t, h, http.MethodGet, users+"/ghost", admin, ""), http.StatusNotFound,
		"the transient user's record")
	mustStatus(t, do(t, h, http.MethodDelete, users+"/ghost", admin, ""), http.StatusNotFound,
		"deleting the transient user")
	mustStatus(t, do(t, h, http.MethodGet, file, bearerAuth(groups), ""), http.StatusOK,
		"the token after the refused delete")
	// The transient user lists its own tokens and revokes them.
	rec := do(t, h, http.MethodGet, "/access/api/v1/tokens", bearerAuth(user), "")
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), "/users/ghost\"") != 2 {
		t.Fatalf("the transient user's tokens: %d %s", rec.Code, rec.Body)
	}
	mustStatus(t, do(t, h, http.MethodDelete, "/access/api/v1/tokens/me", bearerAuth(user), ""),
		http.StatusOK, "the transient user revokes its token")
	mustStatus(t, do(t, h, http.MethodGet, file, bearerAuth(user), ""), http.StatusUnauthorized,
		"the revoked transient token")
	// A user of that name created later is who the tokens act as, and
	// deleting that user revokes them with the user's own.
	rec = do(t, h, http.MethodPut, users+"/ghost", jsonAs(admin),
		`{"email":"ghost@example.com","password":"Ghost-pass-1"}`)
	mustStatus(t, rec, http.StatusCreated, "create the user")
	mustStatus(t, do(t, h, http.MethodDelete, users+"/ghost", admin, ""), http.StatusOK,
		"delete the user")
	mustStatus(t, do(t, h, http.MethodGet, file, bearerAuth(groups), ""), http.StatusUnauthorized,
		"the token after its user was deleted")
}

// TestDefaultExpiryApplies: the default set through the API is what a token
// created without expires_in gets.
func TestDefaultExpiryApplies(t *testing.T) {
	h := statusFixture(t, false, allowBasicTokens)
	rec := do(
		t,
		h,
		http.MethodGet,
		"/access/api/v1/config/token/default_expiry",
		basic("admin", "pw"),
		"",
	)
	// One year while no administrator has set it, token.default-expiry's
	// default (#406).
	if decodeObject(t, rec.Body.String())["default_token_expiration"] != float64(31536000) {
		t.Fatalf("default: %s", rec.Body)
	}
	if got := mintAdmin(t, h, `{}`)["expires_in"]; got != float64(31536000) {
		t.Fatalf("expires_in %v, want a year", got)
	}
	if rec := do(
		t,
		h,
		http.MethodPut,
		"/access/api/v1/config/token/default_expiry",
		jsonAs(
			basic("admin", "pw"),
		),
		`{"default_token_expiration":7200}`,
	); rec.Code != http.StatusOK {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	if got := mintAdmin(t, h, `{}`)["expires_in"]; got != float64(7200) {
		t.Fatalf("expires_in %v, want the new default", got)
	}
	// The legacy route keeps its own default, whatever the Access one is.
	rec = do(t, h, http.MethodPost, "/artifactory/api/security/token",
		formAs(basic("admin", "pw")), "username=admin")
	if got := decodeObject(t, rec.Body.String())["expires_in"]; rec.Code != http.StatusOK ||
		got != float64(3600) {
		t.Fatalf("legacy default: %d %s", rec.Code, rec.Body)
	}
}

// TestLegacyTokenRefreshAndEncryptedPassword: the legacy form refreshes with
// the refresh token alone; the encrypted password is a reference token that
// works as a credential, and a token caller cannot obtain one.
func TestLegacyTokenRefreshAndEncryptedPassword(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(
		t,
		h,
		http.MethodPost,
		"/artifactory/api/security/token",
		formAs(basic("admin", "pw")),
		"refreshable=true&expires_in=86400&scope=api:*",
	)
	created := decodeObject(t, rec.Body.String())
	if rec.Code != http.StatusOK || created["scope"] != "applied-permissions/user" {
		t.Fatalf("legacy create: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/artifactory/api/security/token", formAs(nil), url.Values{
		"grant_type": {
			"refresh_token",
		},
		"refresh_token": {created["refresh_token"].(string)}, //nolint:forcetypeassert // fixture
		"access_token":  {created["access_token"].(string)},  //nolint:forcetypeassert // fixture
	}.Encode())
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy refresh: %d %s", rec.Code, rec.Body)
	}
	// The refresh revoked the first token; the new one is the token caller
	// below.
	refreshed := decodeObject(t, rec.Body.String())
	rec = do(
		t,
		h,
		http.MethodGet,
		"/artifactory/api/security/encryptedPassword",
		basic("admin", "pw"),
		"",
	)
	ref := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.HasPrefix(ref, "cmVmd") {
		t.Fatalf("encrypted password: %d %s", rec.Code, ref)
	}
	if rec := do(t, h, http.MethodGet, "/artifactory/api/system/license",
		map[string]string{"X-JFrog-Art-Api": ref}, ""); rec.Code != http.StatusOK {
		t.Fatalf("the encrypted password as a credential: %d", rec.Code)
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/api/security/encryptedPassword",
		bearerAuth(
			refreshed["access_token"].(string),
		),
		"",
	); rec.Code != http.StatusBadRequest { //nolint:forcetypeassert // fixture
		t.Fatalf("encrypted password for a token caller: %d", rec.Code)
	}
}

func TestLegacyScope(t *testing.T) {
	for in, want := range map[string]string{
		"member-of-groups:readers,ci": "applied-permissions/groups:readers,ci",
		"member-of-groups:*":          "applied-permissions/user",
		"api:*":                       "applied-permissions/user",
		"jfrt@*:admin":                "applied-permissions/admin",
		"api:* member-of-groups:x":    "applied-permissions/user applied-permissions/groups:x",
		"applied-permissions/user":    "applied-permissions/user",
	} {
		if got := legacyScope(in); got != want {
			t.Errorf("legacyScope(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTokenAPIWithoutService: a router built without a token service answers
// the token API 501, as for an operation not built.
func TestTokenAPIWithoutService(t *testing.T) {
	h := statusFixture(t, false, func(c *Config) { c.Tokens = nil })
	for _, r := range []struct{ method, path, body string }{
		{http.MethodPost, "/access/api/v1/tokens", `{}`},
		{http.MethodGet, "/access/api/v1/tokens", ""},
		{http.MethodGet, "/access/api/v1/tokens/x", ""},
		{http.MethodDelete, "/access/api/v1/tokens/revoke", `{"token":"x"}`},
		{http.MethodGet, "/artifactory/api/security/token", ""},
		{http.MethodPost, "/artifactory/api/security/token/revoke", ""},
		{http.MethodGet, "/artifactory/api/security/encryptedPassword", ""},
	} {
		if rec := do(
			t,
			h,
			r.method,
			r.path,
			jsonAs(basic("admin", "pw")),
			r.body,
		); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s: %d, want 501", r.method, r.path, rec.Code)
		}
	}
}

// TestRefreshWithExpiredAccessToken is the JFrog CLI's refresh: the expired
// access token as the Bearer credential and its refresh token in the body.
// The new token works; the expired one refreshes once and no other route
// accepts it.
func TestRefreshWithExpiredAccessToken(t *testing.T) {
	h, secrets := newStatusFixture(t, false)
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/access/api/v1/tokens",
		bearerAuth(secrets["expired-access"]),
		"",
	); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an expired token on another route: %d", rec.Code)
	}
	refresh := `{"grant_type":"refresh_token","refresh_token":"` + secrets["expired-refresh"] + `"}`
	rec := do(
		t,
		h,
		http.MethodPost,
		"/access/api/v1/tokens",
		jsonAs(bearerAuth(secrets["expired-access"])),
		refresh,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh with the expired token: %d %s", rec.Code, rec.Body)
	}
	next := decodeObject(t, rec.Body.String())
	access, _ := next["access_token"].(string)
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/access/api/v1/tokens/me",
		bearerAuth(access),
		"",
	); rec.Code != http.StatusOK ||
		next["expires_in"] != float64(3600) ||
		next["refresh_token"] == "" {
		t.Fatalf("the refreshed token: %d %v", rec.Code, next)
	}
	if rec := do(
		t,
		h,
		http.MethodPost,
		"/access/api/v1/tokens",
		jsonAs(bearerAuth(secrets["expired-access"])),
		refresh,
	); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a second refresh with the replaced token: %d", rec.Code)
	}
}

// TestEncryptedPasswordFollowsThePassword: the encrypted password is the
// same on every request, stands for the password until it is set or
// expired, and is then replaced by a new value.
func TestEncryptedPasswordFollowsThePassword(t *testing.T) {
	h := statusFixture(t, false)
	const encPath = "/artifactory/api/security/encryptedPassword"
	get := func(password string) string {
		t.Helper()
		rec := do(t, h, http.MethodGet, encPath, basic("user", password), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("encrypted password: %d %s", rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	works := func(enc string) int {
		t.Helper()
		art := do(
			t,
			h,
			http.MethodGet,
			"/access/api/v1/tokens",
			map[string]string{"X-JFrog-Art-Api": enc},
			"",
		)
		if pw := do(
			t,
			h,
			http.MethodGet,
			"/access/api/v1/tokens",
			basic("user", enc),
			"",
		); pw.Code != art.Code {
			t.Fatalf("as X-JFrog-Art-Api %d, as a Basic password %d", art.Code, pw.Code)
		}
		return art.Code
	}
	admin := func(method, path, body string) {
		t.Helper()
		if rec := do(
			t,
			h,
			method,
			path,
			jsonAs(basic("admin", "pw")),
			body,
		); rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
		}
	}

	first := get("pw")
	if again := get("pw"); again != first || works(first) != http.StatusOK {
		t.Fatalf("a repeat request: %q, want %q", again, first)
	}
	admin(http.MethodPost, secUsers+"/user", `{"password":"New-pass1"}`)
	if code := works(first); code != http.StatusUnauthorized {
		t.Fatalf("the encrypted password of the old password: %d", code)
	}
	second := get("New-pass1")
	if second == first || works(second) != http.StatusOK {
		t.Fatalf("after a password change: %q", second)
	}
	admin(http.MethodPost, secExpire, `["user"]`)
	if code := works(second); code != http.StatusUnauthorized {
		t.Fatalf("the encrypted password of an expired password: %d", code)
	}
	admin(http.MethodPost, secUnexpire+"user", "")
	if third := get("New-pass1"); third == second || works(third) != http.StatusOK {
		t.Fatalf("after the expiry was lifted: %q", third)
	}
}

// TestLegacyRefreshWithoutAuthenticator: with no authenticator nothing can
// verify the form's access_token, so a legacy refresh is refused.
func TestLegacyRefreshWithoutAuthenticator(t *testing.T) {
	h, secrets := newStatusFixture(t, false, func(c *Config) { c.Auth = nil })
	body := strings.TrimPrefix(
		lRefresh(secrets["legacy-access"], secrets["legacy-refresh"]),
		"form:",
	)
	if rec := do(
		t,
		h,
		http.MethodPost,
		legacyTokenPath,
		formAs(nil),
		body,
	); rec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy refresh without an authenticator: %d %s", rec.Code, rec.Body)
	}
}

// The JFrog CLI swaps a configured user's password for a legacy token
// scoped member-of-groups:*. Artifactory gives such a token the real user's
// identity, so an administrator's token administers; named groups stay
// groups only.
func TestLegacyMemberOfAllGroupsIsTheUser(t *testing.T) {
	h := statusFixture(t, false)
	tokenFor := func(scope string) string {
		t.Helper()
		rec := do(
			t,
			h,
			http.MethodPost,
			"/artifactory/api/security/token",
			formAs(
				basic("admin", "pw"),
			),
			"username=admin&refreshable=true&scope="+url.QueryEscape(scope),
		)
		created := decodeObject(t, rec.Body.String())
		tok, _ := created["access_token"].(string)
		if rec.Code != http.StatusOK || tok == "" {
			t.Fatalf("create %s: %d %s", scope, rec.Code, rec.Body)
		}
		return tok
	}
	create := func(tok, key string) int {
		return do(t, h, http.MethodPut, "/artifactory/api/repositories/"+key,
			jsonAs(bearerAuth(tok)), `{"rclass":"local","packageType":"generic"}`).Code
	}
	if code := create(tokenFor("member-of-groups:*"), "by-all-groups"); code != http.StatusOK {
		t.Fatalf("member-of-groups:* for an administrator: %d, want 200", code)
	}
	if code := create(
		tokenFor("member-of-groups:readers"),
		"by-readers",
	); code != http.StatusForbidden {
		t.Fatalf("member-of-groups:readers for an administrator: %d, want 403", code)
	}
}

// TestTokenListScopeFilter: the scope parameter (Artifactory 7.133.3) keeps
// the tokens whose whole scope, or one of its space-separated scopes, is the
// string; a scope no token has lists nothing, and no parameter lists every
// token. A token issued with a reference token is listed like any other.
func TestTokenListScopeFilter(t *testing.T) {
	h := statusFixture(t, false, allowBasicTokens)
	mint := func(body string) string {
		t.Helper()
		id, _ := mintAdmin(t, h, body)["token_id"].(string)
		return id
	}
	group := mint(`{"scope":"applied-permissions/groups:readers","expires_in":86400}`)
	both := mint(
		`{"scope":"applied-permissions/groups:readers system:metrics:r","expires_in":86400}`,
	)
	user := mint(`{"username":"svc","scope":"applied-permissions/user","expires_in":86400,` +
		`"include_reference_token":true}`)
	list := func(scope string) []string {
		t.Helper()
		path := "/access/api/v1/tokens"
		if scope != "" {
			path += "?scope=" + url.QueryEscape(scope)
		}
		rec := do(t, h, http.MethodGet, path, basic("admin", "pw"), "")
		var page struct {
			Tokens []struct {
				ID string `json:"token_id"`
			} `json:"tokens"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
			t.Fatalf("list %q: %d %s", scope, rec.Code, rec.Body)
		}
		ids := make([]string, 0, len(page.Tokens))
		for _, tk := range page.Tokens {
			ids = append(ids, tk.ID)
		}
		return ids
	}
	has := func(ids []string, want ...string) bool {
		for _, w := range want {
			if !slices.Contains(ids, w) {
				return false
			}
		}
		return true
	}
	if got := list("applied-permissions/groups:readers"); len(got) != 2 || !has(got, group, both) {
		t.Errorf("group scope: %v", got)
	}
	if got := list("applied-permissions/groups:readers system:metrics:r"); len(got) != 1 ||
		!has(got, both) {
		t.Errorf("whole scope: %v", got)
	}
	users := list("applied-permissions/user")
	if !has(users, user) || has(users, group) || has(users, both) {
		t.Errorf("user scope: %v", users)
	}
	if got := list("applied-permissions/groups:writers"); len(got) != 0 {
		t.Errorf("unknown scope: %v", got)
	}
	if got := list(""); !has(got, group, both, user) {
		t.Errorf("no filter: %v", got)
	}
}
