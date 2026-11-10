package app

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// Formats still hosted in the root server test package keep their status and
// route × principal rows in status_<fmt>_test.go and matrix_<fmt>_test.go
// (decision 0032). NuGet, npm, Go and Maven now run their rows in
// test/server/artifacts/<format>.
// Every field may be left empty.

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
	seed func(t *testing.T, db *inmem.Store, blobs blob.Store)
	// tune adjusts the fixture server's configuration.
	tune func(*RouterConfig)
}

// statusParts are the formats' shares of the status table.
var statusParts = []statusPart{
	apiRestrictionStatusPart,
	nixStatusPart,
	machineLearningStatusPart,
	puppetStatusPart,
	chefStatusPart,
	pkgtestStatusPart,
	reindexStatusPart,
	cranArchiveStatusPart,
}

// matrixPart is a format's share of the route × principal matrix.
type matrixPart struct {
	rows func() []matrixRow
	// seed adds what the rows act on to each matrix fixture; a row's own
	// seeder adds what it acts on for one principal.
	seed func(t *testing.T, f *matrixFixture)
	tune func(*RouterConfig)
}

// matrixParts are the formats' shares of the matrix.
var matrixParts = []matrixPart{
	apiRestrictionMatrixPart,
	nixMatrixPart,
	machineLearningMatrixPart,
	puppetMatrixPart,
	chefMatrixPart,
	pkgtestMatrixPart,
	reindexMatrixPart,
	cranArchiveMatrixPart,
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

// Shared root fixtures still include Go repositories while their protocol
// route assertions run in test/server/artifacts/golang.
var goRepos = fixtureRepos("go", repo.Go)

// Shared root fixtures retain Helm repositories for cross-format assertions;
// classic Helm route assertions run in test/server/artifacts/helm.
var helmRepos = fixtureRepos("helm", repo.Helm)

// Shared root fixtures still include Maven repositories while their route
// assertions run in test/server/artifacts/maven.
var mavenRepos = append(fixtureRepos("maven", repo.Maven), repo.Config{
	Key: "maven-releases", Class: repo.Local, PackageType: repo.Maven, HandleSnapshots: new(bool),
})

// Shared root fixtures retain PyPI repositories for cross-format assertions;
// PyPI route assertions run in test/server/artifacts/pypi.
var pypiRepos = fixtureRepos("pypi", repo.PyPI)

// formatRepos are the fixture repositories of the formats P7 and P8 add,
// which both fixtures create and the matrix's principals hold grants on: a
// local, a remote and a virtual repository of each, or a local one alone
// for a package type that is local only.
func formatRepos() []repo.Config {
	return slices.Concat(
		npmRepos,
		goRepos,
		mavenRepos,
		helmRepos,
		support.TerraformBackendRepos(),
		pypiRepos,
		support.TerraformRegistryRepos(),
		support.TerraformProviderRepos(),
		[]repo.Config{{
			Key: "vcs-remote", Class: repo.Remote, PackageType: repo.VCS,
			URL: "https://github.example", VCSGitProvider: repo.GitHub,
		}},
	)
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
func formatGrants(under string, a api.Actions) []matrixGrant {
	repos := formatRepos()
	out := make([]matrixGrant, 0, len(repos))
	for _, c := range repos {
		out = append(out, matrixGrant{c.Key, under, a})
	}
	return out
}

// formatTargets is a permission target on every format repository, for
// paths under the prefix.
func formatTargets(under string) map[string]accesstest.Target {
	out := map[string]accesstest.Target{}
	for _, c := range formatRepos() {
		out[c.Key] = accesstest.Target{Include: []string{under + "**"}}
	}
	return out
}
