package item

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// keeper keeps a signature and an armoured signature beside every file
// ending .bin, as Helm keeps a chart's .prov.
type keeper struct{ bare }

func (keeper) Sidecars(_ repo.Config, p string) []string {
	if strings.HasSuffix(p, ".bin") {
		return []string{p + ".sig", p + ".asc"}
	}
	return nil
}

// keeping is a world whose repositories keep sidecars.
func keeping(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.svc.Formats = func(repo.PackageType) format.Handler { return keeper{} }
	return w
}

// has checks which of paths c holds as files.
func (w *world) has(c repo.Config, want bool, paths ...string) {
	w.t.Helper()
	for _, p := range paths {
		if it, ok := w.get(c, p); ok != want || (ok && it.Folder) {
			w.t.Errorf("%s holds %s: %v, want %v", c.Key, p, ok, want)
		}
	}
}

func TestUnitSidecarsNamed(t *testing.T) {
	w := keeping(t)
	if got := w.svc.Sidecars(
		w.loc,
		"a/x.bin",
	); !slices.Equal(
		got,
		[]string{"a/x.bin.sig", "a/x.bin.asc"},
	) {
		t.Errorf("a keeping format's sidecars: %v", got)
	}
	w.svc.Formats = func(repo.PackageType) format.Handler { return bare{} }
	if got := w.svc.Sidecars(w.loc, "a/x.bin"); got != nil {
		t.Errorf("a format that keeps none: %v", got)
	}
}

// A delete takes a file's sidecars along, into the trash can with it; a
// sidecar deleted alone, a missing one, a folder at a sidecar's path and a
// sidecar the delete names anyway change nothing else.
func TestUnitSidecarsFollowADelete(t *testing.T) {
	ctx := context.Background()
	w := keeping(t)
	w.must("a/x.bin", "x")
	w.must("a/x.bin.sig", "x signed")
	w.must("a/y.txt", "y")
	res, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "a/x.bin"}, "carol")
	if err != nil || res.Files != 2 {
		t.Fatalf("delete %+v %v", res, err)
	}
	w.has(w.loc, false, "a/x.bin", "a/x.bin.sig")
	w.has(w.loc, true, "a/y.txt")
	trashed, err := w.db.Trash().Find(ctx, "libs", "a/x.bin.sig")
	if err != nil || len(trashed) != 1 || trashed[0].DeletedBy != "carol" ||
		!trashed[0].DeletedAt.Equal(at) {
		t.Fatalf("the sidecar's trash entry: %+v %v", trashed, err)
	}
	if w.refs(sums("x signed").SHA256) != 1 {
		t.Error("the trashed sidecar lost its content")
	}

	w.must("a/x.bin", "x")
	w.must("a/x.bin.sig", "x signed")
	if _, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "a/x.bin.sig"}, "carol"); err != nil {
		t.Fatal(err)
	}
	w.has(w.loc, true, "a/x.bin")

	if _, err := w.svc.MakeFolder(
		ctx,
		Target{Repo: w.loc, Path: "a/x.bin.asc"},
		nil,
		"",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "a/x.bin"}, "carol"); err != nil {
		t.Fatal(err)
	}
	if it, ok := w.get(w.loc, "a/x.bin.asc"); !ok || !it.Folder {
		t.Error("a folder at a sidecar's path went with the file")
	}

	w.must("a/x.bin", "x")
	w.must("a/x.bin.sig", "x signed")
	res, err = w.svc.Delete(ctx, Target{Repo: w.loc, Path: "a"}, "carol")
	if err != nil || res.Files != 3 {
		t.Fatalf("a folder's delete names its sidecars once: %+v %v", res, err)
	}
}

