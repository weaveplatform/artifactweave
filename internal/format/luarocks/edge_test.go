package luarocks

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type testContent struct {
	*bytes.Reader
	readErr error
}

func (r *testContent) Read(p []byte) (int, error) {
	if r.readErr != nil {
		return 0, r.readErr
	}
	return r.Reader.Read(p)
}

func (*testContent) Close() error { return nil }

type testBlobs struct {
	body    []byte
	size    int64
	openErr error
	readErr error
}

type itemLookupStore struct {
	store.Store
	item      *types.Item
	getErr    error
	searchErr error
}

func (s itemLookupStore) Items() store.ItemStore {
	return itemLookup{s.Store.Items(), s.item, s.getErr, s.searchErr}
}

type itemLookup struct {
	store.ItemStore
	item      *types.Item
	getErr    error
	searchErr error
}

func (s itemLookup) Search(ctx context.Context, q *aql.Query, opts aql.Options) ([]aql.Hit, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.ItemStore.Search(ctx, q, opts)
}

func (s itemLookup) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (types.Item, error) {
	if s.getErr != nil {
		return types.Item{}, s.getErr
	}
	if s.item != nil {
		return *s.item, nil
	}
	return s.ItemStore.Get(ctx, key, cached, path)
}

func (b testBlobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	if b.openErr != nil {
		return nil, blob.Info{}, b.openErr
	}
	size := b.size
	if size == 0 {
		size = int64(len(b.body))
	}
	return &testContent{
		Reader:  bytes.NewReader(b.body),
		readErr: b.readErr,
	}, blob.Info{
		Size: size,
	}, nil
}

func (b testBlobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{Size: b.size}, nil
}

func testRock(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var body bytes.Buffer
	w := zip.NewWriter(&body)
	for name, content := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func TestUnitStoredContentValidationEdges(t *testing.T) {
	ctx := context.Background()
	specID, _ := Parse("hello-1.0-1.rockspec")
	rockID, _ := Parse("hello-1.0-1.src.rock")
	matching := "package = 'hello'\nversion = '1.0-1'\n"
	for _, tc := range []struct {
		name  string
		id    Identity
		sha   string
		blobs testBlobs
		valid bool
	}{
		{"missing digest", specID, "", testBlobs{}, false},
		{"blob open fault", specID, "sha", testBlobs{openErr: errors.New("offline")}, false},
		{"oversized rockspec", specID, "sha", testBlobs{size: maxRockspec + 1}, false},
		{"rockspec read fault", specID, "sha", testBlobs{body: []byte(matching), readErr: errors.New("read")}, false},
		{"valid rockspec", specID, "sha", testBlobs{body: []byte(matching)}, true},
		{"non-ZIP rock", rockID, "sha", testBlobs{body: []byte("invalid")}, false},
		{"missing embedded rockspec", rockID, "sha", testBlobs{body: testRock(t, map[string]string{"module.lua": "return 1"})}, false},
		{"valid rock", rockID, "sha", testBlobs{body: testRock(t, map[string]string{
			"hello-1.0-1.rockspec": matching, "module.lua": "return 1",
		})}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStored(ctx, tc.blobs, tc.sha, tc.id)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t, error=%v", tc.valid, err)
			}
		})
	}
}

