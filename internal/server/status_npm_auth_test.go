package server

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	// npmSearch is npm search (#291).
	npmSearch = "GET " + npmBase + "{repoKey}/-/v1/search"
	// npm's legacy login and logout (#292), which take no credentials of
	// their own.
	npmLogin  = "PUT " + npmBase + "{repoKey}/-/user/org.couchdb.user:{user}"
	npmLogout = "DELETE " + npmBase + "{repoKey}/-/user/token/{token}"
	// The JFrog operations that answer npm's .npmrc lines (#292).
	npmAuth      = "GET " + npmBase + "auth"
	npmScopeAuth = "GET " + npmBase + "{repoKey}/auth/{scope}"
	// npmWebLogin is npm's web login, which #262 serves; until then npm
	// falls back to the legacy login on its 4xx.
	npmWebLogin = "POST " + npmBase + "{repoKey}/-/v1/login"
)

// npmLoginBody is the body npm login sends for name with password.
func npmLoginBody(name, password string) string {
	return `{"_id":"org.couchdb.user:` + name + `","name":"` + name + `","password":"` +
		password + `","type":"user","roles":[],"date":"2026-10-19T09:00:00.000Z"}`
}

// npmAuthStatusPart is npm search's and npm authentication's share of the
// status table (#291, #292).
var npmAuthStatusPart = statusPart{
	handwritten: map[string]bool{npmSearch: true, npmLogin: true, npmLogout: true},
	generated:   map[string]bool{npmAuth: true, npmScopeAuth: true},
	rows: func() []statusCase {
		local := npmBase + "npm-local/"
		search := local + "-/v1/search?text=status&size=20&from=0"
		login := local + "-/user/org.couchdb.user:"
		return []statusCase{
			{npmSearch, search, "admin", "", 200},
			// What the caller may not read is left out, not refused.
			{npmSearch, search, "user", "", 200},
			{npmSearch, search, "anonymous", "", 200},
			{npmSearch, search, "", "", 401},
			{npmSearch, npmBase + "missing/-/v1/search", "admin", "", 404},
			{npmSearch, npmBase + "libs-local/-/v1/search", "admin", "", 404},
			{npmSearch, npmBase + "npm-remote/-/v1/search", "admin", "", 501},

			// A login needs no credentials, and stale ones count as none:
			// npm sends whatever .npmrc holds. 201, as 7.x answers it
			// (KB 000004147).
			{npmLogin, login + "user", "", npmLoginBody("user", "pw"), 201},
			{npmLogin, login + "user", "anonymous", npmLoginBody("user", "pw"), 201},
			{npmLogin, login + "user", "forged", npmLoginBody("user", "pw"), 201},
			{npmLogin, login + "user", "nope:wrong", npmLoginBody("user", "pw"), 201},
			// The login is the body's, not the caller's.
			{npmLogin, login + "user", "admin", npmLoginBody("user", "pw"), 201},
			// Refusals are 401 or 403, never 400 or 409, which npm reads as
			// "no such user" and "update the user".
			{npmLogin, login + "npm-victim", "", npmLoginBody("npm-victim", "wrong"), 401},
			{npmLogin, login + "npm-ghost", "", npmLoginBody("npm-ghost", "pw"), 401},
			{npmLogin, login + "npm-expired", "", npmLoginBody("npm-expired", "pw"), 403},
			{npmLogin, login + "user", "", npmLoginBody("admin", "pw"), 400},
			{npmLogin, login + "user", "", `{"name":`, 400},
			{
				npmLogin, npmBase + "missing/-/user/org.couchdb.user:user", "",
				npmLoginBody("user", "pw"), 404,
			},
			{
				npmLogin, npmBase + "libs-local/-/user/org.couchdb.user:user", "",
				npmLoginBody("user", "pw"), 404,
			},
			{
				npmLogin, npmBase + "npm-remote/-/user/org.couchdb.user:user", "",
				npmLoginBody("user", "pw"), 501,
			},

			// Logout ends the token it names and answers 200 whatever it
			// finds, so npm forgets a token already gone.
			{npmLogout, local + "-/user/token/no-such-token", "", "", 200},
			{npmLogout, local + "-/user/token/no-such-token", "forged", "", 200},
			{npmLogout, local + "-/user/token/no-such-token", "anonymous", "", 200},
			{npmLogout, npmBase + "missing/-/user/token/no-such-token", "", "", 404},

			{npmWebLogin, local + "-/v1/login", "admin", "{}", 405},
			{npmWebLogin, local + "-/v1/login", "", "{}", 401},

			{npmAuth, npmBase + "auth", "admin", "", 200},
			{npmAuth, npmBase + "auth", "user", "", 200},
			{npmAuth, npmBase + "auth", "admin-token", "", 200},
			{npmAuth, npmBase + "auth", "anonymous", "", 401},
			{npmAuth, npmBase + "auth", "", "", 401},
			{npmAuth, npmBase + "auth", "forged", "", 401},

			{npmScopeAuth, local + "auth/acme", "admin", "", 200},
			{npmScopeAuth, local + "auth/acme", "user-token", "", 200},
			// Any npm repository, whatever its class.
			{npmScopeAuth, npmBase + "npm-remote/auth/acme", "user", "", 200},
			{npmScopeAuth, npmBase + "npm-virtual/auth/acme", "user", "", 200},
			{npmScopeAuth, npmBase + "libs-local/auth/acme", "admin", "", 400},
			{npmScopeAuth, local + "auth/a%20b", "admin", "", 400},
			{npmScopeAuth, npmBase + "missing/auth/acme", "admin", "", 404},
			{npmScopeAuth, local + "auth/acme", "anonymous", "", 401},
			{npmScopeAuth, local + "auth/acme", "", "", 401},
		}
	},
	seed: func(t *testing.T, db store.Store, _ blob.Store) {
		t.Helper()
		ctx := context.Background()
		for _, u := range []types.User{
			// Wrong passwords go to a user of their own, so no suspension
			// reaches the user other rows sign in as.
			{Name: "npm-victim", PasswordHash: cheapHash},
			{Name: "npm-expired", PasswordHash: cheapHash, PasswordExpired: true},
		} {
			if err := db.Users().Create(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
	},
}
