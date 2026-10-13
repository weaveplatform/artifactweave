package item

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// addRepo creates another repository in the world.
func (w *world) addRepo(c repo.Config) repo.Config {
	w.t.Helper()
	c = *repo.Defaults(&c)
	if err := w.db.Repos().Create(context.Background(), c); err != nil {
		w.t.Fatal(err)
	}
	return c
}

func (w *world) must(p, body string) types.Item {
	w.t.Helper()
	it, err := w.deploy(p, body, types.Checksums{})
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}

func (w *world) get(c repo.Config, p string) (types.Item, bool) {
	w.t.Helper()
	it, err := w.db.Items().Get(context.Background(), c.Key, false, p)
	if errors.Is(err, store.ErrNotFound) {
		return it, false
	}
	if err != nil {
		w.t.Fatal(err)
	}
	return it, true
}

func (w *world) transfer(t Transfer) TransferResult {
	w.t.Helper()
	res, err := w.svc.Transfer(context.Background(), t)
	if err != nil {
		w.t.Fatal(err)
	}
	return res
}

func TestCopyFile(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	other := w.addRepo(repo.Config{Key: "other", Class: repo.Local})
	src := w.must("a/x.txt", "content")
	if err := w.db.Items().SetProperties(ctx, src.ID, types.Properties{"k": {"v"}}); err != nil {
		t.Fatal(err)
	}
	res := w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "a/x.txt"}, To: Target{Repo: other, Path: "b/y.txt"},
		By: "bob",
	})
	if res.Files != 1 || res.Folders != 0 || res.Failed != 0 || len(res.Messages) != 1 ||
		res.Messages[0].Level != LevelInfo ||
		!strings.Contains(res.Messages[0].Text, "1 artifacts and 0 folders were copied") {
		t.Fatalf("result %+v", res)
	}
	got, ok := w.get(other, "b/y.txt")
	if !ok || got.Actual != src.Actual || got.CreatedBy != "bob" || !got.Created.Equal(at) {
		t.Fatalf("copy %+v", got)
	}
	if props, _ := w.db.Items().Properties(ctx, got.ID); props["k"][0] != "v" {
		t.Fatalf("properties %v", props)
	}
	if _, ok := w.get(w.loc, "a/x.txt"); !ok {
		t.Fatal("copy removed the source")
	}
	if w.refs(src.Actual.SHA256) != 2 {
		t.Fatal("a copy shares the blob with one more reference")
	}
	// A target ending in a slash, or an existing folder, receives the file
	// under its own name.
	w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "a/x.txt"}, To: Target{Repo: other, Path: "b"},
	})
	w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "a/x.txt"}, To: Target{Repo: other, Path: "new"},
		IntoFolder: true,
	})
	for _, p := range []string{"b/x.txt", "new/x.txt"} {
		if _, ok := w.get(other, p); !ok {
			t.Errorf("%s missing", p)
		}
	}
}

