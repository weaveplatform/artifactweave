package npm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// index makes the tarball it, at its canonical path, the one file of its
// version, with the version's Metadata, sets the properties that follow
// from its package.json, and gives the version latest when rule 2 or 3
// says so (claimLatest), by the dist-tags the tarball carries. Those are
// the write's own, or a copy's or a restore's carried over.
func (h Handler) index(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	it types.Item,
	t *Tarball,
) error {
	props, err := setProperties(ctx, st, it, t)
	if err != nil {
		return err
	}
	at := published(it)
	p, err := st.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Name: t.Name, Type: c.PackageType, Created: at,
	})
	if err != nil {
		return fmt.Errorf("npm: package %s: %w", t.Name, err)
	}
	_, err = st.Packages().Version(ctx, p.ID, t.Version)
	isNew := errors.Is(err, store.ErrNotFound)
	if err != nil && !isNew {
		return fmt.Errorf("npm: version %s of %s: %w", t.Version, t.Name, err)
	}
	pv, err := pkgindex.Sync(ctx, st, p, pkgindex.IndexedVersion{
		Version: t.Version, Created: at, Metadata: metadataOf(t, it, props).encode(),
		Files: []int64{it.ID},
	})
	if err != nil {
		return err //nolint:wrapcheck // pkgindex wraps its own
	}
	// The time setProperties writes at, for the reason it gives.
	return h.Settings.claimLatest(ctx, st, pv, nonEmpty(props[PropDistTag]), isNew, it.Updated)
}

// setProperties sets npm.name, npm.version, npm.description, npm.keywords
// and artifactory.licenses on it as t gives them, removing a description
// or keywords that t no longer has, and returns its properties as they
// then are. A license t lacks is not removed: other tools write
// artifactory.licenses too, and removing theirs would lose what the
// format never wrote. [I] Whether 7.x removes it is unknown (#216).
// Writing them straight to the store runs no Annotated hook: the index
// does not follow them. They follow from the content, so they are written
// at the time the content was, it.Updated: on a deploy, the deploy's own
// time, which the item service read once for the whole write.
func setProperties(
	ctx context.Context,
	st store.Store,
	it types.Item,
	t *Tarball,
) (types.Properties, error) {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return nil, fmt.Errorf("npm: properties of %s: %w", it.FullPath(), err)
	}
	want := types.Properties{PropName: {t.Name}, PropVersion: {t.Version}}
	var remove []string
	if t.Description != "" {
		want[PropDescription] = []string{
			types.TruncatePropertyValue(t.Description, types.PropertyValueMax),
		}
	} else if _, ok := props[PropDescription]; ok {
		remove = append(remove, PropDescription)
	}
	if kw := keywordsValue(t.Keywords); kw != "" {
		want[PropKeywords] = []string{kw}
	} else if _, ok := props[PropKeywords]; ok {
		remove = append(remove, PropKeywords)
	}
	if t.License != "" {
		want[PropLicenses] = []string{
			types.TruncatePropertyValue(t.License, types.PropertyValueMax),
		}
	}
	set := types.Properties{}
	for k, v := range want {
		if !slices.Equal(props[k], v) {
			set[k] = v
		}
	}
	if len(set) == 0 && len(remove) == 0 {
		return props, nil
	}
	if err := st.Items().PatchProperties(ctx, it.ID, set, remove, it.Updated); err != nil {
		return nil, fmt.Errorf("npm: properties of %s: %w", it.FullPath(), err)
	}
	for _, k := range remove {
		delete(props, k)
	}
	maps.Copy(props, set)
	return props, nil
}

// keywordsValue is npm.keywords for keywords: a JSON array, of as many of
// them as fit in a property value, or "" for none. The store would cut a
// longer value (types.PropertyValueMax) to text that is no longer JSON;
// the description it may cut, and setProperties cuts it the same way, so
// that it compares what the store holds. Metadata keeps both whole.
func keywordsValue(keywords []string) string {
	for n := len(keywords); n > 0; n-- {
		v := string(jsonText(keywords[:n]))
		if types.TruncatePropertyValue(v, types.PropertyValueMax) == v {
			return v
		}
	}
	return ""
}

