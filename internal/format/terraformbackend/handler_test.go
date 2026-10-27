package terraformbackend_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// world is an in-memory store with tf-local, a Terraform backend
// repository, and libs-local, a generic one, and the item service over
// them with the format holding k.
type world struct {
	t      *testing.T
	ctx    context.Context
	st     store.Store
	blobs  blob.Store
	h      tfbe.Handler
	items  *item.Service
	tf     repo.Config
	libs   repo.Config
	now    time.Time
	writes context.Context
}

func newWorld(t *testing.T, k *crypt.Keyring) *world {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t: t, ctx: context.Background(), st: inmem.New(), blobs: blobs,
		h:   tfbe.Handler{Settings: tfbe.Settings{Keys: k}},
		now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC),
	}
	w.writes = tfbe.Writing(w.ctx, nil)
	w.items = &item.Service{
		Store: w.st, Blobs: blobs, Now: func() time.Time { return w.now },
		Formats: func(t repo.PackageType) format.Handler {
			if tfbe.Serves(t) {
				return w.h
			}
			return generic.Handler{}
		},
	}
	w.tf = w.repo("tf-local", repo.TerraformBackend)
	w.libs = w.repo("libs-local", repo.Generic)
	return w
}

func (w *world) repo(key string, t repo.PackageType) repo.Config {
	w.t.Helper()
	c := repo.Config{Key: key, Class: repo.Local, PackageType: t}
	if err := w.st.Repos().Create(w.ctx, *repo.Defaults(&c)); err != nil {
		w.t.Fatal(err)
	}
	stored, err := w.st.Repos().Get(w.ctx, key)
	if err != nil {
		w.t.Fatal(err)
	}
	return stored
}

// put stores body at p in c through the item service, as ctx's writer.
func (w *world) put(ctx context.Context, c repo.Config, p string, body []byte) (types.Item, error) {
	w.now = w.now.Add(time.Second)
	return w.items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: p}, Body: bytes.NewReader(body), By: "alice",
	})
}

func (w *world) mustPut(ctx context.Context, c repo.Config, p string, body []byte) types.Item {
	w.t.Helper()
	it, err := w.put(ctx, c, p, body)
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}

// refused checks err is the format's 400 for c.
func refused(t *testing.T, what string, err error) {
	t.Helper()
	var r *format.RejectionError
	if !errors.As(err, &r) || r.Status != http.StatusBadRequest ||
		r.Message != "Repository 'tf-local' is a Terraform backend repository; "+
			"its content is written only through the Terraform backend API" {
		t.Errorf("%s = %v", what, err)
	}
}

// Through the item routes nothing is written into a Terraform backend
// repository: no deploy, checksum, folder, copy or move; the API's own
// writes pass.
func TestUnitItemRouteWritesAreRefused(t *testing.T) {
	w := newWorld(t, nil)
	_, err := w.put(w.ctx, w.tf, tfbe.LatestPath("app"), []byte(aState))
	refused(t, "a deploy", err)
	src := w.mustPut(w.ctx, w.libs, "a.json", []byte(aState))
	_, err = w.items.DeployByChecksum(w.ctx, item.Deploy{
		Target:   item.Target{Repo: w.tf, Path: tfbe.LatestPath("app")},
		Declared: types.Checksums{SHA256: src.Actual.SHA256},
	})
	refused(t, "a checksum deploy", err)
	_, err = w.items.MakeFolder(w.ctx, item.Target{Repo: w.tf, Path: "workspaces/app"}, nil, "x")
	refused(t, "a folder create", err)
	for _, move := range []bool{false, true} {
		res, err := w.items.Transfer(w.ctx, item.Transfer{
			From: item.Target{Repo: w.libs, Path: "a.json"},
			To:   item.Target{Repo: w.tf, Path: tfbe.LatestPath("app")}, Move: move,
		})
		if err != nil || res.Failed != 1 || !strings.Contains(res.Messages[0].Text,
			"is a Terraform backend repository") {
			t.Errorf("a copy or move (move %v) = %+v, %v", move, res, err)
		}
	}

	// The API's own writes pass, a sidecar for one of them included.
	it := w.mustPut(w.writes, w.tf, tfbe.LatestPath("app"), []byte(aState))
	if _, err := w.items.MakeFolder(w.writes, item.Target{Repo: w.tf, Path: "workspaces/b"},
		nil, "x"); err != nil {
		t.Fatal(err)
	}
	_, err = w.items.RecordChecksum(w.ctx, item.Target{Repo: w.tf, Path: it.FullPath()},
		item.SHA1, it.Actual.SHA1)
	refused(t, "a checksum sidecar", err)
	if _, err := w.items.RecordChecksum(w.writes, item.Target{Repo: w.tf, Path: it.FullPath()},
		item.SHA1, it.Actual.SHA1); err != nil {
		t.Fatal(err)
	}
	// A delete goes through, into the trash can, as for any item.
	if _, err := w.items.Delete(
		w.ctx,
		item.Target{Repo: w.tf, Path: it.FullPath()},
		"x",
	); err != nil {
		t.Fatal(err)
	}
}