func TestCopyAndMoveFolder(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	one := w.must("d/1.txt", "one")
	w.must("d/sub/2.txt", "two")
	folder, _ := w.get(w.loc, "d/sub")
	if err := w.db.Items().SetProperties(ctx, folder.ID, types.Properties{"f": {"1"}}); err != nil {
		t.Fatal(err)
	}
	// A missing target folder takes the source's place.
	res := w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "d"}, To: Target{Repo: w.loc, Path: "e"},
	})
	if res.Files != 2 || res.Folders != 2 {
		t.Fatalf("copy %+v", res)
	}
	for _, p := range []string{"e/1.txt", "e/sub/2.txt", "d/1.txt"} {
		if _, ok := w.get(w.loc, p); !ok {
			t.Fatalf("%s missing", p)
		}
	}
	if f, _ := w.get(w.loc, "e/sub"); f.ID == folder.ID {
		t.Fatal("folder not copied")
	} else if props, _ := w.db.Items().Properties(ctx, f.ID); props["f"][0] != "1" {
		t.Fatalf("folder properties %v", props)
	}
	// An existing folder receives the source inside it.
	w.transfer(Transfer{From: Target{Repo: w.loc, Path: "d"}, To: Target{Repo: w.loc, Path: "e"}})
	if _, ok := w.get(w.loc, "e/d/sub/2.txt"); !ok {
		t.Fatal("copy into an existing folder")
	}
	if err := w.db.Items().
		RecordDownload(ctx, one.ID, store.Download{By: "x", At: at}); err != nil {
		t.Fatal(err)
	}
	before := w.refs(one.Actual.SHA256)
	res = w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "d"}, To: Target{Repo: w.loc, Path: "m"}, Move: true,
	})
	if res.Files != 2 || !strings.Contains(res.Messages[0].Text, "were moved") {
		t.Fatalf("move %+v", res)
	}
	if _, ok := w.get(w.loc, "d"); ok {
		t.Fatal("the moved folder is still there")
	}
	moved, ok := w.get(w.loc, "m/1.txt")
	if !ok || moved.CreatedBy != one.CreatedBy || w.refs(one.Actual.SHA256) != before {
		t.Fatalf("moved %+v refs %d", moved, w.refs(one.Actual.SHA256))
	}
	if st, _ := w.db.Items().Stats(ctx, moved.ID); st.DownloadCount != 1 {
		t.Fatalf("statistics did not move: %+v", st)
	}
}

func TestCopyRoot(t *testing.T) {
	w := newWorld(t)
	other := w.addRepo(repo.Config{Key: "other", Class: repo.Local})
	w.must("a.txt", "a")
	w.must("d/b.txt", "b")
	res := w.transfer(Transfer{From: Target{Repo: w.loc}, To: Target{Repo: other, Path: "all"}})
	if res.Files != 2 {
		t.Fatalf("%+v", res)
	}
	for _, p := range []string{"all/a.txt", "all/d/b.txt"} {
		if _, ok := w.get(other, p); !ok {
			t.Errorf("%s missing", p)
		}
	}
}

func TestTransferDryRun(t *testing.T) {
	w := newWorld(t)
	w.must("d/1.txt", "one")
	res := w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "d"}, To: Target{Repo: w.loc, Path: "e"},
		Move: true, Dry: true,
	})
	if res.Files != 1 || res.Folders != 1 ||
		!strings.HasPrefix(res.Messages[0].Text, "Dry run for moving") {
		t.Fatalf("%+v", res)
	}
	if _, ok := w.get(w.loc, "e"); ok {
		t.Fatal("a dry run wrote")
	}
	if _, ok := w.get(w.loc, "d/1.txt"); !ok {
		t.Fatal("a dry run moved")
	}
}

