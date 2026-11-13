package app

import (
	"context"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// The reindex operations of the P8 package types (#516), which answer 501
// until their formats rebuild an index. The status table and the matrix
// reach them now through reindexFake, which stands in for each format until
// it lands. A format that lands as a Reindexer then answers the same rows
// itself, and adds its operation to reindexStatusPart's generated, since
// the stub test no longer holds it to 501. The RPM and Debian metadata
// calculations, and formatReindexes, answer from their own formats.

const (
	reindexPyPI      = "POST /artifactory/api/pypi/{repoKey}/reindex"
	reindexNuGet     = "POST /artifactory/api/nuget/{repoKey}/reindex"
	reindexHelm      = "POST /artifactory/api/helm/{repoKey}/reindex"
	reindexHelmPath  = "POST /artifactory/api/helm/{repoKey}/{pathForPartialReindex}/reindex"
	reindexTerraform = "POST /artifactory/api/terraform/{repoKey}/reindex"
	reindexYum       = "POST /artifactory/api/yum/{repoKey}"
	reindexDebian    = "POST /artifactory/api/deb/reindex/{repoKey}"
	reindexAlpine    = "POST /artifactory/api/alpine/{repoKey}/reindex"
	reindexBower     = "POST /artifactory/api/bower/{repoKey}/reindex"
	reindexCargo     = "POST /artifactory/api/cargo/{repoKey}/reindex"
	reindexCocoaPods = "POST /artifactory/api/cocoapods/{repoKey}/reindex"
	reindexConan     = "POST /artifactory/api/conan/{repoKey}/reindex"
	reindexConda     = "POST /artifactory/api/conda/{repoKey}/reindex"
	reindexCran      = "POST /artifactory/api/cran/reindex/{repoKey}"
	reindexGems      = "POST /artifactory/api/gems/{repoKey}/reindex"
	reindexOpkg      = "POST /artifactory/api/opkg/reindex/{repoKey}"
	reindexPub       = "POST /artifactory/api/pub/{repoKey}/reindex"
	reindexSwift     = "POST /artifactory/api/swift/{repoKey}/reindex"
)

// formatReindex is the reindex operation of a package type whose format
// rebuilds its own index, as itemkit.ReindexOps serves it: Manage on the
// repository, and local repositories, with federated ones when hosted.
type formatReindex struct {
	route  string
	t      repo.PackageType
	hosted bool
	// async is whether the operation declares the async query, which the
	// binder then refuses unless it is a boolean.
	async bool
}

// formatReindexes act on reindex-<type>, a local repository, and
// reindex-<type>-fed, a federated one.
var formatReindexes = []formatReindex{
	{reindexAlpine, repo.Alpine, true, false},
	{reindexBower, repo.Bower, true, false},
	{reindexCargo, repo.Cargo, true, true},
	{reindexCocoaPods, repo.CocoaPods, true, false},
	{reindexConan, repo.Conan, true, true},
	{reindexConda, repo.Conda, true, true},
	{reindexCran, repo.Cran, false, true},
	{reindexGems, repo.Gems, true, false},
	{reindexOpkg, repo.Opkg, true, true},
	{reindexPub, repo.Pub, true, true},
	{reindexSwift, repo.Swift, true, false},
}

// key is the local repository r acts on.
func (r formatReindex) key() string { return "reindex-" + string(r.t) }

// path is r's request path for the repository key.
func (r formatReindex) path(key string) string {
	return strings.Replace(strings.TrimPrefix(r.route, "POST "), "{repoKey}", key, 1)
}

// formatReindexRepos are formatReindexes' repositories.
func formatReindexRepos() []repo.Config {
	out := make([]repo.Config, 0, 2*len(formatReindexes))
	for _, r := range formatReindexes {
		out = append(out,
			repo.Config{Key: r.key(), Class: repo.Local, PackageType: r.t},
			repo.Config{Key: r.key() + "-fed", Class: repo.Federated, PackageType: r.t},
		)
	}
	return out
}

// formatReindexStatusRows are formatReindexes' rows: the classes each
// reindexes, its refusals, and its async query where it declares one.
func formatReindexStatusRows() []statusCase {
	const manager = "reindexer:pw"
	var rows []statusCase
	for _, r := range formatReindexes {
		federated := 400
		if r.hosted {
			federated = 200
		}
		rows = append(rows,
			statusCase{r.route, r.path(r.key()), "admin", "", 200},
			statusCase{r.route, r.path(r.key()), manager, "", 200},
			statusCase{r.route, r.path(r.key() + "-fed"), "admin", "", federated},
			statusCase{r.route, r.path(r.key()), "user", "", 403},
			statusCase{r.route, r.path(r.key()), "anonymous", "", 401},
			statusCase{r.route, r.path(r.key()), "", "", 401},
			statusCase{r.route, r.path("missing"), "admin", "", 404},
			statusCase{r.route, r.path("reindex-helm"), "admin", "", 400},
		)
		if r.async {
			rows = append(rows,
				statusCase{r.route, r.path(r.key()) + "?async=true", manager, "", 200},
				statusCase{r.route, r.path(r.key()) + "?async=false", "admin", "", 200},
				statusCase{r.route, r.path(r.key()) + "?async=maybe", "admin", "", 400},
			)
		}
	}
	return rows
}

// p8Types are the package types whose reindex operations P8 serves.
var p8Types = []repo.PackageType{repo.PyPI, repo.NuGet, repo.Helm, repo.Terraform}

// reindexRepos are the repositories the reindex rows act on. Their keys
// are their own, so they never meet the fixture repositories each P8
// format adds under its name.
var reindexRepos = append([]repo.Config{
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
	{Key: "reindex-rpm", Class: repo.Local, PackageType: repo.RPM},
	{
		Key:         "reindex-rpm-remote",
		Class:       repo.Remote,
		PackageType: repo.RPM,
		URL:         "https://up.example",
	},
	{
		Key: "reindex-rpm-virtual", Class: repo.Virtual, PackageType: repo.RPM,
		Repositories: []string{"reindex-rpm"},
	},
	{Key: "reindex-debian", Class: repo.Local, PackageType: repo.Debian},
}, formatReindexRepos()...)

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
func withReindexFakes(c *RouterConfig) {
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
func createReindexRepos(t *testing.T, st *inmem.Store) {
	t.Helper()
	for _, c := range reindexRepos {
		if err := st.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		if c.PackageType == repo.Helm && (c.Class == repo.Local || c.Class == repo.Federated) {
			for _, p := range []string{"charts", "charts/team", "charts/team/app-1.0.0.tgz"} {
				parent, name := path.Split(p)
				if _, err := st.Items().
					Put(context.Background(), artmodel.Item{RepoKey: c.Key, Path: strings.TrimSuffix(parent, "/"), Name: name, Folder: !strings.HasSuffix(p, ".tgz")}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// reindexStatusPart is the reindex operations' share of the status
// table. reindexer holds Manage on their repositories and nothing else,
// which every operation but Terraform's needs.
var reindexStatusPart = statusPart{
	// The partial reindex is matched before routing (HelmPartialReindex).
	handwritten: map[string]bool{reindexHelmPath: true},
	generated:   reindexGenerated(),
	rows: func() []statusCase {
		const (
			pypi      = "/artifactory/api/pypi/"
			nuget     = "/artifactory/api/nuget/"
			helm      = "/artifactory/api/helm/"
			terraform = "/artifactory/api/terraform/"
			yum       = "/artifactory/api/yum/"
			deb       = "/artifactory/api/deb/reindex/"
			manager   = "reindexer:pw"
		)
		return append([]statusCase{
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
			// Remote/cache reindex is P9; virtual reindex remains P10.
			{reindexNuGet, nuget + "reindex-nuget-remote/reindex", "admin", "", 200},
			{reindexNuGet, nuget + "reindex-nuget-remote-cache/reindex", "admin", "", 200},
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
			{reindexHelmPath, helm + "reindex-helm/missing/reindex", "admin", "", 404},
			{reindexHelmPath, helm + "reindex-helm-virtual/charts/reindex", "admin", "", 400},

			// An administrator's alone.
			{reindexTerraform, terraform + "reindex-terraform/reindex", "admin", "", 200},
			{reindexTerraform, terraform + "reindex-terraform/reindex", manager, "", 403},
			{reindexTerraform, terraform + "reindex-terraform/reindex", "user", "", 403},
			{reindexTerraform, terraform + "reindex-terraform/reindex", "", "", 401},
			{reindexTerraform, terraform + "missing/reindex", "admin", "", 404},
			{reindexTerraform, terraform + "reindex-helm/reindex", "admin", "", 400},

			// Synchronous unless async=true. A virtual repository's
			// calculation composes its members' metadata; a remote
			// repository's is not served yet.
			{reindexYum, yum + "reindex-rpm", "admin", "", 200},
			{reindexYum, yum + "reindex-rpm?async=true", manager, "", 200},
			{reindexYum, yum + "reindex-rpm", "user", "", 403},
			{reindexYum, yum + "reindex-rpm", "", "", 401},
			{reindexYum, yum + "missing", "admin", "", 404},
			{reindexYum, yum + "reindex-helm", "admin", "", 400},
			{reindexYum, yum + "reindex-rpm?path=x/repodata", "admin", "", 400},
			{reindexYum, yum + "reindex-rpm-virtual", manager, "", 200},
			{reindexYum, yum + "reindex-rpm-remote", "admin", "", 501},

			// Local repositories only; writeProps=0 leaves the packages'
			// properties alone.
			{reindexDebian, deb + "reindex-debian", "admin", "", 200},
			{reindexDebian, deb + "reindex-debian?writeProps=0", manager, "", 200},
			{reindexDebian, deb + "reindex-debian?writeProps=2", "admin", "", 400},
			{reindexDebian, deb + "reindex-debian", "user", "", 403},
			{reindexDebian, deb + "reindex-debian", "", "", 401},
			{reindexDebian, deb + "missing", "admin", "", 404},
			{reindexDebian, deb + "reindex-helm", "admin", "", 400},
		}, formatReindexStatusRows()...)
	},
	seed: func(t *testing.T, db *inmem.Store, _ blob.Store) {
		t.Helper()
		ctx := context.Background()
		createReindexRepos(t, db)
		if err := db.Users().Create(ctx, accesstest.User{
			Name: "reindexer", PasswordHash: cheapHash,
		}); err != nil {
			t.Fatal(err)
		}
		targets := map[string]accesstest.Target{}
		for _, c := range reindexRepos {
			targets[c.Key] = accesstest.Target{Include: []string{"**"}}
		}
		if err := db.Permissions().Create(ctx, accesstest.Permission{
			Name: "reindex-manage",
			Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{
					"reindexer": api.ActionManage,
				}},
				Targets: targets,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	},
	tune: withReindexFakes,
}

// reindexGenerated are the reindex operations the rows hold to their
// formats' answers, which the stub test then no longer holds to 501.
func reindexGenerated() map[string]bool {
	out := map[string]bool{
		reindexPyPI: true, reindexNuGet: true, reindexHelm: true, reindexTerraform: true,
		reindexYum: true, reindexDebian: true,
	}
	for _, r := range formatReindexes {
		out[r.route] = true
	}
	return out
}
