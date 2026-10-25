package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/repos"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// trashStatusRows run after itemStatusRows, which trashed
// libs-local/fixture/del.txt in the same fixture.
func trashStatusRows() []statusCase {
	const (
		list    = "GET /artifactory/api/storage/auto-trashcan"
		restore = "POST /artifactory/api/trash/restore/{fromPath}"
		clean   = "DELETE /artifactory/api/trash/clean/{repoPath}"
		remove  = "DELETE /artifactory/{repoKey}/{filePath}"
		empty   = "POST /artifactory/api/trash/empty"
		gcOp    = "POST /artifactory/api/system/storage/gc"
		info    = "GET /artifactory/api/storageinfo"
		calc    = "POST /artifactory/api/storageinfo/calculate"
		zap     = "POST /artifactory/api/zap/{repoPath}"
		tc      = "/artifactory/api/storage/auto-trashcan"
		del     = "libs-local/fixture/del.txt"
		rs      = "/artifactory/api/trash/restore/"
		cl      = "/artifactory/api/trash/clean/"
		create  = "PUT /artifactory/api/repositories/{repoKey}"
		drop    = "DELETE /artifactory/api/repositories/{repoKey}"
		deploy  = "PUT /artifactory/{repoKey}/{filePath}"
		gone    = "/artifactory/api/repositories/trashed-repo"
	)
	return []statusCase{
		{list, tc, "admin", "", 200},
		{list, tc + "/" + del, "admin", "", 200},
		{list, tc + "/libs-local/absent", "admin", "", 404},
		{list, tc + "/gone-repo", "admin", "", 404},
		// "user" manages perm-local and holds nothing in libs-local (#439).
		{list, tc, "user", "", 200},
		{list, tc + "/perm-local", "user", "", 404},
		{list, tc + "/" + del, "user", "", 403},
		{list, tc + "/libs-local/absent", "user", "", 403},
		{list, tc + "/gone-repo", "user", "", 403},
		{list, tc, "anonymous", "", 401},
		{list, tc, "", "", 401},
		// The storage API's views, with the same filter (#455).
		{
			list,
			tc + "?list&deep=1&listFolders=1&mdTimestamps=1&includeRootPath=1",
			"admin",
			"",
			200,
		},
		{list, tc + "/libs-local?list&depth=1", "admin", "", 200},
		{list, tc + "/" + del + "?list", "admin", "", 400},
		{list, tc + "/libs-local/absent?list", "admin", "", 404},
		{list, tc + "/" + del + "?properties", "admin", "", 200},
		{list, tc + "/" + del + "?properties=absent", "admin", "", 404},
		{list, tc + "/libs-local/fixture?properties", "admin", "", 404},
		{list, tc + "/" + del + "?stats", "admin", "", 200},
		{list, tc + "/libs-local/fixture?stats", "admin", "", 400},
		{list, tc + "/" + del + "?lastModified", "admin", "", 200},
		{list, tc + "/libs-local?lastModified", "admin", "", 200},
		{list, tc + "/" + del + "?permissions", "admin", "", 400},
		{list, tc + "?list&stats", "admin", "", 400},
		{list, tc + "?depth=1", "admin", "", 400},
		{list, tc + "?list&deep=1", "user", "", 200},
		{list, tc + "/libs-local?list", "user", "", 403},
		{list, tc + "/" + del + "?properties", "user", "", 403},
		{list, tc + "/" + del + "?stats", "user", "", 403},
		{list, tc + "/libs-local?lastModified", "user", "", 403},
		{list, tc + "/" + del + "?permissions", "user", "", 403},
		{list, tc + "?list", "", "", 401},

		{restore, rs + del + "?to=virt", "admin", "", 400},
		{restore, rs + del, "admin", "", 400},
		{restore, rs + del + "?to=missing-repo", "admin", "", 400},
		{restore, rs + del + "?to=libs-local&transaction-size=0", "admin", "", 400},
		{restore, rs + del + "?to=libs-local", "user", "", 400},
		{restore, rs + del + "?to=libs-local", "", "", 401},
		{restore, rs + del + "?to=libs-local&transaction-size=10", "admin", "", 202},
		{restore, rs + del + "?to=libs-local", "admin", "", 404},
		{restore, rs + "libs-local?to=libs-local", "admin", "", 404},
		// An admin may restore from a repository that is gone (#380).
		{restore, rs + "gone-repo/x?to=libs-local", "admin", "", 404},
		// A repository delete keeps the content in the trash can under the
		// repository's key, which only an admin sees or restores, into a
		// repository that exists (#504).
		{create, gone, "admin", `{"rclass":"local"}`, 200},
		{deploy, "/artifactory/trashed-repo/kept/file.txt", "admin", "kept", 201},
		{drop, gone, "admin", "", 200},
		{list, tc + "/trashed-repo/kept/file.txt", "admin", "", 200},
		{list, tc + "/trashed-repo", "user", "", 403},
		{restore, rs + "trashed-repo?to=libs-local/from-trashed-repo", "user", "", 400},
		{restore, rs + "trashed-repo?to=trashed-repo", "admin", "", 400},
		{restore, rs + "trashed-repo?to=libs-local/from-trashed-repo", "admin", "", 202},
		{list, tc + "/trashed-repo", "admin", "", 404},

		{remove, "/artifactory/" + del, "admin", "", 204},
		{clean, cl + del, "user", "", 400},
		{clean, cl + del, "", "", 401},
		{clean, cl + "gone-repo/x", "user", "", 400},
		{clean, cl + del, "admin", "", 204},
		{clean, cl + "libs-local", "admin", "", 204},

		{empty, "/artifactory/api/trash/empty", "user", "", 400},
		{empty, "/artifactory/api/trash/empty", "anonymous", "", 401},
		{empty, "/artifactory/api/trash/empty", "", "", 401},
		{empty, "/artifactory/api/trash/empty", "admin", "", 202},

		{gcOp, "/artifactory/api/system/storage/gc", "admin", "", 200},
		{gcOp, "/artifactory/api/system/storage/gc", "user", "", 403},
		{gcOp, "/artifactory/api/system/storage/gc", "", "", 401},

		{info, "/artifactory/api/storageinfo", "admin", "", 200},
		{info, "/artifactory/api/storageinfo", "admin-token", "", 200},
		{info, "/artifactory/api/storageinfo", "user", "", 403},
		{info, "/artifactory/api/storageinfo", "user-token", "", 403},
		{info, "/artifactory/api/storageinfo", "anonymous", "", 401},
		{info, "/artifactory/api/storageinfo", "", "", 401},
		{calc, "/artifactory/api/storageinfo/calculate", "admin", "", 202},
		{calc, "/artifactory/api/storageinfo/calculate", "user", "", 403},

		{zap, "/artifactory/api/zap/remote-private-cache", "admin", "", 200},
		{zap, "/artifactory/api/zap/remote-private-cache/absent", "admin", "", 404},
		{zap, "/artifactory/api/zap/libs-local/x", "admin", "", 400},
		{zap, "/artifactory/api/zap/missing-cache", "admin", "", 404},
		{zap, "/artifactory/api/zap/remote-private-cache", "user", "", 403},
		{zap, "/artifactory/api/zap/remote-private-cache", "", "", 401},
	}
}

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
		if rec := do(t, h, c.method(), c.path, headers, c.body); rec.Code != c.want {
			t.Errorf("%s %s as %q: status %d, want %d: %s",
				c.method(), c.path, c.as, rec.Code, c.want, rec.Body)
		}
	}
}

// trashedThenOff seeds what trashed does and then turns the trash can off,
// so the row runs against what was trashed before it was turned off (#490).
// The seed runs once per principal on one fixture, so the trash can is
// turned on again for each principal's delete to reach it.
func trashedThenOff(repoKey, path string) seeder {
	seed := trashed(repoKey, path)
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		setTrashcan(t, f.db, true)
		seed(t, f, p)
		setTrashcan(t, f.db, false)
	}
}

// repositoryTrashed seeds a local repository named key, {p} the principal's
// name, with a file at kept/{p}.txt, and then deletes the repository, which
// moves the file to the trash can under key (#504).
func repositoryTrashed(key string) seeder {
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		ctx := context.Background()
		key := strings.ReplaceAll(key, "{p}", p)
		c := repo.Config{Key: key, Class: repo.Local}
		if err := f.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		f.deploy(t, key, "kept/"+p+".txt", []byte(p))
		if _, _, err := repos.Remove(ctx, f.db, key, "admin"); err != nil {
			t.Fatal(err)
		}
	}
}
