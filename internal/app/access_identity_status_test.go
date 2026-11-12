package app

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
)

// accessIdentityStatusRows covers Access v2 users and groups and the
// identity configuration. The rows share one store with every other row, so
// none of them may change what later rows rely on: the bulk password expiry
// and the basic authentication switch are exercised for real in their own
// tests, the policy is only reset to its defaults, "user" joins no group and
// keeps its password, and the readers group stays (DELETE as admin is
// TestAccDeleteGroupDropsGrants).
func accessIdentityStatusRows() []statusCase {
	const (
		users    = "/access/api/v2/users"
		groups   = "/access/api/v2/groups"
		basicSw  = "/access/api/v1/config/security/authentication/basic_authentication_enabled"
		policy   = "/access/api/v1/config/security/password_policy"
		uList    = "GET " + users
		uCreate  = "POST " + users
		uGet     = "GET " + users + "/{username}"
		uPatch   = "PATCH " + users + "/{username}"
		uDelete  = "DELETE " + users + "/{username}"
		uGroups  = "PATCH " + users + "/{username}/groups"
		uPass    = "PUT " + users + "/{username}/password"
		uExpire  = "POST " + users + "/{username}/password/expire"
		uUnlock  = "POST " + users + "/{username}/unlock"
		allExp   = "POST " + users + "/expire_password_for_all_users"
		allUnexp = "POST " + users + "/unexpire_password_for_all_users"
		gList    = "GET " + groups
		gCreate  = "POST " + groups
		gGet     = "GET " + groups + "/{name}"
		gPatch   = "PATCH " + groups + "/{name}"
		gPut     = "PUT " + groups + "/{name}"
		gDelete  = "DELETE " + groups + "/{name}"
		gMembers = "PATCH " + groups + "/{name}/members"
		bGet     = "GET " + basicSw
		bPut     = "PUT " + basicSw
		pPut     = "PUT " + policy
		pPatch   = "PATCH " + policy
		lockPol  = "/access/api/v1/config/security/user_lock_policy"
		lGet     = "GET " + lockPol
		lPut     = "PUT " + lockPol
		newUser  = `{"username":"v2-user","email":"v2@example.com","password":"V2-pass-1"}`
	)
	return []statusCase{
		{uCreate, users, "admin", newUser, 201},
		{uCreate, users, "admin", newUser, 409},
		{uCreate, users, "admin", `{"username":"v2-nopw","email":"x@example.com"}`, 400},
		{uCreate, users, "user", newUser, 403},
		{uCreate, users, "", newUser, 401},
		{uCreate, users, "anonymous", newUser, 401},

		{uList, users, "admin", "", 200},
		{uList, users + "?limit=0", "admin", "", 400},
		{uList, users + "?role=Developer", "admin", "", 400},
		{uList, users, "user", "", 403},
		{uList, users, "", "", 401},

		{uGet, users + "/v2-user", "admin", "", 200},
		{uGet, users + "/user", "user", "", 200},
		{uGet, users + "/v2-user", "user", "", 403},
		{uGet, users + "/absent", "admin", "", 404},
		{uGet, users + "/v2-user", "", "", 401},

		{uPatch, users + "/v2-user", "admin", `{"email":"new@example.com"}`, 200},
		{uPatch, users + "/absent", "admin", `{"email":"new@example.com"}`, 404},
		{uPatch, users + "/v2-user", "user", `{"admin":true}`, 403},

		{uGroups, users + "/v2-user/groups", "admin", `{"add":["readers"]}`, 200},
		{uGroups, users + "/v2-user/groups", "admin", `{"add":["no-such-group"]}`, 400},
		{uGroups, users + "/absent/groups", "admin", `{"add":["readers"]}`, 404},
		{uGroups, users + "/v2-user/groups", "user", `{"remove":["readers"]}`, 403},

		{uPass, users + "/v2-user/password", "admin", `{"password":"V2-pass-2"}`, 204},
		// A user may set their own password, but not the current one again
		// (the default policy's not_match_old); "user" keeps "pw" for the
		// rows after this.
		{uPass, users + "/user/password", "user", `{"password":"pw"}`, 400},
		{uPass, users + "/v2-user/password", "user", `{"password":"pw3"}`, 403},
		{uPass, users + "/v2-user/password", "admin", `{}`, 400},
		{uPass, users + "/absent/password", "admin", `{"password":"pw"}`, 404},

		{uExpire, users + "/v2-user/password/expire", "admin", "", 204},
		{uExpire, users + "/absent/password/expire", "admin", "", 404},
		{uExpire, users + "/v2-user/password/expire", "user", "", 403},
		{uUnlock, users + "/v2-user/unlock", "admin", "", 204},
		{uUnlock, users + "/absent/unlock", "admin", "", 404},
		{uUnlock, users + "/v2-user/unlock", "user", "", 403},
		{allExp, users + "/expire_password_for_all_users", "user", "", 403},
		{allExp, users + "/expire_password_for_all_users", "", "", 401},
		{allUnexp, users + "/unexpire_password_for_all_users", "user", "", 403},
		{allUnexp, users + "/unexpire_password_for_all_users", "", "", 401},

		{uDelete, users + "/v2-user", "admin", "", 204},
		{uDelete, users + "/v2-user", "admin", "", 404},
		{uDelete, users + "/admin", "admin", "", 403},
		{uDelete, users + "/user", "user", "", 403},

		{gCreate, groups, "admin", `{"name":"v2-group","description":"d"}`, 201},
		{gCreate, groups, "admin", `{"name":"v2-group"}`, 409},
		{gCreate, groups, "admin", `{"name":"v2-ldap","import_from_ldap_group_setting":"x"}`, 400},
		{gCreate, groups, "admin", `{"name":"v2-other","members":["absent"]}`, 404},
		{gCreate, groups, "user", `{"name":"v2-other"}`, 403},
		{gCreate, groups, "", `{"name":"v2-other"}`, 401},
		{gList, groups, "admin", "", 200},
		{gList, groups, "user", "", 403},
		{gGet, groups + "/v2-group", "admin", "", 200},
		{gGet, groups + "/absent", "admin", "", 404},
		{gGet, groups + "/v2-group", "user", "", 403},
		{gPatch, groups + "/v2-group", "admin", `{"auto_join":false}`, 200},
		{gPatch, groups + "/v2-group", "admin", `{"realm":"ldap"}`, 400},
		{gPatch, groups + "/v2-group", "admin", `{"members":["absent"]}`, 200},
		{gPatch, groups + "/absent", "admin", `{"auto_join":false}`, 404},
		{gPatch, groups + "/v2-group", "user", `{"auto_join":true}`, 403},
		{gPut, groups + "/v2-put", "admin", `{"description":"d","members":["admin"]}`, 201},
		{gPut, groups + "/v2-put", "admin", `{"name":"v2-put"}`, 200},
		{gPut, groups + "/v2-put", "admin", `{"name":"other"}`, 400},
		{gPut, groups + "/v2-put", "admin", `{"import_from_ldap_group_setting":"x"}`, 400},
		{gPut, groups + "/v2-put", "admin", `{"members":["absent"]}`, 404},
		{gPut, groups + "/v2-put", "user", `{}`, 403},
		{gPut, groups + "/v2-put", "", `{}`, 401},
		{gPut, groups + "/v2-put", "anonymous", `{}`, 401},
		{gDelete, groups + "/v2-put", "admin", "", 204},
		{gMembers, groups + "/v2-group/members", "admin", `{"add":["admin"]}`, 200},
		{gMembers, groups + "/v2-group/members", "admin", `{"add":["absent"]}`, 404},
		{gMembers, groups + "/absent/members", "admin", `{"add":["admin"]}`, 404},
		{gMembers, groups + "/v2-group/members", "user", `{"remove":["admin"]}`, 403},
		{gDelete, groups + "/v2-group", "admin", "", 204},
		{gDelete, groups + "/v2-group", "admin", "", 404},
		{gDelete, groups + "/readers", "user", "", 403},

		{bGet, basicSw, "admin", "", 200},
		{bGet, basicSw, "user", "", 403},
		{bGet, basicSw, "", "", 401},
		{bPut, basicSw, "admin", `{"enabled":true}`, 200},
		{bPut, basicSw, "user", `{"enabled":false}`, 403},
		{pPut, policy, "admin", `{}`, 204},
		{pPut, policy, "admin", `{"length":-1}`, 400},
		{pPut, policy, "user", `{}`, 403},
		{pPatch, policy, "admin", `{}`, 204},
		{pPatch, policy, "user", `{}`, 403},
		{lGet, lockPol, "admin", "", 200},
		{lGet, lockPol, "user", "", 403},
		{lGet, lockPol, "", "", 401},
		{lPut, lockPol, "admin", `{}`, 200},
		{lPut, lockPol, "admin", `{"attempts":101}`, 400},
		{lPut, lockPol, "admin", `{"secondsToUnlock":-1}`, 400},
		{lPut, lockPol, "admin", `{"passwordExpiryDays":-1}`, 400},
		{lPut, lockPol, "user", `{}`, 403},
		{lPut, lockPol, "", `{}`, 401},
	}
}

