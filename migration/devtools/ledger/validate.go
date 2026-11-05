package ledger

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The header values and patterns below are repeated in
// migration/ledger.schema.json for editors; TestUnitSchemaAgreesWithValidator
// keeps the two equal.
const (
	schemaVersion = 1
	module        = "github.com/weaveplatform/artifactweave"
	stagingRoot   = "migration"
	finalRoot     = "internal"

	legacyRoots       = `(internal|pkg/(aql|blob|cdn|layout|repo|store|types))`
	legacyPattern     = `^` + legacyRoots + `(/[a-z0-9_]+)*$`
	legacyFilePattern = `^` + legacyRoots + `(/[A-Za-z0-9_.-]+)+/?$`
	targetPattern     = `^[a-z][a-z0-9_]*(/[a-z][a-z0-9_]*)*$`
	ticketPattern     = `^(FND|SKL|PLT|STO|ACC|ART|RMT|ECO|WEB|TST|PRO)-[0-9]{2}$`
	phasePattern      = `^P[0-9]{1,2}[a-z]?$`
	namePattern       = `^[a-z][a-z0-9]*$`
	fileNamePattern   = `^[A-Za-z0-9_.-]+$`

	minTargets = 2
	minPhases  = 1
)

var (
	legacyRe     = regexp.MustCompile(legacyPattern)
	legacyFileRe = regexp.MustCompile(legacyFilePattern)
	targetRe     = regexp.MustCompile(targetPattern)
	ticketRe     = regexp.MustCompile(ticketPattern)
	phaseRe      = regexp.MustCompile(phasePattern)
	nameRe       = regexp.MustCompile(namePattern)
	fileNameRe   = regexp.MustCompile(fileNamePattern)

	packageActions = []string{ActionMove, ActionMerge, ActionSplit, ActionDelete}
	fileActions    = []string{ActionMove, ActionSplit, ActionDelete}
	scopes         = []string{ScopeRoot, ScopeRepo}

	// topLevel is what decision 0044 places directly under the staging root:
	// the kernel, the platform, the gateway, the composition root, the
	// layout tests, the developer tools and the thirteen modules. The layout
	// tests reject any other top-level directory, so a target outside this
	// set is a mistake in the ledger.
	topLevel = []string{
		"kernel",
		"platform",
		"gateway",
		"app",
		"layout",
		"devtools",
		"artifacts",
		"access",
		"configuration",
		"notifications",
		"retention",
		"federation",
		"releases",
		"workers",
		"evidence",
		"governance",
		"security",
		"operations",
		"estate",
	}
)

// Validate returns every way the ledger breaks its rules, in a stable order,
// or nil when it breaks none. Each error wraps ErrInvalid and names the
// entry. The rules:
//
//   - the header holds schema 1, this module and the migration and internal
//     roots;
//   - every field has the shape migration/ledger.schema.json describes:
//     known actions and scopes, a legacy path in from, target, ticket, phase
//     and file-name patterns, a to for every move or merge, no to, name or
//     rename on a split or delete package, at least two targets on a split
//     file and at least one phase on a placeholder;
//   - from values are unique, and so are placeholder targets;
//   - a move target is the target of no other package entry, and a merge
//     target is shared by at least two merge entries;
//   - a root-scope target starts with a staged top-level directory and a
//     repo-scope target starts with test/;
//   - a file entry lies below a legacy directory that has a package entry;
//   - no entry targets a placeholder.
func (l *Ledger) Validate() []error {
	v := &validator{}
	v.header(l)
	for _, p := range l.Packages {
		v.pkg(p)
	}
	for _, f := range l.Files {
		v.file(f)
	}
	for _, p := range l.Placeholders {
		v.placeholder(p)
	}
	v.unique(l)
	v.targets(l)
	return v.errs
}

type validator struct {
	errs []error
}

func (v *validator) addf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...)))
}

func (v *validator) header(l *Ledger) {
	if l.Schema != schemaVersion {
		v.addf("schema is %d, not %d", l.Schema, schemaVersion)
	}
	for _, f := range []struct{ name, got, want string }{
		{"module", l.Module, module},
		{"staging_root", l.StagingRoot, stagingRoot},
		{"final_root", l.FinalRoot, finalRoot},
	} {
		if f.got != f.want {
			v.addf("%s is %q, not %q", f.name, f.got, f.want)
		}
	}
}

func (v *validator) pkg(p Package) {
	at := "package " + strconv.Quote(p.From)
	v.match(at, "from", p.From, legacyRe)
	v.oneOf(at, "action", p.Action, packageActions)
	v.match(at, "ticket", p.Ticket, ticketRe)
	v.scope(at, p.Scope)
	if p.Name != "" {
		v.match(at, "name", p.Name, nameRe)
	}
	for _, name := range slices.Sorted(maps.Keys(p.Rename)) {
		v.match(at, "rename key", name, fileNameRe)
		v.match(at, "rename value", p.Rename[name], fileNameRe)
	}
	switch p.Action {
	case ActionMove, ActionMerge:
		v.target(at, "to", p.Scope, p.To)
	case ActionSplit, ActionDelete:
		if p.To != "" || p.Name != "" || p.Rename != nil {
			v.addf("%s: a %s entry has no to, name or rename", at, p.Action)
		}
	}
}

