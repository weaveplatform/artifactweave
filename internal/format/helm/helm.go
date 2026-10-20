// Package helm is the format of classic Helm chart repositories (decision
// 0039); Helm OCI repositories are the Docker format's. It reads each chart
// archive's Chart.yaml, indexes every chart in the repository, in any
// folder, as a version of the package its name gives (decision 0018), with
// the per-version Metadata that index.yaml is built from, and sets the
// chart.* properties. Every write reaches it through the item service, so a
// chart deployed by a REST PUT, by checksum, from an archive, or landed by a
// copy, a move or a restore is indexed the same way.
//
// A chart's provenance file, <chart>.prov, is a sidecar the format keeps
// with its chart (format.SidecarKeeper): it follows the chart through a
// delete, a copy, a move and a restore, and is never a version file.
//
// Each write that changes a repository's charts queues the repository's
// index.yaml work, one item per repository (KindIndex).
package helm

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Properties the format sets on an indexed chart from its Chart.yaml: the
// keys of Artifactory's Helm package search (6.20 PackageSearchCriteria;
// RTFACT-15566). 7.x still writes appVersion as a property (RTFACT-26509);
// that 7.161 writes these three keys and no other is [I] (#216).
const (
	PropName       = "chart.name"
	PropVersion    = "chart.version"
	PropAppVersion = "chart.appVersion"
)

// KindIndex is the format's derived work (decisions 0033 and 0039): a
// repository's index.yaml, keyed by the repository's key alone, since one
// file lists every chart of the repository. Every write that changes the
// charts of a local or federated repository queues it in the write's own
// transaction, so a burst of deploys coalesces into one item.
const KindIndex = "helm.index"

// IndexPath is where a repository's index.yaml lies: its root alone. An
// index.yaml in any other folder is an ordinary file.
const IndexPath = "index.yaml"

// ProvSuffix makes a chart's path its provenance file's: helm fetches the
// file at the chart's URL plus .prov (Helm's provenance documentation).
const ProvSuffix = ".prov"

// Serves reports whether t is the package type of this format.
func Serves(t repo.PackageType) bool { return t == repo.Helm }

// Handler is the Helm format.
type Handler struct {
	// The generic format's hooks for what Helm leaves alone. Admit adds
	// Enforce Layout to its deploy policy (enforce.go).
	generic.Handler
}

// inspection is what Inspect read for a write of a chart-named file.
type inspection struct {
	// sha256 is the content read, so a hook can tell the inspection of its
	// own write from one of other content.
	sha256 string
	// chart is the archive's Chart.yaml, nil when the format cannot index
	// the content.
	chart *Chart
}

// Inspect reads a chart-named file before the write's transaction, so no
// decompression holds the store's write lock. Other content needs no
// reading.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Checksums.SHA256 == "" || !IsChartPath(w.Path) {
		return nil, nil //nolint:nilnil // nothing to read is no failure
	}
	c, err := ReadStored(ctx, env.Blobs, w.Checksums.SHA256)
	if err != nil {
		return nil, err
	}
	return &inspection{sha256: w.Checksums.SHA256, chart: c}, nil
}

// chartOf is the chart the file it holds: the inspection of the write that
// stored it when there was one of this content, or else a read of the
// content, as a restore needs. Nil when it holds none.
func chartOf(ctx context.Context, env format.Env, it types.Item) (*Chart, error) {
	if it.Folder || !IsChartPath(it.FullPath()) {
		return nil, nil //nolint:nilnil // no chart is no failure
	}
	if in, ok := env.Inspection.(*inspection); ok && in.sha256 == it.Actual.SHA256 {
		return in.chart, nil
	}
	return ReadStored(ctx, env.Blobs, it.Actual.SHA256)
}

// Deployed indexes a chart as a file of its version, and takes a file that
// no longer holds a chart, or holds another, out of the version it was
// one of. A chart anywhere in the repository is indexed (RTFACT-25956). It
// queues the repository's index.yaml when the charts changed, and when a
// client stored the root index.yaml, which the next run replaces
// (RTFACT-18128).
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Cached != pkgindex.Stored(c).Cached {
		return nil
	}
	ch, err := chartOf(ctx, env, it)
	if err != nil {
		return err
	}
	changed := ch != nil
	if ch != nil {
		err = index(ctx, env, c, it, ch)
	} else {
		changed, err = unindex(ctx, env, it)
	}
	if err != nil {
		return err
	}
	if changed || it.FullPath() == IndexPath {
		return queueIndex(ctx, env, c, it.Cached)
	}
	return nil
}

// Deleting runs before a file is removed. The item service drops a chart's
// version link with it and prunes a version left without files; a version
// that keeps another archive takes that one's Chart.yaml when the newest
// goes (handOver). A chart's delete, or a client's delete of the root
// index.yaml, queues the repository's index.yaml.
func (Handler) Deleting(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || it.Cached != pkgindex.Stored(c).Cached {
		return nil
	}
	if it.FullPath() == IndexPath {
		return queueIndex(ctx, env, c, it.Cached)
	}
	pv, ok, err := pkgindex.VersionOf(ctx, env.Store, it.ID)
	if err != nil || !ok {
		return err //nolint:wrapcheck // pkgindex wraps its own
	}
	if err := handOver(ctx, env, pv, it); err != nil {
		return err
	}
	return queueIndex(ctx, env, c, it.Cached)
}

// Sidecars names a chart's provenance file, <chart>.prov beside it, which
// the item service keeps with the chart (decision 0039). Any other file
// owns none.
func (Handler) Sidecars(_ repo.Config, p string) []string {
	if !IsChartPath(p) {
		return nil
	}
	return []string{p + ProvSuffix}
}

// derives reports whether c's files have a derived index.yaml: a local or
// federated repository's own files do. A remote repository's cache does
// not; its index.yaml is the remote's, which P9 serves.
func derives(c repo.Config, cached bool) bool {
	return !cached && (c.Class == repo.Local || c.Class == repo.Federated)
}

// queueIndex asks for c's index.yaml in the current transaction.
func queueIndex(ctx context.Context, env format.Env, c repo.Config, cached bool) error {
	if !derives(c, cached) {
		return nil
	}
	return env.Enqueue(ctx, KindIndex, c.Key, nil) //nolint:wrapcheck // the queue names its own
}

// WorkKinds are the kinds the Helm format queues.
func (Handler) WorkKinds() []string { return []string{KindIndex} }

// Owed names the work a read of the root index.yaml of a local or federated
// repository waits for, so a client never reads an index that lags a
// completed write.
func (Handler) Owed(c repo.Config, cached bool, p string) []format.WorkRef {
	if !derives(c, cached) || p != IndexPath {
		return nil
	}
	return []format.WorkRef{{Kind: KindIndex, Key: c.Key}}
}
