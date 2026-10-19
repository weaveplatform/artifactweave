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
	"slices"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
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
}

// Inspect reads a tarball written at a path that could be canonical,
// before the write's transaction. Other content needs no reading.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Checksums.SHA256 == "" || !candidate(w.Path) {
		return nil, nil //nolint:nilnil // nothing to read is no failure
	}
	t, ok, err := ReadStored(ctx, env.Blobs, w.Checksums.SHA256)
	if err != nil || !ok {
		return nil, err
	}
	return t, nil
}

// Deployed indexes a tarball stored at its canonical path, and drops the
// version of a file that no longer is one, such as a tarball overwritten
// with another version's content. A tarball stored anywhere but where the
// index reads (pkgindex.Stored) is not indexed.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Cached != pkgindex.Stored(c).Cached {
		return nil
	}
	t, ok, err := tarballOf(ctx, env, it)
	if err != nil {
		return err
	}
	if ok && it.FullPath() == CanonicalPath(t.Name, t.Version) {
		return index(ctx, env.Store, c, it, t)
	}
	return unindex(ctx, env.Store, it.ID)
}

// tarballOf is what it holds: the inspection of its write when there was
// one of this content, or a read of the content, as a restore needs.
func tarballOf(ctx context.Context, env format.Env, it types.Item) (*Tarball, bool, error) {
	if !candidate(it.FullPath()) {
		return nil, false, nil
	}
	if t, ok := env.Inspection.(*Tarball); ok && t.SHA256 == it.Actual.SHA256 {
		return t, true, nil
	}
	return ReadStored(ctx, env.Blobs, it.Actual.SHA256)
}

// Annotated follows a change to an indexed tarball's dist-tags or
// deprecation, by whichever route it came: the npm protocol, the
// properties API, AQL, jf rt sp, or a deploy, copy, move or restore
// carrying them. A tag the tarball gains is taken from the package's other
// versions in the same transaction, so a tag names one version.
func (Handler) Annotated(
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
	return refresh(ctx, env.Store, pv, it.ID)
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
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	return reindex(ctx, env, c)
}
