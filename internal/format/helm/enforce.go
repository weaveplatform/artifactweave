package helm

import (
	"context"
	"fmt"
	"net/http"
	"path"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// The Enforce Layout refusals, 403 with 7.x's words for each
// (kubernetes-helm-chart-repositories, "Helm Enforce Layout", Examples).
const (
	msgPrefix    = "This action is prevented due to the Enforce Layout Policy, "
	msgMalformed = msgPrefix + "the metadata of the package %s could not be read or is malformed."
	msgNotSemver = msgPrefix + "the specified package version %s-%s does not comply with " +
		"the Semantic Versioning format."
	// JFrog's example prints chart-1.0. for chart-1.0.0.tgz; the whole
	// name is printed here, and the example is on #216.
	msgMismatch = msgPrefix + "the package file %s does not match the package name/version " +
		"%s:%s in the metadata file."
	msgDuplicate = msgPrefix + "a package with the same name and version %s-%s already " +
		"exists in the repository."
)

// Admit applies the deploy policy of every package type, then Enforce
// Layout (decision 0039) to every write that lands a chart-named file in a
// repository that turned it on: a deploy, a checksum deploy, an archive
// entry, and the target of a copy or move, which the item service then
// reports and skips. A .prov and every other file pass, as does a checksum
// upload, which lands no file. The checks run in the order 7.x's
// documentation lists them, which is [I] (#216):
//  1. forceMetadataNameVersion: Chart.yaml can be read and names a chart
//     and a version;
//  2. forceMetadataNameVersion: the version is SemVer 2.0.0;
//  3. forceMetadataNameVersion: the file is <name>-<version> with its
//     extension;
//  4. forceNonDuplicateChart: no chart at another path of the repository
//     has the same name and version. A write to the same path is an
//     overwrite, and a move's own source is no duplicate of it.
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, err //nolint:wrapcheck // the generic policy's Rejection, answered as it is
	}
	names, unique := on(c.ForceMetadataNameVersion), on(c.ForceNonDuplicateChart)
	if w.Op == format.OpChecksum || w.Cached || !IsChartPath(w.Path) || !names && !unique {
		return w, nil
	}
	ch, err := admitted(ctx, env, w)
	if err != nil {
		return w, err
	}
	if names {
		if err := checkNames(w.Path, ch); err != nil {
			return w, err
		}
	}
	if unique && ch != nil {
		if err := checkUnique(ctx, env, c, w, ch); err != nil {
			return w, err
		}
	}
	return w, nil
}

// on reads an Enforce Layout field, off unless set on.
func on(b *bool) bool { return b != nil && *b }

// admitted is the chart the write lands: what Inspect read for it, or a
// read of its content when nothing was inspected.
func admitted(ctx context.Context, env format.Env, w format.Write) (*Chart, error) {
	if in, ok := env.Inspection.(*inspection); ok && in.sha256 == w.Checksums.SHA256 {
		return in.chart, nil
	}
	return ReadStored(ctx, env.Blobs, w.Checksums.SHA256)
}

// checkNames holds the chart-named file at p, which holds ch (nil for none
// the format can read), to Enforce Chart Name and Version.
func checkNames(p string, ch *Chart) error {
	switch {
	case ch == nil:
		return format.Rejectf(http.StatusForbidden, msgMalformed, stem(p))
	case !IsSemver(ch.Version):
		return format.Rejectf(http.StatusForbidden, msgNotSemver, ch.Name, ch.Version)
	case path.Base(p) != ch.Name+"-"+ch.Version+extension(p):
		return format.Rejectf(http.StatusForbidden, msgMismatch, stem(p), ch.Name, ch.Version)
	}
	return nil
}

// checkUnique refuses w, which lands ch, when the index holds a chart of
// the same name and version at another path of c (Prevent Duplicate Chart
// Paths).
func checkUnique(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
	ch *Chart,
) error {
	st := env.Store
	pv, ok, err := stored(ctx, st, c, versionKey{ch.Name, ch.Version})
	if err != nil || !ok {
		return err
	}
	ids, err := st.Packages().Files(ctx, pv.ID)
	if err != nil {
		return fmt.Errorf("helm: files of %s %s: %w", ch.Name, ch.Version, err)
	}
	for _, id := range ids {
		if w.Op == format.OpMove && id == w.From.ID {
			continue
		}
		it, err := st.Items().GetByID(ctx, id)
		if err != nil {
			return fmt.Errorf("helm: item %d: %w", id, err)
		}
		if it.FullPath() != w.Path {
			return format.Rejectf(http.StatusForbidden, msgDuplicate, ch.Name, ch.Version)
		}
	}
	return nil
}
