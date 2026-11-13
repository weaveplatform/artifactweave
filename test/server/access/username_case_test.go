package access_test

// This file holds the tests of how user names are matched (#1432) and how a
// user request naming an unknown group, or a group request naming an unknown
// user, is answered (#1433). Each runs over the in-memory store, SQLite and
// PostgreSQL, since the stores word their errors differently and only the
// identity service may word the answer.

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/postgres"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/sqlite"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	pgdrv "github.com/weaveplatform/artifactweave/internal/platform/sql/postgres"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// userStore is what the user name tests build a router over: any of the
// three backends.
type userStore interface {
	harness.Store
	support.SeedStore
}

// eachUserStore runs test against a router over each backend, seeded with
// the admin (password "pw") and the auto-join group readers, with access
// tokens, Basic authentication with a token, and API restrictions on.
func eachUserStore(t *testing.T, test func(t *testing.T, h http.Handler)) {
	t.Helper()
	for _, backend := range []string{"inmem", "sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) { test(t, userStoreRouter(t, backend)) })
	}
}

func userStoreRouter(t *testing.T, backend string) http.Handler {
	t.Helper()
	ctx := t.Context()
	var db userStore
	switch backend {
	case "inmem":
		db = inmem.New()
	case "sqlite":
		s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "users.db"), sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		db = s
	default:
		s, err := postgres.Open(ctx, platsqltest.PostgresDSN(t), postgres.Options{MaxOpenConns: 8})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		appstoretest.Reset(t, s.DB(), pgdrv.Dialect)
		db = s
	}
	support.Seed(t, db)
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
		Hash: testHash, Tokens: tokens, APIRestrictions: db.APIRestrictions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{
		CompatVersion: "7.155.0", Store: db, Auth: a, PasswordHash: testHash,
		Tokens: tokens, AllowBasicAuthTokens: true,
	})
}

// userStep is one request of the user name tests, with its credentials, and
// the exact answer it gets.
type userStep struct {
	creds              map[string]string
	method, path, body string
	want               int
	wantBody           string
}

// runUserSteps sends each step, a body as JSON, and checks the status and
// the whole body.
func runUserSteps(t *testing.T, h http.Handler, steps []userStep) {
	t.Helper()
	for _, s := range steps {
		headers := s.creds
		if s.body != "" {
			headers = jsonAs(s.creds)
		}
		rec := do(t, h, s.method, s.path, headers, s.body)
		if got := strings.TrimSpace(rec.Body.String()); rec.Code != s.want || got != s.wantBody {
			t.Fatalf("%s %s %s: %d %s, want %d %s", s.method, s.path, s.body, rec.Code, got,
				s.want, s.wantBody)
		}
	}
}

// v2User is the Access v2 document of a user who has not logged in.
func v2User(name, email string, groups ...string) string {
	return `{"admin":false,"disable_ui_access":false,"effective_admin":false,"email":"` + email +
		`","groups":[` + quoted(groups) + `],"internal_password_disabled":false,` +
		`"last_logged_in":null,"manage_webhook":false,"platform_auditor":false,` +
		`"policy_manager":false,"policy_viewer":false,"profile_updatable":true,` +
		`"realm":"internal","reports_manager":false,"resources_manager":false,` +
		`"status":"enabled","username":"` + name + `","watch_manager":false}`
}

// legacyUser is the legacy document of a user.
func legacyUser(name, email string, groups ...string) string {
	return `{"admin":false,"disableUIAccess":false,"email":"` + email + `","groups":[` +
		quoted(groups) + `],"internalPasswordDisabled":false,"mfaStatus":"NONE","name":"` +
		name + `","policyManager":false,"policyViewer":false,"profileUpdatable":true,` +
		`"realm":"internal","reportsManager":false,"status":"ENABLED","watchManager":false}`
}

// legacyUsers is the legacy user list of the seeded users and names.
func legacyUsers(names ...string) string {
	out := make([]string, 0, len(names)+2)
	for _, n := range append([]string{"admin", "anonymous"}, names...) {
		out = append(out, `{"name":"`+n+`","realm":"internal",`+
			`"uri":"http://example.com/artifactory/api/security/users/`+n+`"}`)
	}
	return "[" + strings.Join(out, ",") + "]"
}

func quoted(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return `"` + strings.Join(names, `","`) + `"`
}