// Past the trash can and in a remote's cache, a sidecar's content is
// released with its file's.
func TestUnitSidecarsFollowAPurge(t *testing.T) {
	ctx := context.Background()
	w := keeping(t)
	for _, cached := range []bool{false, true} {
		for _, p := range []string{"x.bin", "x.bin.sig", "x.bin.asc"} {
			if _, err := w.svc.Deploy(ctx, Deploy{
				Target: Target{Repo: w.loc, Cached: cached, Path: p},
				Body:   strings.NewReader(p + " content"),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	res, err := w.svc.Delete(ctx, Target{Repo: w.loc, Cached: true, Path: "x.bin"}, "")
	if err != nil || res.Files != 3 {
		t.Fatalf("delete from the cache %+v %v", res, err)
	}
	if res.BlobRefs[sums("x.bin.asc content").SHA256] != 1 {
		t.Errorf("the cache's delete released %v", res.BlobRefs)
	}
	if _, err := w.svc.Purge(ctx, Target{Repo: w.loc, Path: "x.bin"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"x.bin", "x.bin.sig", "x.bin.asc"} {
		if n := w.refs(sums(p + " content").SHA256); n != 0 {
			t.Errorf("%s keeps %d references", p, n)
		}
	}
}

// A copy or move takes a file's sidecars to the sidecar paths beside its
// target, counted as files; a sidecar the transfer names itself is carried
// once; and a target that owns no sidecar takes none.
func TestUnitSidecarsFollowATransfer(t *testing.T) {
	w := keeping(t)
	other := w.addRepo(repo.Config{Key: "other", Class: repo.Local})
	w.must("a/x.bin", "x")
	w.must("a/x.bin.sig", "x signed")
	res := w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "a/x.bin"}, To: Target{Repo: other, Path: "b/y.bin"},
	})
	if res.Files != 2 || res.Failed != 0 {
		t.Fatalf("copy %+v", res)
	}
	w.has(other, true, "b/y.bin", "b/y.bin.sig")
	w.has(w.loc, true, "a/x.bin", "a/x.bin.sig")
	if sig, _ := w.get(other, "b/y.bin.sig"); sig.Actual.SHA256 != sums("x signed").SHA256 {
		t.Error("the copied sidecar is not the source's")
	}

	if res := w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "a/x.bin"}, To: Target{Repo: w.loc, Path: "c/"},
		IntoFolder: true, Move: true,
	}); res.Files != 2 {
		t.Fatalf("move %+v", res)
	}
	w.has(w.loc, false, "a/x.bin", "a/x.bin.sig")
	w.has(w.loc, true, "c/x.bin", "c/x.bin.sig")

	if res := w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "c"}, To: Target{Repo: other, Path: "d"},
	}); res.Files != 2 || res.Failed != 0 {
		t.Fatalf("a folder's copy carries its sidecars once: %+v", res)
	}
	w.has(other, true, "d/x.bin", "d/x.bin.sig")

	if res := w.transfer(Transfer{
		From: Target{Repo: w.loc, Path: "c/x.bin"}, To: Target{Repo: other, Path: "e/x.txt"},
	}); res.Files != 1 {
		t.Fatalf("a copy to a name that owns no sidecar: %+v", res)
	}
	w.has(other, false, "e/x.txt.sig")
}

// A file whose sidecar cannot land does not go either, and both are
// reported; a dry run counts the sidecars it would carry.
func TestUnitSidecarsHoldTheirOwnerBack(t *testing.T) {
	w := keeping(t)
	other := w.addRepo(repo.Config{Key: "other", Class: repo.Local})
	w.must("a/x.bin", "x")
	w.must("a/x.bin.sig", "x signed")
	if res := w.transfer(Transfer{
		From: Target{
			Repo: w.loc,
			Path: "a/x.bin",
		},
		To:  Target{Repo: other, Path: "x.bin"},
		Dry: true,
	}); res.Files != 2 {
		t.Fatalf("dry run %+v", res)
	}
	w.has(other, false, "x.bin", "x.bin.sig")

	// The target holds another signature, which the caller may not
	// overwrite.
	if _, err := w.svc.Deploy(context.Background(), Deploy{
		Target: Target{Repo: other, Path: "x.bin.sig"}, Body: strings.NewReader("another"),
	}); err != nil {
		t.Fatal(err)
	}
	for _, failFast := range []bool{false, true} {
		res := w.transfer(Transfer{
			From: Target{Repo: w.loc, Path: "a/x.bin"}, To: Target{Repo: other, Path: "x.bin"},
			Move: true, FailFast: failFast,
		})
		if res.Files != 0 || res.Failed != 2 || len(res.Messages) != 3 ||
			!strings.Contains(res.Messages[0].Text, "doesn't have permissions to override") ||
			!strings.Contains(res.Messages[1].Text, "its sidecar cannot go with it") {
			t.Fatalf("fail fast %v: %+v", failFast, res)
		}
		w.has(other, false, "x.bin")
		w.has(w.loc, true, "a/x.bin", "a/x.bin.sig")
	}
}

