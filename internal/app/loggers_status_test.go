package app

import "net/http"

func loggerStatusRows() []statusCase {
	const path = "/artifactory/api/system/debug/loggers"
	const body = `{"classes":{"artifactweave.http":"debug"},"timer":60}`
	var rows []statusCase
	for _, op := range []struct{ route, path, body string }{{"POST " + path, path, body}, {"DELETE " + path, path, ""}, {"GET " + path + "/{className}", path + "/artifactweave.http", ""}} {
		for _, caller := range []string{"", "anonymous", "user", "user-token", "expired", "forged"} {
			status := http.StatusUnauthorized
			if caller == "user" || caller == "user-token" {
				status = http.StatusForbidden
			}
			rows = append(rows, statusCase{op.route, op.path, caller, op.body, status})
		}
	}
	return append(rows,
		statusCase{"POST " + path, path, "admin", `{"classes":{},"timer":60}`, 400},
		statusCase{"POST " + path, path, "admin", `{"classes":{"artifactweave.http":"invalid"},"timer":60}`, 400},
		statusCase{"POST " + path, path, "admin", `{"classes":{"artifactweave.http":"debug"},"timer":0}`, 400},
		statusCase{"POST " + path, path, "admin", body, 200},
		statusCase{"POST " + path, path, "admin", body, 400},
		statusCase{"GET " + path + "/{className}", path + "/artifactweave.http", "admin", "", 200},
		statusCase{"DELETE " + path, path, "admin", "", 200},
		statusCase{"DELETE " + path, path, "admin-token", "", 200},
		statusCase{"GET " + path + "/{className}", path + "/unknown", "admin-token", "", 200},
	)
}