func TestUnitManifestHookBoundaries(t *testing.T) {
	ctx := context.Background()
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "rocks", Class: repo.Local, PackageType: repo.LuaRocks})
	remote := *repo.Defaults(&repo.Config{Key: "upstream", Class: repo.Remote, PackageType: repo.LuaRocks})
	db := inmem.New()
	if err := db.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := db.Repos().Create(ctx, remote); err != nil {
		t.Fatal(err)
	}
	var queued, derived []string
	env := format.Env{
		Store: db,
		Enqueue: func(_ context.Context, kind, key string, _ []byte) error {
			queued = append(queued, kind+":"+key)
			return nil
		},
		Derive: func(_ context.Context, d format.Derived) (types.Item, error) {
			derived = append(derived, d.Path)
			return types.Item{Name: d.Path}, nil
		},
	}
	if refs := h.Owed(c, false, ManifestPath); len(refs) != 1 || refs[0].Kind != KindManifest {
		t.Fatalf("local manifest work: %+v", refs)
	}
	if refs := h.Owed(remote, false, ManifestPath); len(refs) != 0 {
		t.Fatalf("remote manifest work: %+v", refs)
	}
	if refs := h.Owed(c, false, "hello-1.0-1.rockspec"); len(refs) != 0 {
		t.Fatalf("rockspec work: %+v", refs)
	}
	for _, tc := range []struct {
		config repo.Config
		path   string
	}{
		{c, "hello-1.0-1.rockspec"}, {remote, ManifestPath},
	} {
		path, err := h.Resolve(ctx, env, tc.config, format.Download{Path: tc.path})
		if err != nil || path != tc.path {
			t.Fatalf("passthrough %s: %s %v", tc.path, path, err)
		}
	}
	path, err := h.Resolve(ctx, env, c, format.Download{Path: ManifestPath})
	if err != nil || path != ManifestPath || len(queued) != 1 {
		t.Fatalf("manifest resolve: %s %v; queued %v", path, err, queued)
	}
	if h.Classify(c, ManifestPath) != format.ClassDerived ||
		h.Classify(c, "hello-1.0-1.rockspec") != format.ClassUnique {
		t.Fatal("manifest classification")
	}
	if sides := h.Sidecars(
		c,
		"hello-1.0-1.rockspec",
	); !slices.Equal(
		sides,
		[]string{"hello-1.0-1.rockspec.asc"},
	) {
		t.Fatalf("sidecar: %v", sides)
	}
	if sides := h.Sidecars(c, ManifestPath); len(sides) != 0 {
		t.Fatalf("manifest sidecar: %v", sides)
	}
	if err := h.Deleting(
		ctx,
		env,
		c,
		types.Item{Name: "hello-1.0-1.rockspec", Folder: true},
	); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleting(
		ctx,
		env,
		c,
		types.Item{Name: "hello-1.0-1.rockspec"},
	); err != nil ||
		len(queued) != 2 {
		t.Fatalf("rockspec deletion queue: %v %v", err, queued)
	}
	if err := h.Deleting(
		ctx,
		env,
		c,
		types.Item{Name: ManifestPath},
	); err != nil ||
		len(queued) != 3 {
		t.Fatalf("manifest deletion queue: %v %v", err, queued)
	}
	if err := h.Deleting(
		ctx,
		env,
		remote,
		types.Item{Name: "hello-1.0-1.rockspec"},
	); err != nil ||
		len(queued) != 3 {
		t.Fatalf("remote deletion must not queue: %v %v", err, queued)
	}
	if err := h.Deployed(ctx, env, c, types.Item{Name: "folder", Folder: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(ctx, env, c, types.Item{Name: "unrelated.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleted(ctx, env, c, []types.Item{{Name: "unrelated.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.Work(ctx, env, types.WorkItem{Kind: "unrelated", Key: c.Key}); err != nil {
		t.Fatal(err)
	}
	if err := h.Work(ctx, env, types.WorkItem{Kind: KindManifest, Key: "missing"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Work(ctx, env, types.WorkItem{Kind: KindManifest, Key: remote.Key}); err != nil {
		t.Fatal(err)
	}
	if err := h.Work(ctx, env, types.WorkItem{Kind: KindManifest, Key: c.Key}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(
		derived,
		[]string{
			"manifest",
			"manifest-5.1",
			"manifest-5.2",
			"manifest-5.3",
			"manifest-5.4",
			"manifest-5.5",
		},
	) {
		t.Fatalf("derived manifests: %v", derived)
	}
	env.Derive = func(context.Context, format.Derived) (types.Item, error) {
		return types.Item{}, errors.New("derived item unavailable")
	}
	if err := h.Work(ctx, env, types.WorkItem{Kind: KindManifest, Key: c.Key}); err == nil {
		t.Fatal("manifest derivation failure was ignored")
	}
}

func TestUnitAdmissionAndManifestSizeBoundaries(t *testing.T) {
	ctx := context.Background()
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "rocks", Class: repo.Local, PackageType: repo.LuaRocks})
	for _, tc := range []struct {
		name          string
		c             repo.Config
		w             format.Write
		env           format.Env
		expectedError bool
	}{
		{"checksum", c, format.Write{Op: format.OpChecksum, Path: "hello-1.0-1.rockspec"}, format.Env{}, false},
		{"cached", c, format.Write{Op: format.OpDeploy, Path: "hello-1.0-1.rockspec", Cached: true}, format.Env{}, false},
		{"invalid filename", c, format.Write{Op: format.OpDeploy, Path: "bad.rockspec"}, format.Env{}, true},
		{"invalid content", c, format.Write{Op: format.OpDeploy, Path: "hello-1.0-1.rockspec", Checksums: types.Checksums{SHA256: "sha"}}, format.Env{Blobs: testBlobs{body: []byte("bad")}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Admit(ctx, tc.env, tc.c, tc.w)
			if (err != nil) != tc.expectedError {
				t.Fatalf("error: %v", err)
			}
		})
	}
	blackout := true
	blocked := c
	blocked.BlackedOut = &blackout
	if _, err := h.Admit(
		ctx,
		format.Env{},
		blocked,
		format.Write{Op: format.OpDeploy, Path: "hello-1.0-1.rockspec"},
	); err == nil {
		t.Fatal("blacked-out repository accepted an upload")
	}
	if _, err := h.Inspect(ctx, format.Env{}, c, format.Write{Path: "unrelated.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := encodeManifest(
		[]manifestEntry{{"hello", "1.0-1", strings.Repeat("x", maxManifestBytes)}},
	); !errors.Is(
		err,
		errManifestLimit,
	) {
		t.Fatalf("oversized manifest: %v", err)
	}
}

func TestUnitManifestReadFaultBoundaries(t *testing.T) {
	ctx := context.Background()
	c := repo.Config{Key: "rocks", Class: repo.Local, PackageType: repo.LuaRocks}
	entry := manifestEntry{"hello", "1.0-1", "rockspec"}
	item := types.Item{Actual: types.Checksums{SHA256: "sha"}}
	for _, tc := range []struct {
		name  string
		store itemLookupStore
		blobs testBlobs
	}{
		{"item lookup", itemLookupStore{Store: inmem.New(), getErr: errors.New("item offline")}, testBlobs{}},
		{"blob open", itemLookupStore{Store: inmem.New(), item: &item}, testBlobs{openErr: errors.New("blob offline")}},
		{"blob read", itemLookupStore{Store: inmem.New(), item: &item}, testBlobs{body: []byte("lua >= 5.4"), readErr: errors.New("read offline")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := format.Env{Store: tc.store, Blobs: tc.blobs}
			if _, err := filterVersion(ctx, env, c, []manifestEntry{entry}, "5.4"); err == nil {
				t.Fatal("filterVersion accepted a read failure")
			}
		})
	}
	env := format.Env{
		Store: itemLookupStore{Store: inmem.New(), getErr: errors.New("item offline")},
	}
	if _, err := (Handler{}).Resolve(ctx, env, c, format.Download{Path: ManifestPath}); err == nil {
		t.Fatal("manifest resolver accepted an item-store failure")
	}
}

func TestUnitVersionIDMissingPackageAndVersion(t *testing.T) {
	ctx := context.Background()
	c := repo.Config{Key: "rocks", Class: repo.Local, PackageType: repo.LuaRocks}
	db := inmem.New()
	id, _ := Parse("hello-1.0-1.rockspec")
	if _, err := VersionID(ctx, db, c, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing package: %v", err)
	}
	if _, err := db.Packages().
		Ensure(ctx, types.Package{RepoKey: c.Key, Name: id.Name, Type: c.PackageType}); err != nil {
		t.Fatal(err)
	}
	if _, err := VersionID(ctx, db, c, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing version: %v", err)
	}
}

func TestUnitManifestSearchFaultIsNotPublished(t *testing.T) {
	ctx := context.Background()
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "rocks", Class: repo.Local, PackageType: repo.LuaRocks})
	db := inmem.New()
	if err := db.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: itemLookupStore{Store: db, searchErr: errors.New("index offline")}}
	if err := h.Work(ctx, env, types.WorkItem{Kind: KindManifest, Key: c.Key}); err == nil {
		t.Fatal("work published a manifest after search failed")
	}
	if err := h.Reindex(ctx, env, c); err == nil {
		t.Fatal("reindex accepted a failed file search")
	}
	id, _ := Parse("hello-1.0-1.rockspec")
	if err := syncVersion(ctx, env, c, id); err == nil {
		t.Fatal("package version sync accepted a failed file search")
	}
}
