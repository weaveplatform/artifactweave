package pkgmove

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/devtools/ledger"
)

// pathMapPath is the record that outlives the ledger: where every legacy
// package and file went.
const pathMapPath = "docs/implementation/p9d-path-map.json"

// pathMapEntry is a ledger entry with its targets resolved to their final,
// root-relative paths.
type pathMapEntry struct {
	From    string            `json:"from"`
	Action  string            `json:"action"`
	To      string            `json:"to,omitempty"`
	Name    string            `json:"name,omitempty"`
	Rename  map[string]string `json:"rename,omitempty"`
	File    string            `json:"file,omitempty"`
	Targets []string          `json:"targets,omitempty"`
	Ticket  string            `json:"ticket"`
}

// pathMap is the content of docs/implementation/p9d-path-map.json.
type pathMap struct {
	Module   string         `json:"module"`
	Packages []pathMapEntry `json:"packages"`
	Files    []pathMapEntry `json:"files"`
}

// promote runs pkgmove promote.
func (e *env) promote(args []string) error {
	var dryRun, noRegen bool
	flags, err := parse("promote", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&dryRun, "dry-run", false, "print the plan and change nothing")
		fs.BoolVar(&noRegen, "no-regenerate", false, "skip the regeneration steps")
	})
	if err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("%w: promote takes no arguments", errUsage)
	}
	t, err := e.openForChange()
	if err != nil {
		return err
	}
	c, err := t.planPromote()
	if err != nil {
		return err
	}
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
	return e.checkPromoted(t)
}

// planPromote computes the promotion: it refuses unless every package and
// file entry is done and the legacy roots hold no tracked file, then renames
// each top-level staged directory to the final root, removes the ledger and
// the staging README, rewrites "<module>/migration/" and the bare staged
// paths, and writes the path map.
func (t *tree) planPromote() (*change, error) {
	var open []string
	for _, p := range t.report.Packages {
		if p.Status != ledger.StatusDone {
			open = append(open, p.From+" is "+p.Status)
		}
	}
	for _, f := range t.report.Files {
		if f.Status != ledger.StatusDone {
			open = append(open, f.From+" is "+f.Status)
		}
	}
	if len(open) > 0 {
		return nil, fmt.Errorf(
			"%w: every entry must be done first: %s",
			errPrecondition,
			strings.Join(open, "; "),
		)
	}
	legacy, err := t.git.list(
		slices.Concat([]string{"ls-files", "-z", "--"}, ledger.LegacyRoots)...)
	if err != nil {
		return nil, err
	}
	if len(legacy) > 0 {
		return nil, fmt.Errorf("%w: the legacy roots still hold tracked files: %s",
			errPrecondition, strings.Join(legacy, ", "))
	}
	staging, final := t.ledger.StagingRoot, t.ledger.FinalRoot
	c := &change{t: t}
	for _, name := range []string{"README.md", "ledger.json", "ledger.schema.json"} {
		if t.tracked[staging+"/"+name] {
			c.removes = append(c.removes, staging+"/"+name)
		}
	}
	ms := []*mapping{{from: staging, to: final, whole: true, moduleOnly: true}}
	for _, f := range t.under(staging) {
		top, _, isDir := strings.Cut(strings.TrimPrefix(f, staging+"/"), "/")
		from := staging + "/" + top
		if !isDir ||
			slices.ContainsFunc(c.dirMoves, func(m fileMove) bool { return m.from == from }) {
			continue
		}
		to := final + "/" + top
		if _, err := os.Stat(t.abs(to)); err == nil {
			return nil, fmt.Errorf(
				"%w: %s already exists, so git mv would move %s into it",
				errPrecondition,
				to,
				from,
			)
		}
		c.dirMoves = append(c.dirMoves, fileMove{from: from, to: to})
		ms = append(ms, &mapping{from: from, to: to, whole: true})
	}
	mt := newMatcher(t.module, ms, func(string) bool { return false }, t.isFile)
	// A bare "migration" is a word, so the module-only mapping is searched
	// for in its module forms.
	patterns := froms(ms[1:])
	for _, form := range mt.forms {
		patterns = append(patterns, form+staging)
	}
	candidates, err := t.git.grepFiles(patterns)
	if err != nil {
		return nil, err
	}
	relocate := func(p string) string {
		for _, m := range c.dirMoves {
			if strings.HasPrefix(p, m.from+"/") {
				return m.to + strings.TrimPrefix(p, m.from)
			}
		}
		return p
	}
	if c.writes, c.notices, err = t.rewriteAll(
		&rewriter{mt: mt},
		candidates,
		relocate,
		ownershipChange{mapDir: mappingDirs(ms)},
	); err != nil {
		return nil, err
	}
	c.writes = append(c.writes, fileWrite{path: pathMapPath, to: pathMapPath, content: t.pathMap()})
	c.adds = []string{pathMapPath}
	c.steps = t.steps(mt, c, candidates)
	return c, nil
}

