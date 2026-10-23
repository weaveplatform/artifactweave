package npm_test

// npmAuthMatrixPart is npm search's and npm authentication's share of the
// route × principal matrix (#291, #292). A login and a logout act on the
// principal's victim v-{p}, whatever the caller presents: its credentials
// are not needed, and ones that fail, an expired, revoked or registry
// token, count as none (decision 0034).
var npmAuthMatrixPart = matrixPart{
	rows: func() []matrixRow {
		local := npmBase + "npm-local/"
		return []matrixRow{
			// Filtered, not refused: what a caller may not read is left out.
			row(npmSearch, local+"-/v1/search?text=work", "", anyone, 200),
			row(npmLogin, local+"-/user/org.couchdb.user:v-{p}", npmLoginBody("v-{p}", "pw"),
				noCredentials, 201).credentialsOptional(),
			// {token} is v-{p}'s own: the logout revokes it.
			row(npmLogout, local+"-/user/token/{token}", "", noCredentials, 200).
				credentialsOptional(),
			// Each caller gets lines for its own credential, and only
			// what it holds already or its encrypted password, so no
			// principal is refused.
			at(npmAuth, "", signedIn, 200),
			row(npmScopeAuth, local+"auth/acme", "", signedIn, 200),
		}
	},
}

// credentialsOptional marks a row whose route counts credentials that
// fail as none.
func (r matrixRow) credentialsOptional() matrixRow { r.open = true; return r }
