package item

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// recordingFeed is the federation's item feed, recording what it heard.
type recordingFeed struct {
	got []artapi.ItemChange
	err error
}

func (f *recordingFeed) ItemChanged(_ context.Context, c artapi.ItemChange) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, c)
	return nil
}

// heard lists the changes as "put path" or "delete path", folders with a
// trailing slash, and forgets them.
func (f *recordingFeed) heard() []string {
	var out []string
	for _, c := range f.got {
		op, p := "put", c.Path
		if c.Deleted {
			op = "delete"
		}
		if c.Folder {
			p += "/"
		}
		out = append(out, op+" "+p)
	}
	f.got = nil
	return out
}

func feedWorld(t *testing.T) (*world, *recordingFeed) {
	t.Helper()
	w := newWorld(t)
	f := &recordingFeed{}
	w.db.SetItemFeed(f)
	return w, f
}

func wantHeard(t *testing.T, f *recordingFeed, want ...string) {
	t.Helper()
	if got := f.heard(); !slices.Equal(got, want) {
		t.Fatalf("fed %q, want %q", got, want)
	}
}

// TestUnitFeedReportsClientChanges: every change a client makes to an
// item is reported with the item's whole state, in the change's
// transaction; the folders a deploy makes are not.
func TestUnitFeedReportsClientChanges(t *testing.T) {
	ctx := t.Context()
	w, f := feedWorld(t)
	it, err := w.deploy("a/b/f.txt", "one", kernel.Checksums{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || f.got[0].Actual.SHA256 != sums("one").SHA256 || f.got[0].Size != 3 ||
		f.got[0].CreatedBy != "alice" || f.got[0].Repository != "libs" {
		t.Fatalf("deploy fed %+v", f.got)
	}
	wantHeard(t, f, "put a/b/f.txt")
	if err := w.svc.Annotate(ctx, w.loc, it, model.Properties{"k": {"v"}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || !slices.Equal(f.got[0].Properties["k"], []string{"v"}) {
		t.Fatalf("annotate fed %+v", f.got)
	}
	wantHeard(t, f, "put a/b/f.txt")
	if _, err := w.svc.RecordChecksum(ctx, Target{Repo: w.loc, Path: "a/b/f.txt"}, SHA1, sums("one").SHA1); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, f, "put a/b/f.txt")
	if _, err := w.svc.MakeFolder(ctx, Target{Repo: w.loc, Path: "empty"}, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.MakeFolder(ctx, Target{Repo: w.loc, Path: "empty"}, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, f, "put empty/")
	w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "a"}, To: Target{Repo: w.loc, Path: "m"}, Move: true,
		CanOverwrite: true, By: "bob",
	})
	// The folders land with their properties, parents first.
	wantHeard(t, f, "put m/", "put m/b/", "put m/b/f.txt", "delete a/b/f.txt", "delete a/b/", "delete a/")
	if _, err := w.deploy("m/b/g.txt", "two", kernel.Checksums{}); err != nil {
		t.Fatal(err)
	}
	f.heard()
	if _, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "m"}, "bob"); err != nil {
		t.Fatal(err)
	}
	got := f.heard()
	if len(got) != 4 || !slices.Equal(got[2:], []string{"delete m/b/", "delete m/"}) ||
		!strings.HasPrefix(got[0], "delete m/b/") || !strings.HasPrefix(got[1], "delete m/b/") {
		t.Fatalf("folder delete fed %q", got)
	}

	// A feed that fails rolls the change back.
	f.err = errors.New("boom")
	if _, err := w.deploy("x.txt", "x", kernel.Checksums{}); err == nil {
		t.Fatal("deploy succeeded with a failing feed")
	}
	if _, ok := w.get(w.loc, "x.txt"); ok {
		t.Fatal("deploy kept after its feed failed")
	}
}