// pathMap returns the content of docs/implementation/p9d-path-map.json:
// every package and file entry with its targets resolved to final paths.
func (t *tree) pathMap() []byte {
	final := func(scope, to string) string {
		if to == "" || scope == ledger.ScopeRepo {
			return to
		}
		return t.ledger.FinalRoot + "/" + to
	}
	pm := pathMap{Module: t.module, Packages: []pathMapEntry{}, Files: []pathMapEntry{}}
	for _, p := range t.ledger.Packages {
		pm.Packages = append(pm.Packages, pathMapEntry{
			From:   p.From,
			Action: p.Action,
			To:     final(p.Scope, p.To),
			Name:   p.Name,
			Rename: p.Rename,
			Ticket: p.Ticket,
		})
	}
	for _, f := range t.ledger.Files {
		e := pathMapEntry{
			From:   f.From,
			Action: f.Action,
			To:     final(f.Scope, f.To),
			File:   f.File,
			Ticket: f.Ticket,
		}
		for _, target := range f.Targets {
			e.Targets = append(e.Targets, final(f.Scope, target))
		}
		pm.Files = append(pm.Files, e)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	// The map holds only strings, string slices and string maps, which
	// always encode, and a bytes.Buffer write cannot fail.
	_ = enc.Encode(pm)
	return b.Bytes()
}

// checkPromoted runs the stale check after promotion: every legacy path and
// every staged path must be gone, except a legacy path that the promoted tree
// holds again, as migration/app and internal/layout are both legacy and final
// paths.
func (e *env) checkPromoted(t *tree) error {
	if err := t.scan(); err != nil {
		return err
	}
	all := func(string, string) bool { return true }
	var ms []*mapping
	for _, p := range t.report.Packages {
		if len(t.under(p.From)) > 0 {
			continue
		}
		m := &mapping{from: p.From, ticket: p.Ticket}
		if to := t.target(p.Package); to != "" {
			m.to = t.finalPath(to)
			m.rename = p.Rename
			m.whole = t.uniform(p.From, to, all)
		}
		ms = append(ms, m)
	}
	for _, f := range t.report.Files {
		m := fileMapping(f.From, t.fileTarget(f.File), f.Ticket)
		if t.tracked[m.from] || len(t.under(m.from)) > 0 {
			continue
		}
		m.to = t.finalPath(m.to)
		ms = append(ms, m)
	}
	for _, f := range t.files {
		if top, ok := strings.CutPrefix(f, t.ledger.FinalRoot+"/"); ok {
			top, _, _ = strings.Cut(top, "/")
			from := t.ledger.StagingRoot + "/" + top
			if !slices.ContainsFunc(ms, func(m *mapping) bool { return m.from == from }) {
				ms = append(
					ms,
					&mapping{from: from, to: t.ledger.FinalRoot + "/" + top, whole: true},
				)
			}
		}
	}
	mt := newMatcher(t.module, ms, func(d string) bool { return t.ledgerRegions[d] }, t.isFile)
	mt.fileEntry = t.atFileEntry
	problems, err := t.stale(mt)
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

// finalPath returns where the staged path p lives after promotion; a path
// outside the staging root, such as a test/ target, keeps its place.
func (t *tree) finalPath(p string) string {
	if rest, ok := strings.CutPrefix(p, t.ledger.StagingRoot+"/"); ok {
		return t.ledger.FinalRoot + "/" + rest
	}
	return p
}
