package formattest

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// tagKey is the property the tagged format indexes, as npm's dist-tags
// are.
const tagKey = "formattest.tag"

// rebuildKind is the work the tagged format queues per package.
const rebuildKind = "formattest.rebuild"

// tagged is a format that exercises every part of the contract: a file at
// p/v/... belongs to version v of package p, the version's metadata lists
// the tags its files carry, and every deploy queues a rebuild of its
// package.
type tagged struct {
	generic.Handler
	// unlinked, when set, is the suffix of the files the format links to no
	// version and names in their version's metadata instead, as a format
	// that notes a version's sidecars would.
	unlinked string
}

// coordinates names the version of the file at full; ok is false for a
// file outside any.
func coordinates(full string) (p, v string, ok bool) {
	parts := strings.Split(full, "/")
	if len(parts) < 3 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// read reads version v of package p from c's tree; ok is false when it has
// no files to link.
func (h tagged) read(ctx context.Context, st store.Store, c repo.Config, p, v string) (
	pkgindex.IndexedVersion, bool, error,
) {
	out := pkgindex.IndexedVersion{Version: v}
	files, _, err := st.Items().Descendants(ctx, c.Key, pkgindex.Stored(c).Cached, p+"/"+v,
		store.Page{Limit: store.MaxLimit})
	if err != nil {
		return out, false, err
	}
	tags := map[string]bool{}
	var noted []string
	for _, f := range files {
		if f.Folder {
			continue
		}
		if h.unlinked != "" && strings.HasSuffix(f.FullPath(), h.unlinked) {
			noted = append(noted, path.Base(f.FullPath()))
			continue
		}
		if out.Created.IsZero() || f.Created.Before(out.Created) {
			out.Created = f.Created
		}
		props, err := st.Items().Properties(ctx, f.ID)
		if err != nil {
			return out, false, err
		}
		for _, t := range props[tagKey] {
			tags[t] = true
		}
		out.Files = append(out.Files, f.ID)
	}
	meta := strings.Join(slices.Sorted(maps.Keys(tags)), ",")
	if len(noted) > 0 {
		slices.Sort(noted)
		meta += " beside " + strings.Join(noted, ",")
	}
	out.Metadata = []byte(meta)
	return out, len(out.Files) > 0, nil
}

// sync makes the index of the version of the file at full what the tree
// says.
func (h tagged) sync(ctx context.Context, env format.Env, c repo.Config, full string) error {
	p, v, ok := coordinates(full)
	if !ok {
		return nil
	}
	iv, ok, err := h.read(ctx, env.Store, c, p, v)
	if err != nil || !ok {
		return err
	}
	pkg, err := env.Store.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Name: p, Type: c.PackageType, Created: iv.Created,
	})
	if err != nil {
		return err
	}
	_, err = pkgindex.Sync(ctx, env.Store, pkg, iv)
	return err
}

func (h tagged) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if err := h.sync(ctx, env, c, it.FullPath()); err != nil {
		return err
	}
	if p, _, ok := coordinates(it.FullPath()); ok {
		return env.Enqueue(ctx, rebuildKind, p, nil)
	}
	return nil
}

func (h tagged) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	for _, it := range removed {
		if err := h.sync(ctx, env, c, it.FullPath()); err != nil {
			return err
		}
	}
	return nil
}

func (h tagged) Annotated(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
	_ format.Annotation,
) error {
	return h.sync(ctx, env, c, it.FullPath())
}

func (h tagged) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	return env.Store.Tx(ctx, func(ctx context.Context) error {
		all, _, err := env.Store.Items().Descendants(ctx, c.Key, pkgindex.Stored(c).Cached, "",
			store.Page{Limit: store.MaxLimit})
		if err != nil {
			return err
		}
		byName := map[string]*pkgindex.Indexed{}
		seen := map[string]bool{}
		for _, it := range all {
			p, v, ok := coordinates(it.FullPath())
			if !ok || it.Folder || seen[p+"/"+v] {
				continue
			}
			seen[p+"/"+v] = true
			iv, ok, err := h.read(ctx, env.Store, c, p, v)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if byName[p] == nil {
				byName[p] = &pkgindex.Indexed{Name: p}
			}
			byName[p].Versions = append(byName[p].Versions, iv)
		}
		var want []pkgindex.Indexed
		for _, p := range byName {
			want = append(want, *p)
		}
		return pkgindex.Reconcile(ctx, env.Store, c, want)
	})
}

