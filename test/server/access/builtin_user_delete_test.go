package access_test

import (
	"net/http"
	"testing"
)

// TestAccLegacyDeleteBuiltInUser deletes the built-in admin and anonymous
// users through the legacy security API, in any case, and gets the message
// alone, without the identity service's sentinels (#1449). 7.x's legacy
// delete answer for a built-in user is in no source: Access, which refuses
// it, is closed, the UI's DeleteUserService prefixes whatever Access says
// with "Action cancelled.", and OSS 6.20's SecurityServiceImpl.deleteUser
// has no refusal. The 400 and the wording are kept [I]. Access v2 keeps
// its documented 403 with the same message.
func TestAccLegacyDeleteBuiltInUser(t *testing.T) {
	h, _ := seededHandler(t)
	refused := func(name string) string {
		return legacyError(
			http.StatusBadRequest,
			`the built-in user \"`+name+`\" cannot be deleted`,
		)
	}
	runUserSteps(t, h, []userStep{
		{
			admin,
			http.MethodDelete,
			secUsers + "/admin",
			"",
			http.StatusBadRequest,
			refused("admin"),
		},
		{
			admin,
			http.MethodDelete,
			secUsers + "/ADMIN",
			"",
			http.StatusBadRequest,
			refused("admin"),
		},
		{
			admin, http.MethodDelete, secUsers + "/anonymous", "", http.StatusBadRequest,
			refused("anonymous"),
		},
		{
			admin, http.MethodDelete, secUsers + "/Anonymous", "", http.StatusBadRequest,
			refused("anonymous"),
		},
		{
			admin, http.MethodDelete, v2Users + "/anonymous", "", http.StatusForbidden,
			v2Error("FORBIDDEN", `the built-in user \"anonymous\" cannot be deleted`),
		},
		{admin, http.MethodGet, secUsers, "", http.StatusOK, legacyUsers()},
	})
}
