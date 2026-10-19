package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// identityWorld is a seeded store (admin "pw"), a token service, and a server
// that hashes cheaply.
type identityWorld struct {
	h      http.Handler
	db     *inmem.Store
	tokens *token.Service
}

func newIdentityWorld(t *testing.T) identityWorld {
	t.Helper()
	ctx := context.Background()
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
	a, err := auth.NewAuthenticator(
		db.Users(),
		db.Settings(),
		auth.Options{Hash: testHash, Tokens: tokens},
	)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{CompatVersion: "7.155.0", Store: db, Auth: a, PasswordHash: testHash})
	return identityWorld{h: h, db: db, tokens: tokens}
}

// call sends body as JSON with the given credentials and decodes a JSON
// answer into out when out is not nil.
func (w identityWorld) call(
	t *testing.T,
	method, path string,
	creds map[string]string,
	body string,
	want int,
	out any,
) {
	t.Helper()
	headers := map[string]string{}
	for k, v := range creds {
		headers[k] = v
	}
	if body != "" {
		headers["Content-Type"] = "application/json"
	}
	rec := do(t, w.h, method, path, headers, body)
	if rec.Code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, rec.Code, want, rec.Body)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, rec.Body)
		}
	}
}

func (w identityWorld) bearer(t *testing.T, user string, scope string) map[string]string {
	t.Helper()
	req := token.Request{Username: user, ByAdmin: true}
	if scope != "" {
		sc, err := types.ParseScope(scope)
		if err != nil {
			t.Fatal(err)
		}
		req.Scope = sc
	}
	issued, err := w.tokens.Issue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Authorization": "Bearer " + issued.AccessToken}
}

var admin = basic("admin", "pw")

type userDetails struct {
	Username       string   `json:"username"`
	Email          string   `json:"email"`
	Admin          bool     `json:"admin"`
	EffectiveAdmin bool     `json:"effective_admin"`
	Groups         []string `json:"groups"`
	Realm          string   `json:"realm"`
	Status         string   `json:"status"`
	Profile        bool     `json:"profile_updatable"`
	ManageWebhook  *bool    `json:"manage_webhook"`
}

// TestManagedUserFlow follows the jfrog/artifactory provider's
// artifactory_managed_user and artifactory_user on Artifactory 7.84.3 and
// later: create, read, update, groups, delete.
func TestManagedUserFlow(t *testing.T) {
	w := newIdentityWorld(t)
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/groups",
		admin,
		`{"name":"devs"}`,
		http.StatusCreated,
		nil,
	)
	var d userDetails
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users",
		admin,
		`{"username":"Alice","email":"alice@example.com","password":"Secret-1","groups":["devs"],"manage_webhook":true}`,
		http.StatusCreated,
		&d,
	)
	if d.Username != "alice" || d.Realm != "internal" || d.Status != "enabled" || !d.Profile ||
		!slices.Contains(d.Groups, "devs") || d.ManageWebhook == nil || !*d.ManageWebhook {
		t.Fatalf("created %+v", d)
	}
	// Auto-join groups take new users.
	w.call(
		t,
		http.MethodPatch,
		"/access/api/v2/groups/readers",
		admin,
		`{"auto_join":true}`,
		http.StatusOK,
		nil,
	)
	var auto userDetails
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users",
		admin,
		`{"username":"bob","email":"b@example.com","internal_password_disabled":true}`,
		http.StatusCreated,
		&auto,
	)
	if !slices.Contains(auto.Groups, "readers") {
		t.Fatalf("auto-join ignored: %+v", auto)
	}
	w.call(t, http.MethodGet, "/access/api/v2/users/alice", admin, "", http.StatusOK, &d)
	w.call(t, http.MethodPatch, "/access/api/v2/users/alice", admin,
		`{"email":"alice@corp.example","admin":true}`, http.StatusOK, &d)
	if d.Email != "alice@corp.example" || !d.Admin || !d.EffectiveAdmin {
		t.Fatalf("updated %+v", d)
	}
	var g struct{ Groups []string }
	w.call(t, http.MethodPatch, "/access/api/v2/users/alice/groups", admin,
		`{"add":["readers","devs"],"remove":["devs"]}`, http.StatusOK, &g)
	if !slices.Equal(g.Groups, []string{"readers"}) {
		t.Fatalf("groups after add and remove: %v", g.Groups)
	}
	// The user logs in with the password set through Access v2.
	w.call(
		t,
		http.MethodGet,
		"/access/api/v2/users/alice",
		basic("alice", "Secret-1"),
		"",
		http.StatusOK,
		nil,
	)
	// Re-enabling a password the user does not have needs one.
	w.call(t, http.MethodPatch, "/access/api/v2/users/bob", admin,
		`{"internal_password_disabled":false}`, http.StatusBadRequest, nil)
	w.call(t, http.MethodDelete, "/access/api/v2/users/alice", admin, "", http.StatusNoContent, nil)
	w.call(t, http.MethodGet, "/access/api/v2/users/alice", admin, "", http.StatusNotFound, nil)
	// The legacy API sees the same record.
	w.call(t, http.MethodGet, "/artifactory/api/security/users/bob", admin, "", http.StatusOK, nil)
}

