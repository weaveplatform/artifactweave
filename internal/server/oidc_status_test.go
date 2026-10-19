package server

import "strings"

// oidcStatusRows covers the OIDC integration (#83). Rows run in order
// against one fixture: providers ci and gh are created, ci's mapping readers
// is created, replaced and deleted, and both providers are deleted last.
// The exchange rows never reach a provider: their subject tokens are
// refused before any key is fetched.
func oidcStatusRows() []statusCase {
	const (
		cfgs   = "GET /access/api/v1/oidc"
		cfgNew = "POST /access/api/v1/oidc"
		cfgGet = "GET /access/api/v1/oidc/{name}"
		cfgPut = "PUT /access/api/v1/oidc/{name}"
		cfgDel = "DELETE /access/api/v1/oidc/{name}"
		maps   = "GET /access/api/v1/oidc/{provider_name}/identity_mappings"
		mapNew = "POST /access/api/v1/oidc/{provider_name}/identity_mappings"
		mapGet = "GET /access/api/v1/oidc/{provider_name}/identity_mappings/{mapping_name}"
		mapPut = "PUT /access/api/v1/oidc/{provider_name}/identity_mappings/{mapping_name}"
		mapDel = "DELETE /access/api/v1/oidc/{provider_name}/identity_mappings/{mapping_name}"
		xchg   = "POST /access/api/v1/oidc/token"
		base   = "/access/api/v1/oidc"
		ci     = `{"name":"ci","issuer_url":"https://ci.example","provider_type":"Generic OpenID Connect",` +
			`"audience":"jfrog","organization":"","use_default_proxy":false}`
		gh = `{"name":"gh","issuer_url":"https://token.actions.githubusercontent.com",` +
			`"provider_type":"GitHub","organization":"octo-org"}`
		readers = `{"name":"readers","priority":1,"claims":{"sub":"repo:octo-org/*"},` +
			`"token_spec":{"scope":"applied-permissions/groups:readers","audience":"*@*","expires_in":60}}`
		exchange = `{"grant_type":"urn:ietf:params:oauth:grant-type:token-exchange",` +
			`"subject_token_type":"urn:ietf:params:oauth:token-type:id_token","subject_token":"not-a-jwt",` +
			`"provider_name":"%s"}`
	)
	tokenFor := func(provider string) string {
		return strings.Replace(exchange, "%s", provider, 1)
	}
	return []statusCase{
		{cfgs, base, "admin", "", 200},
		{cfgs, base, "user", "", 403},
		{cfgs, base, "", "", 401},

		{cfgNew, base, "admin", ci, 200},
		{cfgNew, base, "admin", ci, 400},
		{cfgNew, base, "admin", gh, 200},
		{
			cfgNew,
			base,
			"admin",
			`{"name":"gh2","issuer_url":"https://token.actions.githubusercontent.com","provider_type":"GitHub"}`,
			400,
		},
		{cfgNew, base, "admin", `{"name":"no spaces","issuer_url":"https://ci.example"}`, 400},
		{cfgNew, base, "admin", `{"name":"x","issuer_url":"ftp://ci.example"}`, 400},
		{
			cfgNew,
			base,
			"admin",
			`{"name":"x","issuer_url":"https://ci.example","provider_type":"Okta"}`,
			400,
		},
		{
			cfgNew,
			base,
			"admin",
			`{"name":"x","issuer_url":"https://ci.example","organization":"o"}`,
			400,
		},
		{cfgNew, base, "admin", "", 400},
		{cfgNew, base, "user", ci, 403},
		{cfgNew, base, "", ci, 401},

		{cfgGet, base + "/ci", "admin", "", 200},
		{cfgGet, base + "/missing", "admin", "", 404},
		{cfgGet, base + "/ci", "user", "", 403},

		{cfgPut, base + "/ci", "admin", ci, 200},
		{cfgPut, base + "/ci", "admin", `{"name":"other","issuer_url":"https://ci.example"}`, 400},
		{cfgPut, base + "/missing", "admin", `{"issuer_url":"https://ci.example"}`, 404},
		{cfgPut, base + "/ci", "admin", "", 400},
		{cfgPut, base + "/ci", "user", ci, 403},

		{mapNew, base + "/ci/identity_mappings", "admin", readers, 200},
		{mapNew, base + "/ci/identity_mappings", "admin", readers, 400},
		{mapNew, base + "/missing/identity_mappings", "admin", readers, 404},
		{
			mapNew,
			base + "/ci/identity_mappings",
			"admin",
			`{"name":"x","token_spec":{"scope":"applied-permissions/nope"}}`,
			400,
		},
		{
			mapNew,
			base + "/ci/identity_mappings",
			"admin",
			`{"name":"x","token_spec":{"scope":"applied-permissions/user"}}`,
			400,
		},
		{
			mapNew,
			base + "/ci/identity_mappings",
			"admin",
			`{"name":"x","priority":0,"token_spec":{"username":"u"}}`,
			400,
		},
		// The page's combination table (IDZ-60): a user with a groups
		// pattern, and a resource pattern beside a user, are accepted.
		{
			mapNew,
			base + "/ci/identity_mappings",
			"admin",
			`{"name":"user-groups","token_spec":{"username":"u","groups_pattern":"{{g}}"}}`,
			200,
		},
		{
			mapNew,
			base + "/ci/identity_mappings",
			"admin",
			`{"name":"user-resource","token_spec":{"username":"u","resource_pattern":"{{jfrog_scope}}"}}`,
			200,
		},
		{
			mapNew,
			base + "/ci/identity_mappings",
			"admin",
			`{"name":"x","token_spec":{"username":"u","username_pattern":"{{a}}"}}`,
			400,
		},
		{
			mapNew,
			base + "/ci/identity_mappings",
			"admin",
			`{"name":"x","token_spec":{"scope":"applied-permissions/groups:g","groups_pattern":"{{g}}"}}`,
			400,
		},
		{
			mapNew,
			base + "/ci/identity_mappings",
			"admin",
			`{"name":"x","token_spec":{"username":"u","scope":"applied-permissions/admin applied-permissions/groups:g"}}`,
			400,
		},
		{
			mapNew,
			base + "/ci/identity_mappings",
			"admin",
			`{"name":"x","token_spec":{"resource_pattern":"{{jfrog_scope}}"}}`,
			400,
		},
		{
			mapNew,
			base + "/ci/identity_mappings",
			"admin",
			`{"name":"x","token_spec":{"username_pattern":"{{a}} | $2"}}`,
			400,
		},
		// A project roles mapping needs no username, as the jfrog/platform
		// provider's example sends none (#476).
		{
			mapNew,
			base + "/ci/identity_mappings",
			"admin",
			`{"name":"project-roles","token_spec":{"scope":"applied-permissions/roles:p1:Developer"}}`,
			200,
		},
		{mapNew, base + "/ci/identity_mappings", "admin", `{"name":"x"}`, 400},
		{mapNew, base + "/ci/identity_mappings", "admin", "", 400},
		{mapNew, base + "/ci/identity_mappings", "user", readers, 403},
		{mapNew, base + "/ci/identity_mappings", "", readers, 401},

		{maps, base + "/ci/identity_mappings", "admin", "", 200},
		{maps, base + "/ci/identity_mappings?project_key=p1", "admin", "", 200},
		{maps, base + "/missing/identity_mappings", "admin", "", 404},
		{maps, base + "/ci/identity_mappings", "user", "", 403},

		{mapGet, base + "/ci/identity_mappings/readers", "admin", "", 200},
		{mapGet, base + "/ci/identity_mappings/missing", "admin", "", 404},
		{mapGet, base + "/missing/identity_mappings/readers", "admin", "", 404},
		{mapGet, base + "/ci/identity_mappings/readers", "user", "", 403},

		{mapPut, base + "/ci/identity_mappings/readers", "admin", readers, 200},
		{
			mapPut,
			base + "/ci/identity_mappings/readers",
			"admin",
			`{"name":"other","token_spec":{"username":"u"}}`,
			400,
		},
		{
			mapPut,
			base + "/ci/identity_mappings/missing",
			"admin",
			`{"token_spec":{"username":"u"}}`,
			404,
		},
		{mapPut, base + "/ci/identity_mappings/readers", "admin", "", 400},
		// A mapping's project cannot change, to another or to none, nor a
		// global mapping gain one (#476).
		{
			mapPut,
			base + "/ci/identity_mappings/project-roles",
			"admin",
			`{"token_spec":{"scope":"applied-permissions/roles:p1:Viewer"}}`,
			200,
		},
		{
			mapPut,
			base + "/ci/identity_mappings/project-roles",
			"admin",
			`{"token_spec":{"scope":"applied-permissions/roles:p2:Viewer"}}`,
			400,
		},
		{
			mapPut,
			base + "/ci/identity_mappings/project-roles",
			"admin",
			`{"token_spec":{"scope":"applied-permissions/groups:readers"}}`,
			400,
		},
		{
			mapPut,
			base + "/ci/identity_mappings/readers",
			"admin",
			`{"token_spec":{"scope":"applied-permissions/roles:p1:Viewer"}}`,
			400,
		},
		{mapPut, base + "/ci/identity_mappings/readers", "user", readers, 403},

		// The exchange needs no credentials; a credential that fails still
		// answers 401, as on every route.
		{xchg, base + "/token", "", `{}`, 400},
		{xchg, base + "/token", "", tokenFor("missing"), 400},
		{xchg, base + "/token", "", tokenFor("ci"), 401},
		{xchg, base + "/token", "anonymous", `{}`, 400},
		{xchg, base + "/token", "admin", tokenFor("ci"), 401},
		{xchg, base + "/token", "forged", tokenFor("ci"), 401},
		{xchg, base + "/token", "", "", 400},

		{mapDel, base + "/ci/identity_mappings/readers", "user", "", 403},
		{mapDel, base + "/ci/identity_mappings/readers", "admin", "", 204},
		{mapDel, base + "/ci/identity_mappings/readers", "admin", "", 404},

		{cfgDel, base + "/gh", "user", "", 403},
		{cfgDel, base + "/gh", "admin", "", 204},
		{cfgDel, base + "/ci", "admin", "", 204},
		{cfgDel, base + "/ci", "admin", "", 404},
	}
}
