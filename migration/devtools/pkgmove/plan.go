package pkgmove

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/migration/devtools/ledger"
)

// entry is one package directory that a move carries.
type entry struct {
	// from and to are root-relative directories.
	from, to string
	// oldName is the package clause in from; newName the one in to.
	oldName, newName string
	rename           map[string]string
	// ticket owns the entry; empty for a re-homed directory.
	ticket string
}

// fileMove is one tracked file that changes path.
type fileMove struct{ from, to string }

// selectTicket returns the pending move and merge entries of ticket. A split
// or delete entry of the ticket is not moved by this tool; it is reported in
// notices instead.
func (t *tree) selectTicket(ticket string) (entries []*entry, notices []string, err error) {
	for _, p := range t.report.Packages {
		switch {
		case p.Ticket != ticket:
		case p.Action == ledger.ActionSplit || p.Action == ledger.ActionDelete:
			notices = append(notices, fmt.Sprintf(
				"%s is a %s entry; move carries only move and merge entries", p.From, p.Action))
		case p.Status != ledger.StatusPending:
			return nil, nil, fmt.Errorf(
				"%w: %s is %s, not pending",
				errPrecondition,
				p.From,
				p.Status,
			)
		default:
			to := t.target(p.Package)
			name := p.Name
			if name == "" {
				name = path.Base(to)
			}
			entries = append(
				entries,
				&entry{from: p.From, to: to, newName: name, rename: p.Rename, ticket: ticket},
			)
		}
	}
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf(
			"%w: %s has no move or merge entry in the ledger",
			errPrecondition,
			ticket,
		)
	}
	return entries, notices, nil
}

// validate fails when the ledger breaks its rules, naming every problem.
func (t *tree) validate() error {
	if errs := t.ledger.Validate(); len(errs) > 0 {
		return fmt.Errorf(
			"%w: the ledger does not validate: %w",
			errPrecondition,
			errors.Join(errs...),
		)
	}
	return nil
}

// selectDir returns the entry that re-homes from, a staged directory or one
// under test/, to to.
func (t *tree) selectDir(from, to, name string) (*entry, error) {
	for _, d := range []string{from, to} {
		if !fs.ValidPath(d) || ledger.IsLegacy(d) ||
			(!strings.HasPrefix(d, t.ledger.StagingRoot+"/") && !strings.HasPrefix(d, "test/")) {
			return nil, fmt.Errorf(
				"%w: %q is not a directory below %s/ or test/; legacy packages move by -ticket",
				errUsage, d, t.ledger.StagingRoot,
			)
		}
	}
	if from == to || strings.HasPrefix(to, from+"/") {
		return nil, fmt.Errorf("%w: -to %s is -from or lies below it", errUsage, to)
	}
	if name == "" {
		name = path.Base(to)
	}
	if !token.IsIdentifier(name) {
		return nil, fmt.Errorf("%w: -name %q is not a Go identifier", errUsage, name)
	}
	if !slices.ContainsFunc(t.under(from), func(f string) bool {
		return path.Dir(f) == from && strings.HasSuffix(f, ".go")
	}) {
		return nil, fmt.Errorf("%w: %s holds no tracked Go files", errPrecondition, from)
	}
	return &entry{from: from, to: to, newName: name}, nil
}

// requireMovable checks the preconditions that concern the directories: no
// untracked file in a source directory, which git mv would leave behind, and
// no Go file directly in a target directory. A target may already exist with
// placeholder subpackages below it.
func (t *tree) requireMovable(entries []*entry) error {
	dirs := make([]string, len(entries))
	for i, e := range entries {
		dirs[i] = e.from
	}
	untracked, err := t.git.list(
		slices.Concat([]string{"ls-files", "--others", "--exclude-standard", "-z", "--"}, dirs)...)
	if err != nil {
		return err
	}
	if len(untracked) > 0 {
		return fmt.Errorf(
			"%w: untracked files would stay behind: %s; commit or remove them",
			errPrecondition, strings.Join(untracked, ", "),
		)
	}
	for _, e := range entries {
		list, err := os.ReadDir(t.abs(e.to))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("read the target: %w", err)
		}
		for _, f := range list {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".go") {
				return fmt.Errorf(
					"%w: the target %s already holds %s",
					errPrecondition,
					e.to,
					f.Name(),
				)
			}
		}
	}
	return nil
}