// TestLastLoggedIn: a user who never logged in has "last_logged_in": null,
// as Artifactory answers since 7.98.7, on create and on read; after a login
// the field is the login's RFC 3339 time.
func TestLastLoggedIn(t *testing.T) {
	w := newIdentityWorld(t)
	field := func(d map[string]json.RawMessage) json.RawMessage {
		t.Helper()
		v, ok := d["last_logged_in"]
		if !ok {
			t.Fatalf("no last_logged_in in %v", d)
		}
		return v
	}
	read := func() json.RawMessage {
		t.Helper()
		var d map[string]json.RawMessage
		w.call(t, http.MethodGet, "/access/api/v2/users/erin", admin, "", http.StatusOK, &d)
		return field(d)
	}
	var created map[string]json.RawMessage
	w.call(t, http.MethodPost, "/access/api/v2/users", admin,
		`{"username":"erin","email":"erin@example.com","password":"Secret-1"}`,
		http.StatusCreated, &created)
	if v := field(created); string(v) != "null" {
		t.Fatalf("created: last_logged_in %s", v)
	}
	if v := read(); string(v) != "null" {
		t.Fatalf("never logged in: last_logged_in %s", v)
	}
	w.call(t, http.MethodGet, "/access/api/v2/users/erin", basic("erin", "Secret-1"), "",
		http.StatusOK, nil)
	var at string
	if err := json.Unmarshal(read(), &at); err != nil {
		t.Fatalf("after login: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Fatalf("after login: %v", err)
	}
}

// TestPlatformGroupFlow follows the jfrog/platform provider's
// platform_group and platform_group_members.
func TestPlatformGroupFlow(t *testing.T) {
	w := newIdentityWorld(t)
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users",
		admin,
		`{"username":"carol","email":"c@example.com","password":"Carol-pass1"}`,
		http.StatusCreated,
		nil,
	)
	var g struct {
		Name            string   `json:"name"`
		Members         []string `json:"members"`
		AdminPrivileges bool     `json:"admin_privileges"`
		ExternalID      *string  `json:"external_id"`
		ManageResources *bool    `json:"manage_resources"`
		Realm           string   `json:"realm"`
	}
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/groups",
		admin,
		`{"name":"ops","admin_privileges":true,"external_id":"ext-1","manage_resources":true,"members":["carol"]}`,
		http.StatusCreated,
		&g,
	)
	if g.Name != "ops" || !g.AdminPrivileges || g.ExternalID == nil || *g.ExternalID != "ext-1" ||
		g.ManageResources == nil || !*g.ManageResources || !slices.Equal(g.Members, []string{"carol"}) ||
		g.Realm != "internal" {
		t.Fatalf("created %+v", g)
	}
	// Membership of an admin group makes an effective admin.
	var d userDetails
	w.call(t, http.MethodGet, "/access/api/v2/users/carol", admin, "", http.StatusOK, &d)
	if d.Admin || !d.EffectiveAdmin {
		t.Fatalf("carol %+v", d)
	}
	// An update may echo the stored name, realm and members back.
	w.call(
		t,
		http.MethodPatch,
		"/access/api/v2/groups/ops",
		admin,
		`{"name":"ops","realm":"internal","members":["carol"],"description":"on call","external_id":"ext-2"}`,
		http.StatusOK,
		&g,
	)
	if g.ExternalID == nil || *g.ExternalID != "ext-2" {
		t.Fatalf("updated %+v", g)
	}
	for _, body := range []string{`{"name":"other"}`, `{"realm_attributes":"x"}`, `[1]`} {
		w.call(
			t,
			http.MethodPatch,
			"/access/api/v2/groups/ops",
			admin,
			body,
			http.StatusBadRequest,
			nil,
		)
	}
	var m struct{ Members []string }
	w.call(t, http.MethodPatch, "/access/api/v2/groups/ops/members", admin,
		`{"add":["admin"],"remove":["carol"]}`, http.StatusOK, &m)
	if !slices.Equal(m.Members, []string{"admin"}) {
		t.Fatalf("members %v", m.Members)
	}
	w.call(
		t,
		http.MethodPatch,
		"/access/api/v2/groups/ops/members",
		admin,
		`{"add":["nobody"]}`,
		http.StatusNotFound,
		nil,
	)
	// members belong to their own endpoint: an update ignores them.
	w.call(
		t,
		http.MethodPatch,
		"/access/api/v2/groups/ops",
		admin,
		`{"members":[]}`,
		http.StatusOK,
		&g,
	)
	if !slices.Equal(g.Members, []string{"admin"}) {
		t.Fatalf("members after an update that sent none: %v", g.Members)
	}
	rec := do(
		t,
		w.h,
		http.MethodPost,
		"/access/api/v2/groups",
		map[string]string{
			"Authorization": admin["Authorization"],
			"Content-Type":  "application/json",
		},
		`{"name":"x","members":["nobody"]}`,
	)
	if rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "One or more users in members were not found") {
		t.Fatalf("a group naming an unknown member: %d %s", rec.Code, rec.Body)
	}
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/groups",
		admin,
		`{"name":"y","members":["carol"],"import_from_ldap_group_setting":"ldap"}`,
		http.StatusBadRequest,
		nil,
	)
	w.call(t, http.MethodDelete, "/access/api/v2/groups/ops", admin, "", http.StatusNoContent, nil)
	w.call(t, http.MethodGet, "/access/api/v2/groups/ops", admin, "", http.StatusNotFound, nil)
}

