package registry_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var start = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// backend opens a store, and reopen opens it again over the same data, as
// a restarted process would.
type backend struct {
	name string
	open func(t *testing.T) (st store.Store, reopen func() store.Store)
}

// backends are the stores sessions are kept in: the in-memory reference,
// SQLite and PostgreSQL, which skips where Docker cannot run Linux
// containers.
func backends() []backend {
	return []backend{
		{"inmem", func(*testing.T) (store.Store, func() store.Store) {
			db := inmem.New()
			return db, func() store.Store { return db }
		}},
		{"sqlite", func(t *testing.T) (store.Store, func() store.Store) {
			path := filepath.Join(t.TempDir(), "aw.db")
			open := func() store.Store {
				st, err := sqlite.Open(context.Background(), path, sqlite.Options{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = st.Close() })
				return st
			}
			return open(), open
		}},
		{"postgres", func(t *testing.T) (store.Store, func() store.Store) {
			dsn := sqltest.PostgresDSN(t)
			open := func() *sqlcommon.Store {
				st, err := postgres.Open(context.Background(), dsn, postgres.Options{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = st.Close() })
				return st
			}
			// The test binary shares one server, so each test starts empty.
			first := open()
			sqltest.Reset(t, first.DB(), first.Dialect())
			return first, func() store.Store { return open() }
		}},
	}
}

// world is a store with a Docker repository, an OCI repository and a
// generic one, a blob store and the service over them.
type world struct {
	t      *testing.T
	db     store.Store
	blobs  blob.Store
	svc    *registry.Service
	docker repo.Config
	oci    repo.Config
	plain  repo.Config
}

func newWorld(t *testing.T, db store.Store) *world {
	t.Helper()
	ctx := context.Background()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, db: db, blobs: blobs}
	for _, c := range []*repo.Config{
		{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
		{Key: "oci-local", Class: repo.Local, PackageType: repo.OCI},
		{Key: "generic-local", Class: repo.Local, PackageType: repo.Generic},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(c)); err != nil {
			t.Fatal(err)
		}
	}
	w.docker, w.oci, w.plain = w.repo("docker-local"), w.repo("oci-local"), w.repo("generic-local")
	w.svc = w.service(db)
	return w
}

func (w *world) repo(key string) repo.Config {
	w.t.Helper()
	c, err := w.db.Repos().Get(context.Background(), key)
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

func (w *world) service(db store.Store) *registry.Service {
	return &registry.Service{Store: db, Blobs: w.blobs, Now: func() time.Time { return start }}
}

func (w *world) owner(name string) registry.Owner {
	return registry.Owner{Repo: w.docker, Name: name, By: "alice"}
}

// deploy stores a file in c at path, last written at.
func (w *world) deploy(c repo.Config, path, body string, at time.Time) types.Item {
	w.t.Helper()
	items := &item.Service{Store: w.db, Blobs: w.blobs, Now: func() time.Time { return at }}
	it, err := items.Deploy(context.Background(), item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: strings.NewReader(body),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}

func (w *world) item(c repo.Config, path string) (types.Item, error) {
	return w.db.Items().Get(context.Background(), c.Key, false, path)
}

// each runs fn against a fresh world on every backend.
func each(t *testing.T, fn func(t *testing.T, w *world, reopen func() store.Store)) {
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			db, reopen := b.open(t)
			fn(t, newWorld(t, db), reopen)
		})
	}
}

