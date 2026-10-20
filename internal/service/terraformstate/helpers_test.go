package terraformstate_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// tfKey is the world's Terraform backend repository, with encryptStates
// as a create leaves it: on.
const tfKey = "tf-local"

var start = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

// world is a store with tf-local and a generic libs-local, and the item,
// trash and state services over them with a clock that moves only when a
// test ticks it.
type world struct {
	t     *testing.T
	ctx   context.Context
	st    store.Store
	blobs blob.Store
	keys  *crypt.Keyring
	items *item.Service
	trash *trash.Service
	svc   *terraformstate.Service
	now   time.Time
	c     repo.Config
}

// masterKey is version v of a master key made of fill.
func masterKey(v uint32, fill byte) crypt.MasterKey {
	return crypt.MasterKey{Version: v, Material: bytes.Repeat([]byte{fill}, 32)}
}

func keyring(t *testing.T, keys ...crypt.MasterKey) *crypt.Keyring {
	t.Helper()
	k, err := crypt.NewKeyring(keys...)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newWorld(t *testing.T, st store.Store) *world {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, ctx: context.Background(), st: st, blobs: blobs, now: start}
	w.useKeys(keyring(t, masterKey(1, 'a')))
	w.c = w.repo(tfKey, nil)
	libs := repo.Config{Key: "libs-local", Class: repo.Local, PackageType: repo.Generic}
	if err := st.Repos().Create(w.ctx, *repo.Defaults(&libs)); err != nil {
		t.Fatal(err)
	}
	return w
}

// newWorldOver is prev's world over st, the same database opened again:
// a restart.
func newWorldOver(t *testing.T, st store.Store, prev *world) *world {
	t.Helper()
	w := &world{t: t, ctx: context.Background(), st: st, blobs: prev.blobs, now: prev.now}
	w.useKeys(prev.keys)
	c, err := st.Repos().Get(w.ctx, tfKey)
	if err != nil {
		t.Fatal(err)
	}
	w.c = c
	return w
}

// memStore is a fresh in-memory store.
func memStore() store.Store { return inmem.New() }

// sqliteFile is a SQLite database a test opens and closes, as a restart
// does.
type sqliteFile struct {
	path string
	st   *sqlcommon.Store
}

func sqliteBackend(t *testing.T) *sqliteFile {
	t.Helper()
	return &sqliteFile{path: filepath.Join(t.TempDir(), "aw.db")}
}