func TestBasicAuthenticationSwitch(t *testing.T) {
	w := newIdentityWorld(t)
	const sw = "/access/api/v1/config/security/authentication/basic_authentication_enabled"
	var st struct{ Enabled *bool }
	w.call(t, http.MethodGet, sw, admin, "", http.StatusOK, &st)
	if st.Enabled == nil || !*st.Enabled {
		t.Fatalf("default %+v", st)
	}
	tok := w.bearer(t, "admin", "")
	w.call(t, http.MethodPut, sw, admin, `{"enabled":false}`, http.StatusOK, nil)
	rec := do(t, w.h, http.MethodGet, sw, admin, "")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatalf("Basic with basic authentication off: %d %s", rec.Code, rec.Body)
	}
	// Tokens keep working, as Bearer and as the Basic password.
	w.call(t, http.MethodGet, sw, tok, "", http.StatusOK, &st)
	if *st.Enabled {
		t.Fatal("still reported on")
	}
	issued, err := w.tokens.Issue(
		context.Background(),
		token.Request{Username: "admin", ByAdmin: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	w.call(t, http.MethodGet, sw, basic("admin", issued.AccessToken), "", http.StatusOK, nil)
	w.call(t, http.MethodPut, sw, tok, `{"enabled":true}`, http.StatusOK, nil)
	w.call(t, http.MethodGet, sw, admin, "", http.StatusOK, nil)
	w.call(t, http.MethodPut, sw, admin, ``, http.StatusBadRequest, nil)
}

func TestPasswordPolicy(t *testing.T) {
	w := newIdentityWorld(t)
	const pol = "/access/api/v1/config/security/password_policy"
	w.call(
		t,
		http.MethodPut,
		pol,
		admin,
		`{"length":8,"digit":1,"uppercase":1,"not_match_old":true}`,
		http.StatusNoContent,
		nil,
	)
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users",
		admin,
		`{"username":"dan","email":"d@example.com","password":"short1A"}`,
		http.StatusBadRequest,
		nil,
	)
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users",
		admin,
		`{"username":"dan","email":"d@example.com","password":"lowercase1"}`,
		http.StatusBadRequest,
		nil,
	)
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users",
		admin,
		`{"username":"dan","email":"d@example.com","password":"Longer-pw1"}`,
		http.StatusCreated,
		nil,
	)
	// The legacy API applies the same policy.
	w.call(t, http.MethodPut, "/artifactory/api/security/users/eve", admin,
		`{"email":"e@example.com","password":"weak"}`, http.StatusBadRequest, nil)
	// not_match_old refuses the current password.
	w.call(t, http.MethodPut, "/access/api/v2/users/dan/password", basic("dan", "Longer-pw1"),
		`{"password":"Longer-pw1"}`, http.StatusBadRequest, nil)
	w.call(t, http.MethodPut, "/access/api/v2/users/dan/password", basic("dan", "Longer-pw1"),
		`{"password":"Another-pw2"}`, http.StatusNoContent, nil)
	// PATCH merges: only the length changes.
	w.call(t, http.MethodPatch, pol, admin, `{"length":4}`, http.StatusNoContent, nil)
	w.call(t, http.MethodPut, "/access/api/v2/users/dan/password", admin,
		`{"password":"abcd"}`, http.StatusBadRequest, nil)
	w.call(t, http.MethodPut, "/access/api/v2/users/dan/password", admin,
		`{"password":"Abc1"}`, http.StatusNoContent, nil)
	// PUT replaces: an empty policy is the default one, at least four
	// characters, not the old password and never the username.
	w.call(t, http.MethodPut, pol, admin, `{}`, http.StatusNoContent, nil)
	for _, pw := range []string{"abc", "Abc1"} {
		w.call(t, http.MethodPut, "/access/api/v2/users/dan/password", admin,
			`{"password":"`+pw+`"}`, http.StatusBadRequest, nil)
	}
	w.call(t, http.MethodPut, "/access/api/v2/users/dan/password", admin,
		`{"password":"abcd"}`, http.StatusNoContent, nil)
	// A record update that sends the current password back changes nothing,
	// so not_match_old does not refuse it.
	w.call(t, http.MethodPatch, "/access/api/v2/users/dan", admin,
		`{"password":"abcd","email":"dan@example.com"}`, http.StatusOK, nil)
	w.call(t, http.MethodPatch, pol, admin, `{"special_char":-2}`, http.StatusBadRequest, nil)
}

