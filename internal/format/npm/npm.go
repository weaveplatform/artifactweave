// Package npm is the format of npm repositories (decision 0034). It reads
// each tarball's package.json, indexes a tarball at its canonical path as
// a version of its package (decision 0018) with the per-version Metadata
// that packuments and search are built from, and keeps the npm.*
// properties. Every write reaches it through the item service, so a
// tarball deployed by npm publish, by jf npm publish's REST deploy, or
// landed by a copy, a move or a restore is indexed the same way.
//
// A .tgz anywhere else, or one without a readable package.json, is stored
// but not indexed: every repository has strict layout (DV-041).
package npm

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Properties the format keeps on an indexed tarball. Name, version,
// description, keywords and license follow from package.json and are set
// by the format; dist-tags and deprecation are the state npm changes after
// a publish, and the index follows them.
const (
	PropName        = "npm.name"
	PropVersion     = "npm.version"
	PropDescription = "npm.description"
	// PropKeywords is one value: the keywords as a JSON array of strings,
	// which Artifactory's keyword search matches with *"<keyword>"*.
	PropKeywords = "npm.keywords"
	// PropDistTag has one value per dist-tag. A tag names one version of
	// its package: a tarball that gains one takes it from the others.
	PropDistTag = "npm.disttag"
	// PropDeprecated is the version's deprecation message.
	PropDeprecated = "npm.deprecated"
	// PropLicenses is package.json's license, which 7.x writes beside the
	// npm.* properties when it indexes a tarball (RTFACT-30834). It is not
	// in npm's namespace: other tools, Xray among them, write it too.
	PropLicenses = "artifactory.licenses"
)

// Serves reports whether t is a package type of this format.
func Serves(t repo.PackageType) bool { return t == repo.NPM }

// Handler is the npm format.
type Handler struct {
	// The generic format's hooks for what npm leaves alone. Admit adds the
	// release and snapshot handling to its deploy policy (policy.go). A
	// republish's 403 belongs to the npm publish protocol: a REST deploy
	// over a tarball is an overwrite, which needs Delete, as for any item
	// (decision 0034).
	generic.Handler
	// Settings are the server's npm options.
	Settings Settings
}

// inspection is what Inspect read for a write to a path that could be
// canonical, or one npm.enforce_path checks.
type inspection struct {
	// sha256 is the content read, so a hook can tell the inspection of its
	// own write from one of other content.
	sha256 string
	// tarball is the content as a package tarball, nil when the format
	// cannot index it.
	tarball *Tarball
}

// Inspect reads a tarball written at a path that could be canonical, or
// one npm.enforce_path checks, before the write's transaction. Other
// content needs no reading.
func (h Handler) Inspect(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (any, error) {
	if w.Checksums.SHA256 == "" || !candidate(w.Path) && !h.Settings.enforces(c, w) {
		return nil, nil //nolint:nilnil // nothing to read is no failure
	}
	t, _, err := ReadStored(ctx, env.Blobs, w.Checksums.SHA256)
	if err != nil {
		return nil, err
	}
	return &inspection{sha256: w.Checksums.SHA256, tarball: t}, nil
}

// Deployed indexes a tarball stored at its canonical path, and drops the
// version of a file that no longer is one, such as a tarball overwritten
// with another version's content. A tarball stored anywhere but where the
// index reads (pkgindex.Stored) is not indexed.
func (h Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Cached != pkgindex.Stored(c).Cached {
		return nil
	}
	in, err := inspectionOf(ctx, env, it)
	if err != nil {
		return err
	}
	if t := in.tarball; t != nil && it.FullPath() == CanonicalPath(t.Name, t.Version) {
		return h.index(ctx, env.Store, c, it, t)
	}
	return h.unindex(ctx, env.Store, it)
}

// Deleted settles latest in each package a delete or a move took a
// tarball from (rule 4): pruning has dropped the versions left without a
// file, and with them, perhaps, the version that carried latest. The
// removed tarballs' versions are gone by now, so their packages are found
// by the names their paths give; a package with no version left is gone
// too, and a stray tarball's package keeps its latest.
func (h Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	stored := pkgindex.Stored(c).Cached
	seen := map[string]bool{}
	for _, it := range removed {
		name, _, ok := canonicalParts(it.FullPath())
		if it.Folder || it.Cached != stored || !ok || seen[name] {
			continue
		}
		seen[name] = true
		p, err := env.Store.Packages().Find(ctx, c.Key, name)
		switch {
		case errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			return fmt.Errorf("npm: package %s: %w", name, err)
		}
		if err := h.Settings.settleLatest(ctx, env.Store, p.ID, env.Now()); err != nil {
			return err
		}
	}
	return nil
}

// inspectionOf is the inspection of the write that stored it when there
// was one of this content, or else a read of the content, as a restore
// needs.
func inspectionOf(ctx context.Context, env format.Env, it types.Item) (*inspection, error) {
	if !candidate(it.FullPath()) {
		return &inspection{}, nil
	}
	if in, ok := env.Inspection.(*inspection); ok && in.sha256 == it.Actual.SHA256 {
		return in, nil
	}
	t, _, err := ReadStored(ctx, env.Blobs, it.Actual.SHA256)
	if err != nil {
		return nil, err
	}
	return &inspection{sha256: it.Actual.SHA256, tarball: t}, nil
}

// Annotated follows a change to an indexed tarball's dist-tags or
// deprecation, by whichever route it came: the npm protocol, the
// properties API, AQL, jf rt sp, or a deploy, copy, move or restore
// carrying them. A tag the tarball gains is taken from the package's other
// versions in the same transaction, so a tag names one version, latest
// included (rule 1). A change that leaves the package without latest
// settles it anew (rule 4), so npm dist-tag rm latest hands latest to the
// version that outranks the others, which may be the one it was taken
// from.
func (h Handler) Annotated(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	it types.Item,
	a format.Annotation,
) error {
	if it.Folder || !followed(a) {
		return nil
	}
	pv, ok, err := versionOf(ctx, env.Store, it.ID)
	if err != nil || !ok {
		return err
	}
	if tags := nonEmpty(a.Set[PropDistTag]); len(tags) > 0 {
		if err := moveTags(ctx, env.Store, pv, tags, a.At); err != nil {
			return err
		}
	}
	if err := refresh(ctx, env.Store, pv, it.ID); err != nil {
		return err
	}
	return h.Settings.settleLatest(ctx, env.Store, pv.PackageID, a.At)
}

// followed reports whether a changes a property the index follows.
func followed(a format.Annotation) bool {
	for _, k := range []string{PropDistTag, PropDeprecated} {
		if _, ok := a.Set[k]; ok || slices.Contains(a.Removed, k) {
			return true
		}
	}
	return false
}

// nonEmpty is values without the empty ones.
func nonEmpty(values []string) []string {
	return slices.DeleteFunc(slices.Clone(values), func(v string) bool { return v == "" })
}

// Reindex rebuilds the repository's npm index from its tarballs and their
// properties, a remote repository's from its cache. Which repositories the
// JFrog reindex operation accepts is the API's rule, not the index's:
// decision 0034's local-only 400 is answered in internal/server.
func (h Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	return h.reindex(ctx, env, c)
}
