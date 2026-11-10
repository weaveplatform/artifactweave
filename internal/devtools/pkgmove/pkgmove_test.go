package pkgmove

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/txtar"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// errFakeRun is what the fake runner returns for a command a script told it
// to fail.
var errFakeRun = errors.New("fake runner failure")

// TestMain runs this test binary as git when a script put a copy of it on
// PATH under that name (failgit).
func TestMain(m *testing.M) {
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") == "git" {
		os.Exit(fakeGit(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeGit fails a git command whose arguments, joined by spaces, match
// $PKGMOVE_GIT_FAIL once $PKGMOVE_GIT_PASS matching commands have passed,
// counting in the file $PKGMOVE_GIT_COUNT, and runs every other command with
// the real git, $PKGMOVE_GIT.
func fakeGit(args []string) int {
	if regexp.MustCompile(os.Getenv("PKGMOVE_GIT_FAIL")).MatchString(strings.Join(args, " ")) {
		count := os.Getenv("PKGMOVE_GIT_COUNT")
		data, _ := os.ReadFile(count)
		n, _ := strconv.Atoi(string(data))
		pass, _ := strconv.Atoi(os.Getenv("PKGMOVE_GIT_PASS"))
		if err := os.WriteFile(count, []byte(strconv.Itoa(n+1)), 0o600); err != nil || n >= pass {
			fmt.Fprintln(os.Stderr, "fake git failure")
			return 128
		}
	}
	cmd := exec.Command(os.Getenv("PKGMOVE_GIT"), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	var exit *exec.ExitError
	switch err := cmd.Run(); {
	case errors.As(err, &exit):
		return exit.ExitCode()
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		return 128
	}
	return 0
}

// TestUnitScripts runs each testdata/*.txtar archive as a repository with a
// script. The archive's comment is the script, one command per line; a line
// starting with "#" is a comment:
//
//	ok|fail|usage ARGS...      run pkgmove ARGS, expecting exit status 0, 1 or 2
//	[!] stdout|stderr RE       the last command's output matches RE, or not
//	cmpout want/NAME           the last command's stdout is want/NAME exactly
//	cmp PATH want/NAME         the repository file PATH is want/NAME exactly
//	[!] exists PATH...         the paths exist, or none does
//	[!] grep PATH RE           the file PATH matches RE, or not
//	runs [CMD[; CMD]...]       the regeneration commands the last pkgmove ran
//	failrun [CMD]              the fake runner fails CMD from now on, or
//	                           no command again
//	failgit [N] [RE]           git fails each command whose arguments, joined
//	                           by spaces, match RE, after N such commands
//	                           have passed; without RE, git works again
//	[!] git|go ARGS...         run git or go in the repository, expecting
//	                           success or failure; the output becomes stdout
//	commit                     commit every change, untracked files included
//	cp want/NAME PATH          write want/NAME to PATH
//	rm PATH                    remove the file PATH
//	cd DIR                     run pkgmove from DIR, relative to the root
//
// Files under want/ stay in the archive. Every other file is written to the
// repository, go.mod with $GOVERSION replaced by the Go version of this
// repository's go.mod, and committed before the script runs. git runs with
// an empty global configuration and no system configuration, and the go
// command with GOWORK=off and GOPROXY=off, so the user's settings never
// change a result. The runner is fake: it records the regeneration commands
// and runs none.
func TestUnitScripts(t *testing.T) {
	setFixtureEnv(t)
	version := goVersion(t)
	archives, err := filepath.Glob(
		filepath.Join(repopath.Path(t, "internal/devtools/pkgmove/testdata"), "*.txtar"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(archives) == 0 {
		t.Fatal("no testdata archives")
	}
	for _, archive := range archives {
		t.Run(strings.TrimSuffix(filepath.Base(archive), ".txtar"), func(t *testing.T) {
			newScript(t, archive, version).run()
		})
	}
}

// setFixtureEnv isolates git and the go command from the user's
// configuration for the rest of the test.
func setFixtureEnv(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"GIT_CONFIG_GLOBAL":   empty,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "pkgmove test",
		"GIT_AUTHOR_EMAIL":    "pkgmove@example.com",
		"GIT_COMMITTER_NAME":  "pkgmove test",
		"GIT_COMMITTER_EMAIL": "pkgmove@example.com",
		"GOWORK":              "off",
		"GOPROXY":             "off",
		"GOFLAGS":             "",
		"GOTOOLCHAIN":         "local",
	} {
		t.Setenv(key, value)
	}
}

// goVersion returns the go directive of this repository's go.mod, which every
// fixture go.mod states, so a fixture builds with the toolchain the
// repository needs.
func goVersion(t *testing.T) string {
	t.Helper()
	f, err := modfile.ParseLax("go.mod", repopath.Read(t, "go.mod"), nil)
	if err != nil || f.Go == nil {
		t.Fatalf("read the go directive: %v", err)
	}
	return f.Go.Version
}

// script is one archive being run.
type script struct {
	t       *testing.T
	archive string
	text    string
	root    string
	cwd     string
	want    map[string][]byte
	stdout  string
	stderr  string
	runs    []string
	failing map[string]bool
	// line is the script line being run, for messages.
	line int
	// gitDir holds the copy of this test binary that failgit runs as git.
	gitDir string
	// pathEnv is PATH without gitDir.
	pathEnv string
}

func newScript(t *testing.T, archive, version string) *script {
	t.Helper()
	ar, err := txtar.ParseFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	s := &script{
		t:       t,
		archive: filepath.Base(archive),
		text:    string(ar.Comment),
		root:    t.TempDir(),
		want:    map[string][]byte{},
		failing: map[string]bool{},
	}
	for _, f := range ar.Files {
		data := f.Data
		switch {
		case strings.HasPrefix(f.Name, "want/"):
			s.want[f.Name] = data
			continue
		case f.Name == "go.mod":
			data = bytes.ReplaceAll(data, []byte("$GOVERSION"), []byte(version))
		}
		s.write(f.Name, data)
	}
	s.gitRun("-c", "init.defaultBranch=main", "init", "-q")
	s.commit()
	return s
}

func (s *script) run() {
	for i, line := range strings.Split(s.text, "\n") {
		s.line = i + 1
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s.exec(line)
	}
}

func (s *script) fatalf(format string, args ...any) {
	s.t.Helper()
	s.t.Fatalf("%s:%d: %s", s.archive, s.line, fmt.Sprintf(format, args...))
}

func (s *script) exec(line string) {
	s.t.Helper()
	rest, negate := strings.CutPrefix(line, "! ")
	cmd, arg, _ := strings.Cut(rest, " ")
	switch cmd {
	case "ok", "fail", "usage":
		s.pkgmove(
			map[string]int{"ok": exitOK, "fail": exitProblems, "usage": exitUsage}[cmd],
			strings.Fields(arg),
		)
	case "stdout":
		s.match("stdout", s.stdout, arg, negate)
	case "stderr":
		s.match("stderr", s.stderr, arg, negate)
	case "cmpout":
		s.compare("stdout", s.stdout, arg)
	case "cmp":
		file, want, _ := strings.Cut(arg, " ")
		s.compare(file, string(s.read(file)), want)
	case "exists":
		s.exists(strings.Fields(arg), negate)
	case "grep":
		file, re, _ := strings.Cut(arg, " ")
		s.match(file, string(s.read(file)), re, negate)
	case "runs":
		s.checkRuns(arg)
	case "failgit":
		s.failGit(arg)
	case "failrun":
		if arg == "" {
			clear(s.failing)
			return
		}
		s.failing[arg] = true
	case "git", "go":
		s.command(negate, cmd, strings.Fields(arg)...)
	case "commit":
		s.commit()
	case "cp":
		from, to, _ := strings.Cut(arg, " ")
		data, ok := s.want[from]
		if !ok {
			s.fatalf("no archive file %s", from)
		}
		s.write(to, data)
	case "rm":
		if err := os.Remove(s.path(arg)); err != nil {
			s.fatalf("%v", err)
		}
	case "cd":
		s.cwd = arg
	default:
		s.fatalf("unknown command %q", cmd)
	}
}

// failGit makes git fail as failgit describes, by putting a copy of this test
// binary first on PATH under the name git; TestMain then runs it as
// fakeGit.
func (s *script) failGit(arg string) {
	s.t.Helper()
	if s.gitDir == "" {
		real, err := exec.LookPath("git")
		if err != nil {
			s.fatalf("%v", err)
		}
		exe, err := os.Executable()
		if err != nil {
			s.fatalf("%v", err)
		}
		s.gitDir, s.pathEnv = s.t.TempDir(), os.Getenv("PATH")
		name := filepath.Join(s.gitDir, "git")
		linked := false
		if runtime.GOOS == "windows" {
			// Windows cannot delete a link to the running test binary, so
			// the temporary directory's cleanup needs a copy.
			name += ".exe"
		} else {
			linked = os.Link(exe, name) == nil
		}
		if !linked {
			data, err := os.ReadFile(exe)
			if err == nil {
				err = os.WriteFile(name, data, 0o700)
			}
			if err != nil {
				s.fatalf("%v", err)
			}
		}
		s.t.Setenv("PKGMOVE_GIT", real)
		// Under -race every exit of the copy, one per git command, would
		// otherwise sleep a second for late race reports.
		s.t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	}
	if arg == "" {
		s.t.Setenv("PATH", s.pathEnv)
		return
	}
	pass := "0"
	if n, re, ok := strings.Cut(arg, " "); ok {
		if _, err := strconv.Atoi(n); err == nil {
			pass, arg = n, re
		}
	}
	s.t.Setenv("PKGMOVE_GIT_FAIL", arg)
	s.t.Setenv("PKGMOVE_GIT_PASS", pass)
	s.t.Setenv("PKGMOVE_GIT_COUNT", filepath.Join(s.gitDir, fmt.Sprintf("count-%d", s.line)))
	s.t.Setenv("PATH", s.gitDir+string(os.PathListSeparator)+s.pathEnv)
}

// pkgmove runs pkgmove with args from the script's directory, with the fake
// runner, and checks its exit status.
func (s *script) pkgmove(want int, args []string) {
	s.t.Helper()
	var stdout, stderr bytes.Buffer
	s.runs = nil
	e := &env{
		ctx:    s.t.Context(),
		dir:    s.path(s.cwd),
		stdout: &stdout,
		stderr: &stderr,
		run: func(root string, args ...string) error {
			if root != s.root {
				s.fatalf("the runner ran in %s, not the root %s", root, s.root)
			}
			cmd := strings.Join(args, " ")
			s.runs = append(s.runs, cmd)
			if s.failing[cmd] {
				return fmt.Errorf("%w: %s", errFakeRun, cmd)
			}
			return nil
		},
	}
	got := e.main(args)
	s.stdout, s.stderr = stdout.String(), stderr.String()
	if got != want {
		s.fatalf("pkgmove %s: exit status %d, want %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), got, want, s.stdout, s.stderr)
	}
}

func (s *script) match(what, text, expr string, negate bool) {
	s.t.Helper()
	re, err := regexp.Compile("(?m)" + expr)
	if err != nil {
		s.fatalf("%v", err)
	}
	if re.MatchString(text) == negate {
		verb := "does not match"
		if negate {
			verb = "matches"
		}
		s.fatalf("%s %s %q:\n%s", what, verb, expr, text)
	}
}

func (s *script) compare(what, got, wantFile string) {
	s.t.Helper()
	want, ok := s.want[wantFile]
	if !ok {
		s.fatalf("no archive file %s", wantFile)
	}
	if got != string(want) {
		s.fatalf("%s differs from %s\ngot:\n%s\nwant:\n%s", what, wantFile, got, want)
	}
}

func (s *script) exists(paths []string, negate bool) {
	s.t.Helper()
	for _, p := range paths {
		_, err := os.Stat(s.path(p))
		if (err == nil) == negate {
			s.fatalf("exists %s: %v, want negated %v", p, err, negate)
		}
	}
}

func (s *script) checkRuns(want string) {
	s.t.Helper()
	if got := strings.Join(s.runs, "; "); got != want {
		s.fatalf("runs %q, want %q", got, want)
	}
}

// command runs git or go in the root and keeps its combined output as
// stdout.
func (s *script) command(negate bool, name string, args ...string) {
	s.t.Helper()
	cmd := exec.CommandContext(s.t.Context(), name, args...)
	cmd.Dir = s.root
	out, err := cmd.CombinedOutput()
	s.stdout, s.stderr = string(out), ""
	if (err == nil) == negate {
		s.fatalf("%s %s: %v, want failure %v\n%s", name, strings.Join(args, " "), err, negate, out)
	}
}

func (s *script) gitRun(args ...string) {
	s.t.Helper()
	s.command(false, "git", args...)
}

func (s *script) commit() {
	s.t.Helper()
	s.gitRun("add", "-A")
	s.gitRun("commit", "-q", "--allow-empty", "-m", fmt.Sprintf("line %d", s.line))
}

func (s *script) path(rel string) string {
	return filepath.Join(s.root, filepath.FromSlash(rel))
}

func (s *script) read(rel string) []byte {
	s.t.Helper()
	data, err := os.ReadFile(s.path(rel))
	if err != nil {
		s.fatalf("%v", err)
	}
	return data
}

func (s *script) write(rel string, data []byte) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.path(rel)), 0o750); err != nil {
		s.fatalf("%v", err)
	}
	if err := os.WriteFile(s.path(rel), data, 0o600); err != nil {
		s.fatalf("%v", err)
	}
}