func TestTransferConflicts(t *testing.T) {
	w := newWorld(t)
	w.must("d/f/1.txt", "one")
	w.must("d/f/2.txt", "two")
	w.must("d/z.txt", "z")
	w.must("d/y.txt", "y")
	// At the target, a file where the folder f goes, a folder where z.txt
	// goes, and an existing y.txt.
	w.must("e/f", "blocker")
	w.must("e/z.txt/inside", "x")
	old := w.must("e/y.txt", "old y")
	res := w.transfer(
		Transfer{From: Target{Repo: w.loc, Path: "d"}, To: Target{Repo: w.loc, Path: "e"}},
	)
	// d lands inside e: no conflicts there.
	if res.Failed != 0 {
		t.Fatalf("%+v", res)
	}
	// Copy a tree straight onto e by copying the root of a repository
	// holding it.
	src := w.addRepo(repo.Config{Key: "src", Class: repo.Local})
	for _, p := range []string{"f/1.txt", "f/2.txt", "z.txt", "y.txt"} {
		if _, err := w.svc.Deploy(context.Background(), Deploy{
			Target: Target{Repo: src, Path: p}, Body: strings.NewReader("new " + p),
		}); err != nil {
			t.Fatal(err)
		}
	}
	res = w.transfer(Transfer{From: Target{Repo: src}, To: Target{Repo: w.loc, Path: "e"}})
	// f and its two files fail (one message for the folder), z.txt fails,
	// and y.txt may not be overwritten.
	if res.Failed != 5 || res.Files != 0 {
		t.Fatalf("%+v", res)
	}
	errorsSeen := 0
	for _, m := range res.Messages {
		if m.Level == LevelError {
			errorsSeen++
		}
	}
	if errorsSeen != 3 || res.Messages[len(res.Messages)-1].Level != LevelWarning {
		t.Fatalf("messages %+v", res.Messages)
	}
	// failFast stops at the first failure.
	res = w.transfer(
		Transfer{From: Target{Repo: src}, To: Target{Repo: w.loc, Path: "e"}, FailFast: true},
	)
	if res.Failed != 1 {
		t.Fatalf("failFast %+v", res)
	}
	// With the right to overwrite, y.txt is replaced and its old content
	// released.
	res = w.transfer(Transfer{
		From: Target{Repo: src, Path: "y.txt"}, To: Target{Repo: w.loc, Path: "e/y.txt"},
		CanOverwrite: true,
	})
	if res.Files != 1 || w.refs(old.Actual.SHA256) != 0 {
		t.Fatalf("overwrite %+v refs %d", res, w.refs(old.Actual.SHA256))
	}
}

func TestMoveKeepsWhatFailed(t *testing.T) {
	w := newWorld(t)
	src := w.addRepo(repo.Config{Key: "src", Class: repo.Local})
	for _, p := range []string{"ok.txt", "sub/bad.txt"} {
		if _, err := w.svc.Deploy(context.Background(), Deploy{
			Target: Target{Repo: src, Path: p}, Body: strings.NewReader(p),
		}); err != nil {
			t.Fatal(err)
		}
	}
	w.must("t/sub/bad.txt/x", "folder in the way")
	res := w.transfer(
		Transfer{From: Target{Repo: src}, To: Target{Repo: w.loc, Path: "t"}, Move: true},
	)
	if res.Files != 1 || res.Failed != 1 {
		t.Fatalf("%+v", res)
	}
	if _, ok := w.get(src, "ok.txt"); ok {
		t.Fatal("the moved file stayed")
	}
	if _, ok := w.get(src, "sub/bad.txt"); !ok {
		t.Fatal("the file that failed to move was removed")
	}
	if _, ok := w.get(src, "sub"); !ok {
		t.Fatal("the folder still holding a file was removed")
	}
}

