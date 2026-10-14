package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

const (
	secUsers    = "/artifactory/api/security/users"
	secGroups   = "/artifactory/api/security/groups"
	secLockPol  = "/artifactory/api/security/userLockPolicy"
	secExpiry   = "/artifactory/api/security/configuration/passwordExpirationPolicy"
	secExpire   = "/artifactory/api/security/users/authorization/expirePassword"
	secUnexpire = "/artifactory/api/security/users/authorization/unexpirePassword/"
	secAPIKey   = "/artifactory/api/security/apiKey"
)

// securityStatusRows covers the legacy security API. The rows create the
// principals they change (sec-user, del-user, sec-group) so the shared
// fixture's admin and user keep working for every other row.
func securityStatusRows() []statusCase {
	const (
		users     = "GET " + secUsers
		user      = "GET " + secUsers + "/{userName}"
		putUser   = "PUT " + secUsers + "/{userName}"
		postUser  = "POST " + secUsers + "/{userName}"
		delUser   = "DELETE " + secUsers + "/{userName}"
		groups    = "GET " + secGroups
		group     = "GET " + secGroups + "/{groupName}"
		putGroup  = "PUT " + secGroups + "/{groupName}"
		postGroup = "POST " + secGroups + "/{groupName}"
		delGroup  = "DELETE " + secGroups + "/{groupName}"
		locked    = "GET /artifactory/api/security/lockedUsers"
		unlockAll = "POST /artifactory/api/security/unlockAllUsers"
		unlock    = "POST /artifactory/api/security/unlockUsers"
		unlockOne = "POST /artifactory/api/security/unlockUsers/{userName}"
		getLock   = "GET " + secLockPol
		putLock   = "PUT " + secLockPol
		getExpiry = "GET " + secExpiry
		putExpiry = "PUT " + secExpiry
		expire    = "POST " + secExpire
		unexpire  = "POST " + secUnexpire + "{userName}"
		newKey    = "POST " + secAPIKey
		rotateKey = "PUT " + secAPIKey
		getKey    = "GET " + secAPIKey
		revokeKey = "DELETE " + secAPIKey
		revokeFor = "DELETE " + secAPIKey + "/{username}"
		newSec    = `{"name":"sec-user","email":"s@example.com","password":"Sec-pass1"}`
	)
	return []statusCase{
		{users, secUsers, "admin", "", 200},
		{users, secUsers, "user", "", 403},
		{users, secUsers, "anonymous", "", 401},
		{users, secUsers, "", "", 401},

		{putUser, secUsers + "/sec-user", "admin", newSec, 201},
		{putUser, secUsers + "/sec-user", "admin", newSec, 201},
		{
			putUser, secUsers + "/del-user", "admin",
			`{"name":"del-user","email":"d@example.com","password":"Del-pass1"}`, 201,
		},
		{putUser, secUsers + "/bad", "admin", `{"name":"bad","password":"x"}`, 400},
		{putUser, secUsers + "/bad", "admin", `{"name":"other","email":"e","password":"x"}`, 400},
		{putUser, secUsers + "/bad", "admin", `{"name":"bad","email":"e"}`, 400},
		{
			putUser, secUsers + "/bad", "admin",
			`{"name":"bad","email":"e","password":"x","groups":["nope"]}`, 400,
		},
		{putUser, secUsers + "/bad", "user", newSec, 403},
		{putUser, secUsers + "/bad", "", newSec, 401},

		{user, secUsers + "/sec-user", "admin", "", 200},
		{user, secUsers + "/nobody", "admin", "", 404},
		{user, secUsers + "/sec-user", "user", "", 403},

		{postUser, secUsers + "/sec-user", "admin", `{"email":"t@example.com"}`, 200},
		{postUser, secUsers + "/sec-user", "admin", `{"groups":["nope"]}`, 400},
		{postUser, secUsers + "/nobody", "admin", `{"email":"t@example.com"}`, 404},
		{postUser, secUsers + "/sec-user", "user", `{"email":"t@example.com"}`, 403},

		{delUser, secUsers + "/del-user", "admin", "", 200},
		{delUser, secUsers + "/admin", "admin", "", 400},
		{delUser, secUsers + "/nobody", "admin", "", 404},
		{delUser, secUsers + "/sec-user", "user", "", 403},

		{groups, secGroups, "admin", "", 200},
		{groups, secGroups, "user", "", 403},
		{
			putGroup,
			secGroups + "/sec-group",
			"admin",
			`{"name":"sec-group","userNames":["sec-user"]}`,
			201,
		},
		{putGroup, secGroups + "/bad", "admin", `{"userNames":["nobody"]}`, 400},
		{putGroup, secGroups + "/bad", "user", `{}`, 403},
		{group, secGroups + "/sec-group?includeUsers=true", "admin", "", 200},
		{group, secGroups + "/nobody", "admin", "", 404},
		{group, secGroups + "/sec-group", "user", "", 403},
		{postGroup, secGroups + "/sec-group", "admin", `{"description":"d"}`, 200},
		{postGroup, secGroups + "/nobody", "admin", `{"description":"d"}`, 404},
		{postGroup, secGroups + "/sec-group", "user", `{"description":"d"}`, 403},
		{delGroup, secGroups + "/sec-group", "admin", "", 200},
		{delGroup, secGroups + "/nobody", "admin", "", 404},
		{delGroup, secGroups + "/readers", "user", "", 403},

		{locked, "/artifactory/api/security/lockedUsers", "admin", "", 200},
		{locked, "/artifactory/api/security/lockedUsers", "user", "", 403},
		{unlockAll, "/artifactory/api/security/unlockAllUsers", "admin", "", 200},
		{unlockAll, "/artifactory/api/security/unlockAllUsers", "user", "", 403},
		{unlock, "/artifactory/api/security/unlockUsers", "admin", `["sec-user","ghost"]`, 200},
		{unlock, "/artifactory/api/security/unlockUsers", "admin", `[]`, 400},
		{unlock, "/artifactory/api/security/unlockUsers", "user", `["sec-user"]`, 403},
		{unlockOne, "/artifactory/api/security/unlockUsers/sec-user", "admin", "", 200},
		{unlockOne, "/artifactory/api/security/unlockUsers/nobody", "admin", "", 404},
		{unlockOne, "/artifactory/api/security/unlockUsers/sec-user", "user", "", 403},

		{getLock, secLockPol, "admin", "", 200},
		{getLock, secLockPol, "user", "", 403},
		{putLock, secLockPol, "admin", `{"enabled":"false","loginAttempts":"3"}`, 200},
		{putLock, secLockPol, "admin", `{"enabled":true,"loginAttempts":0}`, 400},
		{putLock, secLockPol, "user", `{"enabled":false}`, 403},
		{getExpiry, secExpiry, "admin", "", 200},
		{getExpiry, secExpiry, "user", "", 403},
		{putExpiry, secExpiry, "admin", `{"enabled":"false","passwordMaxAge":"30"}`, 200},
		{putExpiry, secExpiry, "admin", `{"passwordMaxAge":0}`, 400},
		{putExpiry, secExpiry, "user", `{"enabled":false}`, 403},
		{expire, secExpire, "admin", `["sec-user","ghost"]`, 200},
		{expire, secExpire, "admin", `[]`, 400},
		{expire, secExpire, "user", `["sec-user"]`, 403},
		{unexpire, secUnexpire + "sec-user", "admin", "", 200},
		{unexpire, secUnexpire + "nobody", "admin", "", 404},
		{unexpire, secUnexpire + "sec-user", "user", "", 403},

		{newKey, secAPIKey, "user", "", 400},
		{newKey, secAPIKey, "", "", 401},
		{rotateKey, secAPIKey, "user", "", 400},
		{rotateKey, secAPIKey, "anonymous", "", 401},
		{getKey, secAPIKey, "user", "", 404},
		{getKey, secAPIKey, "", "", 401},
		{revokeKey, secAPIKey, "user", "", 200},
		{revokeKey, secAPIKey + "?deleteAll=1", "user", "", 403},
		{revokeKey, secAPIKey + "?deleteAll=1", "admin", "", 200},
		{revokeKey, secAPIKey, "anonymous", "", 401},
		{revokeFor, secAPIKey + "/user", "admin", "", 200},
		{revokeFor, secAPIKey + "/nobody", "admin", "", 404},
		{revokeFor, secAPIKey + "/user", "user", "", 403},
	}
}