func v2Error(code, message string) string {
	return `{"errors":[{"code":"` + code + `","message":"` + message + `"}]}`
}

func legacyError(status int, message string) string {
	return fmt.Sprintf(`{"errors":[{"message":"%s","status":%d}]}`, message, status)
}

const (
	v2Users  = "/access/api/v2/users"
	signedIn = `{"errors":[{"message":"No API key exists: API keys are retired","status":404}]}`
)

// TestAccUsernamesFoldCase creates Bob through Access v2 and Dan through the
// legacy API, then names them in other cases on every route that takes a
// user name (#1432). 7.x matches user names without regard to case and
// stores them in lower case: JFrog's SCIM page says "User names are
// case-insensitive", Get Permissions matches "Usernames ...
// case-insensitively, group names exactly", and RTFACT-29660 (7.55.13)
// says a user created "with capitalization" is stored lower-case. So BOB
// is bob on every route, and both creates store the lower-case name.
// Which spelling a found user answers with (the stored one), and that the
// legacy PUT of DAN replaces dan, no source states [I].
func TestAccUsernamesFoldCase(t *testing.T) {
	eachUserStore(t, func(t *testing.T, h http.Handler) {
		bobPerm := `{"name":"bob-perm","repo":{"actions":{"users":{"bob":["read"]}},` +
			`"exclude-patterns":[],"include-patterns":["**"],"repositories":["ANY LOCAL"]}}`
		runUserSteps(t, h, []userStep{
			{
				admin, http.MethodPost, v2Users,
				`{"username":"Bob","email":"bob@example.com","password":"Secret-1"}`,
				http.StatusCreated, v2User("bob", "bob@example.com", "readers"),
			},
			{
				admin, http.MethodGet, v2Users + "/BOB", "", http.StatusOK,
				v2User("bob", "bob@example.com", "readers"),
			},
			{
				admin, http.MethodGet, secUsers + "/BOB", "", http.StatusOK,
				legacyUser("bob", "bob@example.com", "readers"),
			},
			// Kept: BOB is the user bob, so creating it again conflicts.
			{
				admin, http.MethodPost, v2Users,
				`{"username":"BOB","email":"bob@example.com","password":"Secret-1"}`,
				http.StatusConflict, v2Error("CONFLICT", "User 'bob' already exists"),
			},
			{
				admin, http.MethodPatch, v2Users + "/BOB", `{"email":"b2@example.com"}`,
				http.StatusOK, v2User("bob", "b2@example.com", "readers"),
			},

			{
				admin, http.MethodPut, secUsers + "/Dan",
				`{"name":"Dan","email":"dan@example.com","password":"Secret-1"}`,
				http.StatusCreated, "",
			},
			{
				admin, http.MethodGet, secUsers + "/dan", "", http.StatusOK,
				legacyUser("dan", "dan@example.com", "readers"),
			},
			{
				admin, http.MethodGet, v2Users + "/DAN", "", http.StatusOK,
				v2User("dan", "dan@example.com", "readers"),
			},
			// The body's name names the path's user in another case too.
			{
				admin,
				http.MethodPut,
				secUsers + "/DAN",
				`{"name":"dan","email":"dan2@example.com","password":"Secret-2","groups":["readers"]}`,
				http.StatusCreated,
				"",
			},
			{
				admin, http.MethodPost, secUsers + "/DAN", `{"email":"dan3@example.com"}`,
				http.StatusOK, "",
			},
			{
				admin, http.MethodGet, secUsers + "/Dan", "", http.StatusOK,
				legacyUser("dan", "dan3@example.com", "readers"),
			},
			{admin, http.MethodGet, secUsers, "", http.StatusOK, legacyUsers("bob", "dan")},

			// Memberships name users in any case, and hold the stored name.
			{
				admin, http.MethodPut, secGroups + "/crew",
				`{"name":"crew","userNames":["BOB","Dan"]}`, http.StatusCreated, "",
			},
			{
				admin, http.MethodGet, secGroups + "/crew?includeUsers=true", "", http.StatusOK,
				`{"adminPrivileges":false,"autoJoin":false,"name":"crew","policyManager":false,` +
					`"realm":"internal","reportsManager":false,"userNames":["bob","dan"],` +
					`"watchManager":false}`,
			},
			{
				admin, http.MethodPost, v2Groups, `{"name":"team","members":["BOB"]}`,
				http.StatusCreated, strings.Replace(v2GroupDoc("team"), `"members":[]`,
					`"members":["bob"]`, 1),
			},
			{
				admin, http.MethodPatch, v2Groups + "/team/members",
				`{"add":["DAN"],"remove":["BOB"]}`, http.StatusOK, `{"members":["dan"]}`,
			},
			{
				admin, http.MethodPatch, v2Users + "/BOB/groups",
				`{"add":["team"],"remove":["crew"]}`, http.StatusOK,
				`{"groups":["readers","team"]}`,
			},

			// A grant names its user in any case and is stored under the
			// stored name.
			{
				admin, http.MethodPut, permV2 + "/bob-perm",
				strings.Replace(bobPerm, `"bob"`, `"BOB"`, 1), http.StatusOK, "",
			},
			{admin, http.MethodGet, permV2 + "/bob-perm", "", http.StatusOK, bobPerm},
			{admin, http.MethodGet, permV2 + "/users/BOB", "", http.StatusOK, "[" + bobPerm + "]"},

			// BOB logs in as bob, with a password, and changes its own
			// password through either API.
			{
				basic("BOB", "Secret-1"),
				http.MethodGet,
				secAPIKey,
				"",
				http.StatusNotFound,
				signedIn,
			},
			{
				basic("Bob", "Secret-1"), http.MethodPut, v2Users + "/BOB/password",
				`{"password":"Secret-3"}`, http.StatusNoContent, "",
			},
			{
				basic("bob", "Secret-3"), http.MethodPost, secChangePw,
				`{"userName":"BOB","oldPassword":"Secret-3","newPassword1":"Secret-4",` +
					`"newPassword2":"Secret-4"}`, http.StatusOK,
				"Password has been successfully changed",
			},

			// Password expiry and unlocks name the user in any case; the
			// text answers repeat the request's spelling.
			{
				admin, http.MethodPost, secExpire + "/BOB", "", http.StatusOK,
				"BOB's credentials have been expired",
			},
			{
				basic("bob", "Secret-4"), http.MethodGet, secAPIKey, "", http.StatusForbidden,
				legacyError(http.StatusForbidden, "Password has expired"),
			},
			{
				admin, http.MethodPost, secUnexpire + "BOB", "", http.StatusOK,
				"The password of user 'BOB' is no longer expired",
			},
			{
				basic("bob", "Secret-4"),
				http.MethodGet,
				secAPIKey,
				"",
				http.StatusNotFound,
				signedIn,
			},
			{
				admin,
				http.MethodPost,
				v2Users + "/BOB/password/expire",
				"",
				http.StatusNoContent,
				"",
			},
			{
				basic("bob", "Secret-4"), http.MethodGet, secAPIKey, "", http.StatusForbidden,
				legacyError(http.StatusForbidden, "Password has expired"),
			},
			{
				admin, http.MethodPut, v2Users + "/BOB/password", `{"password":"Secret-5"}`,
				http.StatusNoContent, "",
			},
			{
				basic("BOB", "Secret-5"),
				http.MethodGet,
				secAPIKey,
				"",
				http.StatusNotFound,
				signedIn,
			},
			{
				admin, http.MethodPost, secExpire, `["BOB","ghost"]`, http.StatusOK,
				`{"message":"The passwords were expired; unknown users were skipped: ghost",` +
					`"status":"success"}`,
			},
			{
				basic("bob", "Secret-5"), http.MethodGet, secAPIKey, "", http.StatusForbidden,
				legacyError(http.StatusForbidden, "Password has expired"),
			},
			{admin, http.MethodPost, v2Users + "/BOB/unlock", "", http.StatusNoContent, ""},
			{
				admin, http.MethodPost, "/artifactory/api/security/unlockUsers/BOB", "",
				http.StatusOK, "User 'BOB' was unlocked",
			},
			{
				admin, http.MethodDelete, "/artifactory/api/security/apiKey/BOB", "", http.StatusOK,
				`{"info":"No API key was revoked: API keys are retired, use access tokens"}`,
			},

			// API restrictions name the user in any case, and hold whichever
			// spelling the user logs in with.
			{
				admin, http.MethodGet, apiRestrictionPath + "ANONYMOUS", "", http.StatusBadRequest,
				legacyError(http.StatusBadRequest, "A managed user name is required"),
			},
			{
				admin, http.MethodGet, apiRestrictionPath + "BOB", "", http.StatusOK,
				`{"active":false,"actor":"","reason":"","sequence":0,"user":"bob"}`,
			},

			// The built-in admin is protected in any case, with the answers
			// admin gets on each API.
			{
				admin, http.MethodDelete, v2Users + "/ADMIN", "", http.StatusForbidden,
				v2Error("FORBIDDEN", `the built-in user \"admin\" cannot be deleted`),
			},
			{
				admin, http.MethodDelete, secUsers + "/ADMIN", "", http.StatusBadRequest,
				legacyError(http.StatusBadRequest,
					`identity: protected: the built-in user \"admin\" cannot be deleted`),
			},

			{
				admin, http.MethodDelete, secUsers + "/DAN", "", http.StatusOK,
				"User 'DAN' has been removed successfully.",
			},
			{admin, http.MethodDelete, v2Users + "/BOB", "", http.StatusNoContent, ""},
			{admin, http.MethodGet, secUsers, "", http.StatusOK, legacyUsers()},
		})
	})
}

