package layout

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/migration/devtools/ledger"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

// scopePattern is the path-except of the exclusion rule that keeps every
// linter but revive to internal/ and migration/ (decision 0044).
const scopePattern = "^(internal|migration)/"

// wantRevive is the whole of revive's settings in .golangci.yml: the two
// documentation rules, with the stuttering check off and
// checkPublicInterface left off (decision 0044).
var wantRevive = map[string]any{
	"rules": []any{
		map[string]any{"name": "exported", "arguments": []any{"disableStutteringCheck"}},
		map[string]any{"name": "package-comments"},
	},
}

// lintConfig holds the parts of .golangci.yml that decide where each linter
// reports.
type lintConfig struct {
	Linters struct {
		Enable     []string       `yaml:"enable"`
		Settings   map[string]any `yaml:"settings"`
		Exclusions struct {
			Presets     []string        `yaml:"presets"`
			Rules       []exclusionRule `yaml:"rules"`
			PathsExcept []string        `yaml:"paths-except"`
		} `yaml:"exclusions"`
	} `yaml:"linters"`
}

// exclusionRule is one entry of linters.exclusions.rules.
type exclusionRule struct {
	Path       string   `yaml:"path"`
	PathExcept string   `yaml:"path-except"`
	Text       string   `yaml:"text"`
	Source     string   `yaml:"source"`
	Linters    []string `yaml:"linters"`
}

// readLintConfig decodes data, the contents of .golangci.yml.
func readLintConfig(t *testing.T, data []byte) lintConfig {
	t.Helper()
	var cfg lintConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestUnitLintScopeListsEveryLinter(t *testing.T) {
	data := repopath.Read(t, ".golangci.yml")
	for _, p := range checkLintScope(readLintConfig(t, data)) {
		t.Error(p)
	}

	// Each change breaks the configuration in one way the check must see.
	for _, tc := range []struct {
		name   string
		change func(*lintConfig)
		want   string
	}{
		{
			name:   "a linter enabled without joining the scope rule",
			change: func(c *lintConfig) { c.Linters.Enable = append(c.Linters.Enable, "dupl") },
			want:   "want every enabled linter but revive",
		},
		{
			name: "revive in the scope rule",
			change: func(c *lintConfig) {
				i := scopeRuleIndex(c)
				c.Linters.Exclusions.Rules[i].Linters = append(c.Linters.Exclusions.Rules[i].Linters, "revive")
			},
			want: "want every enabled linter but revive",
		},
		{
			name:   "no scope rule",
			change: func(c *lintConfig) { c.Linters.Exclusions.Rules[scopeRuleIndex(c)].PathExcept = "^internal/" },
			want:   "0 exclusion rules have path-except",
		},
		{
			name: "a narrowed scope rule",
			change: func(c *lintConfig) {
				c.Linters.Exclusions.Rules[scopeRuleIndex(c)].Text = "SA1019"
			},
			want: "the scope rule must have no condition besides path-except",
		},
		{
			name: "revive not enabled",
			change: func(c *lintConfig) {
				c.Linters.Enable = slices.DeleteFunc(c.Linters.Enable, func(l string) bool { return l == "revive" })
			},
			want: "revive is not enabled",
		},
		{
			name: "another revive rule",
			change: func(c *lintConfig) {
				c.Linters.Settings["revive"] = map[string]any{"enable-all-rules": true}
			},
			want: "revive settings are",
		},
		{
			name: "checkPublicInterface on",
			change: func(c *lintConfig) {
				c.Linters.Settings["revive"] = map[string]any{"rules": []any{
					map[string]any{
						"name":      "exported",
						"arguments": []any{"disableStutteringCheck", "checkPublicInterface"},
					},
					map[string]any{"name": "package-comments"},
				}}
			},
			want: "revive settings are",
		},
		{
			name: "the comments preset",
			change: func(c *lintConfig) {
				c.Linters.Exclusions.Presets = append(c.Linters.Exclusions.Presets, "comments")
			},
			want: "the comments preset",
		},
		{
			name: "paths-except",
			change: func(c *lintConfig) {
				c.Linters.Exclusions.PathsExcept = []string{`^internal/.*\.go$`}
			},
			want: "paths-except",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := readLintConfig(t, data)
			tc.change(&cfg)
			problems := checkLintScope(cfg)
			if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
				t.Fatalf("problems = %q; want one containing %q", problems, tc.want)
			}
		})
	}
}

// scopeRuleIndex returns the index of the scope rule in c, for the cases
// that break it; the configuration under test always has one.
func scopeRuleIndex(c *lintConfig) int {
	return slices.IndexFunc(c.Linters.Exclusions.Rules, func(r exclusionRule) bool {
		return r.PathExcept == scopePattern
	})
}

