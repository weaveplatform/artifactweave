package pkgmove

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/devtools/ledger"
)

// statusRow is one entry of status, and its object under -json.
type statusRow struct {
	Ticket string `json:"ticket"`
	// From is the legacy path, or the path status was asked about.
	From string `json:"from"`
	// To is the target of a move or merge, with the rest of the asked path
	// and its rename applied; empty for a split or delete entry.
	To     string `json:"to"`
	Action string `json:"action"`
	Status string `json:"status"`
}

// line says where the path of r lives now: at its target once the entry is
// done or bridged, at the legacy path while it is pending, and for a split
// or delete entry at the legacy path with the owning ticket.
func (r statusRow) line() string {
	switch {
	case r.To == "":
		return fmt.Sprintf("%s (%s, %s by %s)", r.From, r.Status, actionWords[r.Action], r.Ticket)
	case r.Status == ledger.StatusPending:
		return fmt.Sprintf("%s (pending, %s; moves to %s)", r.From, r.Ticket, r.To)
	}
	return fmt.Sprintf("%s -> %s (%s, %s)", r.From, r.To, r.Status, r.Ticket)
}

// actionWords are the past participles of the actions without a target.
var actionWords = map[string]string{ledger.ActionSplit: "split", ledger.ActionDelete: "deleted"}

// status runs pkgmove status.
func (e *env) status(args []string) error {
	var (
		ticket string
		asJSON bool
	)
	flags, err := parse("status", args, func(fs *flag.FlagSet) {
		fs.StringVar(&ticket, "ticket", "", "list only the entries of this ticket")
		fs.BoolVar(&asJSON, "json", false, "print JSON objects {ticket, from, to, action, status}")
	})
	if err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return fmt.Errorf("%w: status takes at most one legacy path", errUsage)
	}
	t, err := openTree(e.ctx, e.dir)
	if err != nil {
		return err
	}
	rows := t.rows()
	if flags.NArg() == 1 {
		if rows, err = t.locate(flags.Arg(0)); err != nil {
			return err
		}
	}
	if ticket != "" {
		rows = slices.DeleteFunc(rows, func(r statusRow) bool { return r.Ticket != ticket })
	}
	if asJSON {
		return writeJSON(e.stdout, rows)
	}
	for _, r := range rows {
		fmt.Fprintln(e.stdout, r.line())
	}
	if flags.NArg() == 0 {
		fmt.Fprintln(e.stdout, summary(rows))
	}
	return nil
}

// summary is the last line of a status listing: the number of entries and
// of each status, such as "151 entries: 151 pending".
func summary(rows []statusRow) string {
	switch len(rows) {
	case 0:
		return "0 entries"
	case 1:
		return "1 entry: 1 " + rows[0].Status
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.Status]++
	}
	var parts []string
	for _, s := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
	}
	return fmt.Sprintf("%d entries: %s", len(rows), strings.Join(parts, ", "))
}

// writeJSON writes rows as an indented JSON array. rows is never nil, so an
// empty selection is [], not null.
func writeJSON(w io.Writer, rows []statusRow) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rows); err != nil {
		return fmt.Errorf("write the status: %w", err)
	}
	return nil
}

// rows returns a row for every package and file entry, in ledger order.
func (t *tree) rows() []statusRow {
	rows := make([]statusRow, 0, len(t.report.Packages)+len(t.report.Files))
	for _, p := range t.report.Packages {
		rows = append(rows, statusRow{p.Ticket, p.From, t.target(p.Package), p.Action, p.Status})
	}
	for _, f := range t.report.Files {
		rows = append(rows, t.fileRow(f, f.From))
	}
	return rows
}

// fileRow returns the row of a file entry for the path asked, which is the
// entry's file or lies below its asset directory.
func (t *tree) fileRow(f ledger.FileStatus, asked string) statusRow {
	r := statusRow{Ticket: f.Ticket, From: asked, Action: f.Action, Status: f.Status}
	if f.Action != ledger.ActionMove {
		return r
	}
	target := t.ledger.FileTarget(f.File)
	switch {
	case strings.HasPrefix(asked, f.From):
		r.To = target + strings.TrimPrefix(asked, f.From)
	default: // the asset directory itself, asked without its "/"
		r.To = strings.TrimSuffix(target, "/")
	}
	return r
}

// locate returns where the legacy path p lives: through a file entry for p
// or an asset directory above it, else through the deepest package entry at
// or above p, applying its rename to a file directly in it. A directory that
// is no entry but holds entries below it, such as one that only groups
// generated server packages, lists them.
func (t *tree) locate(p string) ([]statusRow, error) {
	p = strings.TrimSuffix(path.Clean(strings.ReplaceAll(p, `\`, "/")), "/")
	if !fs.ValidPath(p) {
		return nil, fmt.Errorf("%w: %q is not a path relative to the repository root", errUsage, p)
	}
	for _, f := range t.report.Files {
		if p == strings.TrimSuffix(f.From, "/") ||
			(strings.HasSuffix(f.From, "/") && strings.HasPrefix(p, f.From)) {
			return []statusRow{t.fileRow(f, p)}, nil
		}
	}
	var owner *ledger.PackageStatus
	for i, ps := range t.report.Packages {
		if (p == ps.From || strings.HasPrefix(p, ps.From+"/")) &&
			(owner == nil || len(ps.From) > len(owner.From)) {
			owner = &t.report.Packages[i]
		}
	}
	var first string
	if owner != nil && p != owner.From {
		first, _, _ = strings.Cut(strings.TrimPrefix(p, owner.From+"/"), "/")
	}
	if owner == nil || (first != "" && t.ledgerRegions[owner.From+"/"+first]) {
		var rows []statusRow
		for _, r := range t.rows() {
			if strings.HasPrefix(r.From, p+"/") {
				rows = append(rows, r)
			}
		}
		if len(rows) == 0 {
			return nil, fmt.Errorf("%w: no ledger entry covers %s", errPrecondition, p)
		}
		return rows, nil
	}
	r := statusRow{Ticket: owner.Ticket, From: p, Action: owner.Action, Status: owner.Status}
	if to := t.target(owner.Package); to != "" {
		rest := strings.TrimPrefix(p, owner.From)
		if renamed, ok := owner.Rename[strings.TrimPrefix(rest, "/")]; ok {
			rest = "/" + renamed
		}
		r.To = to + rest
	}
	return []statusRow{r}, nil
}