func jsonHeaders(h map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/json"}
	for k, v := range h {
		out[k] = v
	}
	return out
}

func decodeInto[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

// TestLegacyUserFlow follows the artifactory_user resource: create, read,
// update, replace, delete.
func TestLegacyUserFlow(t *testing.T) {
	h := statusFixture(t, false)
	admin := jsonHeaders(basic("admin", "pw"))
	if rec := do(t, h, http.MethodPut, secGroups+"/joiners", admin,
		`{"name":"joiners","autoJoin":true}`); rec.Code != http.StatusCreated {
		t.Fatalf("auto-join group: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodPut, secUsers+"/terra", admin,
		`{"name":"terra","email":"t@example.com","password":"Terra-pass1","admin":false,`+
			`"profileUpdatable":true,"groups":["readers"]}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	read := func() map[string]any {
		rec := do(t, h, http.MethodGet, secUsers+"/terra", basic("admin", "pw"), "")
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Terra-pass1") {
			t.Fatalf("read: %d %s", rec.Code, rec.Body)
		}
		return decodeInto[map[string]any](t, rec.Body.String())
	}
	doc := read()
	if doc["email"] != "t@example.com" || doc["realm"] != "internal" || doc["status"] != "ENABLED" {
		t.Fatalf("document: %v", doc)
	}
	if groups := doc["groups"].([]any); len(groups) != 2 { //nolint:forcetypeassert // fixture
		t.Fatalf("a new user joins the auto-join groups too: %v", groups)
	}
	// The new user signs in with the password it was given.
	if rec := do(
		t,
		h,
		http.MethodGet,
		secAPIKey,
		basic("terra", "Terra-pass1"),
		"",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("sign in as the new user: %d %s", rec.Code, rec.Body)
	}
	// A partial update changes only the fields present.
	if rec := do(
		t,
		h,
		http.MethodPost,
		secUsers+"/terra",
		admin,
		`{"email":"u@example.com"}`,
	); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if doc := read(); doc["email"] != "u@example.com" ||
		len(doc["groups"].([]any)) != 2 { //nolint:forcetypeassert // fixture
		t.Fatalf("after update: %v", doc)
	}
	// A replacement sets every field: without groups the user is in none,
	// and the new password replaces the old one.
	if rec := do(
		t,
		h,
		http.MethodPut,
		secUsers+"/terra",
		admin,
		`{"name":"terra","email":"v@example.com","password":"Terra-pass2"}`,
	); rec.Code != http.StatusCreated {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if doc := read(); doc["email"] != "v@example.com" || doc["groups"] != nil {
		t.Fatalf("after replace: %v", doc)
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		secAPIKey,
		basic("terra", "Terra-pass2"),
		"",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("the replaced password: %d", rec.Code)
	}
	list := do(t, h, http.MethodGet, secUsers, basic("admin", "pw"), "")
	items := decodeInto[[]map[string]string](t, list.Body.String())
	if !slices.ContainsFunc(items, func(m map[string]string) bool {
		return m["name"] == "terra" && strings.HasSuffix(m["uri"], secUsers+"/terra") &&
			m["realm"] == "internal"
	}) {
		t.Fatalf("list: %s", list.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodDelete,
		secUsers+"/terra",
		basic("admin", "pw"),
		"",
	); rec.Code != http.StatusOK ||
		rec.Body.String() != "User 'terra' has been removed successfully." {
		t.Fatalf("delete: %d %q", rec.Code, rec.Body)
	}
}

// TestLegacyGroupFlow follows the artifactory_group resource, which always
// sends includeUsers and needs 201 on every PUT.
func TestLegacyGroupFlow(t *testing.T) {
	h := statusFixture(t, false)
	admin := jsonHeaders(basic("admin", "pw"))
	if rec := do(t, h, http.MethodPut, secGroups+"/devs", admin,
		`{"name":"devs","description":"d","userNames":["user"]}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	get := func(include string) map[string]any {
		rec := do(
			t,
			h,
			http.MethodGet,
			secGroups+"/devs?includeUsers="+include,
			basic("admin", "pw"),
			"",
		)
		if rec.Code != http.StatusOK {
			t.Fatalf("read: %d %s", rec.Code, rec.Body)
		}
		return decodeInto[map[string]any](t, rec.Body.String())
	}
	if doc := get(
		"true",
	); doc["description"] != "d" ||
		len(doc["userNames"].([]any)) != 1 { //nolint:forcetypeassert // fixture
		t.Fatalf("with members: %v", doc)
	}
	if doc := get("false"); doc["userNames"] != nil {
		t.Fatalf("members without includeUsers: %v", doc)
	}
	// A replacement without userNames keeps the members; with them it
	// replaces them.
	if rec := do(
		t,
		h,
		http.MethodPut,
		secGroups+"/devs",
		admin,
		`{"name":"devs","description":"e"}`,
	); rec.Code != http.StatusCreated {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if doc := get(
		"true",
	); doc["description"] != "e" ||
		len(doc["userNames"].([]any)) != 1 { //nolint:forcetypeassert // fixture
		t.Fatalf("after replace: %v", doc)
	}
	if rec := do(
		t,
		h,
		http.MethodPost,
		secGroups+"/devs",
		admin,
		`{"userNames":[]}`,
	); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if doc := get("true"); doc["userNames"] != nil {
		t.Fatalf("after emptying: %v", doc)
	}
	list := do(t, h, http.MethodGet, secGroups, basic("admin", "pw"), "")
	if !strings.Contains(list.Body.String(), `"name":"devs"`) {
		t.Fatalf("list: %s", list.Body)
	}
}

// TestLockout checks the configured number of wrong passwords locks a
// user, the lock holds against the right password, and unlocking restores
// access.
func TestLockout(t *testing.T) {
	h := statusFixture(t, false)
	admin := jsonHeaders(basic("admin", "pw"))
	if rec := do(
		t,
		h,
		http.MethodPut,
		secLockPol,
		admin,
		`{"enabled":true,"loginAttempts":2}`,
	); rec.Code != http.StatusOK {
		t.Fatalf("policy: %d %s", rec.Code, rec.Body)
	}
	probe := func(password string) int {
		return do(t, h, http.MethodGet, secAPIKey, basic("user", password), "").Code
	}
	if probe("pw") != http.StatusNotFound {
		t.Fatal("user cannot sign in before the lock")
	}
	for range 2 {
		if code := probe("wrong"); code != http.StatusUnauthorized {
			t.Fatalf("wrong password: %d", code)
		}
	}
	if code := probe("pw"); code != http.StatusUnauthorized {
		t.Fatalf("a locked user signed in: %d", code)
	}
	rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/api/security/lockedUsers",
		basic("admin", "pw"),
		"",
	)
	if got := decodeInto[[]string](t, rec.Body.String()); !slices.Equal(got, []string{"user"}) {
		t.Fatalf("locked users: %s", rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodPost,
		"/artifactory/api/security/unlockUsers/user",
		basic("admin", "pw"),
		"",
	); rec.Code != http.StatusOK {
		t.Fatalf("unlock: %d %s", rec.Code, rec.Body)
	}
	if code := probe("pw"); code != http.StatusNotFound {
		t.Fatalf("unlocking did not restore access: %d", code)
	}
	// Disabling the policy clears locks.
	for range 2 {
		probe("wrong")
	}
	if rec := do(
		t,
		h,
		http.MethodPut,
		secLockPol,
		admin,
		`{"enabled":false}`,
	); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d", rec.Code)
	}
	if code := probe("pw"); code != http.StatusNotFound {
		t.Fatalf("disabling the policy left the lock: %d", code)
	}
	policy := do(t, h, http.MethodGet, secLockPol, basic("admin", "pw"), "")
	if policy.Body.String() != "{\"enabled\":false,\"loginAttempts\":5}\n" {
		t.Fatalf("policy with real JSON types and the default limit: %q", policy.Body)
	}
}

// TestPasswordExpiry checks an expired password is refused with its own
// message, after the password verifies, and unexpiring restores access.
func TestPasswordExpiry(t *testing.T) {
	h := statusFixture(t, false)
	admin := jsonHeaders(basic("admin", "pw"))
	if rec := do(
		t,
		h,
		http.MethodPost,
		secExpire,
		admin,
		`["user","ghost"]`,
	); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "ghost") {
		t.Fatalf("expire: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, http.MethodGet, secAPIKey, basic("user", "pw"), "")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("an expired password: %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		secAPIKey,
		basic("user", "wrong"),
		"",
	); strings.Contains(
		rec.Body.String(),
		"expired",
	) {
		t.Fatalf("a wrong password learned of the expiry: %s", rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodPost,
		secUnexpire+"user",
		basic("admin", "pw"),
		"",
	); rec.Code != http.StatusOK {
		t.Fatalf("unexpire: %d", rec.Code)
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		secAPIKey,
		basic("user", "pw"),
		"",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("after unexpiring: %d %s", rec.Code, rec.Body)
	}
	policy := do(t, h, http.MethodPut, secExpiry, admin, `{"enabled":"true","passwordMaxAge":"90"}`)
	if policy.Code != http.StatusOK ||
		policy.Body.String() != "{\"enabled\":true,\"notifyByEmail\":true,\"passwordMaxAge\":90}\n" {
		t.Fatalf("policy from strings, defaults for the rest: %d %q", policy.Code, policy.Body)
	}
}

// TestLastAdmin keeps one administrator: the seeded admin cannot be
// demoted while it is the only one, and can once another exists.
func TestLastAdmin(t *testing.T) {
	h := statusFixture(t, false)
	admin := jsonHeaders(basic("admin", "pw"))
	if rec := do(
		t,
		h,
		http.MethodPost,
		secUsers+"/admin",
		admin,
		`{"admin":false}`,
	); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "last administrator") {
		t.Fatalf("demoting the last admin: %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodPost,
		secUsers+"/user",
		admin,
		`{"admin":true,"email":"u@example.com"}`,
	); rec.Code != http.StatusOK {
		t.Fatalf("a second admin: %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodPost,
		secUsers+"/admin",
		admin,
		`{"admin":false,"email":"a@example.com"}`,
	); rec.Code != http.StatusOK {
		t.Fatalf("demoting one of two admins: %d %s", rec.Code, rec.Body)
	}
}

func TestNormalizePolicyBodyPassesOtherDocuments(t *testing.T) {
	h := statusFixture(t, false)
	// Not an object: left for the decoder, which refuses it.
	if rec := do(
		t,
		h,
		http.MethodPut,
		secLockPol,
		jsonHeaders(basic("admin", "pw")),
		`[1]`,
	); rec.Code != http.StatusBadRequest {
		t.Fatalf("an array body: %d %s", rec.Code, rec.Body)
	}
	// A string that is neither a boolean nor a number stays a string.
	if rec := do(
		t,
		h,
		http.MethodPut,
		secLockPol,
		jsonHeaders(basic("admin", "pw")),
		`{"enabled":"maybe"}`,
	); rec.Code != http.StatusBadRequest {
		t.Fatalf("a string flag: %d %s", rec.Code, rec.Body)
	}
}
