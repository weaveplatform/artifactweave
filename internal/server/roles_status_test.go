package server

import (
	"net/http"
	"testing"
)

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
