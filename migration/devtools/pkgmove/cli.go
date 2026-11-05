package pkgmove

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/devtools/ledger"
)

// Exit codes of Main.
const (
	exitOK       = 0
	exitProblems = 1
	exitUsage    = 2
)

var (
	// errUsage marks a command line that pkgmove cannot run.
	errUsage = errors.New("usage")
	// errPrecondition marks a tree or ledger state that a command refuses to
	// work on.
	errPrecondition = errors.New("refused")
	// errProblems reports that a command printed problems, so Main exits 1
	// without printing more.
	errProblems = errors.New("problems found")
	// errUnexpected marks output or input that does not have the expected
	// form.
	errUnexpected = errors.New("unexpected content")
)

const usage = `usage:
  pkgmove status  [-ticket ID] [-json] [legacy-path]
  pkgmove move    -ticket ID [-dry-run] [-no-regenerate] [-skip GLOB]...
  pkgmove move    -from DIR -to DIR [-name NAME] [-dry-run] [-no-regenerate] [-skip GLOB]...
  pkgmove check   [-ticket ID | -from DIR]
  pkgmove fmt     [-check]
  pkgmove promote [-dry-run] [-no-regenerate]

Exit status: 0 success, 1 problems (printed as file:line: message), 2 usage.
See migration/README.md, "Moving packages with pkgmove".
`

// env is what a command runs against: the working directory, the output
// streams and the runner of regeneration commands.
type env struct {
	ctx    context.Context
	dir    string
	stdout io.Writer
	stderr io.Writer
	run    runner
}

// Main runs pkgmove with args, the command line without the program name,
// from the working directory, and returns the exit status: 0 on success, 1
// when it found problems (printed to stdout as file:line: message) or failed
// (printed to stderr), and 2 for a command line it cannot run. move and
// promote must run from the module root, with a clean working tree; the
// other commands run from anywhere inside the repository.
func Main(args []string, stdout, stderr io.Writer) int {
	ctx := context.Background()
	e := &env{ctx: ctx, dir: ".", stdout: stdout, stderr: stderr, run: execRunner(ctx, stderr)}
	return e.main(args)
}

func (e *env) main(args []string) int {
	err := e.dispatch(args)
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errUsage):
		fmt.Fprintf(e.stderr, "pkgmove: %v\n\n%s", err, usage)
		return exitUsage
	case !errors.Is(err, errProblems):
		fmt.Fprintf(e.stderr, "pkgmove: %v\n", err)
	}
	return exitProblems
}

func (e *env) dispatch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: no command", errUsage)
	}
	switch args[0] {
	case "status":
		return e.status(args[1:])
	case "move":
		return e.move(args[1:])
	case "check":
		return e.check(args[1:])
	case "fmt":
		return e.fmtLedger(args[1:])
	case "promote":
		return e.promote(args[1:])
	case "help", "-h", "-help", "--help":
		fmt.Fprint(e.stdout, usage)
		return nil
	}
	return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
}

// parse parses the flags of the command name. Positional arguments are left
// in the returned set; flags come first, as the flag package requires.
func parse(name string, args []string, define func(*flag.FlagSet)) (*flag.FlagSet, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	define(fs)
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errUsage, name, err)
	}
	return fs, nil
}

// globs is a repeatable flag.
type globs []string

func (g *globs) String() string { return strings.Join(*g, ",") }

func (g *globs) Set(v string) error {
	*g = append(*g, v)
	return nil
}