func (f *sqliteFile) open(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(context.Background(), f.path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.st = st
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func (f *sqliteFile) close(t *testing.T) {
	t.Helper()
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// useKeys rebuilds the services over keys, as a restart with another
// configuration does.
func (w *world) useKeys(keys *crypt.Keyring) {
	w.keys = keys
	now := func() time.Time { return w.now }
	w.items = &item.Service{
		Store: w.st, Blobs: w.blobs, Now: now,
		Formats: formats.Settings{TerraformBackend: tfbe.Settings{Keys: keys}}.For,
	}
	w.trash = &trash.Service{Store: w.st, Items: w.items, Now: now}
	w.svc = &terraformstate.Service{Items: w.items, Keys: keys, Now: now}
}

// repo creates a Terraform backend repository, as tune leaves it.
func (w *world) repo(key string, tune func(*repo.Config)) repo.Config {
	w.t.Helper()
	c := repo.Config{Key: key, Class: repo.Local, PackageType: repo.TerraformBackend}
	repo.Defaults(&c)
	if tune != nil {
		tune(&c)
	}
	if err := w.st.Repos().Create(w.ctx, c); err != nil {
		w.t.Fatal(err)
	}
	stored, err := w.st.Repos().Get(w.ctx, key)
	if err != nil {
		w.t.Fatal(err)
	}
	return stored
}

func (w *world) tick() { w.now = w.now.Add(time.Second) }

// state is a Terraform state of lineage with serial.
func state(serial int64, lineage string) []byte {
	return fmt.Appendf(nil, `{"version":4,"terraform_version":"1.16.6","serial":%d,`+
		`"lineage":%q,"outputs":{},"resources":[]}`, serial, lineage)
}

// workspace creates workspace name in c.
func (w *world) workspace(c repo.Config, name string) terraformstate.Workspace {
	w.t.Helper()
	ws, err := w.svc.CreateWorkspace(w.ctx, c, name, "1.16.6", "alice")
	if err != nil {
		w.t.Fatal(err)
	}
	return ws
}

// write writes a state of serial to workspace name of c as by.
func (w *world) write(
	c repo.Config,
	name string,
	serial int64,
	by string,
) (terraformstate.State, error) {
	w.tick()
	body := state(serial, "lin-"+name)
	return w.svc.WriteState(w.ctx, c, name, terraformstate.StateWrite{
		Body: bytes.NewReader(body), MD5: tfbe.MD5(body), Serial: serial, By: by,
	})
}

// mustWrite is write that must succeed.
func (w *world) mustWrite(c repo.Config, name string, serial int64) terraformstate.State {
	w.t.Helper()
	st, err := w.write(c, name, serial, "alice")
	if err != nil {
		w.t.Fatal(err)
	}
	return st
}

// stored is the content of the file at p in c, as the item routes serve it.
func (w *world) stored(c repo.Config, p string) []byte {
	w.t.Helper()
	it, err := w.st.Items().Get(w.ctx, c.Key, false, p)
	if err != nil {
		w.t.Fatal(err)
	}
	r, _, err := w.blobs.Open(w.ctx, it.Actual.SHA256)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		w.t.Fatal(err)
	}
	return b
}

// index is c's package index: each workspace's versions with their
// metadata and the paths of their files.
func (w *world) index(c repo.Config) map[string]map[string]string {
	w.t.Helper()
	out := map[string]map[string]string{}
	pkgs, _, err := w.st.Packages().List(w.ctx, c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, p := range pkgs {
		out[p.Name] = map[string]string{}
		vs, err := w.st.Packages().Versions(w.ctx, p.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		for _, v := range vs {
			ids, err := w.st.Packages().Files(w.ctx, v.ID)
			if err != nil {
				w.t.Fatal(err)
			}
			desc := string(v.Metadata)
			for _, id := range ids {
				it, err := w.st.Items().GetByID(w.ctx, id)
				if err != nil {
					w.t.Fatal(err)
				}
				desc += " " + it.FullPath()
			}
			out[p.Name][v.Version] = desc
		}
	}
	return out
}

// reindexed checks that c's index is the one a reindex reads from the
// tree, and returns it.
func (w *world) reindexed(c repo.Config) map[string]map[string]string {
	w.t.Helper()
	kept := w.index(c)
	pkgs, _, err := w.st.Packages().List(w.ctx, c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, p := range pkgs {
		if err := w.st.Packages().Delete(w.ctx, p.ID); err != nil {
			w.t.Fatal(err)
		}
	}
	if err := w.items.Reindex(w.ctx, c); err != nil {
		w.t.Fatal(err)
	}
	rebuilt := w.index(c)
	if fmt.Sprint(kept) != fmt.Sprint(rebuilt) {
		w.t.Fatalf("the hooks kept\n%v\na reindex reads\n%v", kept, rebuilt)
	}
	return kept
}

// eachStore runs fn on a fresh world over every store backend.
func eachStore(t *testing.T, fn func(t *testing.T, w *world)) {
	t.Helper()
	for _, b := range formattest.Backends() {
		t.Run(b.Name, func(t *testing.T) { fn(t, newWorld(t, b.Open(t))) })
	}
}

// props are the properties of the item at p in c.
func (w *world) props(c repo.Config, p string) types.Properties {
	w.t.Helper()
	it, err := w.st.Items().Get(w.ctx, c.Key, false, p)
	if err != nil {
		w.t.Fatal(err)
	}
	props, err := w.st.Items().Properties(w.ctx, it.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return props
}
