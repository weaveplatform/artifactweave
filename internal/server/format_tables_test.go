package server

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Each format with pull requests of its own keeps its share of the status
// table, the route × principal matrix and their guards in
// status_<fmt>_test.go and matrix_<fmt>_test.go (decision 0032), listed in
// statusParts and matrixParts. A format's pull request then touches only
// its own files. Every field may be left empty.

// statusPart is a format's share of the status table.
type statusPart struct {
	// handwritten are the hand-written routes the format implements, by
	// "METHOD pattern"; the guards require rows for each.
	handwritten map[string]bool
	// generated are the generated JFrog operations it implements, which
	// then no longer answer 501.
	generated map[string]bool
	rows      func() []statusCase
	// seed adds what the rows act on to the status fixture.
	seed func(t *testing.T, db store.Store, blobs blob.Store)
	// tune adjusts the fixture server's configuration.
	tune func(*Config)
}

// statusParts are the formats' shares of the status table.
var statusParts = []statusPart{
	npmStatusPart, goStatusPart, mavenStatusPart, pkgtestStatusPart,
	npmAuthStatusPart, npmLifecycleStatusPart, reindexStatusPart,
}

// matrixPart is a format's share of the route × principal matrix.
type matrixPart struct {
	rows func() []matrixRow
	// seed adds what the rows act on to each matrix fixture; a row's own
	// seeder adds what it acts on for one principal.
	seed func(t *testing.T, f *matrixFixture)
	tune func(*Config)
}

// matrixParts are the formats' shares of the matrix.
var matrixParts = []matrixPart{
	npmMatrixPart, goMatrixPart, mavenMatrixPart, pkgtestMatrixPart,
	npmAuthMatrixPart, npmLifecycleMatrixPart, reindexMatrixPart,
}

// partRoutes is the routes each status part lists under pick.
func partRoutes(pick func(statusPart) map[string]bool) []map[string]bool {
	out := make([]map[string]bool, 0, len(statusParts))
	for _, p := range statusParts {
		out = append(out, pick(p))
	}
	return out
}

func partStatusRows() []statusCase {
	var rows []statusCase
	for _, p := range statusParts {
		if p.rows != nil {
			rows = append(rows, p.rows()...)
		}
	}
	return rows
}

func partMatrixRows() []matrixRow {
	var rows []matrixRow
	for _, p := range matrixParts {
		if p.rows != nil {
			rows = append(rows, p.rows()...)
		}
	}
	return rows
}

// formatRepos are the fixture repositories of the formats P7 adds, which
// both fixtures create and the matrix's principals hold grants on: a
// local, a remote and a virtual repository of each.
func formatRepos() []repo.Config {
	return slices.Concat(npmRepos, goRepos, mavenRepos)
}

// fixtureRepos is a format's fixture repositories, all of package type t:
// <name>-local, <name>-remote, and <name>-virtual over the local one and
// deploying to it.
func fixtureRepos(name string, t repo.PackageType) []repo.Config {
	local := name + "-local"
	return []repo.Config{
		{Key: local, Class: repo.Local, PackageType: t},
		{Key: name + "-remote", Class: repo.Remote, PackageType: t, URL: "https://up.example"},
		{
			Key: name + "-virtual", Class: repo.Virtual, PackageType: t,
			Repositories: []string{local}, DefaultDeploymentRepo: local,
		},
	}
}

// formatGrants is a grant of a on every format repository under the
// prefix.
func formatGrants(under string, a types.Actions) []matrixGrant {
	repos := formatRepos()
	out := make([]matrixGrant, 0, len(repos))
	for _, c := range repos {
		out = append(out, matrixGrant{c.Key, under, a})
	}
	return out
}

// formatTargets is a permission target on every format repository, for
// paths under the prefix.
func formatTargets(under string) map[string]types.Target {
	out := map[string]types.Target{}
	for _, c := range formatRepos() {
		out[c.Key] = types.Target{Include: []string{under + "**"}}
	}
	return out
}
