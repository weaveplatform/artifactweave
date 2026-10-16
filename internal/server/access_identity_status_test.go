package server

// accessIdentityStatusRows covers Access v2 users and groups and the
// identity configuration. The rows share one store with every other row, so
// none of them may change what later rows rely on: the bulk password expiry
// and the basic authentication switch are exercised for real in their own
// tests, the policy is only reset to its defaults, "user" joins no group and
// keeps its password, and the readers group stays (DELETE as admin is
// TestDeleteGroupDropsGrants).
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
