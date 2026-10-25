package terraformstate_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// A write moves the current state to its timestamped name and stores the
// new one sealed, so the item routes see only the envelope; every version
// reads back plain, and the index is the tree's.
func TestUnitStateVersionsRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		w.workspace(w.c, "app")
		if _, err := w.svc.CurrentState(
			w.ctx,
			w.c,
			"app",
		); !errors.Is(
			err,
			terraformstate.ErrNoState,
		) {
			t.Fatalf("the current state of a new workspace = %v", err)
		}
		first := w.mustWrite(w.c, "app", 1)
		if !first.Current || first.Ms != w.now.UnixMilli() || first.Meta.Serial != 1 ||
			first.Meta.Lineage != "lin-app" || first.Meta.MD5 != tfbe.MD5(state(1, "lin-app")) ||
			first.Meta.Size != int64(len(state(1, "lin-app"))) || !first.Meta.Sealed ||
			first.CreatedBy != "alice" || first.ID() != "tf-local__app__"+tfbe.Version(first.Ms) ||
			!first.Created().Equal(w.now) {
			t.Fatalf("the first state = %+v", first)
		}
		stored := w.stored(w.c, tfbe.LatestPath("app"))
		if !crypt.IsSealed(stored) || bytes.Contains(stored, []byte("lin-app")) {
			t.Fatalf("state.latest.json holds %q", stored)
		}
		second := w.mustWrite(w.c, "app", 2)
		states, err := w.svc.States(w.ctx, w.c, "app")
		if err != nil || len(states) != 2 || states[0].Ms != second.Ms || !states[0].Current ||
			states[1].Ms != first.Ms || states[1].Current ||
			states[1].Path != tfbe.StatePath("app", first.Ms) {
			t.Fatalf("States = %+v, %v", states, err)
		}
		if cur, err := w.svc.CurrentState(w.ctx, w.c, "app"); err != nil || cur.Ms != second.Ms {
			t.Fatalf("CurrentState = %+v, %v", cur, err)
		}
		for ms, want := range map[int64][]byte{first.Ms: state(1, "lin-app"), second.Ms: state(2, "lin-app")} {
			plain, st, err := w.svc.ReadState(w.ctx, w.c, "app", ms)
			if err != nil || !bytes.Equal(plain, want) || st.Ms != ms {
				t.Fatalf("ReadState(%d) = %q, %+v, %v", ms, plain, st, err)
			}
		}
		if _, err := w.svc.State(w.ctx, w.c, "app", 7); !errors.Is(err, terraformstate.ErrNoState) {
			t.Fatalf("State of a version never written = %v", err)
		}
		c, st, err := w.svc.ResolveState(w.ctx, first.ID())
		if err != nil || c.Key != tfKey || st.Ms != first.Ms || st.Current {
			t.Fatalf("ResolveState(%s) = %s %+v, %v", first.ID(), c.Key, st, err)
		}
		for _, id := range []string{
			"tf-local__app", "tf-local__app__x", "tf-local__app__01", "tf-local__none__1", "nope",
			"tf-local__app__9",
		} {
			if _, _, err := w.svc.ResolveState(
				w.ctx,
				id,
			); !errors.Is(
				err,
				terraformstate.ErrNoState,
			) {
				t.Errorf("ResolveState(%q) = %v", id, err)
			}
		}
		idx := w.reindexed(w.c)
		if len(idx["app"]) != 2 ||
			!strings.HasSuffix(idx["app"][tfbe.Version(second.Ms)], " "+tfbe.LatestPath("app")) ||
			!strings.HasSuffix(
				idx["app"][tfbe.Version(first.Ms)],
				" "+tfbe.StatePath("app", first.Ms),
			) {
			t.Fatalf("the index = %v", idx)
		}
	})
}