// carry fills each entry's old package name and returns the files the
// entries carry: the tracked files directly in from, renamed by the entry,
// and those in subdirectories that hold no package at any depth (asset trees
// such as testdata/ and migrations/). A subdirectory holding a package stays,
// since it has its own entry.
func (t *tree) carry(entries []*entry) ([]fileMove, error) {
	var moves []fileMove
	dest := map[string]string{}
	for _, e := range entries {
		var goFiles []string
		for _, f := range t.under(e.from) {
			rel := strings.TrimPrefix(f, e.from+"/")
			first, _, nested := strings.Cut(rel, "/")
			switch {
			case nested && t.isRegion(e.from+"/"+first):
				continue
			case nested:
			case strings.HasSuffix(f, ".go"):
				goFiles = append(goFiles, f)
				fallthrough
			default:
				if renamed, ok := e.rename[rel]; ok {
					rel = renamed
				}
			}
			to := e.to + "/" + rel
			if other, taken := dest[to]; taken {
				return nil, fmt.Errorf(
					"%w: %s and %s would both move to %s; give one a rename in the ledger",
					errPrecondition, other, f, to,
				)
			}
			if t.tracked[to] {
				return nil, fmt.Errorf(
					"%w: %s would overwrite the tracked file %s",
					errPrecondition,
					f,
					to,
				)
			}
			dest[to] = f
			moves = append(moves, fileMove{from: f, to: to})
		}
		name, err := t.packageName(goFiles)
		if err != nil {
			return nil, err
		}
		e.oldName = name
	}
	slices.SortFunc(moves, func(a, b fileMove) int { return strings.Compare(a.from, b.from) })
	return moves, nil
}

// packageName returns the package that the Go files declare: the most common
// name among the non-test files, else among the test files without "_test".
// A file tagged ignore may declare another package, such as a generator's
// main, and does not outvote the package.
func (t *tree) packageName(files []string) (string, error) {
	names, testNames := map[string]int{}, map[string]int{}
	fset := token.NewFileSet()
	for _, f := range files {
		src, err := t.read(f)
		if err != nil {
			return "", err
		}
		file, err := parser.ParseFile(fset, f, src, parser.PackageClauseOnly)
		if err != nil {
			return "", fmt.Errorf("read the package clause: %w", err)
		}
		if strings.HasSuffix(f, "_test.go") {
			testNames[strings.TrimSuffix(file.Name.Name, "_test")]++
		} else {
			names[file.Name.Name]++
		}
	}
	if len(names) == 0 {
		names = testNames
	}
	best := ""
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if best == "" || names[name] > names[best] {
			best = name
		}
	}
	return best, nil
}

// golangciPath is the lint configuration.
const golangciPath = ".golangci.yml"

