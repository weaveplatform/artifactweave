package server

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