// groupExchange is one request to the Access v2 group routes and the exact
// answer it must get.
type groupExchange struct {
	method, path string
	creds        map[string]string
	body         string
	status       int
	answer       string
}

// exchange makes each request in order on w and compares status and body
// byte for byte.
func exchange(t *testing.T, w identityWorld, steps []groupExchange) {
	t.Helper()
	for _, s := range steps {
		headers := map[string]string{}
		for k, v := range s.creds {
			headers[k] = v
		}
		if s.body != "" {
			headers["Content-Type"] = "application/json"
		}
		rec := do(t, w.h, s.method, s.path, headers, s.body)
		if rec.Code != s.status || rec.Body.String() != s.answer {
			t.Errorf("%s %s %s:\n got %d %q\nwant %d %q",
				s.method, s.path, s.body, rec.Code, rec.Body, s.status, s.answer)
		}
	}
}

// groupWorld is an identity world with "user", a non-admin whose password
// is "pw", for the group routes' answers.
func groupWorld(t *testing.T) (identityWorld, map[string]string) {
	t.Helper()
	w := newIdentityWorld(t)
	if err := w.db.Users().Create(
		context.Background(), accesstest.User{Name: "user", PasswordHash: cheapHash},
	); err != nil {
		t.Fatal(err)
	}
	return w, basic("user", "pw")
}