// TestAccUsernameRestrictionFoldsCase sets an API restriction on BOB and
// holds bob to it, whatever case bob logs in with, and clears it through Bob
// (#1432).
func TestAccUsernameRestrictionFoldsCase(t *testing.T) {
	eachUserStore(t, func(t *testing.T, h http.Handler) {
		until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		runUserSteps(t, h, []userStep{
			{
				admin, http.MethodPost, v2Users,
				`{"username":"bob","email":"bob@example.com","password":"Secret-1"}`,
				http.StatusCreated, v2User("bob", "bob@example.com", "readers"),
			},
		})
		set := do(t, h, http.MethodPut, apiRestrictionPath+"BOB", jsonAs(admin),
			`{"until":"`+until.Format(time.RFC3339)+`","reason":"Stop polling"}`)
		if set.Code != http.StatusOK || !strings.Contains(set.Body.String(), `"user":"bob"`) {
			t.Fatalf("set: %d %s", set.Code, set.Body)
		}
		restricted := legacyError(http.StatusForbidden, "API access is temporarily restricted")
		runUserSteps(t, h, []userStep{
			{
				basic("bob", "Secret-1"),
				http.MethodGet,
				secAPIKey,
				"",
				http.StatusForbidden,
				restricted,
			},
			{
				basic("BOB", "Secret-1"),
				http.MethodGet,
				secAPIKey,
				"",
				http.StatusForbidden,
				restricted,
			},
			{admin, http.MethodDelete, apiRestrictionPath + "Bob", "", http.StatusNoContent, ""},
			{
				basic("BOB", "Secret-1"),
				http.MethodGet,
				secAPIKey,
				"",
				http.StatusNotFound,
				signedIn,
			},
		})
		audit := do(t, h, http.MethodGet, apiRestrictionAuditPath+"?user=BOB", admin, "")
		if audit.Code != http.StatusOK || strings.Count(audit.Body.String(), `"user":"bob"`) != 2 {
			t.Fatalf("audit: %d %s", audit.Code, audit.Body)
		}
	})
}

