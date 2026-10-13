package specoracle

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Baseline is the committed list of accepted gaps. It names only operation
// keys and finding strings (paths, field names, status codes), never JFrog's
// text, so it can live in the repository while the cache cannot.
type Baseline struct {
	Missing   []string            `json:"missing"`
	Divergent map[string][]string `json:"divergent"`
}

// ReadBaseline loads a baseline file. A missing file is an empty baseline,
// which makes every documented operation a regression: that is the right
// failure mode for a repository that forgot to commit the file.
func ReadBaseline(path string) (Baseline, error) {
	b := Baseline{Divergent: map[string][]string{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return b, fmt.Errorf("specoracle: read baseline: %w", err)
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, fmt.Errorf("%s: %w", path, err)
	}
	if b.Divergent == nil {
		b.Divergent = map[string][]string{}
	}
	return b, nil
}

// WriteBaseline records the report's current gaps as the accepted baseline.
func (r Report) WriteBaseline(path string) error {
	b := Baseline{Missing: append([]string(nil), r.Missing...), Divergent: map[string][]string{}}
	for k, v := range r.Divergent {
		b.Divergent[k] = append([]string(nil), v...)
	}
	sort.Strings(b.Missing)
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("specoracle: encode baseline: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("specoracle: write baseline: %w", err)
	}
	return nil
}

// Diff splits the report against a baseline into regressions (new gaps) and
// improvements (baseline entries that no longer apply and can be removed).
type Diff struct {
	Regressions  []string
	Improvements []string
}

// Diff compares the report with the accepted baseline.
func (r Report) Diff(b Baseline) Diff {
	var d Diff
	missing := toSet(b.Missing)
	for _, k := range r.Missing {
		if !missing[k] {
			d.Regressions = append(d.Regressions, "missing operation "+k)
		}
	}
	current := toSet(r.Missing)
	for _, k := range b.Missing {
		if !current[k] {
			d.Improvements = append(d.Improvements, "no longer missing "+k)
		}
	}
	for k, findings := range r.Divergent {
		accepted := toSet(b.Divergent[k])
		for _, f := range findings {
			if !accepted[f] {
				d.Regressions = append(d.Regressions, k+": "+f)
			}
		}
	}
	for k, findings := range b.Divergent {
		now := toSet(r.Divergent[k])
		for _, f := range findings {
			if !now[f] {
				d.Improvements = append(d.Improvements, k+": resolved "+f)
			}
		}
	}
	sort.Strings(d.Regressions)
	sort.Strings(d.Improvements)
	return d
}

func toSet(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, x := range list {
		m[x] = true
	}
	return m
}