// TestExpiredPasswordChangesItself checks a user whose password expired can
// set a new one with it, and do nothing else.
func TestExpiredPasswordChangesItself(t *testing.T) {
	w := newIdentityWorld(t)
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users",
		admin,
		`{"username":"fay","email":"f@example.com","password":"Old-pass1"}`,
		http.StatusCreated,
		nil,
	)
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users",
		admin,
		`{"username":"gus","email":"g@example.com","password":"Gus-pass1"}`,
		http.StatusCreated,
		nil,
	)
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users/fay/password/expire",
		admin,
		"",
		http.StatusNoContent,
		nil,
	)
	old := basic("fay", "Old-pass1")
	w.call(t, http.MethodGet, "/access/api/v2/users/fay", old, "", http.StatusForbidden, nil)
	w.call(
		t,
		http.MethodPut,
		"/access/api/v2/users/gus/password",
		old,
		`{"password":"x"}`,
		http.StatusForbidden,
		nil,
	)
	w.call(t, http.MethodPut, "/access/api/v2/users/fay/password", basic("fay", "wrong"),
		`{"password":"New-pass1"}`, http.StatusUnauthorized, nil)
	w.call(
		t,
		http.MethodPut,
		"/access/api/v2/users/fay/password",
		old,
		`{"password":"New-pass1"}`,
		http.StatusNoContent,
		nil,
	)
	w.call(
		t,
		http.MethodGet,
		"/access/api/v2/users/fay",
		basic("fay", "New-pass1"),
		"",
		http.StatusOK,
		nil,
	)
}

