package specoracle

import (
	"fmt"
	"sort"
	"strings"
)

// Report is the outcome of comparing JFrog's operations with ours.
type Report struct {
	// Missing lists operation keys JFrog documents that we do not define.
	Missing []string
	// Divergent maps an operation key to the findings against our definition.
	Divergent map[string][]string
	// Matched lists keys we define with no findings.
	Matched []string
	// Extra lists keys we define that JFrog does not document (probes, stubs).
	Extra []string
	// Families records the family of every JFrog key, for the summary.
	Families map[string]string
	// Errors carries parse problems from either side.
	Errors []string
}

// Compare checks every documented operation against ours. Descriptions are
// never compared: only what a client can observe on the wire.
func Compare(theirs, ours Oracle) Report {
	r := Report{Divergent: map[string][]string{}, Families: map[string]string{}}
	r.Errors = append(r.Errors, theirs.Errors...)
	r.Errors = append(r.Errors, ours.Errors...)
	for key, top := range theirs.Ops {
		r.Families[key] = top.Family
		oop, ok := ours.Ops[key]
		if !ok {
			r.Missing = append(r.Missing, key)
			continue
		}
		findings := compareOp(top, oop)
		if len(findings) == 0 {
			r.Matched = append(r.Matched, key)
		} else {
			r.Divergent[key] = findings
		}
	}
	for key := range ours.Ops {
		if _, ok := theirs.Ops[key]; !ok {
			r.Extra = append(r.Extra, key)
		}
	}
	sort.Strings(r.Missing)
	sort.Strings(r.Matched)
	sort.Strings(r.Extra)
	return r
}

func compareOp(theirs, ours Operation) []string {
	var f []string
	ourParams := map[string]Param{}
	for _, p := range ours.Params {
		ourParams[p.In+":"+p.Name] = p
	}
	for _, p := range theirs.Params {
		if p.In == "path" {
			continue
		}
		op, ok := ourParams[p.In+":"+p.Name]
		if !ok {
			if p.Required {
				f = append(f, fmt.Sprintf("missing required %s parameter %s", p.In, p.Name))
			} else {
				f = append(f, fmt.Sprintf("missing %s parameter %s", p.In, p.Name))
			}
			continue
		}
		for _, missing := range diff(p.Enum, op.Enum) {
			f = append(f, fmt.Sprintf("%s parameter %s lacks enum value %s", p.In, p.Name, missing))
		}
	}
	for _, mt := range diff(theirs.RequestMedia, ours.RequestMedia) {
		f = append(f, "missing request media type "+mt)
	}
	for _, prop := range diff(theirs.RequestProps, ours.RequestProps) {
		f = append(f, "missing request property "+prop)
	}
	for code, props := range theirs.Responses {
		ourProps, ok := ours.Responses[code]
		if !ok {
			if code == "default" {
				continue
			}
			f = append(f, "missing response "+code)
			continue
		}
		for _, prop := range diff(props, ourProps) {
			f = append(f, fmt.Sprintf("missing response %s property %s", code, prop))
		}
	}
	sort.Strings(f)
	return f
}

// diff returns the elements of a that are not in b.
func diff(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, x := range b {
		set[x] = true
	}
	var out []string
	for _, x := range a {
		if !set[x] {
			out = append(out, x)
		}
	}
	return out
}

// FamilySummary is one row of Summary.
type FamilySummary struct {
	Family    string
	Total     int
	Missing   int
	Divergent int
	Matched   int
}

// Summary counts outcomes per documentation family, sorted by family.
func (r Report) Summary() []FamilySummary {
	rows := map[string]*FamilySummary{}
	get := func(key string) *FamilySummary {
		fam := r.Families[key]
		row, ok := rows[fam]
		if !ok {
			row = &FamilySummary{Family: fam}
			rows[fam] = row
		}
		row.Total++
		return row
	}
	for _, k := range r.Missing {
		get(k).Missing++
	}
	for k := range r.Divergent {
		get(k).Divergent++
	}
	for _, k := range r.Matched {
		get(k).Matched++
	}
	out := make([]FamilySummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Family < out[j].Family })
	return out
}

// String renders the report in a stable order so it can be diffed.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(
		&b,
		"%-45s %6s %8s %10s %8s\n",
		"family",
		"total",
		"missing",
		"divergent",
		"matched",
	)
	var total FamilySummary
	for _, row := range r.Summary() {
		fmt.Fprintf(
			&b,
			"%-45s %6d %8d %10d %8d\n",
			row.Family,
			row.Total,
			row.Missing,
			row.Divergent,
			row.Matched,
		)
		total.Total += row.Total
		total.Missing += row.Missing
		total.Divergent += row.Divergent
		total.Matched += row.Matched
	}
	fmt.Fprintf(
		&b,
		"%-45s %6d %8d %10d %8d\n",
		"TOTAL",
		total.Total,
		total.Missing,
		total.Divergent,
		total.Matched,
	)
	if len(r.Divergent) > 0 {
		b.WriteString("\nDivergent:\n")
		keys := make([]string, 0, len(r.Divergent))
		for k := range r.Divergent {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s\n", k)
			for _, f := range r.Divergent[k] {
				fmt.Fprintf(&b, "    - %s\n", f)
			}
		}
	}
	if len(r.Extra) > 0 {
		b.WriteString("\nExtra (ours only):\n")
		for _, k := range r.Extra {
			fmt.Fprintf(&b, "  %s\n", k)
		}
	}
	if len(r.Errors) > 0 {
		b.WriteString("\nParse errors:\n")
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "  %s\n", e)
		}
	}
	return b.String()
}
