package server

import (
	"context"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The reindex operations of the P8 package types (#516), which answer 501
// until their formats rebuild an index. The status table and the matrix
// reach them now through reindexFake, which stands in for each format until
// it lands. A format that lands as a Reindexer then answers the same rows
// itself, and adds its operation to reindexStatusPart's generated, since
// the stub test no longer holds it to 501.

const (
	reindexPyPI      = "POST /artifactory/api/pypi/{repoKey}/reindex"
	reindexNuGet     = "POST /artifactory/api/nuget/{repoKey}/reindex"
	reindexHelm      = "POST /artifactory/api/helm/{repoKey}/reindex"
	reindexHelmPath  = "POST /artifactory/api/helm/{repoKey}/{pathForPartialReindex}/reindex"
	reindexTerraform = "POST /artifactory/api/terraform/{repoKey}/reindex"
)

// p8Types are the package types whose reindex operations P8 serves.
var p8Types = []repo.PackageType{repo.PyPI, repo.NuGet, repo.Helm, repo.Terraform}

// reindexRepos are the repositories the reindex rows act on. Their keys
// are their own, so they never meet the fixture repositories each P8
// format adds under its name.
var reindexRepos = []repo.Config{
	{Key: "reindex-pypi", Class: repo.Local, PackageType: repo.PyPI},
	{
		Key:         "reindex-pypi-remote",
		Class:       repo.Remote,
		PackageType: repo.PyPI,
		URL:         "https://up.example",
	},
	{Key: "reindex-nuget", Class: repo.Local, PackageType: repo.NuGet},
	{
		Key:         "reindex-nuget-remote",
		Class:       repo.Remote,
		PackageType: repo.NuGet,
		URL:         "https://up.example",
	},
	{
		Key: "reindex-nuget-virtual", Class: repo.Virtual, PackageType: repo.NuGet,
		Repositories: []string{"reindex-nuget"},
	},
	{Key: "reindex-helm", Class: repo.Local, PackageType: repo.Helm},
	{Key: "reindex-helm-fed", Class: repo.Federated, PackageType: repo.Helm},
	{
		Key: "reindex-helm-virtual", Class: repo.Virtual, PackageType: repo.Helm,
		Repositories: []string{"reindex-helm"},
	},
	{
		Key: "reindex-terraform", Class: repo.Local, PackageType: repo.Terraform,
		TerraformType: repo.TerraformModule,
	},
}

// reindexFake is the format of a P8 package type until its own lands: the
// generic format made a PathReindexer whose rebuilds change nothing.
// reindex_test.go shows what the queue runs.
type reindexFake struct{ generic.Handler }

func (reindexFake) Reindex(context.Context, format.Env, repo.Config) error { return nil }

func (reindexFake) ReindexPath(context.Context, format.Env, repo.Config, string) error {
	return nil
}

// withReindexFakes gives each P8 package type whose format is still the
// generic one reindexFake. A type whose own format has landed keeps it.
func withReindexFakes(c *Config) {
	next := c.Formats
	if next == nil {
		next = c.FormatSettings.For
	}
	c.Formats = func(t repo.PackageType) format.Handler {
		h := next(t)
		if _, plain := h.(generic.Handler); plain && slices.Contains(p8Types, t) {
			return reindexFake{}
		}
		return h
	}
}

// createReindexRepos creates reindexRepos in st.
func createReindexRepos(t *testing.T, st store.Store) {
	t.Helper()
	for _, c := range reindexRepos {
		if err := st.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
}

// reindexStatusPart is the P8 reindex operations' share of the status
// table. reindexer holds Manage on their repositories and nothing else,
// which every operation but Terraform's needs.
var reindexStatusPart = statusPart{
	// The partial reindex is matched before routing (helmPartialReindex).
	handwritten: map[string]bool{reindexHelmPath: true},
	generated:   map[string]bool{reindexPyPI: true},
	rows: func() []statusCase {
		const (
			pypi      = "/artifactory/api/pypi/"
			nuget     = "/artifactory/api/nuget/"
			helm      = "/artifactory/api/helm/"
			terraform = "/artifactory/api/terraform/"
			manager   = "reindexer:pw"
		)
		return []statusCase{
			// Synchronous by default, and at once with async.
			{reindexPyPI, pypi + "reindex-pypi/reindex", "admin", "", 200},
			{reindexPyPI, pypi + "reindex-pypi/reindex?async=true", "admin", "", 200},
			{reindexPyPI, pypi + "reindex-pypi/reindex?async=1", manager, "", 200},
			{reindexPyPI, pypi + "reindex-pypi/reindex?async=maybe", "admin", "", 400},
			{reindexPyPI, pypi + "reindex-pypi/reindex", manager, "", 200},
			{reindexPyPI, pypi + "reindex-pypi/reindex", "user", "", 403},
			{reindexPyPI, pypi + "reindex-pypi/reindex", "anonymous", "", 401},
			{reindexPyPI, pypi + "reindex-pypi/reindex", "", "", 401},
			{reindexPyPI, pypi + "missing/reindex", "admin", "", 404},
			{reindexPyPI, pypi + "reindex-nuget/reindex", "admin", "", 400},
			// Local repositories only.
			{reindexPyPI, pypi + "reindex-pypi-remote/reindex", "admin", "", 400},
			{reindexPyPI, pypi + "reindex-pypi-remote-cache/reindex", "admin", "", 400},

			{reindexNuGet, nuget + "reindex-nuget/reindex", "admin", "", 200},
			{reindexNuGet, nuget + "reindex-nuget/reindex", manager, "", 200},
			{reindexNuGet, nuget + "reindex-nuget/reindex", "user", "", 403},
			{reindexNuGet, nuget + "reindex-nuget/reindex", "", "", 401},
			{reindexNuGet, nuget + "missing/reindex", "admin", "", 404},
			{reindexNuGet, nuget + "reindex-helm/reindex", "admin", "", 400},
			// 7.x reindexes them; they are P9's and P10's.
			{reindexNuGet, nuget + "reindex-nuget-remote/reindex", "admin", "", 501},
			{reindexNuGet, nuget + "reindex-nuget-remote-cache/reindex", "admin", "", 501},
			{reindexNuGet, nuget + "reindex-nuget-virtual/reindex", manager, "", 501},
			{reindexNuGet, nuget + "reindex-nuget-virtual/reindex", "user", "", 403},

			{reindexHelm, helm + "reindex-helm/reindex", "admin", "", 200},
			{reindexHelm, helm + "reindex-helm/reindex", manager, "", 200},
			{reindexHelm, helm + "reindex-helm/reindex", "user", "", 403},
			{reindexHelm, helm + "reindex-helm/reindex", "", "", 401},
			{reindexHelm, helm + "missing/reindex", "admin", "", 404},
			// Local and federated repositories only.
			{reindexHelm, helm + "reindex-helm-fed/reindex", manager, "", 200},
			{reindexHelm, helm + "reindex-helm-virtual/reindex", "admin", "", 400},
			{reindexHelm, helm + "reindex-pypi/reindex", "admin", "", 400},

			{reindexHelmPath, helm + "reindex-helm/charts/reindex", "admin", "", 200},
			{
				reindexHelmPath,
				helm + "reindex-helm/charts/team/app-1.0.0.tgz/reindex",
				manager,
				"",
				200,
			},
			{reindexHelmPath, helm + "reindex-helm-fed/charts/reindex", "admin", "", 200},
			{reindexHelmPath, helm + "reindex-helm/charts/reindex", "user", "", 403},
			{reindexHelmPath, helm + "reindex-helm/charts/reindex", "anonymous", "", 401},
			{reindexHelmPath, helm + "reindex-helm/charts/reindex", "", "", 401},
			{reindexHelmPath, helm + "missing/charts/reindex", "admin", "", 404},
			{reindexHelmPath, helm + "reindex-helm-virtual/charts/reindex", "admin", "", 400},

			// An administrator's alone.
			{reindexTerraform, terraform + "reindex-terraform/reindex", "admin", "", 200},
			{reindexTerraform, terraform + "reindex-terraform/reindex", manager, "", 403},
			{reindexTerraform, terraform + "reindex-terraform/reindex", "user", "", 403},
			{reindexTerraform, terraform + "reindex-terraform/reindex", "", "", 401},
			{reindexTerraform, terraform + "missing/reindex", "admin", "", 404},
			{reindexTerraform, terraform + "reindex-helm/reindex", "admin", "", 400},
		}
	},
	seed: func(t *testing.T, db store.Store, _ blob.Store) {
		t.Helper()
		ctx := context.Background()
		createReindexRepos(t, db)
		if err := db.Users().Create(ctx, types.User{
			Name: "reindexer", PasswordHash: cheapHash,
		}); err != nil {
			t.Fatal(err)
		}
		targets := map[string]types.Target{}
		for _, c := range reindexRepos {
			targets[c.Key] = types.Target{Include: []string{"**"}}
		}
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: "reindex-manage",
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					"reindexer": types.ActionManage,
				}},
				Targets: targets,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	},
	tune: withReindexFakes,
}