// requireLintNames refuses to rename a package that a wrapcheck exception
// names: extra-ignore-sigs match a constructor by its package name, such as
// apierror.New(, so after the rename the exception would stop applying and
// every return of the constructor would fail the lint. Decision 0044 keeps
// those names.
func (t *tree) requireLintNames(entries []*entry) error {
	src, err := t.read(golangciPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cfg struct {
		Linters struct {
			Settings struct {
				Wrapcheck struct {
					//nolint:tagliatelle // golangci-lint's key, not this project's YAML
					ExtraIgnoreSigs []string `yaml:"extra-ignore-sigs"`
				} `yaml:"wrapcheck"`
			} `yaml:"settings"`
		} `yaml:"linters"`
	}
	if err := yaml.Unmarshal(src, &cfg); err != nil {
		return fmt.Errorf("%w: %s: %w", errUnexpected, golangciPath, err)
	}
	for _, e := range entries {
		for _, sig := range cfg.Linters.Settings.Wrapcheck.ExtraIgnoreSigs {
			if e.oldName != e.newName &&
				(strings.HasPrefix(sig, e.oldName+".") || strings.Contains(sig, "/"+e.oldName+".")) {
				return fmt.Errorf(
					"%w: %s would rename package %s to %s, but the wrapcheck exception %q in %s "+
						"matches it by name; keep the name with a name field in the ledger, "+
						"or change the exception first",
					errPrecondition, e.from, e.oldName, e.newName, sig, golangciPath,
				)
			}
		}
	}
	return nil
}

// entryMappings returns a mapping for each entry, for each done entry above
// them and for each directory above them that moves with them. done reports
// whether a ledger entry counts as moved once the change is made: selected by
// it, or done already.
func (t *tree) entryMappings(entries []*entry, done func(ledger.PackageStatus) bool) []*mapping {
	var ms []*mapping
	for _, e := range entries {
		whole := t.uniform(e.from, e.to, done)
		if e.ticket == "" {
			// A re-homed directory has no ledger entries below it to
			// compare, so whole means that no package lives below it.
			whole = !t.packageBelow(e.from)
		}
		ms = append(
			ms,
			&mapping{from: e.from, to: e.to, rename: e.rename, whole: whole, ticket: e.ticket},
		)
	}
	ms = append(ms, t.ancestorMappings(froms(ms), done)...)
	return append(ms, t.parentMappings(ms, done)...)
}

// doneMapping returns the mapping of a done package entry. A split or delete
// entry has no single replacement, so its old path maps to nothing.
func (t *tree) doneMapping(p ledger.PackageStatus, done func(ledger.PackageStatus) bool) *mapping {
	to := t.target(p.Package)
	return &mapping{
		from:   p.From,
		to:     to,
		rename: p.Rename,
		whole:  to != "" && t.uniform(p.From, to, done),
		ticket: p.Ticket,
	}
}

// ancestorMappings returns the mapping of each done package entry above one
// of dirs that has none yet. Moving the last entries below a done entry can
// make everything below it move alike, and from then on its directory
// prefixes and P/... patterns have a replacement and are stale.
func (t *tree) ancestorMappings(dirs []string, done func(ledger.PackageStatus) bool) []*mapping {
	var ms []*mapping
	for _, p := range t.report.Packages {
		if p.Status == ledger.StatusDone && !slices.Contains(dirs, p.From) &&
			slices.ContainsFunc(
				dirs,
				func(d string) bool { return strings.HasPrefix(d, p.From+"/") },
			) {
			ms = append(ms, t.doneMapping(p, done))
		}
	}
	return ms
}

// uniform reports whether everything below dir lands at the same position
// below target: every package entry at or below dir is done, by done, and
// moves or merges to the matching place below target.
func (t *tree) uniform(dir, target string, done func(ledger.PackageStatus) bool) bool {
	for _, p := range t.report.Packages {
		if p.From != dir && !strings.HasPrefix(p.From, dir+"/") {
			continue
		}
		if !done(p) || t.target(p.Package) != target+strings.TrimPrefix(p.From, dir) {
			return false
		}
	}
	return true
}

// packageBelow reports whether a package directory of the tree lies below
// dir.
func (t *tree) packageBelow(dir string) bool {
	return slices.ContainsFunc(t.under(dir), func(f string) bool {
		return strings.HasSuffix(f, ".go") && inPackage(f) && path.Dir(f) != dir
	})
}

// parentMappings returns a mapping for each directory above the mappings
// that is no package entry itself, holds no file of its own and whose
// entries all move alike, such as internal/server/gen above the generated
// servers: the Makefile and the coverage configuration name that directory,
// and those names must move with its packages. A legacy root never maps:
// "internal" is also a word, and every package below it would have to move
// alike.
func (t *tree) parentMappings(ms []*mapping, done func(ledger.PackageStatus) bool) []*mapping {
	seen := map[string]bool{}
	var parents []*mapping
	for _, m := range ms {
		if m.ticket == "" || m.to == "" {
			continue
		}
		dir, target := m.from, m.to
		for {
			parent := path.Dir(dir)
			to, ok := strings.CutSuffix(target, "/"+path.Base(dir))
			if !ok || !ledger.IsLegacy(parent) || slices.Contains(ledger.LegacyRoots, parent) ||
				seen[parent] || t.entries[parent] || !t.uniform(parent, to, done) ||
				t.holdsOwnFiles(parent) {
				break
			}
			seen[parent] = true
			parents = append(parents, &mapping{from: parent, to: to, whole: true, ticket: m.ticket})
			dir, target = parent, to
		}
	}
	return parents
}

// holdsOwnFiles reports whether a tracked file below dir belongs to no
// package entry below dir, so that it would stay when they all move.
func (t *tree) holdsOwnFiles(dir string) bool {
	return slices.ContainsFunc(t.under(dir), func(f string) bool {
		for d := path.Dir(f); d != dir; d = path.Dir(d) {
			if t.entries[d] {
				return false
			}
		}
		return true
	})
}

// change is everything a move or a promotion does to the tree, computed
// before anything is written, so that -dry-run prints what a run would do.
type change struct {
	t       *tree
	entries []*entry
	// moves are the files that git mv moves one by one; dirMoves the
	// directories it moves whole; removes the files git rm deletes.
	moves, dirMoves []fileMove
	removes         []string
	writes          []fileWrite
	// adds are files the change creates, which git add tracks.
	adds    []string
	notices []string
	steps   []step
}

// fileWrite is the new content of a tracked file.
type fileWrite struct {
	// path is where the file is before the change, and to after it.
	path, to string
	content  []byte
	counts   editCounts
}

// planMove computes the change that moves entries: the files they carry, the
// rewrite of every tracked file that names their old paths, the ownership
// inventory and the regeneration steps.
func (t *tree) planMove(entries []*entry, skip []string) (*change, error) {
	moves, err := t.carry(entries)
	if err != nil {
		return nil, err
	}
	if err := t.requireLintNames(entries); err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for _, e := range entries {
		selected[e.from] = true
	}
	ms := t.entryMappings(entries, func(p ledger.PackageStatus) bool {
		return selected[p.From] || p.Status == ledger.StatusDone
	})
	mt := newMatcher(t.module, ms, t.isRegion, t.isFile)
	candidates, err := t.git.grepFiles(froms(ms))
	if err != nil {
		return nil, err
	}
	rw := &rewriter{mt: mt, renamed: map[string]*entry{}, moved: map[string]*entry{}, skip: skip}
	var clauses []string
	for _, e := range entries {
		if e.oldName != e.newName {
			rw.renamed[t.module+"/"+e.from] = e
		}
	}
	newPath := map[string]string{}
	for _, m := range moves {
		newPath[m.from] = m.to
		if e := rw.renamed[t.module+"/"+path.Dir(m.from)]; e != nil &&
			strings.HasSuffix(m.from, ".go") {
			rw.moved[m.from] = e
			clauses = append(clauses, m.from)
		}
	}
	if rw.typed, err = t.loadTyped(t.importerDirs(candidates, rw.renamed)); err != nil {
		return nil, err
	}
	writes, notices, err := t.rewriteAll(
		rw,
		slices.Concat(candidates, clauses),
		func(p string) string {
			if to, ok := newPath[p]; ok {
				return to
			}
			return p
		},
		mappingDirs(ms),
	)
	if err != nil {
		return nil, err
	}
	c := &change{t: t, entries: entries, moves: moves, writes: writes, notices: notices}
	c.steps = t.steps(mt, c, candidates)
	return c, nil
}

// steps returns the regeneration steps of c.
func (t *tree) steps(mt *matcher, c *change, candidates []string) []step {
	changed := make([]string, 0, len(c.writes))
	moved := make([]string, 0, len(c.moves)+len(c.dirMoves))
	for _, w := range c.writes {
		changed = append(changed, w.path)
	}
	for _, m := range slices.Concat(c.moves, c.dirMoves) {
		moved = append(moved, m.from)
	}
	return regenSteps(regenInputs{
		changed: changed,
		moved:   moved,
		generatedNamesOld: slices.ContainsFunc(candidates, func(p string) bool {
			return strings.HasSuffix(p, ".gen.go") && t.names(mt, p)
		}),
		mentions: func(p string) bool { return t.names(mt, p) },
	})
}

// froms returns the old paths of ms.
func froms(ms []*mapping) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.from
	}
	return out
}