// taggedFormat is the fixture of the tagged format: two files per version,
// the second rewritten by a redeploy.
func taggedFormat(h format.Handler) Format {
	return Format{
		Type:    repo.NPM,
		Handler: h,
		Version: func(p, v string, n int) Version {
			return Version{Package: p, Version: v, Files: []File{
				{Path: p + "/" + v + "/" + p + ".tgz", Body: []byte(p + v)},
				{
					Path: p + "/" + v + "/" + p + ".json",
					Body: fmt.Appendf(nil, "%s %s %d", p, v, n),
				},
			}}
		},
		Properties: []types.Properties{{tagKey: {"latest"}}, {tagKey: {"next", "beta"}}},
		Work:       []string{rebuildKind},
	}
}

func TestUnitTaggedFormatKeepsTheContract(t *testing.T) {
	Run(t, taggedFormat(tagged{}))
}

// indexKind is the work the indexing format queues per repository.
const indexKind = "formattest.index"

// indexing derives one index per repository, as Helm's index.yaml is
// (decision 0039): every write that changes a repository's versions queues
// it, keyed by the repository, beside the tagged format's rebuild of each
// package.
type indexing struct{ tagged }

func (h indexing) Deployed(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
) error {
	if err := h.tagged.Deployed(ctx, env, c, it); err != nil {
		return err
	}
	return env.Enqueue(ctx, indexKind, c.Key, nil)
}

func (h indexing) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	if err := h.tagged.Deleted(ctx, env, c, removed); err != nil {
		return err
	}
	return env.Enqueue(ctx, indexKind, c.Key, nil)
}

func indexingFormat(h format.Handler) Format {
	f := taggedFormat(h)
	f.RepoWork = []string{indexKind}
	return f
}

// A format with work keyed by repository passes beside work keyed by
// package.
func TestUnitRepositoryWorkKeepsTheContract(t *testing.T) {
	Run(t, indexingFormat(indexing{}))
}

// prov is the suffix of the sidecars the keeping format keeps.
const prov = ".prov"

// keeping keeps each tarball's provenance file with it, links it to no
// version and notes it in the version's metadata, as Helm keeps a chart's
// .prov (decision 0039).
type keeping struct{ tagged }

func (keeping) Sidecars(_ repo.Config, p string) []string {
	if strings.HasSuffix(p, ".tgz") {
		return []string{p + prov}
	}
	return nil
}

// keepingFormat is the tagged fixture with a provenance file after each
// version's tarball.
func keepingFormat(h format.Handler) Format {
	f := taggedFormat(h)
	files := f.Version
	f.Version = func(p, v string, n int) Version {
		out := files(p, v, n)
		owner := out.Files[0].Path
		out.Files = append(out.Files, File{
			Path:      owner + prov,
			Body:      fmt.Appendf(nil, "signed %s %s %d", p, v, n),
			SidecarOf: owner,
		})
		return out
	}
	return f
}

// A format whose files own sidecars passes, the sidecars following them
// through every write and its metadata noting them rebuilt from empty.
func TestUnitSidecarsKeepTheContract(t *testing.T) {
	Run(t, keepingFormat(keeping{tagged{unlinked: prov}}))
}

// Both options together, as the Helm format will run them.
func TestUnitRepositoryWorkAndSidecarsKeepTheContract(t *testing.T) {
	f := keepingFormat(keepingIndex{indexing{tagged{unlinked: prov}}})
	f.RepoWork = []string{indexKind}
	Run(t, f)
}

// keepingIndex keeps sidecars and queues an index per repository.
type keepingIndex struct{ indexing }

func (keepingIndex) Sidecars(c repo.Config, p string) []string { return keeping{}.Sidecars(c, p) }

// withoutAnnotated forgets to follow property changes.
type withoutAnnotated struct{ t tagged }

func (h withoutAnnotated) Deployed(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
) error {
	return h.t.Deployed(ctx, env, c, it)
}

func (h withoutAnnotated) Deleting(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
) error {
	return h.t.Deleting(ctx, env, c, it)
}

