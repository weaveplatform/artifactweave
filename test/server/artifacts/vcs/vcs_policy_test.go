package vcs_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// This pins the path at which the download policy checks a VCS remote's
// references. The refs that a listing and every tag, branch or release
// download read first are checked at 7.x's cached refs file,
// <owner>/<name>/gitrefs (RTFACT-30171): an exclude such as
// team/project/* refuses them, as RTFACT-30743 shows 7.x refusing
// jqlang/jq/*, where the project folder admitted it, and the refusal names
// that file without asking upstream. Read permission is still checked
// first, at the folder, a commit download reads no refs, and the blackout
// refuses the refs at the same path.
func TestAccVCSRefsFollowThePolicyAtTheirCachedPath(t *testing.T) {
	var calls int
	w := vcsWorldWithSource(
		t,
		func(ctx context.Context, q source.Request, permit source.Permit) (source.Result, error) {
			calls++
			return vcsProviderData(t, ctx, q, permit)
		},
	)
	patch := func(change func(*repo.Config)) {
		t.Helper()
		c, err := w.db.Repos().Get(t.Context(), "vcs-gitlab")
		if err != nil {
			t.Fatal(err)
		}
		change(&c)
		if err = w.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	const base = "/artifactory/api/vcs/"
	refsFirst := []string{
		"tags/vcs-gitlab/team/project",
		"branches/vcs-gitlab/team/project",
		"refs/vcs-gitlab/team/project",
		"downloadTag/vcs-gitlab/team/project/v1.0.0",
		"downloadBranch/vcs-gitlab/team/project/main",
		"downloadRelease/vcs-gitlab/team/project/v1.0.0",
		"downloadTagFile/vcs-gitlab/team/project/v1.0.0!README.md",
	}
	refused := func(path, message string) {
		t.Helper()
		res := w.do(http.MethodGet, base+path, "reader", "")
		wantStatus(t, res, http.StatusNotFound)
		if !strings.Contains(res.Body.String(), message) {
			t.Fatalf("%s: %s, want %q", path, res.Body.String(), message)
		}
		wantStatus(t, w.do(http.MethodGet, base+path, "user", ""), http.StatusForbidden)
	}
	for _, exclude := range []string{"team/project/*", "**/gitrefs"} {
		patch(func(c *repo.Config) { c.ExcludesPattern = &exclude })
		calls = 0
		for _, path := range refsFirst {
			refused(path, "The repository 'vcs-gitlab' rejected the artifact "+
				"'vcs-gitlab:team/project/gitrefs' due to its include/exclude pattern settings.")
		}
		if calls != 0 {
			t.Fatalf("refused refs reached upstream %d times", calls)
		}
		commit := w.do(http.MethodGet,
			base+"downloadCommit/vcs-gitlab/team/project/"+vcsTestOID, "reader", "")
		wantStatus(t, commit, http.StatusOK)
	}
	on := true
	patch(func(c *repo.Config) { c.ExcludesPattern, c.BlackedOut = nil, &on })
	refused("tags/vcs-gitlab/team/project", "The repository 'vcs-gitlab' is blacked out "+
		"and cannot serve artifact 'vcs-gitlab:team/project/gitrefs'.")
}
