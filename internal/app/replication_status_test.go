package app

func replicationStatusRows() []statusCase {
	const base = "/artifactory/api/replications/"
	const member = base + "{repoKey}"
	const mine = base + "rep-status"
	const global = "/artifactory/api/system/replications"
	const body = `{"url":"https://target.example/repository"}`
	return []statusCase{
		{"PUT /artifactory/api/repositories/{repoKey}", "/artifactory/api/repositories/rep-status", "admin", `{"rclass":"local","packageType":"generic"}`, 200},
		{"GET " + member, mine, "admin", "", 404},
		{"PUT " + member, mine, "user", body, 403},
		{"PUT " + member, mine, "", body, 401},
		{"PUT " + member, mine, "admin", `{"url":"invalid"}`, 400},
		{"PUT " + member, mine, "admin", body, 201},
		{"GET " + member, mine, "admin", "", 200},
		{"GET " + member, mine, "user", "", 403},
		{"GET " + member, mine, "", "", 401},
		{"POST " + member, mine, "admin", `{"enabled":false}`, 200},
		{"POST " + member, mine, "admin", `{"socketTimeoutMillis":0}`, 400},
		{"POST " + member, mine, "user", `{}`, 403},
		{"POST " + member, mine, "", `{}`, 401},
		{"POST " + member, base + "missing", "admin", `{}`, 404},
		{"POST " + member, base + "disable", "admin", `{"include":["https://target.example/*"]}`, 200},
		{"POST " + member, base + "enable", "admin", `{}`, 200},
		{"GET " + global, global, "admin", "", 200},
		{"GET " + global, global, "user", "", 403},
		{"GET " + global, global, "", "", 401},
		{"POST " + global + "/block", global + "/block?pull=false", "admin", "", 200},
		{"POST " + global + "/block", global + "/block?pull=invalid", "admin", "", 400},
		{"POST " + global + "/block", global + "/block", "user", "", 403},
		{"POST " + global + "/block", global + "/block", "", "", 401},
		{"POST " + global + "/unblock", global + "/unblock", "admin", "", 200},
		{"POST " + global + "/unblock", global + "/unblock?push=invalid", "admin", "", 400},
		{"POST " + global + "/unblock", global + "/unblock", "user", "", 403},
		{"POST " + global + "/unblock", global + "/unblock", "", "", 401},
		{"DELETE " + member, mine, "user", "", 403},
		{"DELETE " + member, mine, "", "", 401},
		{"DELETE " + member, mine + "?url=https%3A%2F%2Fother.example", "admin", "", 404},
		{"DELETE " + member, mine, "admin", "", 200},
		{"DELETE " + member, mine, "admin", "", 404},
	}
}