func (h withoutAnnotated) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	r []types.Item,
) error {
	return h.t.Deleted(ctx, env, c, r)
}

func (h withoutAnnotated) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	return h.t.Reindex(ctx, env, c)
}

// openDoor overrides Admit and drops the generic deploy policy.
type openDoor struct{ tagged }

func (openDoor) Admit(
	_ context.Context,
	_ format.Env,
	_ repo.Config,
	w format.Write,
) (format.Write, error) {
	return w, nil
}

// noReindex keeps an index it cannot rebuild.
type noReindex struct{ t tagged }

func (h noReindex) Deployed(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
) error {
	return h.t.Deployed(ctx, env, c, it)
}

func (h noReindex) Deleting(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
) error {
	return nil
}

func (h noReindex) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	r []types.Item,
) error {
	return h.t.Deleted(ctx, env, c, r)
}

// skewed rebuilds an index that disagrees with the one its hooks keep.
type skewed struct{ tagged }

func (h skewed) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	if err := h.tagged.Reindex(ctx, env, c); err != nil {
		return err
	}
	pkgs, _, err := env.Store.Packages().List(ctx, c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		return err
	}
	for _, p := range pkgs {
		vs, err := env.Store.Packages().Versions(ctx, p.ID)
		if err != nil {
			return err
		}
		for _, v := range vs {
			v.Created = v.Created.Add(time.Hour)
			if _, err := env.Store.Packages().PutVersion(ctx, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// perVersion queues its work per version, not per package.
type perVersion struct{ tagged }

func (h perVersion) Deployed(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
) error {
	if err := h.sync(ctx, env, c, it.FullPath()); err != nil {
		return err
	}
	p, v, _ := coordinates(it.FullPath())
	return env.Enqueue(ctx, rebuildKind, p+"/"+v, nil)
}

// keyedIndex queues its per-repository work under the key key gives, and
// only on a deploy.
type keyedIndex struct {
	tagged
	key func(c repo.Config, full string) string
}

func (h keyedIndex) Deployed(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
) error {
	if err := h.tagged.Deployed(ctx, env, c, it); err != nil {
		return err
	}
	return env.Enqueue(ctx, indexKind, h.key(c, it.FullPath()), nil)
}

// byPackage keys the index by package, as if it were per package.
func byPackage(_ repo.Config, full string) string {
	p, _, _ := coordinates(full)
	return p
}

// byRepository keys the index by repository.
func byRepository(c repo.Config, _ string) string { return c.Key }

// forgetful notes sidecars in its hooks but links them on a reindex.
type forgetful struct{ keeping }

func (forgetful) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	return tagged{}.Reindex(ctx, env, c)
}

// escaped is a store whose work queue is another store's, so queued work
// escapes the transaction of the write that asked for it.
type escaped struct {
	store.Store
	queue store.WorkQueue
}

func (e escaped) WorkQueue() store.WorkQueue { return e.queue }

// Each way of breaking the contract fails the scenario that guards it.
func TestUnitTheSuiteCatchesBrokenFormats(t *testing.T) {
	for _, c := range []struct {
		name     string
		f        Format
		st       func() store.Store
		scenario func(*world) error
		want     string
	}{
		{"annotated", taggedFormat(withoutAnnotated{}), nil, propertiesScenario, "after setting"},
		{"admit", taggedFormat(openDoor{}), nil, patternsScenario, "want a 409 refusal"},
		{"reindexer", taggedFormat(noReindex{}), nil, deployScenario, "cannot reindex"},
		{"skewed", taggedFormat(skewed{}), nil, deployScenario, "a reindex changed"},
		{"work", taggedFormat(perVersion{}), nil, workScenario, "queued work"},
		{"undeclared work", func() Format {
			f := taggedFormat(tagged{})
			f.Work = nil
			return f
		}(), nil, workScenario, "queued work"},
		{"escaped work", taggedFormat(tagged{}), func() store.Store {
			return escaped{inmem.New(), inmem.New().WorkQueue()}
		}, rollbackScenario, "left queued work"},
		{"fixture", func() Format {
			f := taggedFormat(tagged{})
			next := f.Version
			f.Version = func(p, v string, n int) Version {
				out := next(p, v, n)
				out.Linked = []string{out.Files[0].Path}
				return out
			}
			return f
		}(), nil, deployScenario, "index (-want +got)"},
		{
			"repository work keyed by package",
			indexingFormat(keyedIndex{key: byPackage}), nil, workScenario, "queued work",
		},
		{
			"repository work under another key",
			indexingFormat(keyedIndex{key: func(c repo.Config, _ string) string {
				return "index:" + c.Key
			}}), nil, workScenario, "no queued formattest.index item keyed formattest-a",
		},
		{
			"repository work forgotten on a move",
			indexingFormat(keyedIndex{key: byRepository}), nil, workScenario, "after moving",
		},
		{
			"repository work undeclared",
			taggedFormat(indexing{}), nil, workScenario, "queued work",
		},
		{
			"a sidecar left behind by a delete",
			keepingFormat(tagged{unlinked: prov}), nil, deleteScenario, "still holds",
		},
		{
			"a sidecar left behind by a copy",
			keepingFormat(tagged{unlinked: prov}), nil, copyScenario, "lacks the file",
		},
		{
			"a sidecar left behind by a move",
			keepingFormat(tagged{unlinked: prov}), nil, moveScenario, "still holds",
		},
		{
			"a sidecar left in the trash can",
			keepingFormat(tagged{unlinked: prov}), nil, restoreScenario, "lacks the file",
		},
		{
			"a sidecar left in the cache",
			keepingFormat(tagged{unlinked: prov}), nil, cacheScenario, "still holds",
		},
		{
			"a sidecar deployed first, left behind",
			keepingFormat(tagged{unlinked: prov}), nil, sidecarScenario, "deployed after its sidecars",
		},
		{
			"a linked sidecar",
			keepingFormat(keeping{}), nil, deployScenario, "index (-want +got)",
		},
		{
			"a sidecar a reindex links",
			keepingFormat(forgetful{keeping{tagged{unlinked: prov}}}), nil, sidecarScenario,
			"a reindex changed",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store.Store(inmem.New())
			if c.st != nil {
				st = c.st()
			}
			err := c.scenario(newWorld(t, st, c.f))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error with %q, got %v", c.want, err)
			}
		})
	}
}

