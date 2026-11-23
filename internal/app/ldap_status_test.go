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
