package item

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

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

// sameHistory reports whether a copied or moved file kept its source's
// creation, modification and update times and users.
func sameHistory(got, src types.Item) bool {
	return got.Created.Equal(src.Created) && got.CreatedBy == src.CreatedBy &&
		got.Modified.Equal(src.Modified) && got.ModifiedBy == src.ModifiedBy &&
		got.Updated.Equal(src.Updated)
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
	// Deployed an hour before the copy, so the copy's times tell which
	// moment they came from.
	w.svc.Now = func() time.Time { return at.Add(-time.Hour) }
	src := w.must("a/x.txt", "content")
	w.svc.Now = func() time.Time { return at }
	if err := w.db.Items().
		SetProperties(ctx, src.ID, types.Properties{"k": {"v"}}, time.Now()); err != nil {
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
	// The file keeps its own history (Artifactory 7.x's default); the folder
	// the copy made is new, made now by the caller.
	got, ok := w.get(other, "b/y.txt")
	if !ok || got.Actual != src.Actual || !sameHistory(got, src) || got.Created.Equal(at) {
		t.Fatalf("copy %+v of %+v", got, src)
	}
	if dir, ok := w.get(other, "b"); !ok || dir.CreatedBy != "bob" || !dir.Created.Equal(at) {
		t.Fatalf("folder %+v", dir)
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
	if err := w.db.Items().
		SetProperties(ctx, folder.ID, types.Properties{"f": {"1"}}, time.Now()); err != nil {
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
	if !ok || !sameHistory(moved, one) || w.refs(one.Actual.SHA256) != before {
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
	}); !errors.Is(err, ErrNoSource) || !errors.Is(err, ErrNotFound) ||
		!strings.Contains(err.Error(), "libs:missing") {
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
		"artifactPathPattern": "[orgPath]/[module]-[baseRev].[ext]",
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
	// The kept path is reported as a warning (#505).
	if want := (Message{LevelWarning, "Cannot translate mvn:notes.txt from layout " +
		"maven-2-default to layout gradle-default: layout maven-2-default does not read it; " +
		"it is copied to gradle:notes.txt"}); !slices.Contains(res.Messages, want) {
		t.Fatalf("%+v", res.Messages)
	}
	w.transfer(
		Transfer{From: Target{Repo: maven}, To: Target{Repo: custom}, TranslateLayouts: true},
	)
	if _, ok := w.get(custom, "org/acme/app-1.0.jar"); !ok {
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

// A file a translation cannot convert is copied, or moved, at its own path
// with a warning that names it and why (#505): ivy-default needs a type
// Maven's paths do not give, and ansible-default drops its tokens, so it
// builds no path at all. Without a translation nothing is reported.
func TestTransferLayoutsWarnUntranslated(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	maven := w.addRepo(repo.Config{Key: "mvn", Class: repo.Local, RepoLayoutRef: "maven-2-default"})
	ivy := w.addRepo(repo.Config{Key: "ivy", Class: repo.Local, RepoLayoutRef: "ivy-default"})
	galaxy := w.addRepo(
		repo.Config{Key: "galaxy", Class: repo.Local, RepoLayoutRef: "ansible-default"},
	)
	const jar = "org/acme/app/1.0/app-1.0.jar"
	if _, err := w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: maven, Path: jar}, Body: strings.NewReader(jar),
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		to   repo.Config
		move bool
		want string
	}{
		{ivy, false, "Cannot translate mvn:" + jar + " from layout maven-2-default to layout " +
			"ivy-default: layout ivy-default cannot build a path from what layout " +
			"maven-2-default reads in it; it is copied to ivy:" + jar},
		{galaxy, true, "Cannot translate mvn:" + jar + " from layout maven-2-default to layout " +
			"ansible-default: layout ansible-default cannot build a path; it is moved to galaxy:" +
			jar},
	} {
		res := w.transfer(Transfer{
			From: Target{Repo: maven}, To: Target{Repo: c.to}, TranslateLayouts: true,
			Move: c.move,
		})
		if res.Files != 1 || res.Failed != 0 ||
			!slices.Contains(res.Messages, Message{LevelWarning, c.want}) {
			t.Fatalf("%s: %+v", c.to.Key, res)
		}
		if _, ok := w.get(c.to, jar); !ok {
			t.Fatalf("%s: the file is not at its own path", c.to.Key)
		}
	}
	res := w.transfer(Transfer{From: Target{Repo: galaxy}, To: Target{Repo: maven}})
	if len(res.Messages) != 1 || res.Messages[0].Level != LevelInfo {
		t.Fatalf("without a translation: %+v", res.Messages)
	}
}

// A translation never lands two source paths on one target path (#496):
// a file whose values the target layout has no place for is refused and
// reported, not written over the version that does fit, and so is a kept
// path another file translates to. Under failFast the first refusal ends
// the transfer.
func TestTransferLayoutsNeverMerge(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	gomod := w.addRepo(repo.Config{Key: "go-local", Class: repo.Local, RepoLayoutRef: "go-default"})
	flat := w.addRepo(
		repo.Config{Key: "flat", Class: repo.Local, RepoLayoutRef: "simple-default"},
	)
	for _, p := range []string{
		"example.com/lib/@v/v1.2.3.zip",
		"example.com/lib/@v/v1.2.3-rc.1.zip",
	} {
		if _, err := w.svc.Deploy(ctx, Deploy{
			Target: Target{Repo: gomod, Path: p}, Body: strings.NewReader(p),
		}); err != nil {
			t.Fatal(err)
		}
	}
	res := w.transfer(Transfer{
		From: Target{Repo: gomod}, To: Target{Repo: flat}, TranslateLayouts: true,
	})
	if res.Files != 1 || res.Failed != 1 {
		t.Fatalf("%+v", res)
	}
	got, ok := w.get(flat, "example.com/lib/lib-1.2.3.zip")
	if !ok || got.Actual.SHA256 != sums("example.com/lib/@v/v1.2.3.zip").SHA256 {
		t.Fatalf("the release: %+v %v", got, ok)
	}
	want := Message{LevelError, "Cannot transfer go-local:example.com/lib/@v/v1.2.3-rc.1.zip " +
		"to flat:example.com/lib/lib-1.2.3.zip: layout simple-default has no place for " +
		"[fileItegRev] rc.1 from layout go-default, so another path could translate to " +
		"example.com/lib/lib-1.2.3.zip"}
	if !slices.Contains(res.Messages, want) {
		t.Fatalf("%+v", res.Messages)
	}

	// A path the source layout does not read keeps its place, so it may
	// meet a translated one; whichever comes second is refused.
	maven := w.addRepo(
		repo.Config{Key: "mvn", Class: repo.Local, RepoLayoutRef: "maven-2-default"},
	)
	for _, p := range []string{"a/b/b-1.0.pom", "a/b/1.0/b-1.0.pom"} {
		if _, err := w.svc.Deploy(ctx, Deploy{
			Target: Target{Repo: flat, Path: p}, Body: strings.NewReader(p),
		}); err != nil {
			t.Fatal(err)
		}
	}
	res = w.transfer(Transfer{
		From: Target{Repo: flat, Path: "a"}, To: Target{Repo: maven}, TranslateLayouts: true,
	})
	want = Message{LevelError, "Cannot transfer flat:a/b/1.0/b-1.0.pom to mvn:a/b/1.0/b-1.0.pom: " +
		"flat:a/b/b-1.0.pom lands there too"}
	if res.Files != 1 || res.Failed != 1 || res.Messages[0] != want {
		t.Fatalf("%+v", res)
	}
	got, ok = w.get(maven, "a/b/1.0/b-1.0.pom")
	if !ok || got.Actual.SHA256 != sums("a/b/b-1.0.pom").SHA256 {
		t.Fatalf("the translated file: %+v %v", got, ok)
	}

	// failFast stops at the refusal, which comes before the release in
	// the source's order, and writes nothing.
	again := w.addRepo(
		repo.Config{Key: "flat2", Class: repo.Local, RepoLayoutRef: "simple-default"},
	)
	res = w.transfer(Transfer{
		From: Target{Repo: gomod}, To: Target{Repo: again}, TranslateLayouts: true, FailFast: true,
	})
	if res.Files != 0 || res.Failed != 1 {
		t.Fatalf("failFast: %+v", res)
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
			SetProperties(ctx, one.ID, types.Properties{"k": {"v"}}, time.Now()); err != nil {
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

// Env.Move moves one file as Transfer does, says why a file in the way, a
// folder in the way or the target's refusal kept it where it was, and
// fails as Transfer fails.
func TestEnvMove(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	move := w.svc.env().Move
	w.must("a.txt", "a")
	w.must("taken.txt", "t")
	w.must("dir/x.txt", "x")
	if why, err := move(ctx, w.loc, "a.txt", "to/a.txt", "mover"); err != nil || why != "" {
		t.Fatalf("move: %q %v", why, err)
	}
	if _, ok := w.get(w.loc, "to/a.txt"); !ok {
		t.Fatal("not moved")
	}
	for _, to := range []string{"taken.txt", "dir"} {
		why, err := move(ctx, w.loc, "to/a.txt", to, "mover")
		if err != nil || why != "libs:"+to+" is taken" {
			t.Fatalf("onto %s: %q %v", to, why, err)
		}
	}
	excluded := "**/*.txt"
	picky := w.addRepo(repo.Config{Key: "picky", Class: repo.Local, ExcludesPattern: &excluded})
	if _, err := w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: picky, Path: "a.bin"}, Body: strings.NewReader("b"),
	}); err != nil {
		t.Fatal(err)
	}
	if why, err := move(ctx, picky, "a.bin", "a.txt", "mover"); err != nil ||
		!strings.Contains(why, "Cannot transfer") {
		t.Fatalf("refused: %q %v", why, err)
	}
	if _, err := move(ctx, w.loc, "absent.txt", "b.txt", "mover"); !errors.Is(err, ErrNoSource) {
		t.Fatalf("no source: %v", err)
	}
	w.svc.Store = faulty{w.db, "get:b.txt"}
	if _, err := move(ctx, w.loc, "taken.txt", "b.txt", "mover"); !errors.Is(err, errBoom) {
		t.Fatalf("store failure: %v", err)
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
