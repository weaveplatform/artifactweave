package terraformstate_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestWorkspacesRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		got, err := w.svc.Workspaces(w.ctx, w.c)
		if err != nil || len(got) != 0 {
			t.Fatalf("Workspaces of an empty repository = %v, %v", got, err)
		}
		dev := w.workspace(w.c, "app-dev")
		if dev.ID() != "tf-local__app-dev" || dev.TerraformVersion != "1.16.6" ||
			dev.CreatedBy != "alice" || !dev.Created.Equal(start) || dev.Lock != nil {
			t.Fatalf("created %+v", dev)
		}
		solo, err := w.svc.CreateWorkspace(w.ctx, w.c, "solo", "", "bob")
		if err != nil || solo.TerraformVersion != terraformstate.LatestVersion {
			t.Fatalf("a create without a version = %+v, %v", solo, err)
		}
		if _, err := w.svc.CreateWorkspace(w.ctx, w.c, "app-dev", "1.0.0", "bob"); !errors.Is(
			err, terraformstate.ErrTaken) {
			t.Fatalf("a create of a taken name = %v", err)
		}
		for _, bad := range []string{"", ".", "..", "a/b", "a b", "tab\there", `back\slash`} {
			if _, err := w.svc.CreateWorkspace(w.ctx, w.c, bad, "", "bob"); !errors.Is(
				err, terraformstate.ErrInvalidName) {
				t.Errorf("a create of %q = %v", bad, err)
			}
		}
		got, err = w.svc.Workspaces(w.ctx, w.c)
		if err != nil || len(got) != 2 || got[0].Name != "app-dev" || got[1].Name != "solo" {
			t.Fatalf("Workspaces = %+v, %v", got, err)
		}

		// The record is plain JSON, never sealed.
		var rec map[string]string
		if err := json.Unmarshal(w.stored(w.c, tfbe.RecordPath("app-dev")), &rec); err != nil {
			t.Fatal(err)
		}
		if rec["name"] != "app-dev" || rec["id"] != "tf-local__app-dev" ||
			rec["terraform-version"] != "1.16.6" || rec["createdBy"] != "alice" ||
			rec["created"] != "2026-10-20T09:00:00Z" {
			t.Fatalf("workspace.json = %v", rec)
		}

		for _, missing := range []string{"none", "a/b", ""} {
			if _, err := w.svc.Workspace(w.ctx, w.c, missing); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Workspace(%q) = %v", missing, err)
			}
		}
	})
}

func TestResolveIDs(t *testing.T) {
	w := newWorld(t, memStore())
	w.workspace(w.c, "app__dev")
	other := w.repo("tf__x", nil)
	w.workspace(other, "y")
	for id, want := range map[string]string{
		"tf-local__app__dev": "tf-local/app__dev",
		"tf__x__y":           "tf__x/y",
	} {
		c, ws, err := w.svc.ResolveWorkspace(w.ctx, id)
		if err != nil || c.Key+"/"+ws.Name != want {
			t.Errorf("ResolveWorkspace(%q) = %s/%s, %v; want %s", id, c.Key, ws.Name, err, want)
		}
	}
	for _, id := range []string{"tf-local__none", "libs-local__x", "nope", "missing__x", "__"} {
		if _, _, err := w.svc.ResolveWorkspace(w.ctx, id); !errors.Is(
			err, terraformstate.ErrNoWorkspace) {
			t.Errorf("ResolveWorkspace(%q) = %v", id, err)
		}
	}
	for _, key := range []string{"libs-local", "missing"} {
		if _, err := w.svc.Repository(w.ctx, key); !errors.Is(err, terraformstate.ErrNoRepository) {
			t.Errorf("Repository(%q) = %v", key, err)
		}
	}
}