// importerDirs returns the sorted directories of the Go files among
// candidates that import a package of renamed, which the typed load must
// cover.
func (t *tree) importerDirs(candidates []string, renamed map[string]*entry) []string {
	if len(renamed) == 0 {
		return nil
	}
	dirs := map[string]bool{}
	fset := token.NewFileSet()
	for _, p := range candidates {
		if !strings.HasSuffix(p, ".go") || !inPackage(p) {
			continue
		}
		// A file that does not parse is rewritten as text, so its
		// directory needs no type information.
		f, err := parser.ParseFile(fset, t.abs(p), nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		for _, spec := range f.Imports {
			if old, _ := strconv.Unquote(spec.Path.Value); renamed[old] != nil {
				dirs[path.Dir(p)] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(dirs))
}

// print writes the plan of c: each entry, each file or directory that moves,
// each file that a rewrite changes with what changed, the notices and the
// regeneration steps.
func (c *change) print(w io.Writer) {
	for _, e := range c.entries {
		fmt.Fprintf(w, "move %s -> %s", e.from, e.to)
		if e.oldName != e.newName {
			fmt.Fprintf(w, " (package %s becomes %s)", e.oldName, e.newName)
		}
		if e.ticket != "" {
			fmt.Fprintf(w, " [%s]", e.ticket)
		}
		fmt.Fprintln(w)
	}
	for _, m := range slices.Concat(c.dirMoves, c.moves) {
		fmt.Fprintf(w, "  git mv %s %s\n", m.from, m.to)
	}
	for _, r := range c.removes {
		fmt.Fprintf(w, "  git rm %s\n", r)
	}
	for _, wr := range c.writes {
		verb := "rewrite"
		if slices.Contains(c.adds, wr.path) {
			verb = "write"
		}
		fmt.Fprintf(w, "  %s %s", verb, wr.path)
		if wr.to != wr.path {
			fmt.Fprintf(w, " (at %s)", wr.to)
		}
		if s := wr.counts.String(); s != "" {
			fmt.Fprintf(w, ": %s", s)
		}
		fmt.Fprintln(w)
	}
	for _, n := range c.notices {
		fmt.Fprintf(w, "note: %s\n", n)
	}
	for _, s := range c.steps {
		fmt.Fprintf(w, "regenerate: %s (%s)\n", strings.Join(s.args, " "), s.why)
	}
}
