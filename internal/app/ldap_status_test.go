package app

// ldapStatusRows covers the LDAP connections (#1832) and their connection
// test (#1833). Rows run in order against one fixture: corp is created,
// read, replaced and deleted; every operation refuses callers who are not
// administrators. The test's 204 needs a directory, so only
// test/server/access/ldap_login_test.go proves it; here an unreachable
// directory answers 400 with the connect failure.
func ldapStatusRows() []statusCase {
	const (
		list   = "GET /access/api/v1/ldap/settings"
		create = "POST /access/api/v1/ldap/settings"
		update = "PUT /access/api/v1/ldap/settings"
		get    = "GET /access/api/v1/ldap/settings/{key}"
		del    = "DELETE /access/api/v1/ldap/settings/{key}"
		test   = "POST /access/api/v1/ldap/settings/test"
		base   = "/access/api/v1/ldap/settings"
		corp   = `{"key":"corp","ldap_url":"ldap://ldap.example.com","user_dn_pattern":"uid={0}"}`
	)
	return []statusCase{
		{list, base, "admin", "", 200},
		{list, base, "user", "", 403},
		{list, base, "", "", 401},

		{create, base, "admin", corp, 201},
		{create, base, "admin", corp, 400},
		{create, base, "admin", `{"key":"bad","ldap_url":"http://h","user_dn_pattern":"uid={0}"}`, 400},
		{create, base, "user", corp, 403},
		{create, base, "", corp, 401},

		{get, base + "/corp", "admin", "", 200},
		{get, base + "/missing", "admin", "", 404},
		{get, base + "/corp", "user", "", 403},

		{update, base, "admin", corp, 200},
		{update, base, "admin", `{"key":"missing","ldap_url":"ldap://h","user_dn_pattern":"uid={0}"}`, 404},
		{update, base, "admin", `{"ldap_url":"ldap://h"}`, 400},
		{update, base, "user", corp, 403},

		{del, base + "/corp", "user", "", 403},
		{del, base + "/corp", "admin", "", 204},
		{del, base + "/corp", "admin", "", 404},

		{test, base + "/test", "admin", `{"key":"corp","ldap_url":"ldap://127.0.0.1:1","user_dn_pattern":"uid={0}"}`, 400},
		{test, base + "/test", "admin", `{"key":"bad","ldap_url":"http://h","user_dn_pattern":"uid={0}"}`, 400},
		{test, base + "/test", "admin", "", 400},
		{test, base + "/test", "user", corp, 403},
		{test, base + "/test", "", corp, 401},
	}
}

// ldapGroupStatusRows covers the LDAP group settings (#1834), in order
// against one fixture: the dir connection, whose directory nothing
// answers, then the teams setting created, read, replaced, searched,
// imported into, refreshed and deleted. The search's and refresh's 200
// need a directory, so test/server/access/ldap_groups_test.go proves them;
// here the unreachable directory answers 400.
func ldapGroupStatusRows() []statusCase {
	const (
		conn     = "POST /access/api/v1/ldap/settings"
		connDel  = "DELETE /access/api/v1/ldap/settings/{key}"
		list     = "GET /access/api/v1/ldap/groups"
		create   = "POST /access/api/v1/ldap/groups"
		update   = "PUT /access/api/v1/ldap/groups"
		get      = "GET /access/api/v1/ldap/groups/{name}"
		del      = "DELETE /access/api/v1/ldap/groups/{name}"
		strategy = "GET /access/api/v1/ldap/groups/strategy"
		search   = "POST /access/api/v1/ldap/groups/search"
		imp      = "POST /access/api/v1/ldap/groups/import"
		refresh  = "POST /access/api/v1/ldap/groups/{name}/refresh"
		base     = "/access/api/v1/ldap/groups"
		teams    = `{"name":"teams","enabled_ldap":"dir"}`
	)
	return []statusCase{
		{
			conn, "/access/api/v1/ldap/settings", "admin",
			`{"key":"dir","ldap_url":"ldap://127.0.0.1:1","user_dn_pattern":"uid={0}"}`, 201,
		},

		{list, base, "admin", "", 200},
		{list, base, "user", "", 403},
		{list, base, "", "", 401},

		{create, base, "admin", teams, 201},
		{create, base, "admin", teams, 400},
		{create, base, "admin", `{"name":"bad","enabled_ldap":"dir","strategy":"NESTED"}`, 400},
		{create, base, "user", teams, 403},
		{create, base, "", teams, 401},

		{get, base + "/teams", "admin", "", 200},
		{get, base + "/missing", "admin", "", 404},
		{get, base + "/teams", "user", "", 403},

		{update, base, "admin", `{"name":"teams","enabled_ldap":"dir","sub_tree":true}`, 200},
		{update, base, "admin", `{"name":"missing","enabled_ldap":"dir"}`, 404},
		{update, base, "user", teams, 403},

		{strategy, base + "/strategy?strategy=DYNAMIC", "admin", "", 200},
		{strategy, base + "/strategy", "user", "", 403},

		{search, base + "/search", "admin", teams, 400},
		{search, base + "/search", "user", teams, 403},

		{imp, base + "/import", "admin", `{"ldap_group_settings":` + teams + `,"import_groups":[]}`, 204},
		{imp, base + "/import", "user", `{"ldap_group_settings":` + teams + `,"import_groups":[]}`, 403},

		{refresh, base + "/teams/refresh", "admin", "", 400},
		{refresh, base + "/missing/refresh", "admin", "", 404},
		{refresh, base + "/teams/refresh", "user", "", 403},

		{del, base + "/teams", "user", "", 403},
		{del, base + "/teams", "admin", "", 204},
		{del, base + "/teams", "admin", "", 404},

		{connDel, "/access/api/v1/ldap/settings/dir", "admin", "", 204},
	}
}