func TestDeleteWorkspaceGoesToTheTrashCan(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		w.workspace(w.c, "app")
		first := w.mustWrite(w.c, "app", 1)
		w.mustWrite(w.c, "app", 2)
		if _, err := w.svc.Lock(w.ctx, w.c, "app", "bob"); err != nil {
			t.Fatal(err)
		}
		var locked *terraformstate.LockedError
		if err := w.svc.DeleteWorkspace(w.ctx, w.c, "app", "alice"); !errors.As(err, &locked) ||
			locked.Holder != "bob" {
			t.Fatalf("a delete of bob's locked workspace by alice = %v", err)
		}
		// The holder may delete it, as Terraform locks what it deletes.
		if err := w.svc.DeleteWorkspace(w.ctx, w.c, "app", "bob"); err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.Workspace(
			w.ctx,
			w.c,
			"app",
		); !errors.Is(
			err,
			terraformstate.ErrNoWorkspace,
		) {
			t.Fatalf("a deleted workspace = %v", err)
		}
		if len(w.index(w.c)) != 0 {
			t.Fatalf("a deleted workspace left the index %v", w.index(w.c))
		}
		if err := w.svc.DeleteWorkspace(w.ctx, w.c, "app", "bob"); !errors.Is(
			err, terraformstate.ErrNoWorkspace) {
			t.Fatalf("a delete of a deleted workspace = %v", err)
		}

		n, err := w.trash.Restore(w.ctx, w.c.Key, tfbe.Folder("app"),
			trash.Destination{Repo: w.c}, "admin", nil)
		if err != nil || n != 4 {
			t.Fatalf("restore = %d, %v", n, err)
		}
		ws, err := w.svc.Workspace(w.ctx, w.c, "app")
		if err != nil || ws.Lock == nil || ws.Lock.By != "bob" {
			t.Fatalf("the restored workspace = %+v, %v", ws, err)
		}
		states, err := w.svc.States(w.ctx, w.c, "app")
		if err != nil || len(states) != 2 || states[0].Meta.Serial != 2 || !states[0].Current {
			t.Fatalf("restored states = %+v, %v", states, err)
		}
		plain, _, err := w.svc.ReadState(w.ctx, w.c, "app", first.Ms)
		if err != nil || string(plain) != string(state(1, "lin-app")) {
			t.Fatalf("the restored earlier state reads %q, %v", plain, err)
		}
		w.reindexed(w.c)
	})
}

func TestLocks(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		w.workspace(w.c, "app")
		w.tick()
		ws, err := w.svc.Lock(w.ctx, w.c, "app", "alice")
		if err != nil || ws.Lock == nil || ws.Lock.By != "alice" || !ws.Lock.At.Equal(w.now) {
			t.Fatalf("Lock = %+v, %v", ws, err)
		}
		props := w.props(w.c, tfbe.Folder("app"))
		if props[tfbe.PropLockedBy][0] != "alice" ||
			props[tfbe.PropLockedAt][0] != strconv.FormatInt(w.now.UnixMilli(), 10) {
			t.Fatalf("the folder's properties = %v", props)
		}
		var locked *terraformstate.LockedError
		for _, by := range []string{"alice", "bob"} {
			if _, err := w.svc.Lock(w.ctx, w.c, "app", by); !errors.As(err, &locked) ||
				locked.Holder != "alice" {
				t.Fatalf("a second lock by %s = %v", by, err)
			}
		}
		if _, err := w.svc.Unlock(w.ctx, w.c, "app", "bob"); !errors.As(err, &locked) ||
			err.Error() != "Workspace app is locked by User alice" {
			t.Fatalf("bob's unlock = %v", err)
		}
		if ws, err := w.svc.Unlock(w.ctx, w.c, "app", "alice"); err != nil || ws.Lock != nil {
			t.Fatalf("alice's unlock = %+v, %v", ws, err)
		}
		if len(w.props(w.c, tfbe.Folder("app"))) != 0 {
			t.Fatalf("an unlock left %v", w.props(w.c, tfbe.Folder("app")))
		}
		var unlocked *terraformstate.NotLockedError
		if _, err := w.svc.Unlock(w.ctx, w.c, "app", "alice"); !errors.As(err, &unlocked) ||
			err.Error() != "Workspace app is not locked" {
			t.Fatalf("an unlock of an unlocked workspace = %v", err)
		}
		if _, err := w.svc.ForceUnlock(w.ctx, w.c, "app"); !errors.As(err, &unlocked) {
			t.Fatalf("a force-unlock of an unlocked workspace = %v", err)
		}
		if _, err := w.svc.Lock(w.ctx, w.c, "app", "bob"); err != nil {
			t.Fatal(err)
		}
		if ws, err := w.svc.ForceUnlock(w.ctx, w.c, "app"); err != nil || ws.Lock != nil {
			t.Fatalf("a force-unlock of bob's lock = %+v, %v", ws, err)
		}
		for _, op := range []func() error{
			func() error { _, err := w.svc.Lock(w.ctx, w.c, "none", "bob"); return err },
			func() error { _, err := w.svc.Unlock(w.ctx, w.c, "none", "bob"); return err },
			func() error { _, err := w.svc.ForceUnlock(w.ctx, w.c, "a/b"); return err },
		} {
			if err := op(); !errors.Is(err, terraformstate.ErrNoWorkspace) {
				t.Errorf("a lock change of a missing workspace = %v", err)
			}
		}
	})
}

