package npm

import (
	"context"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Admit applies the deploy policy of every package type with the
// repository's release and snapshot handling between its checks, in
// Artifactory's order (RealRepoBase.assertValidPath): a blacked-out
// repository, then handleReleases and handleSnapshots, then the include
// and exclude patterns. 7.x refuses an npm local deploy the handling
// forbids with 409 (KB 000006800). The policy applies to every write that
// lands a file, an npm publish's included, as it does for Maven; a
// checksum upload is exempt, as Artifactory exempts checksum paths. Last,
// npm.enforce_path checks a deploy's file name (checkFileName).
func (h Handler) Admit(
	_ context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if err := generic.CheckBlackedOut(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // the generic policy's Rejection, answered as it is
	}
	if w.Op != format.OpChecksum {
		if err := checkPolicy(c, w.Path); err != nil {
			return w, err
		}
	}
	if err := generic.CheckPatterns(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // the generic policy's Rejection, answered as it is
	}
	if err := h.Settings.checkFileName(env, c, w); err != nil {
		return w, err
	}
	return w, nil
}

// enforces reports whether npm.enforce_path checks w, a write to c: an
// upload of a .tgz to a local or federated repository, by a REST deploy,
// a checksum deploy, an archive's entry or npm publish. 7.x documents the
// property as rejecting uploads to npm local repositories
// (npm-repositories). [I] Whether it checks a federated repository, a copy
// or a move, or a package tarball named other than .tgz is undocumented
// (#216); a copy or move is not an upload, and the file it lands stays
// unindexed off its canonical path, as any stray tarball does.
func (s Settings) enforces(c repo.Config, w format.Write) bool {
	return s.EnforcePath && w.Op == format.OpDeploy && !w.Cached &&
		(c.Class == repo.Local || c.Class == repo.Federated) &&
		strings.HasSuffix(w.Path, ".tgz")
}

// checkFileName refuses with 400, under npm.enforce_path, the upload of a
// package tarball whose file name is not the one its package.json gives,
// <name>-<version>.tgz with the version cleaned as the index cleans it:
// 7.x's property prevents "uploading packages with file names that do not
// match the internal metadata of the package" (npm-repositories), which
// RTDEV-86877 (7.146.22) enforces on every such upload. A scoped
// package's file name keeps its scope, as npm names its tarball
// (CanonicalPath), so @scope/name/-/@scope/name-1.0.0.tgz passes and
// @scope/name/-/name-1.0.0.tgz does not. Content without a readable
// package.json names no package to compare and is stored, as strict layout
// stores it. [I] 7.x's status and message, whether it compares the folders
// before the file name or a scoped name's scope, and how it treats content
// without a readable package.json are undocumented (#216).
func (s Settings) checkFileName(env format.Env, c repo.Config, w format.Write) error {
	if !s.enforces(c, w) {
		return nil
	}
	in, ok := env.Inspection.(*inspection)
	if !ok || in.tarball == nil {
		return nil
	}
	want := fileName(in.tarball.Name, in.tarball.Version)
	if w.Path == want || strings.HasSuffix(w.Path, "/"+want) {
		return nil
	}
	return format.Reject(http.StatusBadRequest,
		"The repository '%s' rejected '%s': the file name of npm package %s@%s must be %s.",
		c.Key, w.Path, in.tarball.Name, in.tarball.Version, want)
}

// checkPolicy refuses a tarball of a pre-release in a repository that does
// not handle snapshots, or of a release in one that does not handle
// releases, with 409 and the message KB 000006800 quotes from 7.x; the KB
// elides its opening, which is 6.20's SnapshotPolicyException's, as Maven
// gives it. The version is read from the path, as Artifactory reads a path's module
// through the repository's layout, so a copy or move is classed as a
// deploy is. A path that names no version this way is exempt, as
// Artifactory exempts a path its layout cannot read.
//
// [I] 7.x's rule for which npm versions are snapshots is unknown, and the
// npm-default layout 7.x exports does not settle it: its greedy [module]
// reads a pre-release's last hyphen as the one before the base revision,
// leaving no integration revision. A pre-release is classed as a snapshot
// (#216).
func checkPolicy(c repo.Config, p string) error {
	if handles(c.HandleReleases) && handles(c.HandleSnapshots) {
		return nil
	}
	version, ok := pathVersion(p)
	if !ok {
		return nil
	}
	// CleanVersion has dropped any build metadata, so a hyphen can only
	// start a pre-release.
	snapshot := strings.Contains(version, "-")
	if snapshot && handles(c.HandleSnapshots) || !snapshot && handles(c.HandleReleases) {
		return nil
	}
	return format.Reject(http.StatusConflict,
		"The repository '%s' rejected the resolution of an artifact '%s:%s' due to conflict in "+
			"the snapshot release handling policy.", c.Key, c.Key, p)
}

// handles reads handleReleases or handleSnapshots, both on unless set off.
func handles(b *bool) bool { return b == nil || *b }

// pathVersion is the version p names when it has the shape of a canonical
// path, <name>/-/<name>-<version>.tgz with a name npm accepts and a SemVer
// version, cleaned as the index cleans it.
func pathVersion(p string) (string, bool) {
	_, version, ok := canonicalParts(p)
	if !ok {
		return "", false
	}
	return CleanVersion(version)
}

// canonicalParts reads p as <name>/-/<name>-<version>.tgz, with a name npm
// accepts; the version is as the path spells it.
func canonicalParts(p string) (name, version string, ok bool) {
	name, file, ok := strings.Cut(p, "/-/")
	if !ok || !ValidName(name) {
		return "", "", false
	}
	rest, ok := strings.CutPrefix(file, name+"-")
	if !ok {
		return "", "", false
	}
	version, ok = strings.CutSuffix(rest, ".tgz")
	return name, version, ok
}