// The request's MD5 and serial must be the state's, and the content a
// Terraform state.
func TestUnitStateWriteChecks(t *testing.T) {
	w := newWorld(t, memStore())
	w.workspace(w.c, "app")
	good := state(3, "lin")
	for name, sw := range map[string]terraformstate.StateWrite{
		"md5":    {Body: bytesReader(good), MD5: tfbe.MD5([]byte("x")), Serial: 3},
		"serial": {Body: bytesReader(good), MD5: tfbe.MD5(good), Serial: 4},
		"text":   {Body: strings.NewReader("not json"), MD5: tfbe.MD5([]byte("not json"))},
		"array":  {Body: strings.NewReader("[1]"), MD5: tfbe.MD5([]byte("[1]"))},
		"serial missing": {
			Body: strings.NewReader(`{"lineage":"x"}`), MD5: tfbe.MD5([]byte(`{"lineage":"x"}`)),
		},
	} {
		var invalid *terraformstate.InvalidStateError
		if _, err := w.svc.WriteState(w.ctx, w.c, "app", sw); !errors.As(err, &invalid) ||
			invalid.Error() == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The declared MD5 is hex in any case.
	if _, err := w.svc.WriteState(w.ctx, w.c, "app", terraformstate.StateWrite{
		Body: bytesReader(good), MD5: strings.ToUpper(tfbe.MD5(good)), Serial: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.WriteState(w.ctx, w.c, "none", terraformstate.StateWrite{
		Body: bytesReader(good), MD5: tfbe.MD5(good), Serial: 3,
	}); !errors.Is(err, terraformstate.ErrNoWorkspace) {
		t.Fatalf("a write to a missing workspace = %v", err)
	}
	if _, err := w.svc.WriteState(w.ctx, w.c, "app", terraformstate.StateWrite{
		Body: failingReader{}, MD5: tfbe.MD5(good), Serial: 3,
	}); err == nil {
		t.Fatal("a body that fails to read was stored")
	}
}

// Another user's lock refuses a write; the holder may write, and so may
// anyone while the workspace is unlocked, as -lock=false does.
func TestUnitTheLockRule(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		w.workspace(w.c, "app")
		if _, err := w.write(w.c, "app", 1, "bob"); err != nil {
			t.Fatalf("a write to an unlocked workspace = %v", err)
		}
		if _, err := w.svc.Lock(w.ctx, w.c, "app", "alice"); err != nil {
			t.Fatal(err)
		}
		var locked *terraformstate.LockedError
		if _, err := w.write(
			w.c,
			"app",
			2,
			"bob",
		); !errors.As(err, &locked) ||
			locked.Holder != "alice" {
			t.Fatalf("bob's write under alice's lock = %v", err)
		}
		if _, err := w.svc.NewState(w.ctx, w.c, "app", "bob"); !errors.As(err, &locked) {
			t.Fatalf("bob's state-less create under alice's lock = %v", err)
		}
		if _, err := w.write(w.c, "app", 2, "alice"); err != nil {
			t.Fatalf("the holder's write = %v", err)
		}
		states, err := w.svc.States(w.ctx, w.c, "app")
		if err != nil || len(states) != 2 {
			t.Fatalf("states = %+v, %v", states, err)
		}
	})
}

// A state-less create names a version; the upload stores it under that
// name once.
func TestUnitNamedVersionIsWrittenOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		w.workspace(w.c, "app")
		first := w.mustWrite(w.c, "app", 1)
		// The clock has not moved: the next name is one more than the
		// newest.
		ms, err := w.svc.NewState(w.ctx, w.c, "app", "alice")
		if err != nil || ms != first.Ms+1 {
			t.Fatalf("NewState = %d, %v; want %d", ms, err, first.Ms+1)
		}
		body := state(2, "lin-app")
		sw := terraformstate.StateWrite{Ms: ms, MD5: tfbe.MD5(body), Serial: 2, By: "alice"}
		sw.Body = bytesReader(body)
		st, err := w.svc.WriteState(w.ctx, w.c, "app", sw)
		if err != nil || st.Ms != ms || !st.Current {
			t.Fatalf("the upload = %+v, %v", st, err)
		}
		sw.Body = bytesReader(body)
		if _, err := w.svc.WriteState(
			w.ctx,
			w.c,
			"app",
			sw,
		); !errors.Is(
			err,
			terraformstate.ErrWritten,
		) {
			t.Fatalf("a second upload = %v", err)
		}
		sw.Ms, sw.Body = first.Ms, bytesReader(body)
		if _, err := w.svc.WriteState(
			w.ctx,
			w.c,
			"app",
			sw,
		); !errors.Is(
			err,
			terraformstate.ErrWritten,
		) {
			t.Fatalf("an upload of an earlier version = %v", err)
		}
		if _, err := w.svc.NewState(w.ctx, w.c, "none", "alice"); !errors.Is(
			err, terraformstate.ErrNoWorkspace) {
			t.Fatalf("NewState of a missing workspace = %v", err)
		}
		w.reindexed(w.c)
	})
}

func TestUnitUploadStaysBoundToWorkspaceRecord(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		ws := w.workspace(w.c, "app")
		body := state(1, "lin-app")
		sw := terraformstate.StateWrite{
			WorkspaceRecordID: ws.RecordID,
			Body:              bytesReader(body), MD5: tfbe.MD5(body), Serial: 1, By: "alice",
		}
		if _, err := w.svc.WriteState(w.ctx, w.c, "app", sw); err != nil {
			t.Fatal(err)
		}
		if err := w.svc.DeleteWorkspace(w.ctx, w.c, "app", "alice"); err != nil {
			t.Fatal(err)
		}
		w.workspace(w.c, "app")
		sw.Body = bytesReader(body)
		if _, err := w.svc.WriteState(
			w.ctx,
			w.c,
			"app",
			sw,
		); !errors.Is(
			err,
			terraformstate.ErrWorkspaceChanged,
		) {
			t.Fatalf("upload to a replacement workspace = %v", err)
		}
		if _, err := w.svc.CurrentState(
			w.ctx,
			w.c,
			"app",
		); !errors.Is(
			err,
			terraformstate.ErrNoState,
		) {
			t.Fatalf("replacement workspace acquired state: %v", err)
		}
	})
}

