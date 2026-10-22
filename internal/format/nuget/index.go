package nuget

import (
	"cmp"
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

// member is a file at one of a version's canonical names.
type member struct {
	it   types.Item
	kind kind
	// nuspec is its package's when already read, nil otherwise.
	nuspec *Nuspec
}

// sync makes version key what its files say: the files linked to it now,
// with add among them when it is set. A version with no file left goes,
// and its package with its last version.
func sync(ctx context.Context, env format.Env, c repo.Config, key versionKey, add *member) error {
	return syncFiles(ctx, env, c, key, add, 0)
}

// syncWithout is sync of key without the item with id drop, which no
// longer belongs to it.
func syncWithout(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	key versionKey,
	drop int64,
) error {
	return syncFiles(ctx, env, c, key, nil, drop)
}

func syncFiles(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	key versionKey,
	add *member,
	drop int64,
) error {
	st := env.Store
	p, err := st.Packages().Find(ctx, c.Key, key.pkg)
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("nuget: package %s: %w", key.pkg, err)
	}
	var pv types.PackageVersion
	linked := false
	if exists {
		pv, err = st.Packages().Version(ctx, p.ID, key.version)
		linked = err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("nuget: version %s of %s: %w", key.version, key.pkg, err)
		}
	}
	if !linked && add == nil {
		return nil
	}
	var members []member
	if linked {
		if members, err = linkedMembers(ctx, st, pv, add, drop); err != nil {
			return err
		}
	}
	if add != nil {
		members = append(members, *add)
	}
	if len(members) == 0 {
		return dropVersion(ctx, st, pv)
	}
	pkg, sym, ids := pick(members)
	old, decodeErr := DecodeMetadata(pv.Metadata)
	var n *Nuspec
	if pkg != nil {
		// Metadata that does not decode describes nothing to reuse, so the
		// package is read.
		if n, err = nuspecOf(ctx, env, *pkg, old, decodeErr == nil); err != nil {
			return err
		}
	}
	sources, err := sourcesOf(ctx, env, c, members, old)
	if err != nil {
		return err
	}
	iv := entry(key, pkg, sym, ids, n, sources)
	if !exists {
		p, err = st.Packages().Ensure(ctx, types.Package{
			RepoKey: c.Key, Name: key.pkg, Type: c.PackageType, Created: iv.Created,
		})
		if err != nil {
			return fmt.Errorf("nuget: package %s: %w", key.pkg, err)
		}
	}
	_, err = pkgindex.Sync(ctx, st, p, iv)
	return err //nolint:wrapcheck // pkgindex wraps its own
}

// linkedMembers are the files linked to pv but add's and drop.
func linkedMembers(
	ctx context.Context,
	st store.Store,
	pv types.PackageVersion,
	add *member,
	drop int64,
) ([]member, error) {
	ids, err := st.Packages().Files(ctx, pv.ID)
	if err != nil {
		return nil, fmt.Errorf("nuget: files of version %s: %w", pv.Version, err)
	}
	out := make([]member, 0, len(ids))
	for _, id := range ids {
		if id == drop || add != nil && id == add.it.ID {
			continue
		}
		it, err := st.Items().GetByID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("nuget: file %d of version %s: %w", id, pv.Version, err)
		}
		out = append(out, member{it: it, kind: kindOf(it.FullPath())})
	}
	return out, nil
}

// dropVersion removes pv, whose last file left it, and its package with
// its last version.
func dropVersion(ctx context.Context, st store.Store, pv types.PackageVersion) error {
	if err := st.Packages().DeleteVersion(ctx, pv.ID); err != nil {
		return fmt.Errorf("nuget: drop version %s: %w", pv.Version, err)
	}
	rest, err := st.Packages().Versions(ctx, pv.PackageID)
	if err != nil {
		return fmt.Errorf("nuget: versions of package %d: %w", pv.PackageID, err)
	}
	if len(rest) > 0 {
		return nil
	}
	if err := st.Packages().Delete(ctx, pv.PackageID); err != nil {
		return fmt.Errorf("nuget: drop package %d: %w", pv.PackageID, err)
	}
	return nil
}

// nuspecOf is the nuspec of m's package: the one already read, the one the
// version's Metadata old cached, when ok, if it describes m's content, so
// a .snupkg pushed after its package reads no package, or else a read of
// the content.
func nuspecOf(
	ctx context.Context,
	env format.Env,
	m member,
	old Metadata,
	ok bool,
) (*Nuspec, error) {
	if m.nuspec != nil {
		return m.nuspec, nil
	}
	if ok && old.Nuspec != "" && old.Path == m.it.FullPath() &&
		old.SHA512 == digest(m.it.Actual.SHA512) {
		if n, err := ParseNuspec([]byte(old.Nuspec)); err == nil {
			return n, nil
		}
	}
	n, err := ReadStored(ctx, env.Blobs, m.it.Actual.SHA256)
	if err != nil {
		// A linked file held a package when it was linked, and any write of
		// other content to it unlinks it first, so this is a broken store.
		return nil, fmt.Errorf("nuget: %s: %w", m.it.FullPath(), err)
	}
	return n, nil
}

// older orders files by when they were deployed, a tie broken by path:
// the first of each kind is the one a version's Metadata describes.
func older(a, b member) int {
	return cmp.Or(
		published(a.it).Compare(published(b.it)),
		cmp.Compare(a.it.FullPath(), b.it.FullPath()),
	)
}

