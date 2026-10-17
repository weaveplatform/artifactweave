package maven

import (
	"context"
	"maps"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// maxChecksumBody is the longest checksum upload Artifactory reads
// (UploadServiceUtils.isAbnormalChecksumContentLength).
const maxChecksumBody = 1024

// OwnsSidecars makes every upload named as a checksum sidecar in a Maven
// repository a checksum of its file, as in Artifactory, so one whose file
// is missing answers 404 rather than being stored.
func (Handler) OwnsSidecars() {}

// Inspect reads the POM a deploy, copy or move lands, outside the write's
// transaction; Admit checks it and the index keeps what it names.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum || !IsPom(w.Path) || w.Checksums.SHA256 == "" {
		return nil, nil //nolint:nilnil // nothing to inspect
	}
	return readPOM(ctx, env.Blobs, w.Checksums.SHA256)
}

// Admit applies Artifactory's checks in its order (RealRepoBase
// .assertValidPath, then UploadServiceImpl): a blacked-out repository, the
// release and snapshot policy, the include and exclude patterns, and then
// what each kind of write adds.
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if err := generic.CheckBlackedOut(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // the generic policy's Rejection, answered as it is
	}
	r, err := readerOf(ctx, env, c)
	if err != nil {
		return w, err
	}
	// A checksum's path is exempt from the policy (handlesReleaseSnapshot).
	if w.Op != format.OpChecksum {
		if err := checkPolicy(c, r, w.Path); err != nil {
			return w, err
		}
	}
	if err := generic.CheckPatterns(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // the generic policy's Rejection, answered as it is
	}
	switch w.Op {
	case format.OpChecksum:
		return w, admitChecksum(w)
	case format.OpDeploy:
		// Snapshot metadata is computed from the POMs (#279); under the
		// Maven layout Artifactory reads the client's and drops it with
		// 202, except under deployer, which keeps what is sent.
		if IsSnapshotMetadata(w.Path) && IsMavenLayout(r.l) &&
			c.SnapshotVersionBehavior != repo.Deployer {
			return w, &format.Accepted{Status: http.StatusAccepted}
		}
		return admitPOM(c, r, env.Inspection, w)
	case format.OpCopy, format.OpMove:
		return w, checkTransfer(c, r, env.Inspection, w)
	}
	return w, nil
}

// admitChecksum takes a checksum upload as Artifactory's
// validateAndUploadChecksum does: one over 1024 bytes is refused, and one
// of maven-metadata.xml is read and dropped, as a metadata file's checksums
// are always the server's. The rest are recorded against their file under
// the checksum policy.
func admitChecksum(w format.Write) error {
	if w.Size > maxChecksumBody {
		return format.Reject(http.StatusConflict,
			"Suspicious checksum file, content length of %d bytes is bigger than allowed.", w.Size)
	}
	if IsMetadata(w.Path) {
		return &format.Accepted{Status: http.StatusOK}
	}
	return nil
}

// admitPOM checks a deployed POM against its path (MavenPomInterceptor
// .afterCreate) unless the repository suppresses the checks, which keeps
// the POM anyway, and marks a plugin's.
func admitPOM(c repo.Config, r reader, inspected any, w format.Write) (format.Write, error) {
	p, ok := inspected.(*pom)
	if !ok || !IsPom(w.Path) {
		return w, nil
	}
	if msg := p.problem(w.Path, r.integration(w.Path)); msg != "" && !suppressed(c) {
		return w, format.Reject(http.StatusConflict, "%s", msg)
	}
	if p.isPlugin() {
		props := maps.Clone(w.Properties)
		if props == nil {
			props = types.Properties{}
		}
		props[PluginProperty] = []string{"true"}
		w.Properties = props
	}
	return w, nil
}

// checkTransfer checks a POM a copy or move lands at a POM's path
// (MavenPomInterceptor.isCopyOrMoveAllowed): unless the target suppresses
// the checks, any problem refuses it with 400 and a message that names the
// target alone.
func checkTransfer(c repo.Config, r reader, inspected any, w format.Write) error {
	p, ok := inspected.(*pom)
	if !ok || suppressed(c) || !IsPom(w.Path) || !IsPom(w.From.FullPath()) {
		return nil
	}
	if p.problem(w.Path, r.integration(w.Path)) == "" {
		return nil
	}
	return format.Reject(http.StatusBadRequest, "Failed to validate target path of pom: %s", w.Path)
}

// suppressed reports suppressPomConsistencyChecks; a repository created
// through repo.Defaults always has it.
func suppressed(c repo.Config) bool {
	return c.SuppressPomConsistencyChecks != nil && *c.SuppressPomConsistencyChecks
}
