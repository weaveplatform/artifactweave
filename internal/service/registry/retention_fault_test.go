package registry_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// retentionFaults fails the item reads and deletes, the version listing
// and the blob opens its predicates pick, and delegates the rest.
type retentionFaults struct {
	store.Store
	get, del func(path string) bool
	search   func(q *aql.Query) bool
	versions bool
}

func (f *retentionFaults) Items() store.ItemStore { return faultyItemOps{f.Store.Items(), f} }

func (f *retentionFaults) Packages() store.PackageStore {
	return faultyVersions{f.Store.Packages(), f.versions}
}

type faultyItemOps struct {
	store.ItemStore
	f *retentionFaults
}

//nolint:wrapcheck // test double
func (i faultyItemOps) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (types.Item, error) {
	if i.f.get != nil && i.f.get(p) {
		return types.Item{}, errBoom
	}
	return i.ItemStore.Get(ctx, key, cached, p)
}

//nolint:wrapcheck // test double
func (i faultyItemOps) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	if i.f.search != nil && i.f.search(q) {
		return nil, errBoom
	}
	return i.ItemStore.Search(ctx, q, o)
}

//nolint:wrapcheck // test double
func (i faultyItemOps) Delete(
	ctx context.Context, key string, cached bool, p string,
) (store.DeleteResult, error) {
	if i.f.del != nil && i.f.del(p) {
		return store.DeleteResult{}, errBoom
	}
	return i.ItemStore.Delete(ctx, key, cached, p)
}

type faultyVersions struct {
	store.PackageStore
	fail bool
}

//nolint:wrapcheck // test double
func (v faultyVersions) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	if v.fail {
		return nil, errBoom
	}
	return v.PackageStore.Versions(ctx, id)
}

type faultyOpens struct {
	blob.Store
	open func(sha string) bool
}

//nolint:wrapcheck // test double
func (b faultyOpens) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if b.open != nil && b.open(sha) {
		return nil, blob.Info{}, errBoom
	}
	return b.Store.Open(ctx, sha)
}

// retainedQuery is the search for a tag's kept manifests, the only one
// that tests a property.
func retainedQuery(q *aql.Query) bool {
	and, _ := q.Criteria.(aql.And)
	for _, n := range and {
		if _, ok := n.(*aql.PropertyCriterion); ok {
			return true
		}
	}
	return false
}

// manifestsQuery is the search for every manifest of an image: limited to
// the image's own folders by depth, but neither sorted nor limited like the
// index's and the layout's single-manifest lookups.
func manifestsQuery(q *aql.Query) bool {
	and, _ := q.Criteria.(aql.And)
	for _, n := range and {
		if f, ok := n.(*aql.FieldCriterion); ok && f.Field == aql.FieldDepth {
			return q.Sort == nil && !q.HasLimit && !retainedQuery(q)
		}
	}
	return false
}

// Every store or blob failure in a removal or in retention comes back to
// the caller, whose transaction rolls back.
func TestRetentionFailures(t *testing.T) {
	p1, p2 := `{"p":1}`, `{"p":2}`
	multi, other := listOf(sum(p1), sum(p2)), listOf(sum(p2))
	removeMulti := func(r registry.Removal) func(*world, *registry.Service) error {
		return func(w *world, svc *registry.Service) error {
			return svc.RemoveTag(context.Background(), w.owner("app"), "multi", r)
		}
	}
	both := registry.Removal{Platforms: true, Retained: true}
	pushWith := func(edit func(*repo.Config), tag string) func(*world, *registry.Service) error {
		return func(w *world, svc *registry.Service) error {
			c := w.docker
			edit(&c)
			body, digest := manifestOf(layerB)
			m, err := layout.ParseManifest(layout.MediaTypeOCIManifest, []byte(body), digest)
			if err != nil {
				return err
			}
			return svc.PutManifest(context.Background(), registry.Push{
				Owner: registry.Owner{Repo: c, Name: "app", By: "alice"}, Manifest: m,
				Body: []byte(body), Folders: []string{tag},
			})
		}
	}
	retention := func(c *repo.Config) { c.DockerTagRetention = ptr(2) }
	limit := func(c *repo.Config) { c.MaxUniqueTags = ptr(1) }
	cases := []struct {
		name   string
		faults retentionFaults
		open   func(sha string) bool
		run    func(*world, *registry.Service) error
	}{
		{
			name: "remove: get",
			run:  removeMulti(registry.Removal{}),
			faults: retentionFaults{
				get: func(p string) bool { return p == "app/multi/manifest.json" },
			},
		},
		{
			name: "remove: delete", run: removeMulti(registry.Removal{}),
			faults: retentionFaults{del: func(p string) bool { return p == "app/multi" }},
		},
		{
			name: "remove: read the list", run: removeMulti(both),
			open: func(sha string) bool { return sha == sum(multi) },
		},
		{
			name: "remove: kept manifests", run: removeMulti(both),
			faults: retentionFaults{search: retainedQuery},
		},
		{
			name: "remove: the image's manifests", run: removeMulti(both),
			faults: retentionFaults{search: manifestsQuery},
		},
		{
			name: "remove: read another list", run: removeMulti(both),
			open: func(sha string) bool { return sha == sum(other) },
		},
		{
			name: "remove: platform folder", run: removeMulti(both),
			faults: retentionFaults{get: func(p string) bool {
				return p == "app/"+layout.DigestFolder(sum(p1))
			}},
		},
		{
			name: "remove: delete a platform", run: removeMulti(both),
			faults: retentionFaults{del: func(p string) bool {
				return strings.HasPrefix(p, "app/"+layout.DigestFolder(sum(p1)))
			}},
		},
		{
			name: "retention: kept manifests", run: pushWith(retention, "v1"),
			faults: retentionFaults{search: retainedQuery},
		},
		{
			name: "retention: the digest folder", run: pushWith(retention, "v1"),
			faults: retentionFaults{get: func(p string) bool {
				_, digest := manifestOf(layerA)
				return p == "app/"+layout.DigestFolder(digest)
			}},
		},
		{
			name: "retention: the move",
			run:  pushWith(retention, "v1"),
			faults: retentionFaults{
				del: func(p string) bool { return p == "app/v1/manifest.json" },
			},
		},
		{name: "limit: tags", run: pushWith(limit, "v2"), faults: retentionFaults{versions: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, inmem.New())
			for _, p := range []string{p1, p2} {
				w.deploy(w.docker, "app/"+layout.DigestFolder(sum(p))+"/manifest.json", p, start)
			}
			w.deploy(w.docker, "app/multi/list.manifest.json", multi, start)
			w.deploy(w.docker, "app/other/list.manifest.json", other, start)
			w.upload(imageConfig, layerA, layerB)
			body, digest := manifestOf(layerA)
			if err := w.push(body, digest, "v1"); err != nil {
				t.Fatal(err)
			}
			f := tc.faults
			f.Store = w.db
			svc := &registry.Service{
				Store: &f,
				Blobs: faultyOpens{w.blobs, tc.open},
				Now:   w.svc.Now,
			}
			if err := tc.run(w, svc); !errors.Is(err, errBoom) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