// accessError is the Access error envelope with one error, as the router
// writes it.
func accessError(code, message string) string {
	return `{"errors":[{"code":"` + code + `","message":"` + message + `"}]}` + "\n"
}

// groupJSON is a group's details as the group routes write them. flags
// lists, in order, admin_privileges, auto_join and manage_webhook; extra
// holds description and external_id, or is empty when neither is set.
func groupJSON(name, members, realm, extra string, flags [3]bool) string {
	b := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}
	return `{"admin_privileges":` + b(flags[0]) + `,"auto_join":` + b(flags[1]) + `,` + extra +
		`"manage_resources":false,"manage_webhook":` + b(flags[2]) + `,"members":` + members +
		`,"name":"` + name + `","policy_manager":false,"policy_viewer":false,` + realm +
		`,"reports_manager":false,"watch_manager":false}` + "\n"
}

// TestAccReplaceGroupAnswers follows JFrog's Create or Replace a Group
// (https://docs.jfrog.com/administration/reference/replacegroup.md): 201 for
// a new name, 200 for a replacement that resets every field it leaves out
// and replaces the members, 400 for a body name that differs from the path,
// 404 for an unknown member, admin only. LDAP import answers as Create a
// Group does until P12. Every one of these answered 405 before (#1200).
func TestAccReplaceGroupAnswers(t *testing.T) {
	w, user := groupWorld(t)
	// A group in another realm, as an LDAP import would leave it, with
	// every field set.
	if err := w.db.Groups().Create(context.Background(), accesstest.Group{
		Name: "dir", Description: "d", AutoJoin: true, AdminPrivileges: true, Realm: "ldap",
		RealmAttributes: "ldapGroupName=dir", ExternalID: "e", Members: []string{"admin"},
		Roles: accesstest.RoleFlags{
			WatchManager: true, PolicyViewer: true, PolicyManager: true, ReportsManager: true,
			ResourcesManager: true, ManageWebhook: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	const (
		put, get = http.MethodPut, http.MethodGet
		ops, dir = "/access/api/v2/groups/ops", "/access/api/v2/groups/dir"
		fresh    = "/access/api/v2/groups/new"
		internal = `"realm":"internal"`
		set      = `"description":"d","external_id":"e",`
	)
	var (
		created  = groupJSON("ops", `["admin","user"]`, internal, set, [3]bool{true, false, true})
		reset    = groupJSON("ops", `[]`, internal, "", [3]bool{false, true, false})
		withUser = groupJSON("ops", `["user"]`, internal, "", [3]bool{})
		dirReset = groupJSON("dir", `[]`, `"realm":"ldap","realm_attributes":"ldapGroupName=dir"`,
			"", [3]bool{})
		mismatch = accessError("BAD_REQUEST", "the group name in the body must match the path")
		noLDAP   = accessError(
			"BAD_REQUEST",
			"LDAP group import is not available in artifactweave",
		)
		exclusive = accessError("BAD_REQUEST",
			"members and import_from_ldap_group_setting are mutually exclusive")
		unknown = accessError("NOT_FOUND", "One or more users in members were not found")
		ldap    = `{"import_from_ldap_group_setting":"corp"}`
	)
	exchange(t, w, []groupExchange{
		{put, ops, admin, `{"description":"d","admin_privileges":true,"external_id":"e",` +
			`"manage_webhook":true,"members":["user","admin","user"]}`, 201, created},
		{put, ops, admin, `{"name":"ops","auto_join":true}`, 200, reset},
		{get, ops, admin, "", 200, reset},
		{put, ops, admin, `{"name":"","members":["user"]}`, 200, withUser},
		{put, ops, admin, `{"name":"Ops"}`, 400, mismatch},
		{put, ops, admin, ldap, 400, noLDAP},
		{put, ops, admin, `{"import_from_ldap_group_setting":"corp","members":[]}`, 400, exclusive},
		{put, fresh, admin, ldap, 400, noLDAP},
		{put, ops, admin, `{"members":["user","nobody"]}`, 404, unknown},
		{put, fresh, admin, `{"members":["nobody"]}`, 404, unknown},
		// Neither refusal changed anything.
		{get, ops, admin, "", 200, withUser},
		{get, fresh, admin, "", 404, accessError("NOT_FOUND", "Group 'new' does not exist")},
		{put, dir, admin, `{}`, 200, dirReset},
		{
			put, "/access/api/v2/groups/%20bad", admin, `{}`, 400,
			accessError("BAD_REQUEST", `group name \" bad\" is not valid`),
		},
		{put, ops, admin, "", 400, accessError("BAD_REQUEST", "can't decode JSON body: EOF")},
		{put, ops, user, `{}`, 403, accessError("FORBIDDEN", "Forbidden")},
		{put, ops, nil, `{}`, 401, accessError("UNAUTHORIZED", "Authentication is required")},
		{get, ops, admin, "", 200, withUser},
	})
	// The members a replacement drops leave the group on the user's side too.
	var d struct {
		Groups []string `json:"groups"`
	}
	w.call(t, get, "/access/api/v2/users/admin", admin, "", http.StatusOK, &d)
	if slices.Contains(d.Groups, "ops") || slices.Contains(d.Groups, "dir") {
		t.Fatalf("admin's groups after replacements that dropped admin: %v", d.Groups)
	}
}

// TestAccGroupRoutesKeepTheirAnswers pins the other group routes' answers
// byte for byte as they were before the create or replace route existed
// (#1200), since that change reshaped Create a Group's handler.
func TestAccGroupRoutesKeepTheirAnswers(t *testing.T) {
	w, user := groupWorld(t)
	const (
		post, get           = http.MethodPost, http.MethodGet
		patch, del          = http.MethodPatch, http.MethodDelete
		groups, ops, absent = "/access/api/v2/groups", "/access/api/v2/groups/ops",
			"/access/api/v2/groups/nope"
		internal = `"realm":"internal"`
		set      = `"description":"d","external_id":"e",`
	)
	var (
		created = groupJSON("ops", `["admin"]`, internal, set, [3]bool{true, false, false})
		joined  = groupJSON("ops", `["admin"]`, internal, set, [3]bool{true, true, false})
		nope    = accessError("NOT_FOUND", "Group 'nope' does not exist")
		ldap    = `{"name":"l","import_from_ldap_group_setting":"s"`
	)
	exchange(t, w, []groupExchange{
		{post, groups, admin, `{"name":"ops","description":"d","members":["admin"],` +
			`"admin_privileges":true,"external_id":"e"}`, 201, created},
		{
			post, groups, admin, `{"name":"ops"}`, 409,
			accessError("CONFLICT", "Group 'ops' already exists"),
		},
		{
			post, groups, admin, `{}`, 400,
			accessError("BAD_REQUEST", "a body with the group name is required"),
		},
		{post, groups, admin, "", 400, accessError("BAD_REQUEST", "can't decode JSON body: EOF")},
		{
			post, groups, admin, ldap + `}`, 400,
			accessError("BAD_REQUEST", "LDAP group import is not available in artifactweave"),
		},
		{post, groups, admin, ldap + `,"members":[]}`, 400, accessError("BAD_REQUEST",
			"members and import_from_ldap_group_setting are mutually exclusive")},
		{
			post, groups, admin, `{"name":"m","members":["nobody"]}`, 404,
			accessError("NOT_FOUND", "One or more users in members were not found"),
		},
		{
			post, groups, admin, `{"name":" bad"}`, 400,
			accessError("BAD_REQUEST", `group name \" bad\" is not valid`),
		},
		{post, groups, user, `{"name":"u"}`, 403, accessError("FORBIDDEN", "Forbidden")},
		{
			post, groups, nil, `{"name":"u"}`, 401,
			accessError("UNAUTHORIZED", "Authentication is required"),
		},
		{get, ops, admin, "", 200, created},
		{get, absent, admin, "", 404, nope},
		{patch, ops, admin, `{"auto_join":true}`, 200, joined},
		{
			patch, ops, admin, `{"realm":"ldap"}`, 400,
			accessError("BAD_REQUEST", "realm cannot be changed"),
		},
		{patch, absent, admin, `{"auto_join":true}`, 404, nope},
		{del, ops, admin, "", 204, ""},
		{del, ops, admin, "", 404, accessError("NOT_FOUND", "Group 'ops' does not exist")},
	})
}
