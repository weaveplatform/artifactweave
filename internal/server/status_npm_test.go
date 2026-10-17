package server

import "github.com/weaveplatform/artifactweave/pkg/repo"

// npmRepos are npm's fixture repositories: npm-local, npm-remote and
// npm-virtual.
var npmRepos = fixtureRepos("npm", repo.NPM)

// npmReindex is the JFrog reindex operation, live since the npm format
// rebuilds its index (#286).
const npmReindex = "POST /artifactory/api/npm/{repoKey}/reindex"

// npmStatusPart is npm's share of the status table, which its pull
// requests (#286–#294) fill in.
var npmStatusPart = statusPart{
	generated: map[string]bool{npmReindex: true},
	rows: func() []statusCase {
		const base = "/artifactory/api/npm/"
		return []statusCase{
			{npmReindex, base + "npm-local/reindex", "admin", "", 200},
			{npmReindex, base + "npm-local/reindex", "user", "", 403},
			{npmReindex, base + "npm-local/reindex", "anonymous", "", 401},
			{npmReindex, base + "npm-local/reindex", "", "", 401},
			{npmReindex, base + "missing/reindex", "admin", "", 404},
			{npmReindex, base + "libs-local/reindex", "admin", "", 400},
			// Only local repositories have tarballs to index (decision 0034).
			{npmReindex, base + "npm-remote/reindex", "admin", "", 400},
			{npmReindex, base + "npm-virtual/reindex", "admin", "", 400},
		}
	},
}