// checkLintScope returns every way cfg lets a linter other than revive report
// outside internal/ and migration/, or keeps revive's documentation checks
// from reporting.
func checkLintScope(cfg lintConfig) []string {
	var problems []string
	var scope []exclusionRule
	for _, r := range cfg.Linters.Exclusions.Rules {
		if r.PathExcept == scopePattern {
			scope = append(scope, r)
		}
	}
	want := slices.Sorted(slices.Values(slices.DeleteFunc(
		slices.Clone(cfg.Linters.Enable), func(l string) bool { return l == "revive" },
	)))
	switch {
	case len(scope) != 1:
		problems = append(
			problems,
			fmt.Sprintf(
				"%d exclusion rules have path-except %s; want one",
				len(scope),
				scopePattern,
			),
		)
	case scope[0].Path != "" || scope[0].Text != "" || scope[0].Source != "":
		problems = append(
			problems,
			"the scope rule must have no condition besides path-except, or it hides less",
		)
	case !slices.Equal(slices.Sorted(slices.Values(scope[0].Linters)), want):
		problems = append(problems, fmt.Sprintf(
			"the scope rule lists %v; want every enabled linter but revive: %v",
			slices.Sorted(slices.Values(scope[0].Linters)), want,
		))
	}
	if !slices.Contains(cfg.Linters.Enable, "revive") {
		problems = append(problems, "revive is not enabled")
	}
	if got := cfg.Linters.Settings["revive"]; !reflect.DeepEqual(got, wantRevive) {
		problems = append(problems, fmt.Sprintf("revive settings are %v; want %v", got, wantRevive))
	}
	if slices.Contains(cfg.Linters.Exclusions.Presets, "comments") {
		problems = append(problems, "the comments preset silences revive's documentation checks")
	}
	if len(cfg.Linters.Exclusions.PathsExcept) > 0 {
		problems = append(
			problems,
			"paths-except hides every linter outside its patterns, revive included",
		)
	}
	return problems
}

func TestUnitLintLegacyRuleMatchesLedgerLegacyRoots(t *testing.T) {
	data := repopath.Read(t, ".golangci.yml")
	for _, p := range checkLegacyRule(readLintConfig(t, data), ledger.LegacyRegexp()) {
		t.Error(p)
	}

	for _, tc := range []struct {
		name   string
		change func(*lintConfig)
		want   string
	}{
		{
			name: "a root missing from the pattern",
			change: func(c *lintConfig) {
				i := legacyRuleIndex(c)
				c.Linters.Exclusions.Rules[i].Path = strings.Replace(c.Linters.Exclusions.Rules[i].Path, "cdn|", "", 1)
			},
			want: fmt.Sprintf("want path %q", ledger.LegacyRegexp()),
		},
		{
			name: "no legacy rule",
			change: func(c *lintConfig) {
				c.Linters.Exclusions.Rules = slices.Delete(c.Linters.Exclusions.Rules, legacyRuleIndex(c), legacyRuleIndex(c)+1)
			},
			want: "0 exclusion rules name revive",
		},
		{
			name: "revive silenced elsewhere",
			change: func(c *lintConfig) {
				c.Linters.Exclusions.Rules = append(c.Linters.Exclusions.Rules, exclusionRule{
					Path: "^test/", Linters: []string{"revive", "gosec"},
				})
			},
			want: "2 exclusion rules name revive",
		},
		{
			name: "a narrowed legacy rule",
			change: func(c *lintConfig) {
				c.Linters.Exclusions.Rules[legacyRuleIndex(c)].Text = "exported"
			},
			want: "and no other condition",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := readLintConfig(t, data)
			tc.change(&cfg)
			problems := checkLegacyRule(cfg, ledger.LegacyRegexp())
			if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
				t.Fatalf("problems = %q; want one containing %q", problems, tc.want)
			}
		})
	}
}

// legacyRuleIndex returns the index of the rule that silences revive alone,
// for the cases that break it; the configuration under test always has one.
func legacyRuleIndex(c *lintConfig) int {
	return slices.IndexFunc(c.Linters.Exclusions.Rules, func(r exclusionRule) bool {
		return slices.Equal(r.Linters, []string{"revive"})
	})
}

// checkLegacyRule reports unless exactly one exclusion rule names revive: the
// one that silences revive alone in the legacy roots, whose path is want,
// ledger.LegacyRegexp(). The ledger is where the legacy roots are defined, so
// the lint exclusion cannot drift from the roots the migration empties.
func checkLegacyRule(cfg lintConfig, want string) []string {
	var rules []exclusionRule
	for _, r := range cfg.Linters.Exclusions.Rules {
		if slices.Contains(r.Linters, "revive") {
			rules = append(rules, r)
		}
	}
	switch r := rules; {
	case len(r) != 1:
		return []string{
			fmt.Sprintf("%d exclusion rules name revive; want one, for the legacy roots", len(r)),
		}
	case r[0].Path != want || r[0].PathExcept != "" || r[0].Text != "" || r[0].Source != "" ||
		!slices.Equal(r[0].Linters, []string{"revive"}):
		return []string{
			fmt.Sprintf(
				"the rule that names revive has path %q, path-except %q, text %q, source %q and linters %v; "+
					"want path %q from ledger.LegacyRegexp, linters [revive] and no other condition",
				r[0].Path,
				r[0].PathExcept,
				r[0].Text,
				r[0].Source,
				r[0].Linters,
				want,
			),
		}
	}
	return nil
}