// Only the API changes the lock's properties; any other property may be
// set, and a restore brings the lock back with its folder.
func TestUnitLockPropertiesAreTheAPIs(t *testing.T) {
	w := newWorld(t, nil)
	w.mustPut(w.writes, w.tf, tfbe.LatestPath("app"), []byte(aState))
	folder, err := w.st.Items().Get(w.ctx, "tf-local", false, tfbe.Folder("app"))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		set    types.Properties
		remove []string
	}{
		{types.Properties{tfbe.PropLockedBy: {"mallory"}}, nil},
		{types.Properties{tfbe.PropLockedAt: {"1"}}, nil},
		{nil, []string{tfbe.PropLockedBy}},
		{nil, []string{tfbe.PropLockedAt}},
	} {
		err := w.items.Annotate(w.ctx, w.tf, folder, change.set, change.remove)
		var r *format.RejectionError
		if !errors.As(err, &r) || r.Status != http.StatusBadRequest ||
			!strings.Contains(r.Message, "is a Terraform workspace's lock") {
			t.Errorf("Annotate(%v, %v) = %v", change.set, change.remove, err)
		}
	}
	if err := w.items.Annotate(
		w.ctx,
		w.tf,
		folder,
		types.Properties{"team": {"infra"}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := w.items.Annotate(w.writes, w.tf, folder,
		types.Properties{tfbe.PropLockedBy: {"alice"}}, nil); err != nil {
		t.Fatal(err)
	}
	// A deleted locked workspace comes back locked, even into a folder made
	// since.
	if _, err := w.items.Delete(
		w.ctx,
		item.Target{Repo: w.tf, Path: tfbe.Folder("app")},
		"x",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.items.MakeFolder(w.writes, item.Target{Repo: w.tf, Path: tfbe.Folder("app")},
		nil, "x"); err != nil {
		t.Fatal(err)
	}
	tr := &trash.Service{Store: w.st, Items: w.items}
	if _, err := tr.Restore(w.ctx, "tf-local", tfbe.Folder("app"),
		trash.Destination{Repo: w.tf}, "admin", nil); err != nil {
		t.Fatal(err)
	}
	props, err := w.folderProps("app")
	if err != nil || len(props[tfbe.PropLockedBy]) != 1 || props[tfbe.PropLockedBy][0] != "alice" {
		t.Fatalf("the restored folder's properties = %v, %v", props, err)
	}
}

func (w *world) folderProps(ws string) (types.Properties, error) {
	f, err := w.st.Items().Get(w.ctx, "tf-local", false, tfbe.Folder(ws))
	if err != nil {
		return nil, err //nolint:wrapcheck // test helper
	}
	return w.st.Items().Properties(w.ctx, f.ID) //nolint:wrapcheck // test helper
}

func TestUnitInspectHandsOverWhatTheAPIKnows(t *testing.T) {
	h := tfbe.Handler{}
	known := &tfbe.Meta{Serial: 3, Stored: "abc"}
	write := format.Write{Path: tfbe.LatestPath("app"), Checksums: types.Checksums{SHA256: "abc"}}
	ctx := tfbe.Writing(context.Background(), known)
	got, err := h.Inspect(ctx, format.Env{}, repo.Config{}, write)
	if m, ok := got.(*tfbe.Meta); err != nil || !ok || *m != *known || m == known {
		t.Fatalf("Inspect = %v, %v", got, err)
	}
	for name, ctx := range map[string]context.Context{
		"a client's write":     context.Background(),
		"an API write unknown": tfbe.Writing(context.Background(), nil),
		"other content":        tfbe.Writing(context.Background(), &tfbe.Meta{Stored: "def"}),
	} {
		if got, err := h.Inspect(
			ctx,
			format.Env{},
			repo.Config{},
			write,
		); got != nil ||
			err != nil {
			t.Errorf("Inspect of %s = %v, %v", name, got, err)
		}
	}
}

func TestUnitClassify(t *testing.T) {
	h := tfbe.Handler{}
	for p, want := range map[string]format.Class{
		tfbe.StatePath("app", 12): format.ClassUnique,
		tfbe.LatestPath("app"):    format.ClassMetadata,
		tfbe.RecordPath("app"):    format.ClassMetadata,
	} {
		if got := h.Classify(repo.Config{}, p); got != want {
			t.Errorf("Classify(%s) = %v; want %v", p, got, want)
		}
	}
}

// When the current state and an earlier one name the same version, the
// version links both and the current state describes it, from the hooks
// and from a reindex alike; deleting one leaves the other.
func TestUnitTwoFilesOfOneVersion(t *testing.T) {
	k := keys(t, 'a')
	w := newWorld(t, k)
	latest := w.mustPut(w.writes, w.tf, tfbe.LatestPath("app"), []byte(aState))
	ms := latest.Created.UnixMilli()
	other := []byte(`{"serial":1,"lineage":"old"}`)
	w.mustPut(w.writes, w.tf, tfbe.StatePath("app", ms), other)
	v := w.version("app", ms)
	if m, _ := tfbe.DecodeMeta(v.Metadata); m.Serial != 7 || len(w.files(v)) != 2 {
		t.Fatalf("the version = %s with %d files", v.Metadata, len(w.files(v)))
	}
	w.reindexMatches()
	if _, err := w.items.Delete(
		w.ctx,
		item.Target{Repo: w.tf, Path: tfbe.LatestPath("app")},
		"x",
	); err != nil {
		t.Fatal(err)
	}
	v = w.version("app", ms)
	if m, _ := tfbe.DecodeMeta(v.Metadata); m.Serial != 1 || len(w.files(v)) != 1 {
		t.Fatalf("after deleting the current state the version = %s", v.Metadata)
	}
	w.reindexMatches()
}

// A sealed state the format holds no key for is indexed as unreadable.
func TestUnitSealedStateWithoutAKey(t *testing.T) {
	w := newWorld(t, nil)
	sealed, err := tfbe.Seal(keys(t, 'a'), "tf-local", "app", []byte(aState))
	if err != nil {
		t.Fatal(err)
	}
	it := w.mustPut(w.writes, w.tf, tfbe.LatestPath("app"), sealed)
	m, _ := tfbe.DecodeMeta(w.version("app", it.Created.UnixMilli()).Metadata)
	if !m.Unreadable || !m.Sealed || m.Size != int64(len(sealed)) {
		t.Fatalf("the metadata = %+v", m)
	}
	w.reindexMatches()
}

// An item linked to a version that is no state version's name is a broken
// index, which Deployed reports rather than guesses at.
func TestUnitDeployedOverABrokenIndex(t *testing.T) {
	w := newWorld(t, nil)
	it := w.mustPut(w.writes, w.tf, tfbe.StatePath("app", 5), []byte(aState))
	pv, _, err := pkgindex.VersionOf(w.ctx, w.st, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.Packages().DeleteVersion(w.ctx, pv.ID); err != nil {
		t.Fatal(err)
	}
	if err := pkgindex.Link(w.ctx, w.st, pkgindex.Version{
		RepoKey: "tf-local", PackageType: repo.TerraformBackend, Package: "app", Version: "beta",
	}, it.ID, w.now); err != nil {
		t.Fatal(err)
	}
	if err := w.h.Deployed(w.ctx, w.items.Env(), w.tf, it); err == nil {
		t.Fatal("Deployed over a version named beta succeeded")
	}
	// A cached item, or one that is no state, is not indexed.
	cached := it
	cached.Cached = true
	record := types.Item{ID: it.ID, Name: tfbe.RecordPath("app")}
	for _, other := range []types.Item{cached, record} {
		if err := w.h.Deployed(w.ctx, w.items.Env(), w.tf, other); err != nil {
			t.Errorf("Deployed(%s) = %v", other.FullPath(), err)
		}
	}
}

// Content that cannot be read fails the hook that must read it.
func TestUnitUnreadableContent(t *testing.T) {
	w := newWorld(t, nil)
	it := w.mustPut(w.writes, w.tf, tfbe.StatePath("app", 5), []byte(aState))
	env := w.items.Env()
	env.Blobs = failingBlobs{w.blobs}
	if err := w.h.Reindex(w.ctx, env, w.tf); err == nil {
		t.Fatal("a reindex over content that fails to read succeeded")
	}
	if _, err := tfbe.ReadStored(w.ctx, failingBlobs{w.blobs}, it.Actual.SHA256); err == nil {
		t.Fatal("ReadStored of content that fails to read succeeded")
	}
}

type failingBlobs struct{ blob.Store }

func (b failingBlobs) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	r, info, err := b.Store.Open(ctx, sha)
	if err != nil {
		return nil, info, err //nolint:wrapcheck // test double
	}
	return failingReader{r}, info, nil
}

type failingReader struct{ io.ReadSeekCloser }

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read fails") }

// version is version ms of workspace ws in tf-local.
func (w *world) version(ws string, ms int64) types.PackageVersion {
	w.t.Helper()
	p, err := w.st.Packages().Find(w.ctx, "tf-local", ws)
	if err != nil {
		w.t.Fatal(err)
	}
	v, err := w.st.Packages().Version(w.ctx, p.ID, tfbe.Version(ms))
	if err != nil {
		w.t.Fatal(err)
	}
	return v
}

func (w *world) files(v types.PackageVersion) []int64 {
	w.t.Helper()
	ids, err := w.st.Packages().Files(w.ctx, v.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return ids
}

// reindexMatches checks a reindex from an empty index rebuilds tf-local's.
func (w *world) reindexMatches() {
	w.t.Helper()
	before := w.snapshot()
	pkgs, _, err := w.st.Packages().List(w.ctx, "tf-local", store.Page{Limit: 10})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, p := range pkgs {
		if err := w.st.Packages().Delete(w.ctx, p.ID); err != nil {
			w.t.Fatal(err)
		}
	}
	if err := w.items.Reindex(w.ctx, w.tf); err != nil {
		w.t.Fatal(err)
	}
	if after := w.snapshot(); after != before {
		w.t.Fatalf("the hooks kept\n%s\na reindex reads\n%s", before, after)
	}
}

func (w *world) snapshot() string {
	w.t.Helper()
	var b strings.Builder
	pkgs, _, err := w.st.Packages().List(w.ctx, "tf-local", store.Page{Limit: 10})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, p := range pkgs {
		vs, err := w.st.Packages().Versions(w.ctx, p.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		for _, v := range vs {
			b.WriteString(p.Name + " " + v.Version + " " + string(v.Metadata))
			for _, id := range w.files(v) {
				it, err := w.st.Items().GetByID(w.ctx, id)
				if err != nil {
					w.t.Fatal(err)
				}
				b.WriteString(" " + it.FullPath())
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
