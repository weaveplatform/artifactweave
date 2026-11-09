package server

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// trashOffStatusRows are the trash can's rows while it is off (#490), run
// by TestAccTrashOffStatus in order on a status fixture that trashed
// libs-local/fixture/del.txt and libs-local/fixture/move.txt before the
// trash can was turned off. Every storage view answers 404, to an
// administrator too; what the trash can holds is still restored, cleaned and
// emptied, and a delete no longer reaches it.
func trashOffStatusRows() []statusCase {
	const (
		list    = "GET /artifactory/api/storage/auto-trashcan"
		restore = "POST /artifactory/api/trash/restore/{fromPath}"
		clean   = "DELETE /artifactory/api/trash/clean/{repoPath}"
		remove  = "DELETE /artifactory/{repoKey}/{filePath}"
		empty   = "POST /artifactory/api/trash/empty"
		deploy  = "PUT /artifactory/{repoKey}/{filePath}"
		drop    = "DELETE /artifactory/api/repositories/{repoKey}"
		tc      = "/artifactory/api/storage/auto-trashcan"
		del     = "libs-local/fixture/del.txt"
		moved   = "libs-local/fixture/move.txt"
		rs      = "/artifactory/api/trash/restore/"
	)
	return []statusCase{
		{list, tc, "admin", "", 404},
		{list, tc + "/" + del, "admin", "", 404},
		{list, tc + "/libs-local", "admin", "", 404},
		{list, tc + "?list&deep=1&listFolders=1", "admin", "", 404},
		{list, tc + "/" + del + "?properties", "admin", "", 404},
		{list, tc + "/" + del + "?stats", "admin", "", 404},
		{list, tc + "/libs-local?lastModified", "admin", "", 404},
		{list, tc + "/" + del + "?permissions", "admin", "", 404},
		{list, tc + "/gone-repo", "admin", "", 404},
		{list, tc, "user", "", 404},
		{list, tc + "/" + del, "user", "", 404},
		{list, tc, "anonymous", "", 404},
		{list, tc, "", "", 401},
		{list, tc + "?properties=%zz", "admin", "", 400},

		{restore, rs + del + "?to=libs-local", "user", "", 400},
		{restore, rs + del + "?to=libs-local", "admin", "", 202},
		{clean, "/artifactory/api/trash/clean/" + moved, "admin", "", 204},
		// A delete while the trash can is off removes the file for good.
		{remove, "/artifactory/" + del, "admin", "", 204},
		{restore, rs + del + "?to=libs-local", "admin", "", 404},
		// So does a repository delete, its content with it (#504).
		{deploy, "/artifactory/del-one/kept.txt", "admin", "kept", 201},
		{drop, "/artifactory/api/repositories/del-one", "admin", "", 200},
		{restore, rs + "del-one?to=libs-local", "admin", "", 404},
		{empty, "/artifactory/api/trash/empty", "admin", "", 202},
	}
}

// TestAccTrashOffStatus runs trashOffStatusRows. The trash can is a setting
// with no route of its own, so the rows need a fixture of their own, which
// turns it off once the fixture files are trashed.
func TestAccTrashOffStatus(t *testing.T) {
	var off, on store.Store
	offH := statusFixture(t, false, func(c *Config) { off = c.Store })
	onH := statusFixture(t, true, func(c *Config) { on = c.Store })
	for _, p := range []string{"fixture/del.txt", "fixture/move.txt"} {
		wantStatus(t, do(t, offH, http.MethodDelete, "/artifactory/libs-local/"+p,
			basic("admin", "pw"), ""), http.StatusNoContent)
	}
	setTrashcan(t, off, false)
	setTrashcan(t, on, false)
	for _, c := range trashOffStatusRows() {
		h, headers := offH, map[string]string(nil)
		switch c.as {
		case "anonymous":
			h = onH
		case "admin", "user":
			headers = basic(c.as, "pw")
		}
		if rec := do(t, h, c.method(), c.path, headers, c.body); rec.Code != c.expected {
			t.Errorf("%s %s as %q: status %d, want %d: %s",
				c.method(), c.path, c.as, rec.Code, c.expected, rec.Body)
		}
	}
}
