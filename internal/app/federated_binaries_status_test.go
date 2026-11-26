package app

func federatedBinaryStatusRows() []statusCase {
	const base = "/artifactory/api/federation/"
	const repos = "/artifactory/api/repositories/"
	const list = "GET " + base + "binaries/failures/list"
	const replay = "PUT " + base + "binaries/failures/replay"
	const del = "DELETE " + base + "binaries/failures/delete"
	const tasks = "GET " + base + "binaries/tasks/{repoKey}"
	const state = "GET " + base + "status/repoState/{repoKey}/binaries/"
	rows := []statusCase{
		{"PUT " + repos + "{repoKey}", repos + "fed6-status", "admin", `{"rclass":"federated","packageType":"generic"}`, 200},
		{"PUT " + repos + "{repoKey}", repos + "fed6-local", "admin", `{"rclass":"local","packageType":"generic"}`, 200},
	}
	for _, route := range []struct{ op, path string }{{list, base + "binaries/failures/list?repoKey=fed6-status"}, {replay, base + "binaries/failures/replay?repoKey=fed6-status"}, {del, base + "binaries/failures/delete?repoKey=fed6-status"}, {tasks, base + "binaries/tasks/fed6-status"}} {
		rows = append(rows, statusCase{route.op, route.path, "", "", 401}, statusCase{route.op, route.path, "user", "", 403}, statusCase{route.op, route.path, "admin", "", 200})
	}
	rows = append(rows,
		statusCase{list, base + "binaries/failures/list?repoKey=fed6-status&limit=0", "user", "", 403},
		statusCase{list, base + "binaries/failures/list?repoKey=fed6-status&limit=0", "admin", "", 400},
		statusCase{list, base + "binaries/failures/list?repoKey=fed6-local", "admin", "", 400},
		statusCase{list, base + "binaries/failures/list?repoKey=missing", "admin", "", 404},
		statusCase{replay, base + "binaries/failures/replay?repoKey=fed6-status&sha1=bad", "admin", "", 400},
		statusCase{del, base + "binaries/failures/delete?repoKey=fed6-status&sha1=bad", "admin", "", 400},
		statusCase{state, base + "status/repoState/fed6-status/binaries/", "", "", 401},
		statusCase{state, base + "status/repoState/fed6-status/binaries/", "user", "", 403},
		statusCase{state, base + "status/repoState/fed6-status/binaries/", "admin", "", 200},
		statusCase{state, base + "status/repoState/fed6-local/binaries/", "admin", "", 400},
		statusCase{state, base + "status/repoState/missing/binaries/", "admin", "", 404},
	)
	return rows
}