// TestAccUsernameLockFoldsCase locks bob after failed logins as BOB and Bob,
// and unlocks bob through BOB (#1432). RTFACT-29660 reports 7.55.13 counting
// TEST_user's failures apart from test_user's, and unlockUsers with TEST_user
// doing nothing, against 7.x's own rule that user names are
// case-insensitive; that bug is not followed.
func TestAccUsernameLockFoldsCase(t *testing.T) {
	eachUserStore(t, func(t *testing.T, h http.Handler) {
		locked := legacyError(
			http.StatusForbidden,
			`User is Locked.\nContact System Administrator to Unlock The Account.`,
		)
		runUserSteps(t, h, []userStep{
			{
				admin, http.MethodPost, v2Users,
				`{"username":"Bob","email":"bob@example.com","password":"Secret-1"}`,
				http.StatusCreated, v2User("bob", "bob@example.com", "readers"),
			},
			{
				admin, http.MethodPut, "/access/api/v1/config/security/user_lock_policy",
				`{"clientLoginSuspensionEnabled":false}`, http.StatusOK,
				`{"adminPasswordExpirable":false,"attempts":0,"baseLoginDelayMillis":0,` +
					`"clientLoginSuspensionEnabled":false,"maxLoginDelayIncorrectAttempts":0,` +
					`"maxLoginDelayMillis":0,"passwordExpiryDays":0,"secondsToUnlock":0}`,
			},
			{
				admin, http.MethodPut, secLockPol, `{"enabled":true,"loginAttempts":2}`,
				http.StatusOK, "The user lock policy was updated",
			},
		})
		for _, name := range []string{"BOB", "Bob"} {
			if rec := do(t, h, http.MethodGet, secAPIKey, basic(name, "wrong"), ""); rec.Code !=
				http.StatusUnauthorized {
				t.Fatalf("wrong password as %s: %d %s", name, rec.Code, rec.Body)
			}
		}
		runUserSteps(t, h, []userStep{
			{basic("bob", "Secret-1"), http.MethodGet, secAPIKey, "", http.StatusForbidden, locked},
			{
				admin, http.MethodGet, "/artifactory/api/security/lockedUsers", "", http.StatusOK,
				`["bob"]`,
			},
			{
				admin, http.MethodPost, "/artifactory/api/security/unlockUsers", `["BOB"]`,
				http.StatusOK, "The users were unlocked",
			},
			{
				basic("bob", "Secret-1"),
				http.MethodGet,
				secAPIKey,
				"",
				http.StatusNotFound,
				signedIn,
			},
			{
				admin, http.MethodGet, "/artifactory/api/security/lockedUsers", "", http.StatusOK,
				`[]`,
			},
		})
	})
}

