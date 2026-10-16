package registry_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

var errBoom = errors.New("boom")

// faultyStore fails what its fields name and delegates the rest.
type faultyStore struct {
	store.Store
	uploads *faultyUploads
	items   *faultyItems
	repos   *faultyRepos
}

func (f *faultyStore) Uploads() store.UploadStore { return f.uploads }
func (f *faultyStore) Items() store.ItemStore     { return f.items }
func (f *faultyStore) Repos() store.RepoStore     { return f.repos }

type faultyUploads struct {
	store.UploadStore
	get, del error
	// put fails the put numbered failPut (1 is the first), when set.
	put     error
	puts    int
	failPut int
}

func (u *faultyUploads) Put(ctx context.Context, up store.Upload) error {
	u.puts++
	if u.put != nil && u.puts >= u.failPut {
		return u.put
	}
	return u.UploadStore.Put(ctx, up) //nolint:wrapcheck // test double
}

func (u *faultyUploads) Get(ctx context.Context, id string) (store.Upload, error) {
	if u.get != nil {
		return store.Upload{}, u.get
	}
	return u.UploadStore.Get(ctx, id) //nolint:wrapcheck // test double
}

func (u *faultyUploads) Delete(ctx context.Context, id string) error {
	if u.del != nil {
		return u.del
	}
	return u.UploadStore.Delete(ctx, id) //nolint:wrapcheck // test double
}

type faultyItems struct {
	store.ItemStore
	search, del error
}

func (i *faultyItems) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	if i.search != nil {
		return nil, i.search
	}
	return i.ItemStore.Search(ctx, q, o) //nolint:wrapcheck // test double
}

func (i *faultyItems) Delete(
	ctx context.Context, repoKey string, cached bool, fullPath string,
) (store.DeleteResult, error) {
	if i.del != nil {
		return store.DeleteResult{}, i.del
	}
	return i.ItemStore.Delete(ctx, repoKey, cached, fullPath) //nolint:wrapcheck // test double
}

type faultyRepos struct {
	store.RepoStore
	list error
}

func (r *faultyRepos) List(ctx context.Context, f store.RepoFilter) ([]repo.Config, error) {
	if r.list != nil {
		return nil, r.list
	}
	return r.RepoStore.List(ctx, f) //nolint:wrapcheck // test double
}

// faultyBlobs fails what its fields name, its uploads included.
type faultyBlobs struct {
	blob.Store
	begin, resume               error
	appendErr, commit, abortErr error
}

func (b *faultyBlobs) BeginUpload(
	ctx context.Context,
	o ...blob.UploadOption,
) (blob.Upload, error) {
	if b.begin != nil {
		return nil, b.begin
	}
	u, err := b.Store.BeginUpload(ctx, o...)
	if err != nil {
		return nil, err //nolint:wrapcheck // test double
	}
	return &faultyUpload{Upload: u, b: b}, nil
}

func (b *faultyBlobs) ResumeUpload(ctx context.Context, id string) (blob.Upload, error) {
	if b.resume != nil {
		return nil, b.resume
	}
	u, err := b.Store.ResumeUpload(ctx, id)
	if err != nil {
		return nil, err //nolint:wrapcheck // test double
	}
	return &faultyUpload{Upload: u, b: b}, nil
}

type faultyUpload struct {
	blob.Upload
	b *faultyBlobs
}

func (u *faultyUpload) Append(ctx context.Context, r io.Reader) (int64, error) {
	if u.b.appendErr != nil {
		return 0, u.b.appendErr
	}
	return u.Upload.Append(ctx, r) //nolint:wrapcheck // test double
}

func (u *faultyUpload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if u.b.commit != nil {
		return blob.Info{}, u.b.commit
	}
	return u.Upload.Commit(ctx, want) //nolint:wrapcheck // test double
}

func (u *faultyUpload) Abort(ctx context.Context) error {
	if u.b.abortErr != nil {
		return u.b.abortErr
	}
	return u.Upload.Abort(ctx) //nolint:wrapcheck // test double
}

// faulty is a world whose store and blob store fail on demand.
type faulty struct {
	*world
	st *faultyStore
	bl *faultyBlobs
}

func newFaulty(t *testing.T) *faulty {
	t.Helper()
	db := inmem.New()
	w := newWorld(t, db)
	f := &faulty{world: w}
	f.st = &faultyStore{
		Store:   db,
		uploads: &faultyUploads{UploadStore: db.Uploads()},
		items:   &faultyItems{ItemStore: db.Items()},
		repos:   &faultyRepos{RepoStore: db.Repos()},
	}
	f.bl = &faultyBlobs{Store: w.blobs}
	f.svc = &registry.Service{Store: f.st, Blobs: f.bl, Now: func() time.Time { return start }}
	return f
}

