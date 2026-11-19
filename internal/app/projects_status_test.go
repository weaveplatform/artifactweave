package app

// projectStatusRows uses a fresh project key and leaves seeded identities and
// project "proj" intact for other rows. Detailed bodies and principal cases
// live in test/server/access/projects{,_status}_test.go.
func projectStatusRows() []statusCase {
	const base = "/access/api/v1/projects"
	const p = base + "/p11"
	const role = base + "/{projectKey}/roles"
	const env = base + "/{projectKey}/environments"
	return []statusCase{
		{"GET " + base, base, "admin", "", 200},
		{"POST " + base, base, "admin", `{"project_key":"p11","display_name":"P11 status"}`, 201},
		{"POST " + base, base, "admin", `{"project_key":"p11","display_name":"Duplicate"}`, 409},
		{"POST " + base, base, "admin", `{"project_key":"BAD","display_name":"Invalid"}`, 400},
		{"POST " + base, base, "user", `{"project_key":"deny","display_name":"Denied"}`, 403},
		{"GET " + base + "/{projectKey}", p, "admin", "", 200},
		{"GET " + base + "/{projectKey}", p, "user", "", 403},
		{"GET " + base + "/{projectKey}", p, "", "", 401},
		{"GET " + base + "/{projectKey}", base + "/missing", "admin", "", 404},
		{"PUT " + base + "/{projectKey}", p, "admin", `{"description":"updated"}`, 200},
		{"PUT " + base + "/{projectKey}", p, "admin", `{"display_name":""}`, 400},
		{"GET " + role, p + "/roles", "admin", "", 200},
		{"POST " + role, p + "/roles", "admin", `{"name":"ci","type":"CUSTOM","actions":["READ_REPOSITORY"],"environments":["DEV"]}`, 201},
		{"GET " + role + "/{role}", p + "/roles/ci", "admin", "", 200},
		{"PUT " + role + "/{role}", p + "/roles/ci", "admin", `{"type":"CUSTOM","actions":["READ_BUILD"],"environments":["DEV"]}`, 200},
		{"PUT " + base + "/{projectKey}/users/{user}", p + "/users/admin", "admin", `{"roles":["ci"]}`, 200},
		{"GET " + base + "/{projectKey}/users/{user}", p + "/users/admin", "admin", "", 200},
		{"GET " + base + "/{projectKey}/users", p + "/users", "admin", "", 200},
		{"PUT " + base + "/{projectKey}/groups/{group}", p + "/groups/readers", "admin", `{"roles":["ci"]}`, 200},
		{"GET " + base + "/{projectKey}/groups/{group}", p + "/groups/readers", "admin", "", 200},
		{"GET " + base + "/{projectKey}/groups", p + "/groups", "admin", "", 200},
		{"DELETE " + base + "/{projectKey}/users/{user}", p + "/users/admin", "admin", "", 204},
		{"DELETE " + base + "/{projectKey}/groups/{group}", p + "/groups/readers", "admin", "", 204},
		{"DELETE " + role + "/{role}", p + "/roles/ci", "admin", "", 204},
		{"POST " + env, p + "/environments", "admin", `{"name":"p11-qa"}`, 201},
		{"GET " + env, p + "/environments", "admin", "", 200},
		{"POST " + env + "/{environmentName}/rename", p + "/environments/p11-qa/rename", "admin", `{"new_name":"p11-stage"}`, 200},
		{"DELETE " + env + "/{environmentName}", p + "/environments/p11-stage", "admin", "", 204},
		{"GET /access/api/v2/projects", "/access/api/v2/projects?role_name=Viewer&limit=1", "admin", "", 200},
		{"GET /access/api/v2/projects", "/access/api/v2/projects?role_name=missing", "admin", "", 404},
		{"DELETE " + base + "/{projectKey}", p, "admin", "", 204},
	}
}