// encryptStates applies to later writes; every state reads in the form it
// was written in.
func TestUnitEncryptStatesMayChange(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		plainRepo := w.repo("tf-plain", func(c *repo.Config) { c.EncryptStates = new(bool) })
		w.workspace(plainRepo, "app")
		first := w.mustWrite(plainRepo, "app", 1)
		if got := w.stored(
			plainRepo,
			tfbe.LatestPath("app"),
		); !bytes.Equal(
			got,
			state(1, "lin-app"),
		) ||
			first.Meta.Sealed {
			t.Fatalf("with encryptStates off state.latest.json holds %q", got)
		}
		on := true
		plainRepo.EncryptStates = &on
		second := w.mustWrite(plainRepo, "app", 2)
		if !second.Meta.Sealed || !crypt.IsSealed(w.stored(plainRepo, tfbe.LatestPath("app"))) {
			t.Fatalf("with encryptStates turned on the state is %+v", second)
		}
		if got := w.stored(plainRepo, tfbe.StatePath("app", first.Ms)); !bytes.Equal(
			got, state(1, "lin-app")) {
			t.Fatalf("the earlier state changed form: %q", got)
		}
		for ms, want := range map[int64][]byte{first.Ms: state(1, "lin-app"), second.Ms: state(2, "lin-app")} {
			if plain, _, err := w.svc.ReadState(w.ctx, plainRepo, "app", ms); err != nil ||
				!bytes.Equal(plain, want) {
				t.Fatalf("ReadState(%d) = %q, %v", ms, plain, err)
			}
		}
		w.reindexed(plainRepo)
	})
}

// Without a master key a sealed state is neither written nor read, while
// plain ones are; the index still describes what it read before.
func TestUnitWithoutAMasterKey(t *testing.T) {
	w := newWorld(t, memStore())
	w.workspace(w.c, "app")
	sealed := w.mustWrite(w.c, "app", 1)
	w.useKeys(nil)
	if _, err := w.write(w.c, "app", 2, "alice"); !errors.Is(err, terraformstate.ErrNoKeyring) {
		t.Fatalf("a sealed write without a key = %v", err)
	}
	if _, _, err := w.svc.ReadState(w.ctx, w.c, "app", sealed.Ms); !errors.Is(
		err, terraformstate.ErrNoKeyring) {
		t.Fatalf("a sealed read without a key = %v", err)
	}
	if st, err := w.svc.CurrentState(w.ctx, w.c, "app"); err != nil || st.Meta.Serial != 1 {
		t.Fatalf("the indexed state = %+v, %v", st, err)
	}
	plainRepo := w.repo("tf-plain", func(c *repo.Config) { c.EncryptStates = new(bool) })
	w.workspace(plainRepo, "app")
	w.mustWrite(plainRepo, "app", 1)
	// A reindex without the key cannot read the sealed state: it is
	// indexed as unreadable, and read as such where the index lacks it.
	if err := w.items.Reindex(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	st, err := w.svc.CurrentState(w.ctx, w.c, "app")
	if err != nil || !st.Meta.Unreadable ||
		st.Meta.Size != int64(len(w.stored(w.c, tfbe.LatestPath("app")))) {
		t.Fatalf("a sealed state without a key = %+v, %v", st, err)
	}
}

// A sealed state opens under any key version the keyring holds; Reseal
// moves every state to the active one, after which the old key can go.
func TestUnitRotationReseals(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		w.workspace(w.c, "app")
		first := w.mustWrite(w.c, "app", 1)
		second := w.mustWrite(w.c, "app", 2)
		plainRepo := w.repo("tf-plain", func(c *repo.Config) { c.EncryptStates = new(bool) })
		w.workspace(plainRepo, "app")
		w.mustWrite(plainRepo, "app", 1)

		w.useKeys(keyring(t, masterKey(1, 'a'), masterKey(2, 'b')))
		if plain, _, err := w.svc.ReadState(w.ctx, w.c, "app", first.Ms); err != nil ||
			!bytes.Equal(plain, state(1, "lin-app")) {
			t.Fatalf("a version-1 state under keys 1 and 2 reads %q, %v", plain, err)
		}
		rep, err := w.svc.Reseal(w.ctx)
		if err != nil || rep.Resealed != 2 || len(rep.Unreadable) != 0 {
			t.Fatalf("Reseal = %+v, %v", rep, err)
		}
		for _, p := range []string{tfbe.LatestPath("app"), tfbe.StatePath("app", first.Ms)} {
			if v, err := crypt.Version(w.stored(w.c, p)); err != nil || v != 2 {
				t.Fatalf("%s is sealed under version %d, %v", p, v, err)
			}
		}
		w.reindexed(w.c)
		if rep, err := w.svc.Reseal(w.ctx); err != nil || rep.Resealed != 0 {
			t.Fatalf("a second Reseal = %+v, %v", rep, err)
		}

		w.useKeys(keyring(t, masterKey(2, 'b')))
		for ms, want := range map[int64][]byte{first.Ms: state(1, "lin-app"), second.Ms: state(2, "lin-app")} {
			if plain, _, err := w.svc.ReadState(w.ctx, w.c, "app", ms); err != nil ||
				!bytes.Equal(plain, want) {
				t.Fatalf("after the rotation ReadState(%d) = %q, %v", ms, plain, err)
			}
		}
		st, err := w.svc.CurrentState(w.ctx, w.c, "app")
		if err != nil || st.Meta.Serial != 2 {
			t.Fatalf("after the rotation the current state = %+v, %v", st, err)
		}
	})
}