// versionOf is the version the item with id is the tarball of.
func versionOf(ctx context.Context, st store.Store, id int64) (types.PackageVersion, bool, error) {
	return pkgindex.VersionOf(ctx, st, id) //nolint:wrapcheck // pkgindex wraps its own
}

// unindex drops the version it was the tarball of, if any, now that it
// holds other content, and its package when no version is left; a package
// with versions left that lost latest with it settles it anew (rule 4), at
// the write's time. A version has one file, its tarball, so a file that no
// longer backs it leaves nothing to keep.
//
// The dropped version's dist-tags and deprecation are off the file by
// now: the item service gives a file that took other content only the
// properties the write brings (decision 0034), so none is left for a later
// move to its own canonical path, by a conversion say, to hand to a
// version that never had it, latest included (rule 1).
func (h Handler) unindex(ctx context.Context, st store.Store, it types.Item) error {
	pv, ok, err := versionOf(ctx, st, it.ID)
	if err != nil || !ok {
		return err
	}
	if err := st.Packages().DeleteVersion(ctx, pv.ID); err != nil {
		return fmt.Errorf("npm: drop version %s: %w", pv.Version, err)
	}
	rest, err := st.Packages().Versions(ctx, pv.PackageID)
	if err != nil {
		return fmt.Errorf("npm: versions of package %d: %w", pv.PackageID, err)
	}
	if len(rest) > 0 {
		return h.Settings.settleLatest(ctx, st, pv.PackageID, it.Updated)
	}
	if err := st.Packages().Delete(ctx, pv.PackageID); err != nil {
		return fmt.Errorf("npm: drop package %d: %w", pv.PackageID, err)
	}
	return nil
}

