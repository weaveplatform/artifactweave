package pkgmove

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// minimalLedger is a ledger that validates, with one pending move of
// internal/a to migration/platform/a.
const minimalLedger = `{"schema":1,"module":"github.com/weaveplatform/artifactweave",` +
	`"staging_root":"migration","final_root":"internal",` +
	`"packages":[{"from":"internal/a","action":"move","to":"platform/a","ticket":"PLT-01"}],` +
	`"files":[],"placeholders":[]}`

// writeTree writes files, slash-separated paths to contents, below a new
// temporary directory and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// moduleTree returns a tree with this repository's module, the minimal
// ledger and files.
func moduleTree(t *testing.T, files map[string]string) string {
	t.Helper()
	all := map[string]string{
		"go.mod":                "module github.com/weaveplatform/artifactweave\n",
		"migration/ledger.json": minimalLedger,
	}
	for name, content := range files {
		all[name] = content
	}
	return writeTree(t, all)
}

// noGit makes every git command of the test fail, as outside a repository,
// whatever directory the test runs in.
func noGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "no-repository"))
}

func TestUnitMainPrintsTheUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main([]string{"help"}, &stdout, &stderr)
	if code != exitOK || !strings.HasPrefix(stdout.String(), "usage:") {
		t.Errorf("help: exit %d, stdout %q", code, stdout.String())
	}
	stdout.Reset()
	code = Main(nil, &stdout, &stderr)
	if code != exitUsage || !strings.Contains(stderr.String(), "no command") {
		t.Errorf("no command: exit %d, stderr %q", code, stderr.String())
	}
}

func TestUnitExecRunnerRunsWithoutTheWorkspace(t *testing.T) {
	var out bytes.Buffer
	run := execRunner(t.Context(), &out)
	if err := run(t.TempDir(), "go", "env", "GOWORK"); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "off" {
		t.Errorf("GOWORK is %q, want off", got)
	}
	err := run(t.TempDir(), "go", "no-such-command")
	if err == nil || !strings.Contains(err.Error(), "go no-such-command") {
		t.Errorf("a failing command returned %v", err)
	}
}

func TestUnitGitErrorsNameTheCommand(t *testing.T) {
	noGit(t)
	g := git{ctx: t.Context(), root: t.TempDir()}
	_, err := g.list("ls-files", "-z")
	if err == nil || !strings.HasPrefix(err.Error(), "git ls-files -z: ") {
		t.Errorf("list: %v", err)
	}
	if _, err := g.grep([]string{"x"}, nil); err == nil {
		t.Error("grep outside a repository succeeded")
	}
	if _, err := g.grepFiles([]string{"x"}); err == nil {
		t.Error("grepFiles outside a repository succeeded")
	}
}

func TestUnitGrepFilesFindsNothingInAnEmptyRepository(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", root).
		CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	files, err := git{ctx: t.Context(), root: root}.grepFiles([]string{"internal/a"})
	if err != nil || files != nil {
		t.Errorf("grepFiles = %q, %v; want nothing", files, err)
	}
}

func TestUnitParseGrepRejectsMalformedOutput(t *testing.T) {
	lines, err := parseGrep([]byte("a.md\x003\x00text\n"))
	if err != nil || len(lines) != 1 || lines[0] != (grepLine{"a.md", 3, "text"}) {
		t.Errorf("parseGrep = %v, %v", lines, err)
	}
	if _, err := parseGrep([]byte("a.md:3:text\n")); !errors.Is(err, errUnexpected) {
		t.Errorf("malformed output: %v", err)
	}
}

func TestUnitOpenTreeRefusesWhatItCannotRead(t *testing.T) {
	for name, files := range map[string]map[string]string{
		// go.mod as a directory is found by the root search, then cannot
		// be read.
		"go.mod unreadable": {"go.mod/x": "", "migration/ledger.json": minimalLedger},
		"no ledger":         {"go.mod": "module github.com/weaveplatform/artifactweave\n"},
		// The status derivation reads the target directory.
		"target is a file": {
			"go.mod":                "module github.com/weaveplatform/artifactweave\n",
			"migration/ledger.json": minimalLedger,
			"migration/platform/a":  "a file",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := openTree(t.Context(), writeTree(t, files)); err == nil {
				t.Error("openTree succeeded")
			}
		})
	}
	t.Run("no module", func(t *testing.T) {
		// The temporary directory normally has no go.mod above it; if it
		// has, the search finds one without a ledger, which fails too.
		if _, err := openTree(t.Context(), t.TempDir()); err == nil {
			t.Error("openTree succeeded")
		}
	})
}