func TestExpireAllPasswords(t *testing.T) {
	w := newIdentityWorld(t)
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users",
		admin,
		`{"username":"hal","email":"h@example.com","password":"Hal-pass1"}`,
		http.StatusCreated,
		nil,
	)
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users",
		admin,
		`{"username":"ivy","email":"i@example.com","internal_password_disabled":true}`,
		http.StatusCreated,
		nil,
	)
	tok := w.bearer(t, "admin", "")
	rec := do(
		t,
		w.h,
		http.MethodPost,
		"/access/api/v2/users/expire_password_for_all_users",
		tok,
		"",
	)
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("expire all: %d %s", rec.Code, rec.Body)
	}
	w.call(
		t,
		http.MethodGet,
		"/access/api/v2/users/hal",
		basic("hal", "Hal-pass1"),
		"",
		http.StatusForbidden,
		nil,
	)
	w.call(t, http.MethodGet, "/access/api/v2/users", admin, "", http.StatusForbidden, nil)
	ivy, err := w.db.Users().Get(context.Background(), "ivy")
	if err != nil || ivy.PasswordExpired {
		t.Fatalf("a user without an internal password was expired: %+v %v", ivy, err)
	}
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/users/unexpire_password_for_all_users",
		tok,
		"",
		http.StatusOK,
		nil,
	)
	w.call(
		t,
		http.MethodGet,
		"/access/api/v2/users/hal",
		basic("hal", "Hal-pass1"),
		"",
		http.StatusOK,
		nil,
	)
	w.call(t, http.MethodGet, "/access/api/v2/users", admin, "", http.StatusOK, nil)
}

type userPage struct {
	Users []struct {
		Username string `json:"username"`
		URI      string `json:"uri"`
		Status   string `json:"status"`
	} `json:"users"`
	Cursor *string `json:"cursor"`
}

func (p userPage) names() []string {
	out := make([]string, len(p.Users))
	for i, u := range p.Users {
		out[i] = u.Username
	}
	return out
}