func TestAccChunkedUploadSurvivesARestart(t *testing.T) {
	each(t, func(t *testing.T, w *world, reopen func() store.Store) {
		ctx := context.Background()
		o := w.owner("team/app")
		id, err := w.svc.Begin(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := w.svc.Status(ctx, o, id); err != nil || n != 0 {
			t.Fatalf("status %d %v", n, err)
		}
		if n, err := w.svc.Append(ctx, o, id, 0, strings.NewReader("abc")); err != nil || n != 3 {
			t.Fatalf("first chunk: %d %v", n, err)
		}
		if n, err := w.svc.Append(ctx, o, id, 1, strings.NewReader("zzz")); !errors.Is(
			err, registry.ErrOffset,
		) || n != 3 {
			t.Fatalf("out of order: %d %v", n, err)
		}
		if n, err := w.svc.Append(ctx, o, id, -1, strings.NewReader("def")); err != nil || n != 6 {
			t.Fatalf("streamed: %d %v", n, err)
		}
		row, err := w.db.Uploads().Get(ctx, id)
		if err != nil || row.Backend != registry.Backend || row.RepoKey != "docker-local" ||
			row.Path != "team/app" || row.Bytes != 6 {
			t.Fatalf("row %+v %v", row, err)
		}

		// A new process over the same metadata store and blob store.
		again := w.service(reopen())
		if n, err := again.Status(ctx, o, id); err != nil || n != 6 {
			t.Fatalf("after restart: %d %v", n, err)
		}
		it, err := again.Finish(ctx, o, id, 6, strings.NewReader("ghi"), sum("abcdefghi"))
		if err != nil {
			t.Fatal(err)
		}
		if it.FullPath() != "team/app/_uploads/sha256__"+sum("abcdefghi") || it.Size != 9 ||
			it.CreatedBy != "alice" || it.Actual.SHA256 != sum("abcdefghi") {
			t.Fatalf("item %+v", it)
		}
		if _, err := again.Status(ctx, o, id); !errors.Is(err, registry.ErrUnknown) {
			t.Fatalf("a finished session lingers: %v", err)
		}
		if _, err := w.db.Uploads().Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("row: %v", err)
		}
	})
}

func TestAccSessionsBelongToTheirOwner(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		o := w.owner("app")
		id, err := w.svc.Begin(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		others := map[string]registry.Owner{
			"repository": {Repo: w.oci, Name: "app", By: "alice"},
			"image":      {Repo: w.docker, Name: "other", By: "alice"},
			"user":       {Repo: w.docker, Name: "app", By: "mallory"},
		}
		for name, other := range others {
			if _, err := w.svc.Status(ctx, other, id); !errors.Is(err, registry.ErrUnknown) {
				t.Errorf("another %s: %v", name, err)
			}
		}
		for _, bad := range []string{"", "x", strings.Repeat("0", 32), "../" + id} {
			if _, err := w.svc.Status(ctx, o, bad); !errors.Is(err, registry.ErrUnknown) {
				t.Errorf("id %q: %v", bad, err)
			}
		}
		// A row another backend owns, and one whose state does not decode.
		for name, row := range map[string]store.Upload{
			"backend": {
				ID: strings.Repeat("a", 32), Backend: "s3:hot", RepoKey: "docker-local",
				Started: start, Updated: start,
			},
			"state": {
				ID: strings.Repeat("b", 32), Backend: registry.Backend, RepoKey: "docker-local",
				State: []byte("{"), Started: start, Updated: start,
			},
		} {
			if err := w.db.Uploads().Put(ctx, row); err != nil {
				t.Fatal(err)
			}
			if _, err := w.svc.Status(ctx, o, row.ID); !errors.Is(err, registry.ErrUnknown) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if n, err := w.svc.Status(ctx, o, id); err != nil || n != 0 {
			t.Fatalf("the owner: %d %v", n, err)
		}
	})
}