func (v *validator) file(f File) {
	at := "file " + strconv.Quote(f.From)
	v.match(at, "from", f.From, legacyFileRe)
	v.oneOf(at, "action", f.Action, fileActions)
	v.match(at, "ticket", f.Ticket, ticketRe)
	v.scope(at, f.Scope)
	if f.File != "" {
		v.match(at, "file", f.File, fileNameRe)
	}
	if f.To != "" || f.Action == ActionMove {
		v.target(at, "to", f.Scope, f.To)
	}
	if f.Targets != nil || f.Action == ActionSplit {
		if len(f.Targets) < minTargets {
			v.addf("%s: targets has %d entries, fewer than %d", at, len(f.Targets), minTargets)
		}
		for _, t := range f.Targets {
			v.target(at, "targets", f.Scope, t)
		}
	}
}

func (v *validator) placeholder(p Placeholder) {
	at := "placeholder " + strconv.Quote(p.To)
	v.scope(at, p.Scope)
	v.target(at, "to", p.Scope, p.To)
	if len(p.Phases) < minPhases {
		v.addf("%s: phases has no entry", at)
	}
	for _, phase := range p.Phases {
		v.match(at, "phase", phase, phaseRe)
	}
}

func (v *validator) match(at, field, value string, re *regexp.Regexp) {
	if !re.MatchString(value) {
		v.addf("%s: %s %q does not match %s", at, field, value, re)
	}
}

func (v *validator) oneOf(at, field, value string, allowed []string) {
	if !slices.Contains(allowed, value) {
		v.addf("%s: %s %q is not one of %s", at, field, value, strings.Join(allowed, ", "))
	}
}

func (v *validator) scope(at, scope string) {
	if scope != "" {
		v.oneOf(at, "scope", scope, scopes)
	}
}

func (v *validator) target(at, field, scope, to string) {
	switch {
	case to == "":
		v.addf("%s: %s is empty", at, field)
	case !targetRe.MatchString(to):
		v.addf("%s: %s %q does not match %s", at, field, to, targetPattern)
	case scope == ScopeRepo && !strings.HasPrefix(to, "test/"):
		v.addf("%s: repo-scope %s %q does not start with test/", at, field, to)
	case scope != ScopeRepo && !slices.Contains(topLevel, strings.Split(to, "/")[0]):
		v.addf("%s: %s %q does not start with a staged top-level directory (%s)",
			at, field, to, strings.Join(topLevel, ", "))
	}
}

// unique reports repeated from values across package and file entries, and
// repeated placeholder targets.
func (v *validator) unique(l *Ledger) {
	from := map[string]int{}
	for _, p := range l.Packages {
		from[p.From]++
	}
	for _, f := range l.Files {
		from[f.From]++
	}
	placeholders := map[string]int{}
	for _, p := range l.Placeholders {
		placeholders[l.dir(p.Scope, p.To)]++
	}
	for _, f := range slices.Sorted(maps.Keys(from)) {
		if from[f] > 1 {
			v.addf("from %q appears in %d entries", f, from[f])
		}
	}
	for _, p := range slices.Sorted(maps.Keys(placeholders)) {
		if placeholders[p] > 1 {
			v.addf("placeholder %s appears %d times", p, placeholders[p])
		}
	}
}

// targets checks how entries share targets, where file entries sit and that
// no entry targets a placeholder.
func (v *validator) targets(l *Ledger) {
	byTarget := map[string][]Package{}
	legacyDirs := map[string]bool{}
	for _, p := range l.Packages {
		legacyDirs[p.From] = true
		if p.Action == ActionMove || p.Action == ActionMerge {
			t := l.dir(p.Scope, p.To)
			byTarget[t] = append(byTarget[t], p)
		}
	}
	for _, t := range slices.Sorted(maps.Keys(byTarget)) {
		for _, p := range byTarget[t] {
			switch n := len(byTarget[t]); {
			case p.Action == ActionMove && n > 1:
				v.addf(
					"package %q: move target %s is shared with %d other entries; use merge",
					p.From,
					t,
					n-1,
				)
			case p.Action == ActionMerge && n == 1:
				v.addf("package %q: merge target %s has no other entry; use move", p.From, t)
			}
		}
	}

	targeted := map[string]bool{}
	for t := range byTarget {
		targeted[t] = true
	}
	for _, f := range l.Files {
		if !insideLegacyPackage(f.From, legacyDirs) {
			v.addf("file %q: no package entry covers its directory", f.From)
		}
		for _, t := range append([]string{f.To}, f.Targets...) {
			if t != "" {
				targeted[l.dir(f.Scope, t)] = true
			}
		}
	}
	for _, p := range l.Placeholders {
		if targeted[l.dir(p.Scope, p.To)] {
			v.addf(
				"placeholder %q: it is the target of an entry, but a placeholder holds doc.go only",
				p.To,
			)
		}
	}
}

// insideLegacyPackage reports whether a package entry's directory is an
// ancestor of from. An asset directory's trailing "/" is ignored.
func insideLegacyPackage(from string, legacyDirs map[string]bool) bool {
	for dir := path.Dir(strings.TrimSuffix(from, "/")); dir != "."; dir = path.Dir(dir) {
		if legacyDirs[dir] {
			return true
		}
	}
	return false
}