// moveTags removes tags from every version of to's package but to, so each
// names to alone. The versions' Metadata says which carry them, so only
// those tarballs are read.
func moveTags(
	ctx context.Context, st store.Store, to types.PackageVersion, tags []string, now time.Time,
) error {
	versions, err := st.Packages().Versions(ctx, to.PackageID)
	if err != nil {
		return fmt.Errorf("npm: versions of package %d: %w", to.PackageID, err)
	}
	for _, v := range versions {
		if v.ID == to.ID {
			continue
		}
		m, err := DecodeMetadata(v.Metadata)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(m.Tags, func(t string) bool { return slices.Contains(tags, t) }) {
			continue
		}
		files, err := st.Packages().Files(ctx, v.ID)
		if err != nil {
			return fmt.Errorf("npm: files of version %s: %w", v.Version, err)
		}
		for _, id := range files {
			if err := dropTags(ctx, st, id, tags, now); err != nil {
				return err
			}
			if err := refresh(ctx, st, v, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropTags removes tags from the npm.disttag of the item with id.
func dropTags(ctx context.Context, st store.Store, id int64, tags []string, now time.Time) error {
	props, err := st.Items().Properties(ctx, id)
	if err != nil {
		return fmt.Errorf("npm: properties of item %d: %w", id, err)
	}
	kept := slices.DeleteFunc(slices.Clone(props[PropDistTag]),
		func(t string) bool { return slices.Contains(tags, t) })
	var set types.Properties
	remove := []string{PropDistTag}
	if len(kept) > 0 {
		set, remove = types.Properties{PropDistTag: kept}, nil
	}
	if err := st.Items().PatchProperties(ctx, id, set, remove, now); err != nil {
		return fmt.Errorf("npm: dist-tags of item %d: %w", id, err)
	}
	return nil
}

// refresh brings pv's Metadata in line with the properties of its tarball,
// the item with id. Nothing else in it follows from properties, so the
// tarball need not be read.
func refresh(ctx context.Context, st store.Store, pv types.PackageVersion, id int64) error {
	props, err := st.Items().Properties(ctx, id)
	if err != nil {
		return fmt.Errorf("npm: properties of item %d: %w", id, err)
	}
	m, err := DecodeMetadata(pv.Metadata)
	if err != nil {
		return err
	}
	m.annotate(props)
	b := m.encode()
	if bytes.Equal(b, pv.Metadata) {
		return nil
	}
	pv.Metadata = b
	if _, err := st.Packages().PutVersion(ctx, pv); err != nil {
		return fmt.Errorf("npm: version %s: %w", pv.Version, err)
	}
	return nil
}

// tarballQuery finds the .tgz files c's index reads, a remote repository's
// in its cache (pkgindex.Stored), with their properties when props is set.
func tarballQuery(c repo.Config, props bool) *aql.Query {
	q := &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: "*.tgz"},
	}}
	if props {
		q.Include = []string{"property"}
	}
	return q
}

// reindex brings the index of c in line with its tarballs and their
// properties. The tarballs are read first, outside any transaction, so no
// decompression holds the store's write lock; the transaction then reads
// the tree again and reads only content that changed in between.
func (h Handler) reindex(ctx context.Context, env format.Env, c repo.Config) error {
	st := env.Store
	hits, err := st.Items().Search(ctx, tarballQuery(c, false), aql.Options{})
	if err != nil {
		return fmt.Errorf("npm: tarballs of %s: %w", c.Key, err)
	}
	read := map[string]*Tarball{}
	for _, h := range hits {
		if _, err := readOnce(ctx, env, read, h.Item); err != nil {
			return err
		}
	}
	err = st.Tx(ctx, func(ctx context.Context) error {
		hits, err := st.Items().Search(ctx, tarballQuery(c, true), aql.Options{})
		if err != nil {
			return fmt.Errorf("npm: tarballs of %s: %w", c.Key, err)
		}
		byName := map[string]*pkgindex.Indexed{}
		for _, h := range hits {
			t, err := readOnce(ctx, env, read, h.Item)
			if err != nil {
				return err
			}
			if t == nil || h.Item.FullPath() != CanonicalPath(t.Name, t.Version) {
				continue
			}
			at := published(h.Item)
			p := byName[t.Name]
			if p == nil {
				p = &pkgindex.Indexed{Name: t.Name, Created: at}
				byName[t.Name] = p
			}
			p.Created = earliest(p.Created, at)
			p.Versions = append(p.Versions, pkgindex.IndexedVersion{
				Version: t.Version, Created: at,
				Metadata: metadataOf(t, h.Item, h.Properties).encode(), Files: []int64{h.Item.ID},
			})
		}
		want := make([]pkgindex.Indexed, 0, len(byName))
		for _, p := range byName {
			want = append(want, *p)
		}
		if err := pkgindex.Reconcile(ctx, st, c, want); err != nil {
			return err //nolint:wrapcheck // pkgindex wraps its own
		}
		// latest is kept as the properties have it; a package none of
		// whose tarballs carries it gets it by rule 4, as 7.x's indexing
		// writes it (7.84.10, RTDEV-40221).
		for _, name := range slices.Sorted(maps.Keys(byName)) {
			p, err := st.Packages().Find(ctx, c.Key, name)
			if err != nil {
				return fmt.Errorf("npm: package %s: %w", name, err)
			}
			if err := h.Settings.settleLatest(ctx, st, p.ID, env.Now()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// readOnce reads the tarball it, once per content: nil when the format
// cannot index it or its path could not be canonical.
func readOnce(
	ctx context.Context,
	env format.Env,
	read map[string]*Tarball,
	it types.Item,
) (*Tarball, error) {
	if !candidate(it.FullPath()) {
		return nil, nil //nolint:nilnil // no tarball to read is no failure
	}
	if t, ok := read[it.Actual.SHA256]; ok {
		return t, nil
	}
	t, _, err := ReadStored(ctx, env.Blobs, it.Actual.SHA256)
	if err != nil {
		return nil, err
	}
	read[it.Actual.SHA256] = t
	return t, nil
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