func TestAccFinishVerifiesTheDigest(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		o := w.owner("app")
		id, err := w.svc.Begin(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.Append(ctx, o, id, -1, strings.NewReader("layer")); err != nil {
			t.Fatal(err)
		}
		// A final chunk out of order leaves the session as it was.
		if _, err := w.svc.Finish(
			ctx, o, id, 2, strings.NewReader("x"), sum("layerx"),
		); !errors.Is(err, registry.ErrOffset) {
			t.Fatalf("out of order: %v", err)
		}
		if _, err := w.svc.Finish(
			ctx, o, id, -1, strings.NewReader(""), sum("other"),
		); !errors.Is(err, registry.ErrDigest) {
			t.Fatalf("mismatch: %v", err)
		}
		if _, err := w.svc.Status(ctx, o, id); !errors.Is(err, registry.ErrUnknown) {
			t.Fatalf("a mismatched session lingers: %v", err)
		}
		if _, err := w.item(w.docker, "app/_uploads/sha256__"+sum("other")); !errors.Is(
			err, store.ErrNotFound,
		) {
			t.Fatalf("stored anyway: %v", err)
		}
	})
}

func TestAccCancel(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		o := w.owner("app")
		id, err := w.svc.Begin(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		blobID := blobUpload(t, w, id)
		if err := w.svc.Cancel(ctx, o, id); err != nil {
			t.Fatal(err)
		}
		if err := w.svc.Cancel(ctx, o, id); !errors.Is(err, registry.ErrUnknown) {
			t.Fatalf("cancelled twice: %v", err)
		}
		if _, err := w.blobs.ResumeUpload(ctx, blobID); !errors.Is(err, blob.ErrUploadUnknown) {
			t.Fatalf("blob upload kept: %v", err)
		}
	})
}

// blobUpload is the blob store upload behind session id.
func blobUpload(t *testing.T, w *world, id string) string {
	t.Helper()
	row, err := w.db.Uploads().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var st struct{ Blob string }
	if err := json.Unmarshal(row.State, &st); err != nil || st.Blob == "" {
		t.Fatalf("state %s: %v", row.State, err)
	}
	return st.Blob
}

func TestAccASessionEndsWithItsBlobUpload(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		o := w.owner("app")
		id, err := w.svc.Begin(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		up, err := w.blobs.ResumeUpload(ctx, blobUpload(t, w, id))
		if err != nil {
			t.Fatal(err)
		}
		if err := up.Abort(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.Append(ctx, o, id, -1, strings.NewReader("x")); !errors.Is(
			err, registry.ErrUnknown,
		) {
			t.Fatalf("append: %v", err)
		}
		if _, err := w.db.Uploads().Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("row kept: %v", err)
		}
	})
}

func TestAccMonolithicUpload(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		o := w.owner("app")
		it, err := w.svc.Upload(ctx, o, strings.NewReader("whole"), sum("whole"))
		if err != nil || it.FullPath() != "app/_uploads/sha256__"+sum("whole") {
			t.Fatalf("%+v %v", it, err)
		}
		if _, err := w.svc.Upload(
			ctx, o, strings.NewReader("whole"), sum("partial"),
		); !errors.Is(err, registry.ErrDigest) {
			t.Fatalf("mismatch: %v", err)
		}
		if _, err := w.item(w.docker, "app/_uploads/sha256__"+sum("partial")); !errors.Is(
			err, store.ErrNotFound,
		) {
			t.Fatalf("stored anyway: %v", err)
		}
	})
}

func TestAccMount(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		layer := sum("shared layer")
		w.deploy(w.oci, "base/v1/sha256__"+layer, "shared layer", start)
		o := w.owner("app")
		it, ok, err := w.svc.Mount(ctx, o, w.oci, "base", layer)
		if err != nil || !ok || it.FullPath() != "app/_uploads/sha256__"+layer ||
			it.Size != int64(len("shared layer")) {
			t.Fatalf("mount: %+v %v %v", it, ok, err)
		}
		b, err := w.db.Blobs().Get(ctx, layer)
		if err != nil || b.Refs != 2 {
			t.Fatalf("the content is not shared: %+v %v", b, err)
		}
		// Not in the source image, or in a deeper image only.
		w.deploy(w.oci, "base/sub/v1/sha256__"+sum("deep"), "deep", start)
		for name, c := range map[string]struct{ image, hex string }{
			"missing":  {"base", sum("missing")},
			"deeper":   {"base", sum("deep")},
			"no image": {"nope", layer},
		} {
			if _, ok, err := w.svc.Mount(ctx, o, w.oci, c.image, c.hex); ok || err != nil {
				t.Errorf("%s: %v %v", name, ok, err)
			}
		}
		// Content garbage collection removed meanwhile.
		if err := w.blobs.Delete(ctx, layer); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := w.svc.Mount(
			ctx, registry.Owner{Repo: w.docker, Name: "again", By: "alice"}, w.oci, "base", layer,
		); ok || err != nil {
			t.Fatalf("collected: %v %v", ok, err)
		}
	})
}

