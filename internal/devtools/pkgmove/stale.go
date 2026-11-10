package pkgmove

import (
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/devtools/ledger"
)

// staleMappings returns the mappings whose old paths must no longer appear:
// one for each done package and file entry of ticket, or of every ticket when
// ticket is empty, one for each done entry above those of ticket, and one for
// each directory above them whose entries are all done and moved alike. A
// done split or delete entry has no single replacement, so its old path is
// reported without one.
func (t *tree) staleMappings(ticket string) []*mapping {
	done := func(_, status string) bool { return status == ledger.StatusDone }
	var ms []*mapping
	for _, p := range t.report.Packages {
		if p.Status == ledger.StatusDone && (ticket == "" || p.Ticket == ticket) {
			ms = append(ms, t.doneMapping(p, done))
		}
	}
	for _, f := range t.report.Files {
		if f.Status == ledger.StatusDone && (ticket == "" || f.Ticket == ticket) {
			ms = append(ms, fileMapping(f.From, t.fileTarget(f.File), f.Ticket))
		}
	}
	ms = append(ms, t.ancestorMappings(froms(ms), done)...)
	return append(ms, t.parentMappings(ms, done)...)
}

// stale returns a problem, as file:line: message, for each reference to an
// old path of mt in the tracked text files outside the history set. git grep
// finds the lines that hold an old path anywhere; the matcher decides which
// of them refer to it, as a move would.
func (t *tree) stale(mt *matcher) ([]string, error) {
	if len(mt.mappings) == 0 {
		return nil, nil
	}
	var patterns []string
	for _, m := range mt.mappings {
		patterns = append(patterns, m.from)
	}
	lines, err := t.git.grep(patterns, historyFiles)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, l := range lines {
		for _, r := range mt.find(l.text) {
			problems = append(
				problems,
				fmt.Sprintf("%s:%d: %s", l.file, l.line, staleMessage(l.text, r)),
			)
		}
	}
	slices.Sort(problems)
	return problems, nil
}

// staleMessage says why a reference is stale and, when it has one, what to
// write instead.
func staleMessage(text string, r ref) string {
	var b strings.Builder
	fmt.Fprintf(&b, "stale path %q", text[r.start:r.end])
	switch {
	case r.m.ticket != "" && r.m.to != "":
		fmt.Fprintf(&b, ": %s moved %s to %s", r.m.ticket, r.m.from, r.m.to)
	case r.m.ticket != "":
		fmt.Fprintf(&b, ": %s split or deleted %s", r.m.ticket, r.m.from)
	default:
		fmt.Fprintf(&b, ": %s moved", r.m.from)
	}
	if r.repl != "" {
		fmt.Fprintf(&b, "; write %q", r.repl)
	}
	return b.String()
}