// openForChange opens the tree for move and promote, which change it: they
// run from the module root, the ledger validates, the working tree has no
// uncommitted change to a tracked file (untracked files, such as the copies
// in .worktrees/, do not count), and the tracked files are scanned.
func (e *env) openForChange() (*tree, error) {
	t, err := openTree(e.ctx, e.dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(e.dir, "go.mod")); err != nil {
		return nil, fmt.Errorf("%w: run from the module root, %s", errPrecondition, t.root)
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	dirty, err := t.git.run("status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return nil, err
	}
	if len(dirty) > 0 {
		return nil, fmt.Errorf(
			"%w: the working tree has uncommitted changes; commit or stash them first:\n%s",
			errPrecondition, dirty,
		)
	}
	return t, t.scan()
}

// move runs pkgmove move.
func (e *env) move(args []string) error {
	var (
		ticket, from, to, name string
		dryRun, noRegen        bool
		skip                   globs
	)
	fs, err := parse("move", args, func(fs *flag.FlagSet) {
		fs.StringVar(
			&ticket,
			"ticket",
			"",
			"move the pending move and merge entries of this ticket",
		)
		fs.StringVar(&from, "from", "", "re-home this staged or test/ directory")
		fs.StringVar(&to, "to", "", "the new directory of -from")
		fs.StringVar(&name, "name", "", "the package name at -to (default: its last element)")
		fs.BoolVar(&dryRun, "dry-run", false, "print the plan and change nothing")
		fs.BoolVar(&noRegen, "no-regenerate", false, "skip the regeneration steps")
		fs.Var(&skip, "skip", "a glob of files whose text is not rewritten (repeatable)")
	})
	if err != nil {
		return err
	}
	switch {
	case fs.NArg() > 0:
		return fmt.Errorf("%w: move takes no arguments", errUsage)
	case (ticket == "") == (from == ""):
		return fmt.Errorf("%w: move takes either -ticket or -from", errUsage)
	case from != "" && to == "", ticket != "" && (to != "" || name != ""):
		return fmt.Errorf("%w: -to and -name go with -from, which needs -to", errUsage)
	}
	t, err := e.openForChange()
	if err != nil {
		return err
	}
	var (
		entries []*entry
		notices []string
	)
	if ticket != "" {
		entries, notices, err = t.selectTicket(ticket)
	} else {
		var en *entry
		en, err = t.selectDir(from, to, name)
		entries = []*entry{en}
	}
	if err != nil {
		return err
	}
	if err := t.requireMovable(entries); err != nil {
		return err
	}
	c, err := t.planMove(entries, skip)
	if err != nil {
		return err
	}
	c.notices = slices.Concat(notices, c.notices)
	c.print(e.stdout)
	if dryRun {
		fmt.Fprintln(e.stdout, "dry run: nothing changed")
		return nil
	}
	if err := c.apply(); err != nil {
		return err
	}
	if !noRegen {
		if err := e.regenerate(t.root, c.steps); err != nil {
			return err
		}
	}
	return e.checkTree(t, ticket, from)
}

// check runs pkgmove check.
func (e *env) check(args []string) error {
	var ticket, from string
	fs, err := parse("check", args, func(fs *flag.FlagSet) {
		fs.StringVar(&ticket, "ticket", "", "check only the done entries of this ticket")
		fs.StringVar(&from, "from", "", "check a re-homed directory instead of the ledger")
	})
	if err != nil {
		return err
	}
	if fs.NArg() > 0 || (ticket != "" && from != "") {
		return fmt.Errorf("%w: check takes -ticket or -from, and no arguments", errUsage)
	}
	t, err := openTree(e.ctx, e.dir)
	if err != nil {
		return err
	}
	return e.checkTree(t, ticket, from)
}

// checkTree runs the stale check on t: for the done entries of ticket (all
// when empty), or for the re-homed directory from.
func (e *env) checkTree(t *tree, ticket, from string) error {
	if err := t.derive(); err != nil {
		return err
	}
	if err := t.scan(); err != nil {
		return err
	}
	ms := t.staleMappings(ticket)
	if from != "" {
		if t.packageAt(from) {
			return fmt.Errorf("%w: %s still holds Go files", errPrecondition, from)
		}
		ms = []*mapping{{from: from, whole: !t.packageBelow(from)}}
	}
	problems, err := t.stale(newMatcher(t.module, ms, t.isRegion, t.isFile))
	if err != nil {
		return err
	}
	for _, p := range problems {
		fmt.Fprintln(e.stdout, p)
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %d stale paths", errProblems, len(problems))
	}
	fmt.Fprintf(e.stdout, "check: no stale paths (%d old paths checked)\n", len(ms))
	return nil
}

// fmtLedger runs pkgmove fmt.
func (e *env) fmtLedger(args []string) error {
	var check bool
	fs, err := parse("fmt", args, func(fs *flag.FlagSet) {
		fs.BoolVar(
			&check,
			"check",
			false,
			"report a ledger that is not canonical instead of rewriting it",
		)
	})
	if err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: fmt takes no arguments", errUsage)
	}
	t, err := openTree(e.ctx, e.dir)
	if err != nil {
		return err
	}
	file := t.abs(ledgerPath)
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read the ledger: %w", err)
	}
	canonical := ledger.Format(t.ledger)
	switch {
	case bytes.Equal(raw, canonical):
		return nil
	case check:
		fmt.Fprintf(
			e.stdout,
			"%s:1: not in canonical form; run go run ./tools/pkgmove fmt\n",
			ledgerPath,
		)
		return fmt.Errorf("%w: the ledger is not canonical", errProblems)
	}
	if err := os.WriteFile(file, canonical, 0o600); err != nil {
		return fmt.Errorf("write the ledger: %w", err)
	}
	fmt.Fprintf(e.stdout, "rewrote %s in canonical form\n", ledgerPath)
	return nil
}
