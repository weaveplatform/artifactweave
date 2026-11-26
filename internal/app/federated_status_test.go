package app

// federatedStatusRows are the federated repository operations' answers
// (FED-01, FED-04): 401 and 403 before anything about the request is
// disclosed, then the operation's own 400 and 404.
func federatedStatusRows() []statusCase {
	const repos = "/artifactory/api/repositories/"
	const base = "/artifactory/api/federation/"
	const sync, migrate = "POST " + base + "configSync/{repoKey}", "POST " + base + "migrate/{repoKey}"
	const convert, replace, update = "POST " + base + "convertToLocal", "POST " + base + "replaceUrl", "POST " + base + "updateMemberUrl"
	const urls = `{"oldBaseUrl":"https://old.example","newBaseUrl":"https://new.example"}`
	const noURL = `{"oldBaseUrl":"","newBaseUrl":"https://new.example"}`
	const full, force = "POST " + base + "fullSync/{repoKey}", "POST " + base + "forceFullSync/{repoKey}"
	const recovery, reconnect = "POST " + base + "recovery", "POST " + base + "reconnect"
	const remove, prio = "POST " + base + "removeMember", "PUT " + base + "prioritization/{repoKey}"
	const propagate = "PUT " + base + "propagateCreatedTime"
	return []statusCase{
		{"GET " + base + "status/connectionStates/{repoKey}", base + "status/connectionStates/missing", "user", "", 403},
		{"GET " + base + "status/connectionStates/{repoKey}", base + "status/connectionStates/missing", "", "", 401},
		{"GET " + base + "status/connectionStates/{repoKey}", base + "status/connectionStates/missing", "admin", "", 404},
		{"PUT " + repos + "{repoKey}", repos + "fed-status", "admin", `{"rclass":"federated","packageType":"generic"}`, 200},
		{"PUT " + repos + "{repoKey}", repos + "fed-local-status", "admin", `{"rclass":"local","packageType":"generic"}`, 200},
		{"GET " + base + "status/connectionStates/{repoKey}", base + "status/connectionStates/fed-local-status", "admin", "", 400},
		{"GET " + base + "status/connectionStates/{repoKey}", base + "status/connectionStates/fed-status", "admin", "", 200},
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
		// FED-04's rows have repositories of their own: the rows above
		// convert fed-status and fed-local-status back and forth.
		{"PUT " + repos + "{repoKey}", repos + "fed4-status", "admin", `{"rclass":"federated","packageType":"generic"}`, 200},
		{"PUT " + repos + "{repoKey}", repos + "fed4-local-status", "admin", `{"rclass":"local","packageType":"generic"}`, 200},
		{full, base + "fullSync/fed4-status", "user", "", 403},
		{full, base + "fullSync/fed4-status", "", "", 401},
		{full, base + "fullSync/missing", "admin", "", 404},
		{full, base + "fullSync/fed4-local-status", "admin", "", 400},
		{full, base + "fullSync/fed4-status?mirror=https://nobody.example", "admin", "", 400},
		// isDry is a registered deviation, refused after the caller is.
		{full, base + "fullSync/fed4-status?isDry=true", "user", "", 403},
		{full, base + "fullSync/fed4-status?isDry=true", "admin", "", 400},
		{full, base + "fullSync/fed4-status?isDry=false", "admin", "", 200},
		{full, base + "fullSync/fed4-status", "admin", "", 200},
		{force, base + "forceFullSync/fed4-status", "user", "", 403},
		{force, base + "forceFullSync/fed4-status", "", "", 401},
		{force, base + "forceFullSync/missing", "admin", "", 404},
		{force, base + "forceFullSync/fed4-local-status", "admin", "", 400},
		{force, base + "forceFullSync/fed4-status?mirror=https://nobody.example", "admin", "", 400},
		{force, base + "forceFullSync/fed4-status", "admin", "", 200},
		{recovery, base + "recovery", "user", "", 403},
		{recovery, base + "recovery", "", "", 401},
		{recovery, base + "recovery?repoKey=missing", "admin", "", 404},
		{recovery, base + "recovery?repoKey=fed4-local-status", "admin", "", 400},
		{recovery, base + "recovery?repoKey=fed4-status&force=true", "admin", "", 200},
		{recovery, base + "recovery", "admin", "", 200},
		{reconnect, base + "reconnect", "user", "", 403},
		{reconnect, base + "reconnect", "", "", 401},
		{reconnect, base + "reconnect", "admin", "", 200},
		{remove, base + "removeMember", "user", "https://nobody.example", 403},
		{remove, base + "removeMember", "", "https://nobody.example", 401},
		{remove, base + "removeMember", "admin", "", 400},
		{remove, base + "removeMember", "admin", "https://nobody.example", 400},
		{prio, base + "prioritization/fed4-status?priorityLevel=high", "user", "", 403},
		{prio, base + "prioritization/fed4-status?priorityLevel=high", "", "", 401},
		{prio, base + "prioritization/missing?priorityLevel=high", "admin", "", 404},
		{prio, base + "prioritization/fed4-local-status?priorityLevel=high", "admin", "", 400},
		{prio, base + "prioritization/fed4-status?priorityLevel=urgent", "admin", "", 400},
		{prio, base + "prioritization/fed4-status?priorityLevel=HIGH", "admin", "", 200},
		{prio, base + "prioritization/fed4-status?priorityLevel=standard", "admin", "", 200},
		{propagate, base + "propagateCreatedTime?enabled=true", "user", "", 403},
		{propagate, base + "propagateCreatedTime?enabled=true", "", "", 401},
		{propagate, base + "propagateCreatedTime", "admin", "", 400},
		{propagate, base + "propagateCreatedTime?enabled=true", "admin", "", 200},
		{propagate, base + "propagateCreatedTime?enabled=false", "admin", "", 200},
	}
}