// A state sealed under another master key of an older version cannot be
// resealed: Reseal names it and goes on.
func TestUnitResealNamesWhatDoesNotOpen(t *testing.T) {
	w := newWorld(t, memStore())
	w.workspace(w.c, "app")
	w.mustWrite(w.c, "app", 1)
	w.useKeys(keyring(t, masterKey(1, 'z'), masterKey(2, 'b')))
	rep, err := w.svc.Reseal(w.ctx)
	if err != nil || rep.Resealed != 0 || len(rep.Unreadable) != 1 ||
		rep.Unreadable[0] != "tf-local/"+tfbe.LatestPath("app") {
		t.Fatalf("Reseal = %+v, %v", rep, err)
	}
	w.useKeys(nil)
	if _, err := w.svc.Reseal(w.ctx); !errors.Is(err, terraformstate.ErrNoKeyring) {
		t.Fatalf("Reseal without a keyring = %v", err)
	}
}

// What the item routes do to a workspace's files the index follows: a
// delete of the current state leaves the workspace without one, and a
// trash restore brings it back.
func TestUnitItemDeletesAndRestores(t *testing.T) {
	eachStore(t, func(t *testing.T, w *world) {
		w.workspace(w.c, "app")
		first := w.mustWrite(w.c, "app", 1)
		second := w.mustWrite(w.c, "app", 2)
		if _, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: tfbe.LatestPath("app")},
			"admin"); err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.CurrentState(
			w.ctx,
			w.c,
			"app",
		); !errors.Is(
			err,
			terraformstate.ErrNoState,
		) {
			t.Fatalf("after deleting state.latest.json the current state = %v", err)
		}
		if idx := w.reindexed(w.c); len(idx["app"]) != 1 {
			t.Fatalf("the index = %v", idx)
		}
		// A write now has no current state to move.
		third := w.mustWrite(w.c, "app", 3)
		if third.Ms <= second.Ms {
			t.Fatalf("the third state's version %d is not after %d", third.Ms, second.Ms)
		}
		// Restoring the deleted current state over the third retires the
		// third's version.
		if _, err := w.trash.Restore(w.ctx, w.c.Key, tfbe.LatestPath("app"),
			trash.Destination{Repo: w.c}, "admin", nil); err != nil {
			t.Fatal(err)
		}
		cur, err := w.svc.CurrentState(w.ctx, w.c, "app")
		if err != nil || cur.Ms != second.Ms || cur.Meta.Serial != 2 {
			t.Fatalf("the restored current state = %+v, %v", cur, err)
		}
		idx := w.reindexed(w.c)
		if _, ok := idx["app"][tfbe.Version(third.Ms)]; ok || len(idx["app"]) != 2 {
			t.Fatalf("the index after the restore = %v", idx)
		}
		if _, err := w.svc.State(w.ctx, w.c, "app", first.Ms); err != nil {
			t.Fatal(err)
		}
	})
}

// A state the index lacks is read from its content.
func TestUnitStateWithoutTheIndex(t *testing.T) {
	w := newWorld(t, memStore())
	w.workspace(w.c, "app")
	written := w.mustWrite(w.c, "app", 1)
	pkgs, _, err := w.st.Packages().List(w.ctx, tfKey, store.Page{Limit: 10})
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("packages %v, %v", pkgs, err)
	}
	if err := w.st.Packages().Delete(w.ctx, pkgs[0].ID); err != nil {
		t.Fatal(err)
	}
	st, err := w.svc.CurrentState(w.ctx, w.c, "app")
	if err != nil || st.Meta != written.Meta {
		t.Fatalf("CurrentState without the index = %+v, %v; want %+v", st, err, written)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read fails") }
