package server

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// These are the 17 generated VCS REST operations served by the remote-only
// adapter. The shared fixtures intentionally have no remote source transport:
// an authorized request reaches that boundary and answers 501, while callers
// without repository read permission are refused before reaching it.
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

func vcsMatrixRows() []matrixRow {
	const prefix = "/artifactory/api/vcs/"
	rows := make([]matrixRow, 0, len(vcsTableCases))
	for _, c := range vcsTableCases {
		rows = append(rows, row(
			"GET "+prefix+c.route,
			prefix+c.path,
			"",
			holds("vcs-remote", c.permission, types.ActionRead),
			http.StatusNotImplemented,
		))
	}
	return rows
}

func vcsStatusRows() []statusCase {
	const prefix = "/artifactory/api/vcs/"
	rows := make([]statusCase, 0, len(vcsTableCases)*3)
	for _, c := range vcsTableCases {
		route, path := "GET "+prefix+c.route, prefix+c.path
		rows = append(rows,
			statusCase{route, path, "admin", "", http.StatusNotImplemented},
			statusCase{route, path, "user", "", http.StatusForbidden},
			statusCase{route, path, "", "", http.StatusUnauthorized},
		)
	}
	return rows
}
