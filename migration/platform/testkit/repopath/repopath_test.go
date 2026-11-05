package repopath

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// recorder stands in for testing.TB where a test expects the code under test
// to fail it: Fatal and Fatalf record the message and end the goroutine, as
// the real ones do, instead of failing the surrounding test.
type recorder struct {
	testing.TB
	msg string
}

func (r *recorder) Fatal(args ...any) {
	r.msg = fmt.Sprint(args...)
	runtime.Goexit()
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// fatalOf runs fn on its own goroutine, because Fatal ends the goroutine
// that calls it, and returns the message fn failed with ("" when it did not).
func fatalOf(t *testing.T, fn func(tb testing.TB)) string {
	t.Helper()
	tb := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(tb)
	}()
	<-done
	return tb.msg
}

func write(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, name := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnitFindRootStopsAtNearestModule(t *testing.T) {
	tmp := t.TempDir()
	write(t, tmp, "go.mod", "a/go.mod", "a/b/c/c.go", "d/d.go")

	for _, tc := range []struct{ from, want string }{
		{"a/b/c", "a"},
		{"a", "a"},
		{"d", "."},
		{".", "."},
	} {
		got, err := FindRoot(filepath.Join(tmp, filepath.FromSlash(tc.from)))
		if want := filepath.Join(tmp, filepath.FromSlash(tc.want)); err != nil || got != want {
			t.Errorf("FindRoot(%s) = %q, %v; want %q", tc.from, got, err, want)
		}
	}

	// A relative directory resolves against the working directory, which go
	// test sets to this package, so the search climbs to the repository.
	got, err := FindRoot(".")
	if err != nil || got != Root(t) {
		t.Errorf("FindRoot(.) = %q, %v; want the repository root %q", got, err, Root(t))
	}

	if got, err := FindRoot(t.TempDir()); !errors.Is(err, ErrNoModule) {
		t.Errorf("FindRoot outside any module = %q, %v; want ErrNoModule", got, err)
	}
}

func TestUnitRootFailsOutsideAModule(t *testing.T) {
	root := Root(t)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("Root = %q, which holds no go.mod: %v", root, err)
	}

	t.Chdir(t.TempDir())
	msg := fatalOf(t, func(tb testing.TB) { Root(tb) })
	if !strings.Contains(msg, ErrNoModule.Error()) {
		t.Errorf("Root outside any module failed with %q; want %q", msg, ErrNoModule)
	}
}

func TestUnitPathAndReadAreRootRelative(t *testing.T) {
	root := Root(t)
	if got, want := Path(t, "go.mod"), filepath.Join(root, "go.mod"); got != want {
		t.Errorf("Path(go.mod) = %q; want %q", got, want)
	}
	if got, want := Path(t, "migration/platform/testkit/repopath/doc.go"),
		filepath.Join(root, "migration", "platform", "testkit", "repopath", "doc.go"); got != want {
		t.Errorf("Path of this package's doc.go = %q; want %q", got, want)
	}
	want, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	got := Read(t, "go.mod")
	if !bytes.Equal(got, want) || !bytes.HasPrefix(got, []byte("module ")) {
		t.Errorf("Read(go.mod) = %q; want the module file", got)
	}

	for _, rel := range []string{"", "/go.mod", "../go.mod", "api/../go.mod", "./go.mod", "api//x"} {
		msg := fatalOf(t, func(tb testing.TB) { Path(tb, rel) })
		if !strings.Contains(msg, "not a slash-separated path") {
			t.Errorf("Path(%q) failed with %q; want a rejection", rel, msg)
		}
	}
	msg := fatalOf(t, func(tb testing.TB) { Read(tb, "migration/no-such-file") })
	if !strings.Contains(msg, "no-such-file") {
		t.Errorf("Read of a missing file failed with %q; want the read error", msg)
	}
	if msg := fatalOf(t, func(tb testing.TB) { Read(tb, "go.mod") }); msg != "" {
		t.Errorf("Read(go.mod) failed with %q", msg)
	}
}

func TestUnitGoDirsSkipsDotDirsTestdataAndNestedModules(t *testing.T) {
	// The root's own name starts with a dot, so the test also proves the
	// root is never skipped.
	root := filepath.Join(t.TempDir(), ".checkout")
	write(t, root,
		"go.mod",
		"main.go",
		"a/a.go",
		"a/b/b_test.go",
		"a/testdata/fixture.go",
		"a/notes.md",
		".worktrees/x/go.mod",
		".worktrees/x/c/c.go",
		".git/hooks/hook.go",
		"nested/go.mod",
		"nested/n.go",
		"nested/deeper/d.go",
		"vendor/v/v.go",
		"node_modules/m/m.go",
		"docs/readme.md",
	)
	got, err := GoDirs(root)
	if want := []string{".", "a", "a/b"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("GoDirs = %q, %v; want %q", got, err, want)
	}

	if _, err := GoDirs(filepath.Join(root, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("GoDirs of a missing root = %v; want a not-exist error", err)
	}

	// The rule is about directories: a file whose name starts with a dot is
	// never skipped.
	write(t, root, ".hidden")
	hidden := filepath.Join(root, ".hidden")
	info, err := os.Stat(hidden)
	if err != nil {
		t.Fatal(err)
	}
	if SkipDir(root, hidden, fs.FileInfoToDirEntry(info)) {
		t.Error("SkipDir skipped a file")
	}
}