// A store failure while finding or deleting a sidecar fails the write.
func TestUnitSidecarStoreFailures(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		fail string
		run  func(*world) error
	}{
		{"get:x.bin.sig", func(w *world) error {
			_, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "x.bin"}, "")
			return err
		}},
		{"delete:x.bin.sig", func(w *world) error {
			_, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "x.bin"}, "")
			return err
		}},
		{"get:x.bin.sig", func(w *world) error {
			_, err := w.svc.Transfer(ctx, Transfer{
				From: Target{Repo: w.loc, Path: "x.bin"}, To: Target{Repo: w.loc, Path: "y.bin"},
			})
			return err
		}},
		{"get:y.bin.sig", func(w *world) error {
			_, err := w.svc.Transfer(ctx, Transfer{
				From: Target{Repo: w.loc, Path: "x.bin"}, To: Target{Repo: w.loc, Path: "y.bin"},
			})
			return err
		}},
	} {
		w := keeping(t)
		w.must("x.bin", "x")
		w.must("x.bin.sig", "x signed")
		w.svc.Store = faulty{w.db, c.fail}
		if err := c.run(w); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", c.fail, err)
		}
		w.svc.Store = w.db
		w.has(w.loc, true, "x.bin", "x.bin.sig")
		if entries, _, err := w.db.Trash().List(ctx, "libs", "", store.Page{}); err != nil ||
			len(entries) != 0 {
			t.Errorf("%s: a failed delete trashed %v %v", c.fail, entries, err)
		}
	}
}

// The sidecars' releases add to the delete's own, which may have none.
func TestUnitDeleteSidecarsAddsReleases(t *testing.T) {
	ctx := context.Background()
	w := keeping(t)
	sc := w.must("x.bin.sig", "x signed")
	res, err := w.svc.deleteSidecars(
		ctx,
		Target{Repo: w.loc},
		[]types.Item{sc},
		store.DeleteResult{},
	)
	if err != nil || res.Files != 1 || res.BlobRefs[sums("x signed").SHA256] != 1 {
		t.Errorf("released %+v %v", res, err)
	}
}

// SidecarsBeside names the stored sidecars a delete, copy or move of a file
// changes, with where each lands beside the file's target, so a caller can
// check its rights on them: none for a folder, a missing file, a format
// that keeps none, or a target that keeps no such sidecar.
func TestUnitSidecarsBeside(t *testing.T) {
	ctx := context.Background()
	w := keeping(t)
	w.must("a/x.bin", "x")
	w.must("a/x.bin.sig", "x signed")
	at := func(p, to string) []SidecarMove {
		t.Helper()
		got, err := w.svc.SidecarsBeside(ctx, Target{Repo: w.loc, Path: p}, to)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := at(
		"/a/x.bin",
		"b/y.bin",
	); !slices.Equal(
		got,
		[]SidecarMove{{"a/x.bin.sig", "b/y.bin.sig"}},
	) {
		t.Errorf("a file: %v", got)
	}
	if got := at(
		"a/x.bin",
		"a/x.bin",
	); !slices.Equal(
		got,
		[]SidecarMove{{"a/x.bin.sig", "a/x.bin.sig"}},
	) {
		t.Errorf("a delete: %v", got)
	}
	for p, to := range map[string]string{"a": "b", "a/none.bin": "b/none.bin", "a/x.bin": "b/y.txt"} {
		if got := at(p, to); got != nil {
			t.Errorf("%s to %s: %v", p, to, got)
		}
	}
	for _, fail := range []string{"get:a/x.bin", "get:a/x.bin.sig"} {
		w.svc.Store = faulty{w.db, fail}
		if _, err := w.svc.SidecarsBeside(
			ctx,
			Target{Repo: w.loc, Path: "a/x.bin"},
			"b/y.bin",
		); !errors.Is(
			err,
			errBoom,
		) {
			t.Errorf("%s: %v", fail, err)
		}
	}
	w.svc.Store = w.db
	w.svc.Formats = func(repo.PackageType) format.Handler { return bare{} }
	if got := at("a/x.bin", "b/y.bin"); got != nil {
		t.Errorf("a format that keeps none: %v", got)
	}
}