func TestUserListPagesAndFilters(t *testing.T) {
	w := newIdentityWorld(t)
	ctx := context.Background()
	for _, n := range []string{"u1", "u2", "u3"} {
		w.call(
			t,
			http.MethodPost,
			"/access/api/v2/users",
			admin,
			`{"username":"`+n+`","email":"`+n+`@example.com","password":"User-pass1"}`,
			http.StatusCreated,
			nil,
		)
	}
	w.call(t, http.MethodPost, "/access/api/v2/groups", admin,
		`{"name":"bosses","admin_privileges":true,"members":["u2"]}`, http.StatusCreated, nil)
	// admin, anonymous, u1, u2, u3 in pages of two.
	var all []string
	path := "/access/api/v2/users?limit=2"
	for {
		var p userPage
		w.call(t, http.MethodGet, path, admin, "", http.StatusOK, &p)
		all = append(all, p.names()...)
		if p.Cursor == nil {
			break
		}
		path = "/access/api/v2/users?limit=2&cursor=" + *p.Cursor
	}
	if !slices.Equal(all, []string{"admin", "anonymous", "u1", "u2", "u3"}) {
		t.Fatalf("pages %v", all)
	}
	list := func(query string) userPage {
		t.Helper()
		var p userPage
		w.call(t, http.MethodGet, "/access/api/v2/users"+query, admin, "", http.StatusOK, &p)
		return p
	}
	p := list("?descendingOrder=true&limit=2")
	if !slices.Equal(p.names(), []string{"u3", "u2"}) || p.Cursor == nil {
		t.Fatalf("descending %v", p.names())
	}
	p = list("?descendingOrder=true&cursor=" + *p.Cursor)
	if !slices.Equal(p.names(), []string{"u1", "anonymous", "admin"}) {
		t.Fatalf("descending second page %v", p.names())
	}
	// The filter is a case-insensitive substring: "anonymous" has a u too.
	p = list("?username=U")
	if !slices.Equal(p.names(), []string{"anonymous", "u1", "u2", "u3"}) || p.Cursor != nil ||
		!strings.HasSuffix(p.Users[1].URI, "/access/api/v2/users/u1") {
		t.Fatalf("username filter %+v", p)
	}
	p = list("?onlyAdmins=true")
	if !slices.Equal(p.names(), []string{"admin", "u2"}) {
		t.Fatalf("admins %v", p.names())
	}
	if _, err := w.db.Users().RecordFailedLogin(ctx, "u3", 1); err != nil {
		t.Fatal(err)
	}
	p = list("?status=locked")
	if !slices.Equal(p.names(), []string{"u3"}) {
		t.Fatalf("locked %v", p.names())
	}
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "p", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Targets: map[string]types.Target{"libs": {}},
				Actions: types.Grants{
					Users:  map[string]types.Actions{"u1": types.ActionRead},
					Groups: map[string]types.Actions{"bosses": types.ActionRead},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	p = list("?resourceType=artifact&resourceName=libs")
	if !slices.Equal(p.names(), []string{"u1", "u2"}) {
		t.Fatalf("granted on libs %v", p.names())
	}
	p = list("?resourceType=build&resourceName=libs")
	if len(p.Users) != 0 {
		t.Fatalf("granted on the build resource %v", p.names())
	}
	for _, bad := range []string{"?status=gone", "?cursor=not*base64", "?resourceType=nope&resourceName=x", "?projectKey=p"} {
		w.call(t, http.MethodGet, "/access/api/v2/users"+bad, admin, "", http.StatusBadRequest, nil)
	}
	// A token carrying system:identities:r reads users without being admin.
	reader := w.bearer(t, "u1", "system:identities:r")
	w.call(t, http.MethodGet, "/access/api/v2/users", reader, "", http.StatusOK, nil)
	w.call(t, http.MethodGet, "/access/api/v2/users/u2", reader, "", http.StatusOK, nil)
	w.call(t, http.MethodGet, "/access/api/v2/groups/bosses", reader, "", http.StatusOK, nil)
	w.call(
		t,
		http.MethodGet,
		"/access/api/v2/users",
		basic("u1", "User-pass1"),
		"",
		http.StatusForbidden,
		nil,
	)
}

// TestDeleteGroupDropsGrants deletes the seeded readers group through the
// legacy API and another group through Access v2: each takes its grants
// with it, so a group created later under the same name inherits nothing.
func TestDeleteGroupDropsGrants(t *testing.T) {
	w := newIdentityWorld(t)
	ctx := context.Background()
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/groups",
		admin,
		`{"name":"team"}`,
		http.StatusCreated,
		nil,
	)
	anything, err := w.db.Permissions().Get(ctx, auth.AnythingPermission)
	if err != nil {
		t.Fatal(err)
	}
	anything.Resources[types.ResourceArtifact].Actions.Groups["team"] = types.ActionWrite
	if err := w.db.Permissions().Update(ctx, anything); err != nil {
		t.Fatal(err)
	}
	w.call(
		t,
		http.MethodDelete,
		"/artifactory/api/security/groups/readers",
		admin,
		"",
		http.StatusOK,
		nil,
	)
	w.call(t, http.MethodDelete, "/access/api/v2/groups/team", admin, "", http.StatusNoContent, nil)
	got, err := w.db.Permissions().Get(ctx, auth.AnythingPermission)
	if err != nil {
		t.Fatal(err)
	}
	if g := got.Resources[types.ResourceArtifact].Actions.Groups; len(g) != 0 {
		t.Fatalf("grants left to deleted groups: %v", g)
	}
	w.call(
		t,
		http.MethodPost,
		"/access/api/v2/groups",
		admin,
		`{"name":"readers"}`,
		http.StatusCreated,
		nil,
	)
	w.call(t, http.MethodGet, "/access/api/v2/groups/readers", admin, "", http.StatusOK, nil)
}

