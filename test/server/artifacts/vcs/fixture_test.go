package vcs_test

// This file holds the VCS suite's fixtures: adapters over
// test/server/support/harness and test/server/support, and copies of the VCS
// route table of app's router guards and of vcshttp's ref type. The router is
// the harness's, so a change to its assembly reaches these tests without an
// edit here.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// Config is the router configuration a test adjusts before the router is
// built.
type Config = harness.Config

// itemWorld is the harness's item world under the fields the tests read.
type itemWorld struct {
	t     *testing.T
	h     http.Handler
	db    *inmem.Store
	blobs blob.Store
}

func newItemWorld(t *testing.T, anonymous bool, tune ...harness.Tune) *itemWorld {
	t.Helper()
	w := harness.NewItemWorld(t, anonymous, tune...)
	return &itemWorld{t: w.T, h: w.H, db: w.DB, blobs: w.Blobs}
}

func (w *itemWorld) world() *harness.ItemWorld {
	return &harness.ItemWorld{T: w.t, H: w.h, DB: w.db, Blobs: w.blobs}
}

func (w *itemWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	return w.world().Do(method, path, as, body, headers...)
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	harness.WantStatus(t, rec, want)
}

// sourceIdentity resolves every remote repository to one fixed HTTP source
// identity, as the root fixture of the same name does.
func sourceIdentity(context.Context, repo.Config) (types.SourceIdentity, error) {
	return types.SourceIdentity{
		Kind:       types.ExternalHTTP,
		Repository: "repo-1",
		ID:         "http-1",
		Generation: "revision-1",
	}, nil
}

// sourceBackend is a remote source transport made of one function.
type sourceBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f sourceBackend) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}

// vcsListRef is one ref of a VCS branches or tags answer, with the wire
// fields of the router's own type.
type vcsListRef struct {
	Name     string `json:"name"`
	CommitID string `json:"commitId"` //nolint:tagliatelle // JFrog VCS wire field.
	IsBranch bool   `json:"isBranch"` //nolint:tagliatelle // JFrog VCS wire field.
}

// vcsTableCases are the 17 generated VCS REST operations that the remote-only
// adapter serves, as app's vcs_tables_test.go lists them for the status and
// principal guards, which stay with the router. A route added there belongs
// here too, so that every route is sent upstream.
var vcsTableCases = []struct{ route, path, permission string }{
	{"branches/{repoKey}/{userOrg}/{repo}", "branches/vcs-remote/team/project", "team/project"},
	{
		"downloadBranch/{repoKey}/{userOrg}/{branchName}",
		"downloadBranch/vcs-remote/team/main",
		"team/team/refs/heads/main",
	},
	{
		"downloadBranch/{repoKey}/{userOrg}/{repo}/archive/refs/{branchName}",
		"downloadBranch/vcs-remote/team/project/archive/refs/main",
		"team/project/refs/heads/main",
	},
	{
		"downloadBranch/{repoKey}/{userOrg}/{repo}/{branchName}",
		"downloadBranch/vcs-remote/team/project/main",
		"team/project/refs/heads/main",
	},
	{
		"downloadBranchFile/{repoKey}/{userOrg}/{branchName}",
		"downloadBranchFile/vcs-remote/team/main!README.md",
		"team/team/refs/heads/main/README.md",
	},
	{
		"downloadBranchFile/{repoKey}/{userOrg}/{repo}/archive/refs/{branchName}",
		"downloadBranchFile/vcs-remote/team/project/archive/refs/main!README.md",
		"team/project/refs/heads/main/README.md",
	},
	{
		"downloadBranchFile/{repoKey}/{userOrg}/{repo}/{branchName}",
		"downloadBranchFile/vcs-remote/team/project/main!README.md",
		"team/project/refs/heads/main/README.md",
	},
	{
		"downloadCommit/{repoKey}/{userOrg}/{repo}/{commitId}",
		"downloadCommit/vcs-remote/team/project/0123456789abcdef0123456789abcdef01234567",
		"team/project/refs/commits/0123456789abcdef0123456789abcdef01234567",
	},
	{
		"downloadRelease/{repoKey}/{userOrg}/{repo}/{releaseName}",
		"downloadRelease/vcs-remote/team/project/v1.0.0",
		"team/project/refs/tags/v1.0.0",
	},
	{
		"downloadTag/{repoKey}/{userOrg}/{repo}/archive/refs/{tagName}",
		"downloadTag/vcs-remote/team/project/archive/refs/v1.0.0",
		"team/project/refs/tags/v1.0.0",
	},
	{
		"downloadTag/{repoKey}/{userOrg}/{repo}/{tagName}",
		"downloadTag/vcs-remote/team/project/v1.0.0",
		"team/project/refs/tags/v1.0.0",
	},
	{
		"downloadTag/{repoKey}/{userOrg}/{tagName}",
		"downloadTag/vcs-remote/team/v1.0.0",
		"team/team/refs/tags/v1.0.0",
	},
	{
		"downloadTagFile/{repoKey}/{userOrg}/{repo}/archive/refs/{tagName}",
		"downloadTagFile/vcs-remote/team/project/archive/refs/v1.0.0!README.md",
		"team/project/refs/tags/v1.0.0/README.md",
	},
	{
		"downloadTagFile/{repoKey}/{userOrg}/{repo}/{tagName}",
		"downloadTagFile/vcs-remote/team/project/v1.0.0!README.md",
		"team/project/refs/tags/v1.0.0/README.md",
	},
	{
		"downloadTagFile/{repoKey}/{userOrg}/{tagName}",
		"downloadTagFile/vcs-remote/team/v1.0.0!README.md",
		"team/team/refs/tags/v1.0.0/README.md",
	},
	{"refs/{repoKey}/{userOrg}/{repo}", "refs/vcs-remote/team/project", "team/project"},
	{"tags/{repoKey}/{userOrg}/{repo}", "tags/vcs-remote/team/project", "team/project"},
}
