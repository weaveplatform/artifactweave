// Package pypi is the format of PyPI repositories (decision 0037). It
// reads each distribution's core metadata, a wheel's METADATA or an sdist's
// or egg's PKG-INFO, indexes the file as a file of its project's version
// wherever it is stored (decision 0018), with the per-file records the
// simple index, the PEP 658 metadata files and the JSON API are built
// from, and keeps the pypi.* properties. Every write reaches it through the
// item service in the write's own transaction, so a file deployed by twine,
// by a REST deploy or by jf rt u, or landed by a copy, a move or a restore,
// can be installed as soon as the write answers.
//
// A file of a distribution type whose content yields no Name and Version
// is indexed by its pypi.name and pypi.version properties when it carries
// both, as JFrog requires of manual uploads it cannot read; any other file
// is stored but not indexed.
package pypi

import (
	"context"
	"fmt"
	"path"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Properties the format keeps on an indexed file. Name, version, summary,
// normalised name and Requires-Python follow from the core metadata and are
// set by the format (pypi-repositories; RTFACT-29892); yanked is the state
// a user changes after an upload, and the index follows it.
const (
	PropName           = "pypi.name"
	PropVersion        = "pypi.version"
	PropSummary        = "pypi.summary"
	PropNormalizedName = "pypi.normalized.name"
	PropRequiresPython = "pypi.requires.python"
	// PropYanked yanks the file when set to anything but "false", in any
	// case: "true" or no value is a yank without a reason, any other value
	// the reason. 7.x's project page query reads it (RTFACT-29892). [I]
	// The values 7.x writes are unknown (#216).
	PropYanked = "pypi.yanked"
)

// Serves reports whether t is a package type of this format.
func Serves(t repo.PackageType) bool { return t == repo.PyPI }

// Settings are the server's PyPI options (decision 0037), each off by
// default as its 7.x counterpart is, and read by every PyPI repository.
type Settings struct {
	// EnforceLayout is pypi.enforce_layout, 7.x's
	// artifactory.pypi.enforce.layout: an upload whose file name does not
	// begin with the metadata's Name and Version is refused (Admit).
	EnforceLayout bool
	// EnforceNamingNormalization is pypi.enforce_naming_normalization,
	// 7.x's artifactory.pypi.enforce.naming.normalization: an upload whose
	// file name's name segment holds a "-" is refused (Admit).
	EnforceNamingNormalization bool
	// SimpleJSON is pypi.simple_json, 7.x's "Enable simple json format":
	// the simple index answers PEP 691 JSON to a client that asks for it.
	SimpleJSON bool
	// CoreMetadata is pypi.core_metadata, 7.x's PEP 658 system property:
	// the simple index advertises, and serves, the wheels' core metadata.
	CoreMetadata bool
}

// Handler is the PyPI format.
type Handler struct {
	// The generic format's deploy policy, which Admit adds the reserved
	// namespaces and the enforcement settings to.
	generic.Handler
	// Settings are the server's PyPI options.
	Settings Settings
}

// inspection is what Inspect read for a write of a distribution file.
type inspection struct {
	// sha256 and name are the content and the file name read, so a hook
	// can tell the inspection of its own write from one of another: the
	// file name decides where the metadata is read from.
	sha256, name string
	// dist is the content's core metadata, nil when it yields none.
	dist *Dist
}

// Inspect reads a distribution file's core metadata before the write's
// transaction. Other content needs no reading.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	name := path.Base(w.Path)
	if w.Checksums.SHA256 == "" || !IsDistribution(name) {
		return nil, nil //nolint:nilnil // nothing to read is no failure
	}
	d, _, err := ReadStored(ctx, env.Blobs, w.Checksums.SHA256, name)
	if err != nil {
		return nil, err
	}
	return &inspection{sha256: w.Checksums.SHA256, name: name, dist: d}, nil
}

// distOf is the core metadata of it: the inspection of the write that
// stored it when there was one of this content and name, or else a read
// of the content, as a restore needs. Nil for a file that yields none.
func distOf(ctx context.Context, env format.Env, it types.Item) (*Dist, error) {
	name := path.Base(it.FullPath())
	if in, ok := env.Inspection.(*inspection); ok && in.sha256 == it.Actual.SHA256 &&
		in.name == name {
		return in.dist, nil
	}
	d, _, err := ReadStored(ctx, env.Blobs, it.Actual.SHA256, name)
	return d, err
}