func (f *faulty) begin(t *testing.T) string {
	t.Helper()
	id, err := f.svc.Begin(context.Background(), f.owner("app"))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBlobStoreFailures(t *testing.T) {
	ctx := context.Background()
	o := func(f *faulty) registry.Owner { return f.owner("app") }
	cases := map[string]func(t *testing.T, f *faulty) error{
		"begin": func(_ *testing.T, f *faulty) error {
			f.bl.begin = errBoom
			_, err := f.svc.Begin(ctx, o(f))
			return err
		},
		"begin monolithic": func(_ *testing.T, f *faulty) error {
			f.bl.begin = errBoom
			_, err := f.svc.Upload(ctx, o(f), strings.NewReader("x"), sum("x"))
			return err
		},
		"resume": func(t *testing.T, f *faulty) error {
			id := f.begin(t)
			f.bl.resume = errBoom
			_, err := f.svc.Status(ctx, o(f), id)
			return err
		},
		"append": func(t *testing.T, f *faulty) error {
			id := f.begin(t)
			f.bl.appendErr = errBoom
			_, err := f.svc.Append(ctx, o(f), id, -1, strings.NewReader("x"))
			return err
		},
		"append monolithic": func(_ *testing.T, f *faulty) error {
			f.bl.appendErr = errBoom
			_, err := f.svc.Upload(ctx, o(f), strings.NewReader("x"), sum("x"))
			return err
		},
		"commit": func(t *testing.T, f *faulty) error {
			id := f.begin(t)
			f.bl.commit = errBoom
			_, err := f.svc.Finish(ctx, o(f), id, -1, strings.NewReader("x"), sum("x"))
			return err
		},
		"commit monolithic": func(_ *testing.T, f *faulty) error {
			f.bl.commit = errBoom
			_, err := f.svc.Upload(ctx, o(f), strings.NewReader("x"), sum("x"))
			return err
		},
		"abort on cancel": func(t *testing.T, f *faulty) error {
			id := f.begin(t)
			f.bl.abortErr = errBoom
			return f.svc.Cancel(ctx, o(f), id)
		},
		"abort on reap": func(t *testing.T, f *faulty) error {
			id := f.begin(t)
			row, err := f.db.Uploads().Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			f.bl.abortErr = errBoom
			return f.svc.Reap(ctx, row)
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := c(t, newFaulty(t)); !errors.Is(err, errBoom) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestMetadataStoreFailures(t *testing.T) {
	ctx := context.Background()
	o := func(f *faulty) registry.Owner { return f.owner("app") }
	cases := map[string]func(t *testing.T, f *faulty) error{
		"save on begin": func(_ *testing.T, f *faulty) error {
			f.st.uploads.put, f.st.uploads.failPut = errBoom, 1
			_, err := f.svc.Begin(ctx, o(f))
			return err
		},
		"load": func(t *testing.T, f *faulty) error {
			id := f.begin(t)
			f.st.uploads.get = errBoom
			_, err := f.svc.Status(ctx, o(f), id)
			return err
		},
		"save on append": func(t *testing.T, f *faulty) error {
			id := f.begin(t)
			f.st.uploads.put, f.st.uploads.failPut = errBoom, 2
			_, err := f.svc.Append(ctx, o(f), id, -1, strings.NewReader("x"))
			return err
		},
		"drop on cancel": func(t *testing.T, f *faulty) error {
			id := f.begin(t)
			f.st.uploads.del = errBoom
			return f.svc.Cancel(ctx, o(f), id)
		},
		"drop on finish": func(t *testing.T, f *faulty) error {
			id := f.begin(t)
			f.st.uploads.del = errBoom
			_, err := f.svc.Finish(ctx, o(f), id, -1, strings.NewReader("x"), sum("x"))
			return err
		},
		"find a mount's source": func(_ *testing.T, f *faulty) error {
			f.st.items.search = errBoom
			_, _, err := f.svc.Mount(ctx, o(f), f.oci, "base", sum("x"))
			return err
		},
		"list repositories to expire": func(_ *testing.T, f *faulty) error {
			f.st.repos.list = errBoom
			_, err := f.svc.ExpireUploads(ctx, start)
			return err
		},
		"find expired uploads": func(_ *testing.T, f *faulty) error {
			f.st.items.search = errBoom
			_, err := f.svc.ExpireUploads(ctx, start)
			return err
		},
		"remove an expired upload": func(_ *testing.T, f *faulty) error {
			f.deploy(f.docker, "app/_uploads/sha256__"+sum("old"), "old", start.Add(-48*time.Hour))
			f.st.items.del = errBoom
			_, err := f.svc.ExpireUploads(ctx, start)
			return err
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := c(t, newFaulty(t)); !errors.Is(err, errBoom) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// A file where the image's folders go refuses the item; the upload is
// committed and the session ends anyway.
func TestPathConflicts(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, inmem.New())
	w.svc.Now = nil // the clock
	w.deploy(w.docker, "app", "a file", start)
	o := w.owner("app")
	if _, err := w.svc.Upload(ctx, o, strings.NewReader("x"), sum("x")); err == nil {
		t.Fatal("monolithic upload under a file")
	}
	id, err := w.svc.Begin(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Finish(ctx, o, id, -1, strings.NewReader("y"), sum("y")); err == nil {
		t.Fatal("finished under a file")
	}
	if _, err := w.svc.Status(ctx, o, id); !errors.Is(err, registry.ErrUnknown) {
		t.Fatalf("session kept: %v", err)
	}
	w.deploy(w.oci, "base/v1/sha256__"+sum("z"), "z", start)
	if _, _, err := w.svc.Mount(ctx, o, w.oci, "base", sum("z")); err == nil {
		t.Fatal("mounted under a file")
	}
}
