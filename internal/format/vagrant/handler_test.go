package vagrant

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errFault = errors.New("injected failure")

type boxReader struct{ *bytes.Reader }

func (boxReader) Close() error { return nil }

type boxStore struct {
	blob.Store
	data []byte
	err  error
}

func (b boxStore) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	if b.err != nil {
		return nil, blob.Info{}, b.err
	}
	return boxReader{bytes.NewReader(b.data)}, blob.Info{Size: int64(len(b.data))}, nil
}

type failureStore struct {
	store.Store
	stage string
}

func (s failureStore) Items() store.ItemStore { return failureItems{s.Store.Items(), s.stage} }
func (s failureStore) Packages() store.PackageStore {
	return failurePackages{s.Store.Packages(), s.stage}
}

type failureItems struct {
	store.ItemStore
	stage string
}

func (s failureItems) Descendants(
	ctx context.Context,
	key string,
	cached bool,
	path string,
	p store.Page,
) ([]types.Item, string, error) {
	if s.stage == "descendants" {
		return nil, "", errFault
	}
	return s.ItemStore.Descendants(ctx, key, cached, path, p)
}

func (s failureItems) Properties(ctx context.Context, id int64) (types.Properties, error) {
	if s.stage == "properties" {
		return nil, errFault
	}
	return s.ItemStore.Properties(ctx, id)
}

func (s failureItems) PatchProperties(
	ctx context.Context,
	id int64,
	set types.Properties,
	remove []string,
	at time.Time,
) error {
	if s.stage == "patch" {
		return errFault
	}
	return s.ItemStore.PatchProperties(ctx, id, set, remove, at)
}

type failurePackages struct {
	store.PackageStore
	stage string
}

func (s failurePackages) List(
	ctx context.Context,
	key string,
	p store.Page,
) ([]types.Package, string, error) {
	if s.stage == "packages" {
		return nil, "", errFault
	}
	return s.PackageStore.List(ctx, key, p)
}

func testEnv(t *testing.T) (format.Env, repo.Config, types.Item) {
	t.Helper()
	db := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "boxes", Class: repo.Local, PackageType: repo.Vagrant})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	it, err := db.Items().
		Put(t.Context(), types.Item{RepoKey: c.Key, Name: "test.box", Actual: types.Checksums{SHA256: "digest"}, Created: time.Unix(10, 0), Updated: time.Unix(10, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Items().
		PatchProperties(t.Context(), it.ID, types.Properties{PropName: {"widget"}, PropProvider: {"virtualbox"}, PropVersion: {"1.0"}}, nil, it.Updated); err != nil {
		t.Fatal(err)
	}
	return format.Env{
		Store: db,
		Blobs: boxStore{
			data: tarBox(t, []entry{{"metadata.json", `{"provider":"virtualbox"}`, 0}}, false),
		},
	}, c, it
}

func TestUnitAdmissionBoundaries(t *testing.T) {
	env, c, it := testEnv(t)
	h := Handler{}
	if !Serves(repo.Vagrant) || Serves(repo.Generic) {
		t.Fatal("wrong type registration")
	}
	props := types.Properties{
		PropName:     {"widget"},
		PropProvider: {"virtualbox"},
		PropVersion:  {"1.0"},
	}
	for _, tc := range []struct {
		name      string
		change    func(*repo.Config, *format.Write, *format.Env)
		wantError bool
	}{
		{"valid", func(*repo.Config, *format.Write, *format.Env) {}, false},
		{"checksum", func(_ *repo.Config, w *format.Write, _ *format.Env) { w.Op = format.OpChecksum }, false},
		{"blocked", func(c *repo.Config, _ *format.Write, _ *format.Env) { yes := true; c.BlackedOut = &yes }, true},
		{"remote", func(c *repo.Config, _ *format.Write, _ *format.Env) { c.Class = repo.Remote }, true},
		{"extension", func(_ *repo.Config, w *format.Write, _ *format.Env) { w.Path = "image.txt" }, true},
		{"identity", func(_ *repo.Config, w *format.Write, _ *format.Env) { w.Properties = nil }, true},
		{"provider", func(_ *repo.Config, w *format.Write, _ *format.Env) {
			w.Properties = types.Properties{PropName: {"widget"}, PropVersion: {"1.0"}, PropProvider: {"docker"}}
		}, true},
		{"blob unavailable", func(_ *repo.Config, _ *format.Write, e *format.Env) { e.Blobs = boxStore{err: errFault} }, true},
		{"archive invalid", func(_ *repo.Config, _ *format.Write, e *format.Env) { e.Blobs = boxStore{data: []byte("corrupt")} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc, e := c, env
			w := format.Write{Path: "test.box", Properties: props, Checksums: it.Actual}
			tc.change(&cc, &w, &e)
			_, err := h.Admit(t.Context(), e, cc, w)
			if (err != nil) != tc.wantError {
				t.Fatalf("admission: %v", err)
			}
		})
	}
	for _, w := range []format.Write{{Op: format.OpChecksum}, {Path: "readme"}} {
		if _, err := h.Inspect(t.Context(), env, c, w); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []format.Annotation{{Set: types.Properties{propDigest: {"forged"}}}, {Removed: []string{propProvider}}} {
		if err := h.AdmitAnnotation(t.Context(), env, c, it, a); err == nil {
			t.Fatal("derived property writable")
		}
	}
}

func TestUnitIndexFailuresArePropagated(t *testing.T) {
	for _, stage := range []string{"blob", "invalid archive", "patch", "descendants", "properties", "packages"} {
		t.Run(stage, func(t *testing.T) {
			env, c, it := testEnv(t)
			env.Store = failureStore{Store: env.Store, stage: stage}
			switch stage {
			case "blob":
				env.Blobs = boxStore{err: errFault}
			case "invalid archive":
				env.Blobs = boxStore{data: []byte("invalid")}
			}
			if err := (Handler{}).Deployed(t.Context(), env, c, it); err == nil {
				t.Fatal("deployment accepted failure")
			}
			if err := (Handler{}).Reindex(t.Context(), env, c); err == nil {
				t.Fatal("reindex accepted failure")
			}
		})
	}
}

func TestUnitReindexPagesAndSkipsUnverifiedFiles(t *testing.T) {
	env, c, _ := testEnv(t)
	// Exercise a real page boundary while proving folders, unrelated files and
	// preexisting boxes without verified identity never become native packages.
	for i := range store.MaxLimit + 2 {
		_, err := env.Store.Items().
			Put(t.Context(), types.Item{RepoKey: c.Key, Name: fmt.Sprintf("unrelated-%04d", i), Folder: i%2 == 0})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := (Handler{}).Reindex(t.Context(), env, c); err != nil {
		t.Fatal(err)
	}
	p, err := env.Store.Packages().Find(t.Context(), c.Key, "widget")
	if err != nil {
		t.Fatal(err)
	}
	versions, err := env.Store.Packages().Versions(t.Context(), p.ID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("versions %v %v", versions, err)
	}
	it, err := env.Store.Items().Get(t.Context(), c.Key, false, "test.box")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Store.Items().
		PatchProperties(t.Context(), it.ID, nil, []string{PropName}, it.Updated); err != nil {
		t.Fatal(err)
	}
	if err := (Handler{}).Deleted(t.Context(), env, c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Store.Packages().
		Find(t.Context(), c.Key, "widget"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("unverified indexed: %v", err)
	}
}