// A fixture that contradicts itself fails before any scenario runs.
func TestUnitInconsistentFixturesFail(t *testing.T) {
	sidecar := func(change func(*Version)) Format {
		f := keepingFormat(keeping{tagged{unlinked: prov}})
		files := f.Version
		f.Version = func(p, v string, n int) Version {
			out := files(p, v, n)
			change(&out)
			return out
		}
		return f
	}
	for _, c := range []struct {
		name string
		f    Format
		want string
	}{
		{"a kind keyed both ways", func() Format {
			f := indexingFormat(indexing{})
			f.Work = append(f.Work, indexKind)
			return f
		}(), "in both Work and RepoWork"},
		{"a sidecar of nothing", sidecar(func(v *Version) {
			v.Files[2].SidecarOf = "elsewhere.tgz"
		}), "is no other file of its version"},
		{"a sidecar of a sidecar", sidecar(func(v *Version) {
			v.Files = append(v.Files, File{Path: v.Files[2].Path + prov, SidecarOf: v.Files[2].Path})
		}), "is no other file of its version"},
		{"a linked sidecar", sidecar(func(v *Version) {
			v.Linked = []string{v.Files[0].Path, v.Files[2].Path}
		}), "never a version file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := check(c.f)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error with %q, got %v", c.want, err)
			}
		})
	}
	if err := check(keepingFormat(keeping{tagged{unlinked: prov}})); err != nil {
		t.Fatalf("a consistent fixture: %v", err)
	}
}

// A format that indexes nothing passes only while the index stays empty.
func TestUnitAnIndexWithoutAReindexerFails(t *testing.T) {
	w := newWorld(t, inmem.New(), Format{Type: repo.Generic, Version: taggedFormat(nil).Version})
	if err := w.verify(w.a); err != nil {
		t.Fatalf("an empty index: %v", err)
	}
	if _, err := w.st.Packages().
		Ensure(w.ctx, types.Package{RepoKey: keyA, Name: "stray"}); err != nil {
		t.Fatal(err)
	}
	if err := w.verify(w.a); err == nil || !strings.Contains(err.Error(), "cannot reindex") {
		t.Fatalf("a stray package: %v", err)
	}
}
