package app

// federatedStatusRows are the federated repository operations' answers
// (FED-01): 401 and 403 before anything about the request is disclosed,
// then the operation's own 400 and 404.
func federatedStatusRows() []statusCase {
	const repos = "/artifactory/api/repositories/"
	const base = "/artifactory/api/federation/"
	const sync, migrate = "POST " + base + "configSync/{repoKey}", "POST " + base + "migrate/{repoKey}"
	const convert, replace, update = "POST " + base + "convertToLocal", "POST " + base + "replaceUrl", "POST " + base + "updateMemberUrl"
	const urls = `{"oldBaseUrl":"https://old.example","newBaseUrl":"https://new.example"}`
	const noURL = `{"oldBaseUrl":"","newBaseUrl":"https://new.example"}`
	return []statusCase{
		{"PUT " + repos + "{repoKey}", repos + "fed-status", "admin", `{"rclass":"federated","packageType":"generic"}`, 200},
		{"PUT " + repos + "{repoKey}", repos + "fed-local-status", "admin", `{"rclass":"local","packageType":"generic"}`, 200},
		{sync, base + "configSync/fed-status", "user", "", 403},
		{sync, base + "configSync/fed-status", "", "", 401},
		{sync, base + "configSync/missing", "admin", "", 404},
		{sync, base + "configSync/fed-local-status", "admin", "", 400},
		{sync, base + "configSync/fed-status", "admin", "", 200},
		{migrate, base + "migrate/fed-local-status", "user", "", 403},
		{migrate, base + "migrate/fed-local-status", "", "", 401},
		{migrate, base + "migrate/missing", "admin", "", 400},
		{migrate, base + "migrate/fed-status", "admin", "", 400},
		{migrate, base + "migrate/fed-local-status", "admin", "", 200},
		{convert, base + "convertToLocal", "user", `["fed-status"]`, 403},
		{convert, base + "convertToLocal", "", `["fed-status"]`, 401},
		{convert, base + "convertToLocal", "admin", `[]`, 400},
		{convert, base + "convertToLocal", "admin", `["missing"]`, 400},
		{convert, base + "convertToLocal", "admin", `["fed-status","missing"]`, 200},
		{convert, base + "convertToLocal", "admin", `["fed-local-status"]`, 200},
		{replace, base + "replaceUrl", "user", urls, 403},
		{replace, base + "replaceUrl", "", urls, 401},
		{replace, base + "replaceUrl", "admin", noURL, 400},
		{replace, base + "replaceUrl", "admin", urls, 200},
		{update, base + "updateMemberUrl", "user", urls, 403},
		{update, base + "updateMemberUrl", "", urls, 401},
		{update, base + "updateMemberUrl", "admin", noURL, 400},
		{update, base + "updateMemberUrl", "admin", urls, 200},
	}
}
