package nix

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errDisk = errors.New("storage fault")

type brokenReader struct{ *bytes.Reader }

func (brokenReader) Read([]byte) (int, error) { return 0, errDisk }
func (brokenReader) Close() error             { return nil }

type brokenBlobs struct {
	format.Blobs
	open bool
}

func (b brokenBlobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	if b.open {
		return nil, blob.Info{}, errDisk
	}
	return brokenReader{bytes.NewReader(nil)}, blob.Info{Size: 10}, nil
}

type faultItems struct {
	store.ItemStore
	get, list bool
}

func (f faultItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (types.Item, error) {
	if f.get {
		return types.Item{}, errDisk
	}
	return f.ItemStore.Get(ctx, key, cached, path)
}

func (f faultItems) Descendants(
	ctx context.Context,
	key string,
	cached bool,
	path string,
	page store.Page,
) ([]types.Item, string, error) {
	if f.list {
		return nil, "", errDisk
	}
	return f.ItemStore.Descendants(ctx, key, cached, path, page)
}

type faultDB struct {
	store.Store
	items store.ItemStore
}

func (f faultDB) Items() store.ItemStore { return f.items }

func handlerFixture(t *testing.T) (format.Env, repo.Config, types.Item, types.Item, Info) {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "nix", Class: repo.Local, PackageType: repo.Nix})
	if err = db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	info, archive, _ := fixture(t)
	put := func(path string, data []byte) types.Item {
		up, err := blobs.BeginUpload(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = up.Append(t.Context(), bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		bi, err := up.Commit(t.Context(), blob.Digests{})
		if err != nil {
			t.Fatal(err)
		}
		it, err := db.Items().
			Put(t.Context(), types.Item{RepoKey: c.Key, Name: path, Size: bi.Size, Actual: types.Checksums{SHA256: bi.Digests.SHA256}})
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	return format.Env{
			Store: db,
			Blobs: blobs,
		}, c, put(
			fixturePath,
			info.Encode(info.URL),
		), put(
			info.URL,
			archive,
		), info
}

func TestUnitNixAdmissionAndGenericReadFences(t *testing.T) {
	env, c, meta, archive, info := handlerFixture(t)
	h := Handler{}
	w := format.Write{
		Op:        format.OpDeploy,
		Path:      meta.FullPath(),
		Checksums: meta.Actual,
		Size:      meta.Size,
	}
	in, err := h.Inspect(t.Context(), env, c, w)
	if err != nil {
		t.Fatal(err)
	}
	env.Inspection = in
	if _, err = h.Admit(t.Context(), env, c, w); err != nil {
		t.Fatal(err)
	}
	if err = h.Deployed(t.Context(), env, c, meta); err != nil {
		t.Fatal(err)
	}
	if err = h.Deployed(t.Context(), env, c, types.Item{Folder: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Resolve(
		t.Context(),
		env,
		c,
		format.Download{Path: meta.FullPath(), Readable: func(string) bool { return false }},
	); err == nil {
		t.Fatal("hidden NAR leaked metadata")
	}
	if _, err = h.Resolve(
		t.Context(),
		env,
		c,
		format.Download{Path: meta.FullPath(), Readable: func(string) bool { return true }},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Resolve(t.Context(), env, c, format.Download{Path: info.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Resolve(
		t.Context(),
		env,
		c,
		format.Download{Path: "00000000000000000000000000000000.narinfo"},
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal(err)
	}
	blocked := c
	excluded := "**"
	blocked.ExcludesPattern = &excluded
	if _, err = h.Resolve(
		t.Context(),
		env,
		blocked,
		format.Download{Path: meta.FullPath()},
	); err == nil {
		t.Fatal("excluded metadata exposed")
	}
	if _, err = h.Admit(t.Context(), env, blocked, w); err == nil {
		t.Fatal("excluded metadata admitted")
	}
	for _, class := range []repo.Class{repo.Remote, repo.Virtual} {
		bad := c
		bad.Class = class
		if _, err = h.Admit(t.Context(), env, bad, w); err == nil {
			t.Fatal("invalid publication class")
		}
	}
	checksum := w
	checksum.Op = format.OpChecksum
	if _, err = h.Inspect(t.Context(), env, c, checksum); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Admit(t.Context(), env, c, checksum); err != nil {
		t.Fatal(err)
	}
	wrong := archive
	wrong.Actual.SHA256 = "wrong"
	if _, err = env.Store.Items().Put(t.Context(), wrong); err != nil {
		t.Fatal(err)
	}
	aw := format.Write{
		Op:        format.OpDeploy,
		Path:      archive.FullPath(),
		Checksums: archive.Actual,
		Size:      archive.Size,
	}
	if _, err = h.Admit(t.Context(), env, c, aw); err == nil {
		t.Fatal("immutable archive replaced")
	}
	if _, err = ReferencedArchive(t.Context(), env, c, info, false); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = env.Store.Items().Put(t.Context(), archive); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fixturePath, "nix-cache-info", info.URL} {
		want := format.ClassMetadata
		if path == info.URL {
			want = format.ClassUnique
		}
		if h.Classify(c, path) != want {
			t.Fatal(path)
		}
	}
}

func TestUnitNixStorageFailuresRemainFailures(t *testing.T) {
	for _, open := range []bool{true, false} {
		t.Run(map[bool]string{true: "open", false: "read"}[open], func(t *testing.T) {
			env, c, meta, _, info := handlerFixture(t)
			env.Blobs = brokenBlobs{Blobs: env.Blobs, open: open}
			h := Handler{}
			if _, err := ReadInfo(t.Context(), env, meta); err == nil {
				t.Fatal("metadata failure swallowed")
			}
			if _, err := ReferencedArchive(t.Context(), env, c, info, false); err == nil {
				t.Fatal("archive failure swallowed")
			}
			for _, path := range []string{fixturePath, info.URL, "nix-cache-info"} {
				if _, err := h.Inspect(
					t.Context(),
					env,
					c,
					format.Write{Path: path, Checksums: meta.Actual},
				); err == nil {
					t.Fatal("inspection failure swallowed")
				}
			}
			if err := h.Deployed(t.Context(), env, c, meta); err == nil {
				t.Fatal("deployment failure swallowed")
			}
			if err := h.Reindex(t.Context(), env, c); err == nil {
				t.Fatal("reindex failure swallowed")
			}
			if _, err := h.Resolve(
				t.Context(),
				env,
				c,
				format.Download{Path: fixturePath},
			); err == nil {
				t.Fatal("generic metadata failure swallowed")
			}
		})
	}
	for _, get := range []bool{false, true} {
		t.Run(map[bool]string{false: "list", true: "get"}[get], func(t *testing.T) {
			env, c, meta, _, _ := handlerFixture(t)
			env.Store = faultDB{
				Store: env.Store,
				items: faultItems{ItemStore: env.Store.Items(), get: get, list: !get},
			}
			if err := (Handler{}).Reindex(t.Context(), env, c); !errors.Is(err, errDisk) {
				t.Fatal(err)
			}
			if get {
				if _, err := (Handler{}).Admit(
					t.Context(),
					env,
					c,
					format.Write{Op: format.OpCopy, Path: meta.FullPath(), Checksums: meta.Actual},
				); !errors.Is(
					err,
					errDisk,
				) {
					t.Fatal(err)
				}
			}
		})
	}
	env, c, _, _, _ := handlerFixture(t)
	env.CheckReindex = func(context.Context) error { return errDisk }
	if err := (Handler{}).Reindex(t.Context(), env, c); !errors.Is(err, errDisk) {
		t.Fatal(err)
	}
}

type leaseDB struct {
	store.Store
	leases store.LeaseStore
}

func (s leaseDB) Leases() store.LeaseStore { return s.leases }

type failedLease struct {
	store.LeaseStore
	mode string
}

func (s failedLease) Acquire(context.Context, types.Lease, time.Time) (bool, error) {
	if s.mode == "acquire" {
		return false, errDisk
	}
	return s.mode != "busy", nil
}
func (s failedLease) Release(context.Context, string, string) error { return errDisk }

func TestUnitNixPublicationLockFailures(t *testing.T) {
	for _, mode := range []string{"acquire", "busy", "release"} {
		t.Run(mode, func(t *testing.T) {
			env, c, meta, _, _ := handlerFixture(t)
			env.Store = leaseDB{
				Store:  env.Store,
				leases: failedLease{LeaseStore: env.Store.Leases(), mode: mode},
			}
			if err := (Handler{}).GuardWrite(
				t.Context(),
				env,
				c,
				format.Write{Path: meta.FullPath(), Checksums: meta.Actual},
			); err == nil {
				t.Fatal("publication lock failure ignored")
			}
		})
	}
	env, c, meta, _, _ := handlerFixture(t)
	env.Blobs = brokenBlobs{Blobs: env.Blobs, open: true}
	if err := (Handler{}).GuardWrite(
		t.Context(),
		env,
		c,
		format.Write{Path: meta.FullPath(), Checksums: meta.Actual},
	); !errors.Is(
		err,
		errDisk,
	) {
		t.Fatal(err)
	}
	if err := (Handler{}).GuardWrite(
		t.Context(),
		env,
		c,
		format.Write{Op: format.OpChecksum, Path: meta.FullPath()},
	); err != nil {
		t.Fatal(err)
	}
}