func TestGroupListPagesAndFilters(t *testing.T) {
	w := newIdentityWorld(t)
	for _, n := range []string{"alpha", "beta", "gamma"} {
		w.call(
			t,
			http.MethodPost,
			"/access/api/v2/groups",
			admin,
			`{"name":"`+n+`"}`,
			http.StatusCreated,
			nil,
		)
	}
	type groupPage struct {
		Groups []struct {
			Name string `json:"group_name"`
			URI  string `json:"uri"`
		} `json:"groups"`
		Cursor *string `json:"cursor"`
	}
	names := func(p groupPage) []string {
		out := []string{}
		for _, g := range p.Groups {
			out = append(out, g.Name)
		}
		return out
	}
	list := func(query string) groupPage {
		t.Helper()
		var p groupPage
		w.call(t, http.MethodGet, "/access/api/v2/groups"+query, admin, "", http.StatusOK, &p)
		return p
	}
	p := list("?limit=2")
	if !slices.Equal(names(p), []string{"alpha", "beta"}) || p.Cursor == nil {
		t.Fatalf("first page %v", names(p))
	}
	p = list("?limit=2&cursor=" + *p.Cursor)
	if !slices.Equal(names(p), []string{"gamma", "readers"}) || p.Cursor != nil {
		t.Fatalf("second page %v", names(p))
	}
	p = list("?groupName=AM")
	if !slices.Equal(names(p), []string{"gamma"}) ||
		!strings.HasSuffix(p.Groups[0].URI, "/access/api/v2/groups/gamma") {
		t.Fatalf("name filter %+v", p)
	}
	p = list("?resourceType=artifact&resourceName=none")
	if len(p.Groups) != 0 {
		t.Fatalf("granted %v", names(p))
	}
	w.call(
		t,
		http.MethodGet,
		"/access/api/v2/groups?role=Developer",
		admin,
		"",
		http.StatusBadRequest,
		nil,
	)
	w.call(
		t,
		http.MethodGet,
		"/access/api/v2/groups?limit=-1",
		admin,
		"",
		http.StatusBadRequest,
		nil,
	)
}

func TestAccessIdentityWithoutStore(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Name: "admin", Admin: true})
	svc := accessService{}
	if _, err := svc.GetUserList(
		ctx,
		accessgen.GetUserListRequestObject{},
	); !errors.Is(
		err,
		apierror.ErrNotImplemented,
	) {
		t.Errorf("users without a store: %v", err)
	}
	if _, err := svc.GetGroupList(
		ctx,
		accessgen.GetGroupListRequestObject{},
	); !errors.Is(
		err,
		apierror.ErrNotImplemented,
	) {
		t.Errorf("groups without a store: %v", err)
	}
	if _, err := svc.GetBasicAuthenticationStatus(
		ctx, accessgen.GetBasicAuthenticationStatusRequestObject{},
	); !errors.Is(err, apierror.ErrNotImplemented) {
		t.Errorf("the basic authentication switch without a store: %v", err)
	}
}