func TestTransferRefused(t *testing.T) {
	w := newWorld(t)
	w.must("d/1.txt", "one")
	w.must("file", "f")
	ctx := context.Background()
	for name, tr := range map[string]Transfer{
		"same location": {From: Target{Repo: w.loc, Path: "d"}, To: Target{Repo: w.loc, Path: "d"}},
		"into itself":   {From: Target{Repo: w.loc, Path: "d"}, To: Target{Repo: w.loc, Path: "d/x"}},
		"root into itself": {
			From: Target{Repo: w.loc}, To: Target{Repo: w.loc, Path: "x"},
		},
		"below a file": {From: Target{Repo: w.loc, Path: "d"}, To: Target{Repo: w.loc, Path: "file/x"}},
	} {
		if _, err := w.svc.Transfer(ctx, tr); !errors.Is(err, ErrInvalidTransfer) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := w.svc.Transfer(ctx, Transfer{
		From: Target{Repo: w.loc, Path: "missing"}, To: Target{Repo: w.loc, Path: "x"},
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing source: %v", err)
	}
}

func TestTransferLayouts(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	maven := w.addRepo(repo.Config{Key: "mvn", Class: repo.Local, RepoLayoutRef: "maven-2-default"})
	gradle := w.addRepo(
		repo.Config{Key: "gradle", Class: repo.Local, RepoLayoutRef: "gradle-default"},
	)
	custom := w.addRepo(repo.Config{Key: "custom", Class: repo.Local, RepoLayoutRef: "flat"})
	doc, _ := json.Marshal(map[string]string{
		"artifactPathPattern": "[module]-[baseRev].[ext]",
	})
	if err := w.db.Configs().Put(ctx, types.ConfigDocument{
		Kind: types.ConfigLayout, Key: "flat", Document: doc,
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"org/acme/app/1.0/app-1.0.jar", "notes.txt"} {
		if _, err := w.svc.Deploy(ctx, Deploy{
			Target: Target{Repo: maven, Path: p}, Body: strings.NewReader(p),
		}); err != nil {
			t.Fatal(err)
		}
	}
	res := w.transfer(Transfer{
		From: Target{Repo: maven}, To: Target{Repo: gradle}, TranslateLayouts: true,
	})
	if res.Files != 2 {
		t.Fatalf("%+v", res)
	}
	if _, ok := w.get(gradle, "org.acme/app/1.0/app-1.0.jar"); !ok {
		t.Fatal("the jar was not translated to the gradle layout")
	}
	if _, ok := w.get(gradle, "notes.txt"); !ok {
		t.Fatal("a path outside the layout is kept")
	}
	w.transfer(
		Transfer{From: Target{Repo: maven}, To: Target{Repo: custom}, TranslateLayouts: true},
	)
	if _, ok := w.get(custom, "app-1.0.jar"); !ok {
		t.Fatal("translation to a stored layout")
	}
	// Below the target root the paths are kept, with a warning.
	res = w.transfer(Transfer{
		From: Target{Repo: maven}, To: Target{Repo: gradle, Path: "sub"}, TranslateLayouts: true,
	})
	if res.Messages[0].Level != LevelWarning {
		t.Fatalf("%+v", res.Messages)
	}
	if _, ok := w.get(gradle, "sub/org/acme/app/1.0/app-1.0.jar"); !ok {
		t.Fatal("paths kept below the root")
	}
	bad := w.addRepo(repo.Config{Key: "bad", Class: repo.Local, RepoLayoutRef: "no-such-layout"})
	if _, err := w.svc.Transfer(ctx, Transfer{
		From: Target{Repo: maven}, To: Target{Repo: bad}, TranslateLayouts: true,
	}); err == nil {
		t.Fatal("an unknown layout must fail")
	}
}

func TestTransferStoreFailures(t *testing.T) {
	ctx := context.Background()
	for _, fail := range []string{
		"get", "descendants", "properties", "put", "putFolder", "ref", "unref", "patch",
		"stats", "putStats", "delete", "children", "versionOf",
	} {
		w := newWorld(t)
		one := w.must("d/1.txt", "one")
		w.must("e/1.txt", "old")
		if err := w.db.Items().
			SetProperties(ctx, one.ID, types.Properties{"k": {"v"}}); err != nil {
			t.Fatal(err)
		}
		if err := w.db.Items().
			RecordDownload(ctx, one.ID, store.Download{By: "x", At: at}); err != nil {
			t.Fatal(err)
		}
		w.svc.Store = faulty{w.db, fail}
		_, err := w.svc.Transfer(ctx, Transfer{
			From: Target{Repo: w.loc, Path: "d"}, To: Target{Repo: w.loc, Path: "n/e"},
			Move: true, CanOverwrite: true,
		})
		if !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", fail, err)
		}
	}
}

func TestCopyOverwriteReleaseFails(t *testing.T) {
	w := newWorld(t)
	w.must("a.txt", "new")
	w.must("b.txt", "old")
	w.svc.Store = faulty{w.db, "unref"}
	if _, err := w.svc.Transfer(context.Background(), Transfer{
		From: Target{Repo: w.loc, Path: "a.txt"}, To: Target{Repo: w.loc, Path: "b.txt"},
		CanOverwrite: true,
	}); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
}
