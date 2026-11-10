package repo

import (
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel/antpath"
)

// RepoPatterns are a repository's include and exclude patterns: Ant
// patterns, comma-separated in the configuration. AQL filters what it
// finds with them, and the generic format refuses writes they reject.
type RepoPatterns struct {
	include, exclude []antpath.Pattern
}

// PatternsOf compiles c's patterns. An unset or empty includesPattern
// includes everything, as Artifactory's default "**/*" does.
func PatternsOf(c Config) RepoPatterns {
	include := compilePatterns(c.IncludesPattern)
	if len(include) == 0 {
		include = []antpath.Pattern{antpath.CompilePattern(DefaultIncludesPattern)}
	}
	return RepoPatterns{include: include, exclude: compilePatterns(c.ExcludesPattern)}
}

func compilePatterns(s *string) []antpath.Pattern {
	if s == nil {
		return nil
	}
	var out []antpath.Pattern
	for _, p := range strings.Split(*s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, antpath.CompilePattern(p))
		}
	}
	return out
}

// Accepts reports whether the patterns admit path. A folder is admitted
// when an include could match something below it, so the folders leading to
// included files stay visible.
func (p RepoPatterns) Accepts(path string, folder bool) bool {
	for _, x := range p.exclude {
		if x.Match(path) {
			return false
		}
	}
	for _, in := range p.include {
		if in.Match(path) || (folder && in.MatchBelow(path)) {
			return true
		}
	}
	return false
}
