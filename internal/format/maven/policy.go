package maven

import (
	"context"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Resolve resolves a download's version tokens and non-unique snapshot name
// (resolveTokens), then refuses the resolved path when the repository's
// release and snapshot policy does, as Artifactory's assertValidPath does
// for downloads and deploys alike. The policy reads the resolved path: a
// [RELEASE] that names a release is a release download.
func (Handler) Resolve(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	d format.Download,
) (string, error) {
	p, err := resolveTokens(ctx, env, c, d)
	if err != nil {
		return "", err
	}
	if handles(c.HandleReleases) && handles(c.HandleSnapshots) {
		return p, nil
	}
	r, err := readerOf(ctx, env, c)
	if err != nil {
		return "", err
	}
	if err := checkPolicy(c, r, p); err != nil {
		return "", err
	}
	return p, nil
}

// checkPolicy refuses a file of a snapshot in a repository that does not
// handle snapshots, or of a release in one that does not handle releases,
// with 409 and SnapshotPolicyException's message
// (RealRepoBase.handlesReleaseSnapshot). A path is a snapshot's when the
// layout reads an integration revision from it or it names
// [INTEGRATION]; metadata, checksums and paths the layout does not read are
// exempt.
func checkPolicy(c repo.Config, r reader, p string) error {
	if handles(c.HandleReleases) && handles(c.HandleSnapshots) {
		return nil
	}
	co, ok := r.read(p)
	if !ok {
		return nil
	}
	snapshot := co.Integration() || strings.Contains(p, "[INTEGRATION]")
	if snapshot && handles(c.HandleSnapshots) || !snapshot && handles(c.HandleReleases) {
		return nil
	}
	return format.Rejectf(http.StatusConflict,
		"The repository '%s' rejected the resolution of an artifact '%s:%s' due to conflict in "+
			"the snapshot release handling policy.", c.Key, c.Key, p)
}

// handles reads handleReleases or handleSnapshots, both on unless set off.
func handles(b *bool) bool { return b == nil || *b }

// integration reports whether the layout reads p as an integration
// (snapshot) file, as Artifactory's module info does for the POM check;
// a path the layout does not read is not one.
func (r reader) integration(p string) bool {
	if r.l == nil {
		return false
	}
	co, _ := r.l.Read(p)
	return co.Integration()
}
