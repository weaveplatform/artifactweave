package pkgmove

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"

	"github.com/weaveplatform/artifactweave/migration/devtools/ledger"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

// ledgerPath is where the ledger lives, relative to the repository root.
const ledgerPath = "migration/ledger.json"

// typedGOOS are the operating systems whose builds the typed load covers:
// the three that CI tests on, so a file such as x_windows.go is renamed with
// type information too.
var typedGOOS = []string{"linux", "darwin", "windows"}

// tree is the repository a command works on.
type tree struct {
	ctx    context.Context
	git    git
	root   string
	module string
	ledger *ledger.Ledger
	report ledger.Report
	// entries holds the from of every package entry.
	entries map[string]bool
	// ledgerRegions holds every directory at or above a package entry.
	ledgerRegions map[string]bool
	// files are the tracked files, sorted, as slash-separated paths
	// relative to root; scan fills them.
	files   []string
	tracked map[string]bool
	// regions adds to ledgerRegions every directory at or above a package
	// directory of the tree: a directory below a moving package that keeps
	// its own entry, or holds a package, stays.
	regions map[string]bool
}

// openTree opens the repository whose root is the nearest directory at or
// above dir holding a go.mod, and loads its ledger and the ledger's status.
// The module in go.mod must be the ledger's module.
func openTree(ctx context.Context, dir string) (*tree, error) {
	root, err := repopath.FindRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open the repository: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("open the repository: %w", err)
	}
	l, err := ledger.Load(filepath.Join(root, filepath.FromSlash(ledgerPath)))
	if err != nil {
		return nil, fmt.Errorf("open the repository: %w", err)
	}
	module := modfile.ModulePath(data)
	if module != l.Module {
		return nil, fmt.Errorf(
			"%w: %s is module %q, but the ledger is for %q",
			errPrecondition, root, module, l.Module,
		)
	}
	t := &tree{
		ctx:           ctx,
		git:           git{ctx: ctx, root: root},
		root:          root,
		module:        module,
		ledger:        l,
		entries:       map[string]bool{},
		ledgerRegions: map[string]bool{},
	}
	for _, p := range l.Packages {
		t.entries[p.From] = true
		addAncestors(t.ledgerRegions, p.From)
	}
	if err := t.derive(); err != nil {
		return nil, err
	}
	return t, nil
}

// derive derives the status of every ledger entry from the tree.
func (t *tree) derive() error {
	report, err := t.ledger.Status(t.root)
	if err != nil {
		return fmt.Errorf("open the repository: %w", err)
	}
	t.report = report
	return nil
}

// scan lists the tracked files and the package regions; a command that moves
// files scans again after the move.
func (t *tree) scan() error {
	files, err := t.git.list("ls-files", "-z")
	if err != nil {
		return err
	}
	slices.Sort(files)
	t.files = files
	t.tracked = make(map[string]bool, len(files))
	t.regions = maps.Clone(t.ledgerRegions)
	for _, f := range files {
		t.tracked[f] = true
		if strings.HasSuffix(f, ".go") && inPackage(f) {
			addAncestors(t.regions, path.Dir(f))
		}
	}
	return nil
}

// inPackage reports whether a Go file at p belongs to a package: the go
// command ignores directories named testdata or starting with "." or "_".
func inPackage(p string) bool {
	for _, elem := range strings.Split(path.Dir(p), "/") {
		if elem == "testdata" || strings.HasPrefix(elem, ".") || strings.HasPrefix(elem, "_") {
			return false
		}
	}
	return true
}

func addAncestors(set map[string]bool, dir string) {
	for ; dir != "." && dir != "/" && !set[dir]; dir = path.Dir(dir) {
		set[dir] = true
	}
}

// packageAt reports whether dir holds a tracked Go file directly.
func (t *tree) packageAt(dir string) bool {
	return slices.ContainsFunc(t.under(dir), func(f string) bool {
		return path.Dir(f) == dir && strings.HasSuffix(f, ".go")
	})
}

// abs returns the file system path of p, a slash-separated path relative to
// the root.
func (t *tree) abs(p string) string { return filepath.Join(t.root, filepath.FromSlash(p)) }

// read returns the content of the file at p, relative to the root.
func (t *tree) read(p string) ([]byte, error) {
	data, err := os.ReadFile(t.abs(p))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	return data, nil
}

// isRegion reports whether dir keeps its own entry when a parent moves.
func (t *tree) isRegion(dir string) bool { return t.regions[dir] }

// isFile reports whether p is a tracked file.
func (t *tree) isFile(p string) bool { return t.tracked[p] }

// under returns the tracked files below dir, which must have been scanned.
func (t *tree) under(dir string) []string {
	prefix := dir + "/"
	i, _ := slices.BinarySearch(t.files, prefix)
	j := i
	for j < len(t.files) && strings.HasPrefix(t.files[j], prefix) {
		j++
	}
	return t.files[i:j]
}

// target returns the root-relative directory of a package entry's target, or
// empty for a split or delete entry.
func (t *tree) target(p ledger.Package) string {
	if p.Action != ledger.ActionMove && p.Action != ledger.ActionMerge {
		return ""
	}
	return t.ledger.Dir(p.Scope, p.To)
}

// typedFile is a file that a typed load compiled, with the type information
// that the qualifier rename needs.
type typedFile struct {
	file *ast.File
	fset *token.FileSet
	info *types.Info
	pkg  *types.Package
}

// loadTyped type-checks the packages in dirs, root-relative directories, with
// their tests and the e2e tag, once for each of typedGOOS, and returns the
// files any load compiled, by root-relative path. A file several loads
// compile keeps the first load's information. A file no load compiles, such
// as one tagged ignore, is absent, and the caller treats it by syntax alone.
func (t *tree) loadTyped(dirs []string) (map[string]*typedFile, error) {
	files := map[string]*typedFile{}
	if len(dirs) == 0 {
		return files, nil
	}
	patterns := make([]string, len(dirs))
	for i, d := range dirs {
		patterns[i] = "./" + d
	}
	for _, goos := range typedGOOS {
		cfg := &packages.Config{
			Context: t.ctx,
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
				packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
			Dir: t.root,
			// GOWORK=off as for every build here; cgo off as the release
			// builds have it, so the compiled files are the source files.
			Env:        append(os.Environ(), "GOOS="+goos, "GOWORK=off", "CGO_ENABLED=0"),
			BuildFlags: []string{"-tags=e2e"},
			Tests:      true,
		}
		pkgs, err := packages.Load(cfg, patterns...)
		if err != nil {
			return nil, fmt.Errorf("type-check the importers for GOOS=%s: %w", goos, err)
		}
		for _, p := range pkgs {
			if err := buildError(goos, p); err != nil {
				return nil, err
			}
			for i, f := range p.Syntax {
				rel, err := filepath.Rel(t.root, p.CompiledGoFiles[i])
				if err != nil || files[filepath.ToSlash(rel)] != nil {
					continue
				}
				files[filepath.ToSlash(rel)] = &typedFile{
					file: f,
					fset: p.Fset,
					info: p.TypesInfo,
					pkg:  p.Types,
				}
			}
		}
	}
	return files, nil
}

// buildError returns an error for a package that does not build for goos,
// except when its build constraints leave out every file there: a load for
// another GOOS compiles such a package.
func buildError(goos string, p *packages.Package) error {
	for _, e := range p.Errors {
		if !strings.Contains(e.Msg, "build constraints exclude all Go files") {
			return fmt.Errorf(
				"%w: GOOS=%s: %s does not build: %w; a move starts from a tree that builds",
				errPrecondition, goos, p.ID, e,
			)
		}
	}
	return nil
}
