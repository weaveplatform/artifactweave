package access_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

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

// TestAccLegacyUserFlow follows the artifactory_user resource: create, read,
// update, replace, delete.
func TestAccLegacyUserFlow(t *testing.T) {
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

// TestAccLegacyGroupFlow follows the artifactory_group resource, which always
// sends includeUsers and needs 201 on every PUT.
func TestAccLegacyGroupFlow(t *testing.T) {
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

// TestAccLockout checks the configured number of wrong passwords locks a
// user, the lock holds against the right password, and unlocking restores
// access.
func TestAccLockout(t *testing.T) {
	h := statusFixture(t, false)
	admin := jsonHeaders(basic("admin", "pw"))
	// The temporary login suspension, the other defence, would answer the
	// second wrong password's successors 403 for a second; this test is
	// about the lock alone. TestAccLoginSuspension covers the suspension.
	if rec := do(t, h, http.MethodPut, "/access/api/v1/config/security/user_lock_policy", admin,
		`{"clientLoginSuspensionEnabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("suspension off: %d %s", rec.Code, rec.Body)
	}
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
	// A locked account answers 403 with Artifactory's message, whatever
	// password it sends (#475).
	for _, pw := range []string{"pw", "wrong"} {
		rec := do(t, h, http.MethodGet, secAPIKey, basic("user", pw), "")
		if rec.Code != http.StatusForbidden ||
			!strings.Contains(rec.Body.String(), `User is Locked.\nContact System Administrator`) {
			t.Fatalf("a locked user with %q: %d %s", pw, rec.Code, rec.Body)
		}
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

// TestAccAccessLockPolicyIsTheArtifactoryPolicies: the Access user lock policy
// and the Artifactory lock and expiration policies are one state, so a
// change through either API reads back through the other, and a lock limit
// set through Access locks users.
func TestAccAccessLockPolicyIsTheArtifactoryPolicies(t *testing.T) {
	const accessLock = "/access/api/v1/config/security/user_lock_policy"
	h := statusFixture(t, false)
	admin := jsonHeaders(basic("admin", "pw"))
	read := func(path string) string {
		t.Helper()
		rec := do(t, h, http.MethodGet, path, basic("admin", "pw"), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	write := func(path, body string) string {
		t.Helper()
		rec := do(t, h, http.MethodPut, path, admin, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s %s: %d %s", path, body, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	defaults := `{"adminPasswordExpirable":false,"attempts":0,"baseLoginDelayMillis":1000,` +
		`"clientLoginSuspensionEnabled":true,"maxLoginDelayIncorrectAttempts":2,` +
		`"maxLoginDelayMillis":60000,"passwordExpiryDays":0,"secondsToUnlock":1}` + "\n"
	if got := read(accessLock); got != defaults {
		t.Fatalf("Access defaults: %q", got)
	}

	// Through Access, read through Artifactory. Omitted fields are stored
	// as 0 or false, as Access does.
	sent := `{"attempts":2,"passwordExpiryDays":30,"secondsToUnlock":60}`
	stored := `{"adminPasswordExpirable":false,"attempts":2,"baseLoginDelayMillis":0,` +
		`"clientLoginSuspensionEnabled":false,"maxLoginDelayIncorrectAttempts":0,` +
		`"maxLoginDelayMillis":0,"passwordExpiryDays":30,"secondsToUnlock":60}` + "\n"
	if got := write(accessLock, sent); got != stored {
		t.Fatalf("stored: %q", got)
	}
	if got := read(secLockPol); got != "{\"enabled\":true,\"loginAttempts\":2}\n" {
		t.Fatalf("Artifactory lock policy: %q", got)
	}
	if got := read(
		secExpiry,
	); got != "{\"enabled\":true,\"notifyByEmail\":true,\"passwordMaxAge\":30}\n" {
		t.Fatalf("Artifactory expiration policy: %q", got)
	}

	// The limit set through Access locks a user after two failures. The
	// login suspension, which answers 403 too for a second from the second
	// failure, is off: the write above stored clientLoginSuspensionEnabled
	// false, as its omission means. Each answer is still checked for what
	// it says, so the suspension's 403 can never pass for the lock's, however
	// fast the probes come (#493).
	probe := func(password string) *httptest.ResponseRecorder {
		return do(t, h, http.MethodGet, secAPIKey, basic("user", password), "")
	}
	for range 2 {
		if rec := probe("wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("a wrong password: %d %s", rec.Code, rec.Body)
		}
	}
	// A locked account answers 403 with its own message, the right password
	// included (#475).
	if rec := probe("pw"); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), `User is Locked.\nContact System Administrator`) {
		t.Fatalf("the Access limit did not lock: %d %s", rec.Code, rec.Body)
	}

	// Through Artifactory, read through Access; the other fields stay.
	write(secLockPol, `{"enabled":true,"loginAttempts":7}`)
	write(secExpiry, `{"enabled":false,"passwordMaxAge":30}`)
	if got := read(accessLock); !strings.Contains(got, `"attempts":7,`) ||
		!strings.Contains(
			got,
			`"passwordExpiryDays":0,`,
		) || !strings.Contains(got, `"secondsToUnlock":60}`) {
		t.Fatalf("Access after Artifactory writes: %q", got)
	}

	// attempts 0 turns locking off, unlocks, and keeps the limit. The
	// suspension stays off, so the right password signs in at once after
	// the failures above.
	write(accessLock, `{}`)
	if got := read(secLockPol); got != "{\"enabled\":false,\"loginAttempts\":7}\n" {
		t.Fatalf("Artifactory after attempts 0: %q", got)
	}
	if rec := probe("pw"); rec.Code != http.StatusNotFound {
		t.Fatalf("turning locking off left the lock: %d %s", rec.Code, rec.Body)
	}
}

// TestAccLoginSuspension checks the temporary login suspension: from the
// second wrong password, every Basic login with that name answers 403 on
// either surface, the right password included. A name that does not exist
// is suspended alike, and an access token of the suspended user still
// works. The base delay is raised from its default second so that a slow
// run cannot outlast it, and the wait read back may have fallen by up to
// nine seconds, so a loaded -race run does not fail on the clock;
// internal/access/core/authn checks the delays themselves against a fake one.
func TestAccLoginSuspension(t *testing.T) {
	h := statusFixture(t, false)
	if rec := do(t, h, http.MethodPut, "/access/api/v1/config/security/user_lock_policy",
		jsonHeaders(basic("admin", "pw")),
		`{"clientLoginSuspensionEnabled":true,"maxLoginDelayIncorrectAttempts":2,`+
			`"baseLoginDelayMillis":30000,"maxLoginDelayMillis":60000}`); rec.Code != http.StatusOK {
		t.Fatalf("policy: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, http.MethodPost, "/artifactory/api/security/token",
		map[string]string{
			"Authorization": basic("user", "pw")["Authorization"],
			"Content-Type":  "application/x-www-form-urlencoded",
		}, "username=user")
	tok, _ := decodeObject(t, rec.Body.String())["access_token"].(string)
	if rec.Code != http.StatusOK || tok == "" {
		t.Fatalf("token: %d %s", rec.Code, rec.Body)
	}
	const message = `"message":"This request is blocked due to recurrent login failures, ` +
		`please try again in (30|2[1-9]) seconds"`
	suspended := map[string]*regexp.Regexp{
		secAPIKey: regexp.MustCompile(
			`^\{"errors":\[\{` + message + `,"status":403\}\]\}\n$`,
		),
		"/access/api/v2/users/x": regexp.MustCompile(
			`^\{"errors":\[\{"code":"FORBIDDEN",` + message + `\}\]\}\n$`,
		),
	}
	for _, name := range []string{"user", "ghost"} {
		for range 2 {
			if rec := do(t, h, http.MethodGet, secAPIKey, basic(name, "wrong"), ""); rec.Code !=
				http.StatusUnauthorized {
				t.Fatalf("%s, wrong password: %d %s", name, rec.Code, rec.Body)
			}
		}
		for path, body := range suspended {
			rec := do(t, h, http.MethodGet, path, basic(name, "pw"), "")
			if rec.Code != http.StatusForbidden || !body.MatchString(rec.Body.String()) {
				t.Fatalf("%s suspended, %s: %d %s", name, path, rec.Code, rec.Body)
			}
		}
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		secAPIKey,
		bearerAuth(tok),
		"",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("a token of the suspended user: %d %s", rec.Code, rec.Body)
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		secAPIKey,
		basic("admin", "pw"),
		"",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("another name: %d %s", rec.Code, rec.Body)
	}
}

// TestAccPasswordExpiry checks an expired password is refused with its own
// message, after the password verifies, and unexpiring restores access.
func TestAccPasswordExpiry(t *testing.T) {
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
	if rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "Password has expired") {
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

// TestAccLastAdmin keeps one administrator: the seeded admin cannot be
// demoted while it is the only one, and can once another exists.
func TestAccLastAdmin(t *testing.T) {
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

func TestUnitNormalizePolicyBodyPassesOtherDocuments(t *testing.T) {
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

// TestAccLegacyChangePassword follows a user whose password an administrator
// expired through the path form: every other request with it answers 403,
// the legacy change of its own password goes through with it, and the new
// password then works. The old password must verify for an administrator
// too.
func TestAccLegacyChangePassword(t *testing.T) {
	h := statusFixture(t, false)
	admin := jsonHeaders(basic("admin", "pw"))
	mustStatus(t, do(t, h, http.MethodPut, secUsers+"/dora", admin,
		`{"name":"dora","email":"d@example.com","password":"Dora-pass1"}`), 201, "create dora")
	rec := do(t, h, http.MethodPost, secExpire+"/dora", basic("admin", "pw"), "")
	if rec.Code != http.StatusOK || rec.Body.String() != "dora's credentials have been expired" {
		t.Fatalf("expire dora: %d %q", rec.Code, rec.Body)
	}
	old := jsonHeaders(basic("dora", "Dora-pass1"))
	rec = do(t, h, http.MethodGet, secAPIKey, old, "")
	if rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "Password has expired") {
		t.Fatalf("an expired password: %d %s", rec.Code, rec.Body)
	}
	mustStatus(t, do(t, h, http.MethodPost, secChangePw, old,
		changeBody("user", "pw", "User-pass2")), 403, "an expired password changing another's")
	rec = do(
		t,
		h,
		http.MethodPost,
		secChangePw,
		old,
		changeBody("dora", "Dora-pass1", "Dora-pass2"),
	)
	if rec.Code != http.StatusOK || rec.Body.String() != "Password has been successfully changed" {
		t.Fatalf("change an expired password: %d %q", rec.Code, rec.Body)
	}
	mustStatus(t, do(t, h, http.MethodGet, secAPIKey, basic("dora", "Dora-pass2"), ""),
		404, "the new password")
	mustStatus(t, do(t, h, http.MethodGet, secAPIKey, basic("dora", "Dora-pass1"), ""),
		401, "the old password")

	rec = do(t, h, http.MethodPost, secChangePw, admin, changeBody("dora", "Dora-pass1", "Dora-3"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "old password") {
		t.Fatalf("an administrator with a wrong old password: %d %s", rec.Code, rec.Body)
	}
	rec = do(
		t,
		h,
		http.MethodPost,
		secChangePw,
		admin,
		`{"userName":"dora","oldPassword":"Dora-pass2","newPassword1":"Dora-3","newPassword2":"Dora-4"}`,
	)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "do not match") {
		t.Fatalf("a mismatch: %d %s", rec.Code, rec.Body)
	}
	mustStatus(t, do(t, h, http.MethodPost, secChangePw, admin,
		changeBody("dora", "Dora-pass2", "Dora-pass3")), 200, "an administrator's change")
	mustStatus(t, do(t, h, http.MethodGet, secAPIKey, basic("dora", "Dora-pass3"), ""),
		404, "the administrator's new password")
}

// TestAccLegacyExpireEveryPassword expires every password, the caller's
// included: the expired administrator may then change its own password and
// do nothing else, not even with its administrator rights.
func TestAccLegacyExpireEveryPassword(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(t, h, http.MethodPost, secExpire+"ForAllUsers", basic("admin", "pw"), "")
	if rec.Code != http.StatusOK ||
		rec.Body.String() != "Credentials have been successfully expired for all users" {
		t.Fatalf("expire every password: %d %q", rec.Code, rec.Body)
	}
	for _, u := range []string{"admin", "user"} {
		mustStatus(t, do(t, h, http.MethodGet, secAPIKey, basic(u, "pw"), ""),
			403, u+"'s expired password")
	}
	admin := jsonHeaders(basic("admin", "pw"))
	mustStatus(t, do(t, h, http.MethodPost, secChangePw, admin,
		changeBody("user", "pw", "User-pass2")), 403, "an expired administrator changing another's")
	mustStatus(t, do(t, h, http.MethodPost, secExpire+"/user", admin, ""),
		403, "an expired administrator's other request")
	mustStatus(t, do(t, h, http.MethodPost, secChangePw, admin,
		changeBody("admin", "pw", "Admin-pass2")), 200, "an expired administrator's own change")
	mustStatus(t, do(t, h, http.MethodGet, secUsers, basic("admin", "Admin-pass2"), ""),
		200, "the administrator's new password")
}

// TestAccPlusInSecurityNames checks that a "+" in a user, group or permission
// name in the path stands for a space, in the legacy paths, the v2
// permission targets and their sub-paths, while %2B stays a plus sign: a
// name with a plus can be created and read, and every listing's detail URL
// leads back to its entry.
func TestAccPlusInSecurityNames(t *testing.T) {
	h := statusFixture(t, false)
	admin := jsonHeaders(basic("admin", "pw"))
	read := func(path, want string) {
		t.Helper()
		rec := do(t, h, http.MethodGet, path, basic("admin", "pw"), "")
		mustStatus(t, rec, 200, path)
		if got := decodeObject(t, rec.Body.String())["name"]; got != want {
			t.Fatalf("%s: name %v, want %q", path, got, want)
		}
	}
	for _, path := range []string{
		permV1 + "/Any+Remote", permV1 + "/Any%20Remote", permV2 + "/Any+Remote",
	} {
		read(path, "Any Remote")
	}

	mustStatus(t, do(t, h, http.MethodPut, secUsers+"/ann+lee", admin,
		`{"name":"ann lee","email":"a@example.com","password":"Ann-pass1"}`), 201, "create ann lee")
	read(secUsers+"/ann%20lee", "ann lee")
	mustStatus(t, do(t, h, http.MethodPut, secUsers+"/bo%2Bb", admin,
		`{"name":"bo+b","email":"b@example.com","password":"Bob-pass1"}`), 201, "create bo+b")
	read(secUsers+"/bo%2Bb", "bo+b")
	mustStatus(t, do(t, h, http.MethodGet, secUsers+"/bo+b", basic("admin", "pw"), ""),
		404, "bo+b spelled with a bare plus")
	mustStatus(t, do(t, h, http.MethodPut, secGroups+"/dev+ops", admin,
		`{"name":"dev ops","userNames":["ann lee","bo+b"]}`), 201, "create dev ops")
	read(secGroups+"/dev%20ops", "dev ops")
	mustStatus(t, do(t, h, http.MethodPut, secGroups+"/c%2B%2B", admin,
		`{"name":"c++"}`), 201, "create c++")
	read(secGroups+"/c%2B%2B", "c++")
	mustStatus(t, do(t, h, http.MethodPut, permV1+"/ann+perm", admin,
		`{"repositories":["perm-local"],"principals":{"users":{"ann lee":["r"]},`+
			`"groups":{"dev ops":["r"]}}}`), 200, "create ann perm")
	read(permV2+"/ann%20perm", "ann perm")
	mustStatus(t, do(t, h, http.MethodPut, permV2+"/x%2By", admin,
		`{"repo":{"repositories":["perm-local"],"actions":{"users":{"bo+b":["read"]}}}}`),
		200, "create x+y")
	read(permV1+"/x%2By", "x+y")

	// Every listing's detail URL leads back to the entry it lists.
	for _, list := range []string{secUsers, secGroups, permV1, permV2} {
		rec := do(t, h, http.MethodGet, list, basic("admin", "pw"), "")
		for _, it := range decodeInto[[]map[string]string](t, rec.Body.String()) {
			read(it["uri"], it["name"])
		}
	}

	// The sub-paths take the plus too.
	for _, path := range []string{permV2 + "/users/ann+lee", permV2 + "/groups/dev+ops"} {
		rec := do(t, h, http.MethodGet, path, basic("admin", "pw"), "")
		mustStatus(t, rec, 200, path)
		if !strings.Contains(rec.Body.String(), `"ann perm"`) {
			t.Fatalf("%s: %s", path, rec.Body)
		}
	}
	mustStatus(t, do(t, h, http.MethodPost, secExpire+"/ann+lee", basic("admin", "pw"), ""),
		200, "expire ann lee")
	mustStatus(t, do(t, h, http.MethodGet, secAPIKey, basic("ann lee", "Ann-pass1"), ""),
		403, "ann lee expired")
	mustStatus(t, do(t, h, http.MethodPost, secUnexpire+"ann+lee", basic("admin", "pw"), ""),
		200, "unexpire ann lee")
	mustStatus(
		t,
		do(t, h, http.MethodPost, secUsers+"/ann+lee", admin, `{"email":"l@example.com"}`),
		200,
		"update ann lee",
	)
	mustStatus(t, do(t, h, http.MethodPost, secGroups+"/dev+ops", admin, `{"description":"d"}`),
		200, "update dev ops")
	for _, path := range []string{
		permV1 + "/ann+perm", permV2 + "/x%2By", secGroups + "/dev+ops", secUsers + "/ann+lee",
	} {
		mustStatus(t, do(t, h, http.MethodDelete, path, basic("admin", "pw"), ""), 200, path)
		mustStatus(t, do(t, h, http.MethodGet, path, basic("admin", "pw"), ""), 404, path)
	}
}

// TestAccKeyEncryption: POST /api/system/encrypt and /api/system/decrypt
// answer an administrator 200 text/plain "DONE", as 7.x's SystemResource
// does, and change nothing, since secrets stay sealed (decision 0017). A
// user who is not an administrator gets 403 and a caller without
// credentials 401 with the Basic challenge, whether anonymous access is on
// or off (#1385).
func TestAccKeyEncryption(t *testing.T) {
	const (
		forbidden = `{"errors":[{"message":"Forbidden","status":403}]}` + "\n"
		required  = `{"errors":[{"message":"Authentication is required","status":401}]}` + "\n"
	)
	for _, anonymous := range []bool{false, true} {
		h := statusFixture(t, anonymous)
		for _, path := range []string{"/artifactory/api/system/encrypt", "/artifactory/api/system/decrypt"} {
			for _, tc := range []struct {
				who                     string
				headers                 map[string]string
				status                  int
				contentType, body, auth string
			}{
				{"admin", basic("admin", "pw"), http.StatusOK, "text/plain", "DONE", ""},
				{"user", basic("user", "pw"), http.StatusForbidden, "application/json", forbidden, ""},
				{"no credentials", nil, http.StatusUnauthorized, "application/json", required, apierror.BasicChallenge},
			} {
				rec := do(t, h, http.MethodPost, path, tc.headers, "")
				if rec.Code != tc.status || rec.Body.String() != tc.body ||
					rec.Header().Get("Content-Type") != tc.contentType ||
					rec.Header().Get("WWW-Authenticate") != tc.auth {
					t.Errorf(
						"%s as %s (anonymous access %v): %d %q %q %q, want %d %q %q %q",
						path,
						tc.who,
						anonymous,
						rec.Code,
						rec.Header().Get("Content-Type"),
						rec.Body,
						rec.Header().
							Get("WWW-Authenticate"),
						tc.status,
						tc.contentType,
						tc.body,
						tc.auth,
					)
				}
			}
		}
	}
}