// TestAccUsernameTokensFoldCase mints a token for BOB, which is bob's, and
// logs in with it under Basic authentication as BOB (#1432). RTFACT-10141
// made 7.17.2 accept an API key or encrypted password with the user name in
// upper case; RTFACT-23454 reports an access token refused the same way
// ("Token principal mismatch"), which is taken as the same bug.
func TestAccUsernameTokensFoldCase(t *testing.T) {
	eachUserStore(t, func(t *testing.T, h http.Handler) {
		runUserSteps(t, h, []userStep{
			{
				admin, http.MethodPost, v2Users,
				`{"username":"bob","email":"bob@example.com","password":"Secret-1"}`,
				http.StatusCreated, v2User("bob", "bob@example.com", "readers"),
			},
		})
		created := mintAdmin(t, h, `{"username":"BOB","scope":"applied-permissions/user",`+
			`"expires_in":86400,"refreshable":true,"include_reference_token":true}`)
		id, _ := created["token_id"].(string)
		reference, _ := created["reference_token"].(string)
		access, _ := created["access_token"].(string)
		refresh, _ := created["refresh_token"].(string)
		rec := do(t, h, http.MethodGet, "/access/api/v1/tokens/"+id, admin, "")
		got := decodeObject(t, rec.Body.String())
		if subject, _ := got["subject"].(string); rec.Code != http.StatusOK ||
			!strings.HasSuffix(subject, "/users/bob") {
			t.Fatalf("token for BOB: %d %s", rec.Code, rec.Body)
		}
		runUserSteps(t, h, []userStep{
			{basic("BOB", reference), http.MethodGet, secAPIKey, "", http.StatusNotFound, signedIn},
			{basic("Bob", access), http.MethodGet, secAPIKey, "", http.StatusNotFound, signedIn},
		})
		// bob names itself BOB, which is no other user.
		own := do(t, h, http.MethodPost, "/access/api/v1/tokens", jsonAs(basic("bob", "Secret-1")),
			`{"username":"BOB","expires_in":3600}`)
		ownToken, _ := decodeObject(t, own.Body.String())["access_token"].(string)
		if own.Code != http.StatusOK || ownToken == "" {
			t.Fatalf("bob mints for BOB: %d %s", own.Code, own.Body)
		}
		runUserSteps(t, h, []userStep{
			{bearerAuth(ownToken), http.MethodGet, secAPIKey, "", http.StatusNotFound, signedIn},
			{
				basic("Bob", "Secret-1"), http.MethodPost, "/access/api/v1/tokens",
				`{"username":"admin"}`, http.StatusForbidden,
				v2Error("FORBIDDEN", "Only an administrator can create tokens for other users"),
			},
		})
		// npm's login checks the password as a Basic login does, and answers
		// for the stored user.
		runUserSteps(t, h, []userStep{
			{
				admin, http.MethodPut, "/artifactory/api/repositories/npm-local",
				`{"key":"npm-local","rclass":"local","packageType":"npm"}`, http.StatusOK,
				"Successfully created repository 'npm-local'",
			},
		})
		login := do(t, h, http.MethodPut,
			"/artifactory/api/npm/npm-local/-/user/org.couchdb.user:BOB", jsonAs(nil),
			`{"name":"BOB","password":"Secret-1"}`)
		if got := decodeObject(t, login.Body.String()); login.Code != http.StatusCreated ||
			got["id"] != "org.couchdb.user:bob" {
			t.Fatalf("npm login as BOB: %d %s", login.Code, login.Body)
		}
		// BOB refreshes bob's token: the authenticated user is the token's.
		grant := url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {refresh},
		}.Encode()
		if rec := do(t, h, http.MethodPost, "/access/api/v1/tokens",
			formAs(basic("BOB", "Secret-1")), grant); rec.Code != http.StatusOK {
			t.Fatalf("refresh as BOB: %d %s", rec.Code, rec.Body)
		}
	})
}

