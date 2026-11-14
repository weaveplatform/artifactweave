package sql

import "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"

// RepoPageQueries are the statements Repos().Page sends for l, so the plan
// test explains exactly those.
func RepoPageQueries(l port.RepoListing) (count, window string, countArgs, windowArgs []any) {
	return repoPageQueries(l)
}