func TestUnitCheckTreeDerivesTheStatusAgain(t *testing.T) {
	root := moduleTree(t, map[string]string{"internal/a/a.go": "package a\n"})
	tr, err := openTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	platform := filepath.Join(root, "migration", "platform")
	if err := os.MkdirAll(platform, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platform, "a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e := &env{ctx: t.Context(), dir: root, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	if err := e.checkTree(tr, "", ""); err == nil {
		t.Error("checkTree succeeded on a tree whose status cannot be derived")
	}
}

func TestUnitReadErrorsStopAChange(t *testing.T) {
	tr := &tree{ctx: t.Context(), root: t.TempDir()}
	if _, err := tr.packageName([]string{"internal/a/missing.go"}); err == nil {
		t.Error("packageName read a missing file")
	}
	r := &rewriter{mt: newMatcher("example.com/m", nil, never, never)}
	identity := func(p string) string { return p }
	_, _, err := tr.rewriteAll(r, []string{"missing.md"}, identity, mappingDirs(nil))
	if err == nil {
		t.Error("rewriteAll read a missing file")
	}
}

func TestUnitLoadTypedFailsOnABrokenModule(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":  "module example.com/broken\n\nrequire (\n",
		"a/a.go":  "package a\n",
		"b/b.txt": "",
	})
	tr := &tree{ctx: t.Context(), root: root}
	if _, err := tr.loadTyped([]string{"a"}); err == nil {
		t.Error("loadTyped succeeded on a module whose go.mod does not parse")
	}
}

func TestUnitFmtReportsAWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only file")
	}
	root := moduleTree(t, nil)
	ledgerFile := filepath.Join(root, "migration", "ledger.json")
	if err := os.Chmod(ledgerFile, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ledgerFile, 0o600) })
	e := &env{ctx: t.Context(), dir: root, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	err := e.fmtLedger(nil)
	if err == nil || !strings.HasPrefix(err.Error(), "write the ledger: ") {
		t.Errorf("fmt of a read-only ledger: %v", err)
	}
}

// failingWriter fails every write.
type failingWriter struct{}

var errWrite = errors.New("write failed")

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

func TestUnitWriteJSONReportsAWriteFailure(t *testing.T) {
	if err := writeJSON(failingWriter{}, []statusRow{}); !errors.Is(err, errWrite) {
		t.Errorf("writeJSON = %v", err)
	}
}

func TestUnitDropDuplicateImports(t *testing.T) {
	for _, c := range []struct {
		name, src, want string
		n               int
	}{
		{"does not parse", "not go", "not go", 0},
		{
			"block",
			"package p\n\nimport (\n\t\"a/b\"\n\tx \"a/b\"\n\t\"a/b\"\n\tx \"a/b\"\n)\n",
			"package p\n\nimport (\n\t\"a/b\"\n\tx \"a/b\"\n)\n",
			2,
		},
		{
			"declarations without parentheses, the last at the end of the source",
			"package p\n\nimport \"a/b\"\nimport \"a/b\"",
			"package p\n\nimport \"a/b\"\n",
			1,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, n := dropDuplicateImports([]byte(c.src))
			if string(got) != c.want || n != c.n {
				t.Errorf("got %q, %d; want %q, %d", got, n, c.want, c.n)
			}
		})
	}
}

func TestUnitEditCountsName(t *testing.T) {
	if s := (editCounts{}).String(); s != "" {
		t.Errorf("no edits: %q", s)
	}
	got := editCounts{paths: 2, imports: 1, duplicates: 3}.String()
	if want := "1 import, 3 removed repeated imports, 2 paths"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUnitGlobs(t *testing.T) {
	for _, c := range []struct {
		glob, path string
		want       bool
	}{
		{"api/src/**/*.yaml", "api/src/a/b/c.yaml", true},
		{"api/src/**/*.yaml", "api/src/c.yaml", true},
		{"api/src/**/*.yaml", "api/c.yaml", false},
		{"**/migrations/*.sql", "migrations/1.sql", true},
		{"**/migrations/*.sql", "a/migrations/b/1.sql", false},
		{"docs/research/evidence/**", "docs/research/evidence/a/b.md", true},
		{"[", "[", false},
	} {
		if got := matchAny([]string{c.glob}, c.path); got != c.want {
			t.Errorf("matchAny(%q, %q) = %v", c.glob, c.path, got)
		}
	}
}