// fedWorld is a world with the federated repository "fed" and the content
// "one" in the blob store, which a member's change refers to.
func fedWorld(t *testing.T) (*world, *recordingFeed, repo.Config) {
	t.Helper()
	w, f := feedWorld(t)
	fed := *repo.Defaults(&repo.Config{Key: "fed", Class: repo.Federated})
	if err := w.db.Repos().Create(t.Context(), fed); err != nil {
		t.Fatal(err)
	}
	if _, err := w.deploy("src/one.txt", "one", kernel.Checksums{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.deploy("src/two.txt", "two", kernel.Checksums{}); err != nil {
		t.Fatal(err)
	}
	f.heard()
	return w, f, fed
}

func put(p, body string, props map[string][]string) artapi.ItemChange {
	return artapi.ItemChange{
		Repository: "fed", Path: p, Size: int64(len(body)), Actual: kernel.Checksums{SHA256: sums(body).SHA256},
		Created: at, CreatedBy: "carol", Modified: at, ModifiedBy: "carol", Properties: props,
	}
}

// TestUnitApplyFederated applies other members' changes: a new file, its
// properties, new content, a folder, and refuses what is in the way. None
// of it is fed back.
func TestUnitApplyFederated(t *testing.T) {
	ctx := t.Context()
	w, f, fed := fedWorld(t)
	one := sums("one").SHA256
	before := w.refs(one)
	if err := w.svc.ApplyFederated(ctx, fed, put("d/one.txt", "one", map[string][]string{"k": {"1"}})); err != nil {
		t.Fatal(err)
	}
	it, ok := w.get(fed, "d/one.txt")
	if !ok || it.Actual != sums("one") || it.CreatedBy != "carol" || w.refs(one) != before+1 {
		t.Fatalf("applied %+v (refs %d)", it, w.refs(one))
	}
	if props, _ := w.db.Items().Properties(ctx, it.ID); !props.Has("k", "1") {
		t.Fatalf("properties %v", props)
	}
	// The same content with other properties changes the properties only.
	if err := w.svc.ApplyFederated(ctx, fed, put("d/one.txt", "one", map[string][]string{"j": {"2"}})); err != nil {
		t.Fatal(err)
	}
	again, _ := w.get(fed, "d/one.txt")
	if props, _ := w.db.Items().Properties(ctx, again.ID); again.ID != it.ID || props.Has("k", "1") || !props.Has("j", "2") {
		t.Fatalf("properties %v of %d, was %d", props, again.ID, it.ID)
	}
	// Other content replaces the file and releases the old content.
	if err := w.svc.ApplyFederated(ctx, fed, put("d/one.txt", "two", nil)); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.get(fed, "d/one.txt"); got.Actual.SHA256 != sums("two").SHA256 || w.refs(one) != before {
		t.Fatalf("replaced %+v (refs %d)", got, w.refs(one))
	}
	folder := artapi.ItemChange{Repository: "fed", Path: "e/f", Folder: true, Properties: map[string][]string{"p": {"q"}}}
	if err := w.svc.ApplyFederated(ctx, fed, folder); err != nil {
		t.Fatal(err)
	}
	if got, ok := w.get(fed, "e/f"); !ok || !got.Folder {
		t.Fatalf("folder %+v", got)
	}
	wantHeard(t, f)

	for _, c := range []struct {
		change artapi.ItemChange
		want   error
	}{
		{put("d", "one", nil), kernel.ErrConflict},
		{put("d/one.txt/x", "one", nil), kernel.ErrConflict},
		{artapi.ItemChange{Repository: "fed", Path: "d/one.txt", Folder: true}, kernel.ErrConflict},
		{artapi.ItemChange{Repository: "fed", Path: "n.txt"}, kernel.ErrInvalid},
		{artapi.ItemChange{Repository: "fed", Path: "/"}, kernel.ErrInvalidItemPath},
	} {
		if err := w.svc.ApplyFederated(ctx, fed, c.change); !errors.Is(err, c.want) {
			t.Fatalf("%+v: %v, want %v", c.change, err, c.want)
		}
	}
}

// TestUnitDeleteFederated deletes a file, and a folder only once it is
// empty; nothing at the path, or the other kind of item, is left alone.
func TestUnitDeleteFederated(t *testing.T) {
	ctx := t.Context()
	w, f, fed := fedWorld(t)
	for _, p := range []string{"a/one.txt", "a/b/two.txt"} {
		if err := w.svc.ApplyFederated(ctx, fed, put(p, strings.TrimSuffix(p[strings.LastIndex(p, "/")+1:], ".txt"), nil)); err != nil {
			t.Fatal(err)
		}
	}
	all := []string{"a", "a/b", "a/b/two.txt", "a/one.txt"}
	present := func() []string {
		var out []string
		for _, p := range all {
			if _, ok := w.get(fed, p); ok {
				out = append(out, p)
			}
		}
		return out
	}
	for _, s := range []struct {
		path   string
		folder bool
		left   []string
	}{
		{"nothing", false, all},
		{"a/one.txt", true, all},
		{"a/b", false, all},
		{"a/b", true, all},
		{"a/b/two.txt", false, []string{"a", "a/b", "a/one.txt"}},
		{"a/b", true, []string{"a", "a/one.txt"}},
		{"a/one.txt", false, []string{"a"}},
		{"a", true, nil},
	} {
		if err := w.svc.DeleteFederated(ctx, fed, s.path, s.folder); err != nil {
			t.Fatalf("delete %s: %v", s.path, err)
		}
		if got := present(); !slices.Equal(got, s.left) {
			t.Fatalf("after deleting %s (folder %v): %v, want %v", s.path, s.folder, got, s.left)
		}
	}
	wantHeard(t, f)
	if err := w.svc.DeleteFederated(ctx, fed, "", false); !errors.Is(err, kernel.ErrInvalidItemPath) {
		t.Fatalf("empty path: %v", err)
	}
}