// Deployed indexes a distribution file stored where the index reads
// (pkgindex.Stored), wherever its path, as the file of its project's
// version, after setting the pypi.* properties its core metadata gives. A
// file that no longer belongs to the version it was indexed under, such as
// one overwritten with another version's content, leaves it.
func (h Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Cached != pkgindex.Stored(c).Cached || !IsDistribution(it.FullPath()) {
		return nil
	}
	d, err := distOf(ctx, env, it)
	if err != nil {
		return err
	}
	var props types.Properties
	if d != nil {
		props, err = setProperties(ctx, env.Store, it, d)
	} else {
		props, err = env.Store.Items().Properties(ctx, it.ID)
	}
	if err != nil {
		return fmt.Errorf("pypi: properties of %s: %w", it.FullPath(), err)
	}
	e, ok := entryOf(it, d, props)
	return place(ctx, env.Store, c, it, e, ok)
}

// Deleting drops the record of a file about to be removed from its
// version's metadata. The item service drops the file's link and prunes a
// version left without files.
func (Handler) Deleting(ctx context.Context, env format.Env, _ repo.Config, it types.Item) error {
	if !IsDistribution(it.FullPath()) {
		return nil
	}
	pv, ok, err := pkgindex.VersionOf(ctx, env.Store, it.ID)
	if err != nil || !ok {
		return err //nolint:wrapcheck // pkgindex wraps its own
	}
	m, err := DecodeMetadata(pv.Metadata)
	if err != nil || !m.drop(it.FullPath()) {
		return err
	}
	pv.Metadata = m.encode()
	if len(m.Files) > 0 {
		pv.Created = m.created()
	}
	if _, err := env.Store.Packages().PutVersion(ctx, pv); err != nil {
		return fmt.Errorf("pypi: version %s: %w", pv.Version, err)
	}
	return nil
}

// followed are the properties the index follows: yanked on every file,
// and the rest on a file indexed by its properties alone.
var followed = []string{PropYanked, PropName, PropVersion, PropRequiresPython}

// Annotated follows a change to an indexed file's pypi.yanked, by
// whichever route it came: the properties API, AQL, jf rt sp, or a deploy,
// copy, move or restore carrying it, so the pages follow at once. A file
// indexed by its properties alone follows its pypi.name, pypi.version and
// pypi.requires.python too, and one that gains the first two is indexed by
// them; the core metadata decides for a file that has it, so a change to
// those leaves it where it is.
func (Handler) Annotated(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
	a format.Annotation,
) error {
	if it.Folder || it.Cached != pkgindex.Stored(c).Cached || !IsDistribution(it.FullPath()) ||
		!slices.ContainsFunc(followed, func(k string) bool {
			_, set := a.Set[k]
			return set || slices.Contains(a.Removed, k)
		}) {
		return nil
	}
	st := env.Store
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("pypi: properties of %s: %w", it.FullPath(), err)
	}
	pv, linked, err := pkgindex.VersionOf(ctx, st, it.ID)
	if err != nil {
		return err //nolint:wrapcheck // pkgindex wraps its own
	}
	if linked {
		m, err := DecodeMetadata(pv.Metadata)
		if err != nil {
			return err
		}
		if f, ok := m.find(it.FullPath()); ok && f.Metadata != "" {
			return reyank(ctx, st, pv, m, f, props)
		}
	}
	// The item as stored: the caller's may predate the write that runs
	// this hook.
	if it, err = st.Items().GetByID(ctx, it.ID); err != nil {
		return fmt.Errorf("pypi: item %d: %w", it.ID, err)
	}
	e, ok := entryOf(it, nil, props)
	return place(ctx, st, c, it, e, ok)
}

// reyank brings the record f of a file with core metadata in pv's
// Metadata, m, in line with its yanked state in props.
func reyank(
	ctx context.Context,
	st store.Store,
	pv types.PackageVersion,
	m Metadata,
	f File,
	props types.Properties,
) error {
	yanked, reason := yankedBy(props)
	if f.Yanked == yanked && f.YankedReason == reason {
		return nil
	}
	f.Yanked, f.YankedReason = yanked, reason
	m.put(f, m.Text(f))
	pv.Metadata = m.encode()
	if _, err := st.Packages().PutVersion(ctx, pv); err != nil {
		return fmt.Errorf("pypi: version %s: %w", pv.Version, err)
	}
	return nil
}

// Reindex rebuilds the repository's PyPI index from its files and their
// properties, a remote repository's from its cache. Which repositories the
// JFrog reindex operation accepts is the API's rule (internal/server).
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	return reindex(ctx, env, c)
}

// uploaded is when it was last deployed: a copy or a move keeps the
// source's, as 7.x keeps a copied or moved item's times.
func uploaded(it types.Item) time.Time {
	return it.Modified.UTC().Truncate(time.Microsecond)
}
