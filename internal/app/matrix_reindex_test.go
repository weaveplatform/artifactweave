package app

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
)

// reindexMatrixPart is the reindex operations' share of the matrix:
// Manage on the repository, which only the administrators hold on
// reindexRepos here, and an administrator for Terraform's. The status
// table's reindexer holds Manage alone.
var reindexMatrixPart = matrixPart{
	rows: func() []matrixRow {
		const (
			pypi      = "/artifactory/api/pypi/"
			nuget     = "/artifactory/api/nuget/"
			helm      = "/artifactory/api/helm/"
			terraform = "/artifactory/api/terraform/"
			yum       = "/artifactory/api/yum/"
			deb       = "/artifactory/api/deb/reindex/"
		)
		manages := func(key string) rule { return holds(key, "", api.ActionManage) }
		rows := []matrixRow{
			row(reindexPyPI, pypi+"reindex-pypi/reindex", "", manages("reindex-pypi"), 200),
			row(reindexPyPI, pypi+"reindex-pypi/reindex?async=true", "",
				manages("reindex-pypi"), 200),
			row(reindexNuGet, nuget+"reindex-nuget/reindex", "", manages("reindex-nuget"), 200),
			row(reindexHelm, helm+"reindex-helm/reindex", "", manages("reindex-helm"), 200),
			row(reindexHelmPath, helm+"reindex-helm/charts/team/reindex", "",
				manages("reindex-helm"), 200),
			row(reindexTerraform, terraform+"reindex-terraform/reindex", "", administrators, 200),
			row(reindexYum, yum+"reindex-rpm", "", manages("reindex-rpm"), 200),
			row(reindexDebian, deb+"reindex-debian", "", manages("reindex-debian"), 200),
		}
		for _, r := range formatReindexes {
			rows = append(rows, row(r.route, r.path(r.key()), "", manages(r.key()), 200))
		}
		return rows
	},
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		createReindexRepos(t, f.db)
	},
	tune: withReindexFakes,
}
