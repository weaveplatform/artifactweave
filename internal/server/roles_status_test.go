package server

import (
	"net/http"
	"testing"
)

// roleStatusRows covers global environments and roles. Rows run in order
// against one fixture: QA is created, renamed to STAGE and deleted; ci is
// created, replaced and deleted.
func roleStatusRows() []statusCase {
	const (
		envs    = "GET /access/api/v1/environments"
		envNew  = "POST /access/api/v1/environments"
		envMove = "POST /access/api/v1/environments/{environmentName}/rename"
		envDel  = "DELETE /access/api/v1/environments/{environmentName}"
		roles   = "GET /access/api/v1/roles"
		roleNew = "POST /access/api/v1/roles"
		roleGet = "GET /access/api/v1/roles/{role}"
		rolePut = "PUT /access/api/v1/roles/{role}"
		roleDel = "DELETE /access/api/v1/roles/{role}"
		envPath = "/access/api/v1/environments"
		rolPath = "/access/api/v1/roles"
		ci      = `{"name":"ci","type":"CUSTOM_GLOBAL","actions":["READ_REPOSITORY"],"environments":["dev"]}`
	)
	return []statusCase{
		{envs, envPath, "admin", "", 200},
		{envs, envPath, "user", "", 403},
		{envs, envPath, "anonymous", "", 401},
		{envs, envPath, "", "", 401},

		{envNew, envPath, "admin", `{"name":"QA"}`, 201},
		{envNew, envPath, "admin", `{"name":"qa"}`, 409},
		{envNew, envPath, "admin", `{"name":"Dev"}`, 409},
		{envNew, envPath, "admin", `{"name":"no spaces"}`, 400},
		{envNew, envPath, "user", `{"name":"X"}`, 403},
		{envNew, envPath, "", `{"name":"X"}`, 401},

		{envMove, envPath + "/qa/rename", "admin", `{"new_name":"STAGE"}`, 200},
		{envMove, envPath + "/DEV/rename", "admin", `{"new_name":"DEVELOP"}`, 400},
		{envMove, envPath + "/STAGE/rename", "admin", `{"new_name":"prod"}`, 400},
		{envMove, envPath + "/STAGE/rename", "admin", `{"new_name":"bad name"}`, 400},
		{envMove, envPath + "/MISSING/rename", "admin", `{"new_name":"Y"}`, 404},
		{envMove, envPath + "/STAGE/rename", "user", `{"new_name":"Y"}`, 403},
		{envMove, envPath + "/STAGE/rename", "", `{"new_name":"Y"}`, 401},

		{roles, rolPath, "admin", "", 200},
		{roles, rolPath, "user", "", 403},
		{roles, rolPath, "", "", 401},

		{roleNew, rolPath, "admin", ci, 201},
		{roleNew, rolPath, "admin", ci, 409},
		{roleNew, rolPath, "admin", `{"name":"Viewer","type":"CUSTOM_GLOBAL"}`, 409},
		{roleNew, rolPath, "admin", `{"name":"x","type":"CUSTOM_GLOBAL","actions":["FLY"]}`, 400},
		{
			roleNew,
			rolPath,
			"admin",
			`{"name":"x","type":"CUSTOM_GLOBAL","environments":["NOPE"]}`,
			400,
		},
		{roleNew, rolPath, "admin", `{"name":"x","type":"ADMIN"}`, 400},
		{roleNew, rolPath, "user", ci, 403},
		{roleNew, rolPath, "", ci, 401},

		{roleGet, rolPath + "/ci", "admin", "", 200},
		{roleGet, rolPath + "/Project%20Admin", "admin", "", 200},
		{roleGet, rolPath + "/missing", "admin", "", 404},
		{roleGet, rolPath + "/ci", "user", "", 403},
		{roleGet, rolPath + "/ci", "", "", 401},

		{
			rolePut,
			rolPath + "/ci",
			"admin",
			`{"name":"ci","type":"CUSTOM_GLOBAL","actions":["READ_BUILD"]}`,
			200,
		},
		{rolePut, rolPath + "/ci", "admin", `{"name":"other","type":"CUSTOM_GLOBAL"}`, 400},
		{
			rolePut,
			rolPath + "/Viewer",
			"admin",
			`{"name":"Viewer","type":"PREDEFINED","actions":["READ_BUILD"]}`,
			200,
		},
		{rolePut, rolPath + "/Viewer", "admin", `{"name":"Viewer","type":"CUSTOM_GLOBAL"}`, 400},
		{rolePut, rolPath + "/Viewer", "admin", `{"name":"Watcher","type":"PREDEFINED"}`, 400},
		{
			rolePut,
			rolPath + "/Project%20Admin",
			"admin",
			`{"name":"Project Admin","type":"ADMIN"}`,
			400,
		},
		{rolePut, rolPath + "/missing", "admin", `{"name":"missing","type":"CUSTOM_GLOBAL"}`, 404},
		{rolePut, rolPath + "/ci", "user", `{"name":"ci","type":"CUSTOM_GLOBAL"}`, 403},
		{rolePut, rolPath + "/ci", "", `{"name":"ci","type":"CUSTOM_GLOBAL"}`, 401},

		{roleDel, rolPath + "/Viewer", "admin", "", 400},
		{roleDel, rolPath + "/ci", "user", "", 403},
		{roleDel, rolPath + "/ci", "", "", 401},
		{roleDel, rolPath + "/ci", "admin", "", 204},
		{roleDel, rolPath + "/ci", "admin", "", 404},

		{envDel, envPath + "/PROD", "admin", "", 400},
		{envDel, envPath + "/STAGE", "user", "", 403},
		{envDel, envPath + "/STAGE", "", "", 401},
		{envDel, envPath + "/stage", "admin", "", 204},
		{envDel, envPath + "/STAGE", "admin", "", 404},
	}
}

// TestAccGlobalEnvironmentLimitStatus checks the global environment past the
// limit answers 400 in the Access envelope; DEV and PROD count toward it.
func TestAccGlobalEnvironmentLimitStatus(t *testing.T) {
	h := statusFixture(t, false, func(c *Config) { c.GlobalEnvironmentLimit = 3 })
	admin := jsonHeaders(basic("admin", "pw"))
	const envs = "/access/api/v1/environments"
	if rec := do(
		t,
		h,
		http.MethodPost,
		envs,
		admin,
		`{"name":"QA"}`,
	); rec.Code != http.StatusCreated {
		t.Fatalf("third: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, http.MethodPost, envs, admin, `{"name":"STAGE"}`)
	if want := `{"errors":[{"code":"BAD_REQUEST","message":` +
		`"at most 3 global environments may exist: invalid"}]}` + "\n"; rec.Code !=
		http.StatusBadRequest || rec.Body.String() != want {
		t.Fatalf("fourth: %d %s", rec.Code, rec.Body)
	}
}