// TestAccUnknownGroupOrMember names a group that does not exist in a user
// request, and a user that does not exist in a legacy group request, on
// every store (#1433). The identity service words the 400, the same on every
// backend, as the permission routes do. Add or Remove User from Groups
// documents 400 "Bad Request - Invalid input" and Create a User only 400
// for an invalid object; the message is not documented [I]. The Access v2
// group routes keep their documented 404 for an unknown member.
func TestAccUnknownGroupOrMember(t *testing.T) {
	eachUserStore(t, func(t *testing.T, h http.Handler) {
		noGroup := "Group 'nope' does not exist"
		noUser := "User 'ghost' does not exist"
		runUserSteps(t, h, []userStep{
			{
				admin, http.MethodPost, v2Users,
				`{"username":"bob","email":"bob@example.com","password":"Secret-1"}`,
				http.StatusCreated, v2User("bob", "bob@example.com", "readers"),
			},
			{
				admin,
				http.MethodPost,
				v2Users,
				`{"username":"eve","email":"e@example.com","password":"Secret-1","groups":["nope"]}`,
				http.StatusBadRequest,
				v2Error("BAD_REQUEST", noGroup),
			},
			{
				admin, http.MethodGet, v2Users + "/eve", "", http.StatusNotFound,
				v2Error("NOT_FOUND", "User 'eve' does not exist"),
			},
			{
				admin, http.MethodPatch, v2Users + "/bob/groups", `{"add":["readers","nope"]}`,
				http.StatusBadRequest, v2Error("BAD_REQUEST", noGroup),
			},
			{
				admin, http.MethodPut, secUsers + "/eve",
				`{"name":"eve","email":"e@example.com","password":"Secret-1","groups":["nope"]}`,
				http.StatusBadRequest, legacyError(http.StatusBadRequest, noGroup),
			},
			{
				admin, http.MethodPost, secUsers + "/bob", `{"groups":["nope"]}`,
				http.StatusBadRequest, legacyError(http.StatusBadRequest, noGroup),
			},
			{admin, http.MethodGet, secUsers, "", http.StatusOK, legacyUsers("bob")},
			{
				admin, http.MethodPut, secGroups + "/gang", `{"name":"gang","userNames":["ghost"]}`,
				http.StatusBadRequest, legacyError(http.StatusBadRequest, noUser),
			},
			{
				admin, http.MethodGet, secGroups + "/gang", "", http.StatusNotFound,
				legacyGroupMissing("gang"),
			},
			{
				admin, http.MethodPost, secGroups + "/readers", `{"userNames":["bob","ghost"]}`,
				http.StatusBadRequest, legacyError(http.StatusBadRequest, noUser),
			},
			// Kept: the Access v2 group routes answer an unknown member 404.
			{
				admin, http.MethodPost, v2Groups, `{"name":"gang","members":["ghost"]}`,
				http.StatusNotFound,
				v2Error("NOT_FOUND", "One or more users in members were not found"),
			},
			{
				admin, http.MethodPatch, v2Groups + "/readers/members", `{"add":["ghost"]}`,
				http.StatusNotFound, v2Error("NOT_FOUND", "A user to add does not exist"),
			},
			{
				admin, http.MethodGet, v2Users + "/bob", "", http.StatusOK,
				v2User("bob", "bob@example.com", "readers"),
			},
		})
	})
}