// Of concurrent locks, one holds and every other is refused, on every
// store: PostgreSQL's READ COMMITTED included.
func TestConcurrentLocksYieldOneHolder(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		w.workspace(w.c, "app")
		const lockers = 8
		var wg sync.WaitGroup
		errs := make([]error, lockers)
		for i := range lockers {
			wg.Go(func() {
				_, errs[i] = w.svc.Lock(w.ctx, w.c, "app", string(rune('a'+i)))
			})
		}
		wg.Wait()
		held := 0
		for _, err := range errs {
			var locked *terraformstate.LockedError
			switch {
			case err == nil:
				held++
			case !errors.As(err, &locked):
				t.Errorf("a refused lock = %v", err)
			}
		}
		ws, err := w.svc.Workspace(w.ctx, w.c, "app")
		if held != 1 || err != nil || ws.Lock == nil ||
			len(w.props(w.c, tfbe.Folder("app"))[tfbe.PropLockedBy]) != 1 {
			t.Fatalf("%d holders; the workspace %+v, %v", held, ws, err)
		}
	})
}

// Concurrent writes to an unlocked workspace keep every state, each under
// its own version, with one current.
func TestConcurrentWritesKeepEveryState(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		w.workspace(w.c, "app")
		const writers = 4
		var wg sync.WaitGroup
		errs := make([]error, writers)
		for i := range writers {
			wg.Go(func() {
				body := state(int64(i+1), "lin")
				_, errs[i] = w.svc.WriteState(w.ctx, w.c, "app", terraformstate.StateWrite{
					Body: bytesReader(body), MD5: tfbe.MD5(body), Serial: int64(i + 1), By: "alice",
				})
			})
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		states, err := w.svc.States(w.ctx, w.c, "app")
		if err != nil || len(states) != writers || !states[0].Current || states[1].Current {
			t.Fatalf("states = %+v, %v", states, err)
		}
		w.reindexed(w.c)
	})
}

// The lock is the folder's properties, so it outlives the process.
func TestLockSurvivesARestart(t *testing.T) {
	b := sqliteBackend(t)
	w := newWorld(t, b.open(t))
	w.workspace(w.c, "app")
	if _, err := w.svc.Lock(w.ctx, w.c, "app", "alice"); err != nil {
		t.Fatal(err)
	}
	b.close(t)
	again := newWorldOver(t, b.open(t), w)
	ws, err := again.svc.Workspace(again.ctx, again.c, "app")
	if err != nil || ws.Lock == nil || ws.Lock.By != "alice" {
		t.Fatalf("after a restart %+v, %v", ws, err)
	}
}

// A workspace whose record went through the item routes is gone, though
// its folder and states stay; a create of its name takes them back.
func TestRecordDeletedThroughTheItemRoutes(t *testing.T) {
	w := newWorld(t, memStore())
	w.workspace(w.c, "app")
	w.mustWrite(w.c, "app", 1)
	if _, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: tfbe.RecordPath("app")},
		"admin"); err != nil {
		t.Fatal(err)
	}
	if got, err := w.svc.Workspaces(w.ctx, w.c); err != nil || len(got) != 0 {
		t.Fatalf("Workspaces = %+v, %v", got, err)
	}
	for _, op := range []func() error{
		func() error { _, err := w.svc.Lock(w.ctx, w.c, "app", "bob"); return err },
		func() error { _, err := w.svc.CurrentState(w.ctx, w.c, "app"); return err },
		func() error { _, err := w.write(w.c, "app", 2, "bob"); return err },
	} {
		if err := op(); !errors.Is(err, terraformstate.ErrNoWorkspace) {
			t.Errorf("an operation on a workspace without a record = %v", err)
		}
	}
	w.workspace(w.c, "app")
	if st, err := w.svc.CurrentState(w.ctx, w.c, "app"); err != nil || st.Meta.Serial != 1 {
		t.Fatalf("the recreated workspace's state = %+v, %v", st, err)
	}
}

// A record the server cannot read is a fault, not a missing workspace.
func TestUnreadableRecord(t *testing.T) {
	w := newWorld(t, memStore())
	w.workspace(w.c, "app")
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, tfbe.RecordPath("app"))
	if err != nil {
		t.Fatal(err)
	}
	// Point the record at content that is no JSON: the workspace.json of
	// the generic repository.
	if _, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: w.libs(), Path: "x"}, Body: bytesReader([]byte("not json")),
	}); err != nil {
		t.Fatal(err)
	}
	x, err := w.st.Items().Get(w.ctx, "libs-local", false, "x")
	if err != nil {
		t.Fatal(err)
	}
	it.Actual = x.Actual
	if _, err := w.st.Items().Put(w.ctx, it); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Workspace(
		w.ctx,
		w.c,
		"app",
	); err == nil ||
		errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a workspace with an unreadable record = %v", err)
	}
	it.Actual.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := w.st.Items().Put(w.ctx, it); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Workspaces(w.ctx, w.c); err == nil {
		t.Fatal("a workspace whose record's content is gone listed")
	}
}

// libs is the world's generic repository.
func (w *world) libs() repo.Config {
	w.t.Helper()
	c, err := w.st.Repos().Get(w.ctx, "libs-local")
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}