func TestAccReap(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		id, err := w.svc.Begin(ctx, w.owner("app"))
		if err != nil {
			t.Fatal(err)
		}
		blobID := blobUpload(t, w, id)
		row, err := w.db.Uploads().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.svc.Reap(ctx, row); err != nil {
			t.Fatal(err)
		}
		if _, err := w.db.Uploads().Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("row kept: %v", err)
		}
		if _, err := w.blobs.ResumeUpload(ctx, blobID); !errors.Is(err, blob.ErrUploadUnknown) {
			t.Fatalf("blob upload kept: %v", err)
		}
		// A row whose blob upload is gone, or whose state does not decode,
		// only goes.
		if err := w.svc.Reap(ctx, row); err != nil {
			t.Fatal(err)
		}
		junk := store.Upload{
			ID: strings.Repeat("c", 32), Backend: registry.Backend, State: []byte("?"),
			Started: start, Updated: start,
		}
		if err := w.db.Uploads().Put(ctx, junk); err != nil {
			t.Fatal(err)
		}
		if err := w.svc.Reap(ctx, junk); err != nil {
			t.Fatal(err)
		}
		if _, err := w.db.Uploads().Get(ctx, junk.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("junk kept: %v", err)
		}
	})
}

func TestAccExpireUploads(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		old, recent := start.Add(-48*time.Hour), start.Add(-time.Hour)
		gone := []string{
			"app/_uploads/sha256__" + sum("old"),
			"team/app/_uploads/sha256__" + sum("old2"),
		}
		w.deploy(w.docker, gone[0], "old", old)
		w.deploy(w.docker, gone[1], "old2", old)
		type file struct {
			c          repo.Config
			path, body string
			at         time.Time
		}
		kept := []file{
			{w.docker, "app/_uploads/sha256__" + sum("recent"), "recent", recent},
			{w.docker, "app/v1/sha256__" + sum("tagged"), "tagged", old},
			{w.docker, "app/_uploads/notes.txt", "notes", old},
			{w.plain, "app/_uploads/sha256__" + sum("generic"), "generic", old},
		}
		for _, f := range kept {
			w.deploy(f.c, f.path, f.body, f.at)
		}

		n, err := w.svc.ExpireUploads(ctx, start.Add(-24*time.Hour))
		if err != nil || n != 2 {
			t.Fatalf("expired %d: %v", n, err)
		}
		for _, p := range gone {
			if _, err := w.item(w.docker, p); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s kept: %v", p, err)
			}
		}
		for _, f := range kept {
			if _, err := w.item(f.c, f.path); err != nil {
				t.Errorf("%s/%s: %v", f.c.Key, f.path, err)
			}
		}
		// Expired uploads bypass the trash can and release their content.
		if entries, _, err := w.db.Trash().List(ctx, "", "", store.Page{}); err != nil ||
			len(entries) != 0 {
			t.Fatalf("trash %v %v", entries, err)
		}
		if b, err := w.db.Blobs().Get(ctx, sum("old")); err != nil || b.Refs != 0 {
			t.Fatalf("blob %+v %v", b, err)
		}
	})
}

func TestAccExpireWithoutImageRepositories(t *testing.T) {
	svc := &registry.Service{Store: inmem.New()}
	if n, err := svc.ExpireUploads(context.Background(), start); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
}