// pick is the package and the symbols a version whose files are members
// is described by, each the one of its kind deployed first, and the IDs of
// all its files.
func pick(members []member) (pkg, sym *member, ids []int64) {
	members = slices.Clone(members)
	slices.SortFunc(members, older)
	for i := range members {
		f := &members[i]
		ids = append(ids, f.it.ID)
		switch {
		case f.kind == pkgFile && pkg == nil:
			pkg = f
		case f.kind == symbolsFile && sym == nil:
			sym = f
		}
	}
	slices.Sort(ids)
	return pkg, sym, ids
}

// entry is the index's entry for version key, whose files are ids: its
// Metadata describes pkg, whose nuspec is n, and names sym, and it is
// dated by pkg, or by sym when there is no package.
func entry(
	key versionKey,
	pkg, sym *member,
	ids []int64,
	n *Nuspec,
	sources []SymbolSource,
) pkgindex.IndexedVersion {
	var (
		m       Metadata
		created time.Time
	)
	if pkg != nil {
		m = describe(n, pkg.it)
		created = m.Published
	}
	if sym != nil {
		m.Symbols = sym.it.FullPath()
		if pkg == nil {
			created = published(sym.it)
		}
	}
	m.SymbolSources = sources
	return pkgindex.IndexedVersion{
		Version: key.version, Created: created, Metadata: m.encode(), Files: ids,
	}
}

// candidateQuery finds the .nupkg and .snupkg files c's index reads, a
// remote repository's in its cache (pkgindex.Stored), with their
// properties when props is set.
func candidateQuery(c repo.Config, props bool) *aql.Query {
	q := &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		aql.Or{
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: "*" + PackageExt},
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: "*" + SymbolsExt},
		},
	}}
	if props {
		q.Include = []string{"property"}
	}
	return q
}

// reindex brings the index of c in line with its packages, and their
// nuget.* properties with their nuspecs. The packages are read first,
// outside any transaction, so no decompression holds the store's write
// lock; the transaction then reads the tree again and reads only content
// that changed in between.
func reindex(ctx context.Context, env format.Env, c repo.Config) error {
	if c.Class == repo.Remote {
		return reindexRemote(ctx, env, c)
	}
	st := env.Store
	hits, err := st.Items().Search(ctx, candidateQuery(c, false), aql.Options{})
	if err != nil {
		return fmt.Errorf("nuget: packages of %s: %w", c.Key, err)
	}
	cache := map[string]*Nuspec{}
	var oldSymbols Metadata
	for _, h := range hits {
		if _, err := readOnce(ctx, env, cache, h.Item); err != nil {
			return err
		}
		if c.Class == repo.Local && kindOf(h.Item.FullPath()) == symbolsFile {
			s, err := symbolsOf(ctx, env, h.Item, nil)
			if err != nil {
				return err
			}
			oldSymbols.SymbolSources = append(oldSymbols.SymbolSources, s)
		}
	}
	err = st.Tx(ctx, func(ctx context.Context) error {
		hits, err := st.Items().Search(ctx, candidateQuery(c, true), aql.Options{})
		if err != nil {
			return fmt.Errorf("nuget: packages of %s: %w", c.Key, err)
		}
		groups := map[versionKey][]member{}
		for _, h := range hits {
			n, err := readOnce(ctx, env, cache, h.Item)
			if err != nil {
				return err
			}
			k := kindOf(h.Item.FullPath())
			if n == nil || !canonical(h.Item.FullPath(), k, n) {
				continue
			}
			if k == pkgFile {
				if err := patchProperties(ctx, st, h.Item, h.Properties, n, env.Now()); err != nil {
					return err
				}
			}
			key := keyOf(n)
			groups[key] = append(groups[key], member{it: h.Item, kind: k, nuspec: n})
		}
		if err := reconcile(ctx, env, c, groups, oldSymbols); err != nil {
			return err
		}
		return queueSymbols(ctx, env, c)
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// reconcile makes c's index hold exactly the versions groups gives.
func reconcile(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	groups map[versionKey][]member,
	oldSymbols Metadata,
) error {
	byName := map[string]*pkgindex.Indexed{}
	for _, key := range slices.SortedFunc(maps.Keys(groups), func(a, b versionKey) int {
		return cmp.Or(cmp.Compare(a.pkg, b.pkg), cmp.Compare(a.version, b.version))
	}) {
		pkg, sym, ids := pick(groups[key])
		var n *Nuspec
		if pkg != nil {
			n = pkg.nuspec
		}
		sources, err := sourcesOf(ctx, env, c, groups[key], oldSymbols)
		if err != nil {
			return err
		}
		iv := entry(key, pkg, sym, ids, n, sources)
		p := byName[key.pkg]
		if p == nil {
			p = &pkgindex.Indexed{Name: key.pkg, Created: iv.Created}
			byName[key.pkg] = p
		}
		if iv.Created.Before(p.Created) {
			p.Created = iv.Created
		}
		p.Versions = append(p.Versions, iv)
	}
	want := make([]pkgindex.Indexed, 0, len(byName))
	for _, p := range byName {
		want = append(want, *p)
	}
	return pkgindex.Reconcile(ctx, env.Store, c, want) //nolint:wrapcheck // pkgindex wraps its own
}

// readOnce reads the package it holds, once per content: nil when it is
// no .nupkg or .snupkg, or no valid package.
func readOnce(
	ctx context.Context,
	env format.Env,
	cache map[string]*Nuspec,
	it types.Item,
) (*Nuspec, error) {
	if kindOf(it.FullPath()) == other {
		return nil, nil //nolint:nilnil // no package to read is no failure
	}
	if n, ok := cache[it.Actual.SHA256]; ok {
		return n, nil
	}
	in, err := read(ctx, env, it.Actual.SHA256)
	if err != nil {
		return nil, err
	}
	cache[it.Actual.SHA256] = in.nuspec
	return in.nuspec, nil
}
