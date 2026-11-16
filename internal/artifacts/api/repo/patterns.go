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

// AcceptsRequest reports whether the patterns admit a request for path, as
// Artifactory decides it before it looks for the item (7.161.26
// RepoBase.accepts): the root ("") and a system path (systemPath) whatever
// the patterns say, any other path as Accepts does. path is the one
// Artifactory checks: the archive of an archive entry and the file a
// checksum sidecar describes.
func (p RepoPatterns) AcceptsRequest(path string, folder bool) bool {
	return path == "" || systemPath(path) || p.Accepts(path, folder)
}

// systemPath reports a path Artifactory's include and exclude patterns do
// not apply to: a Maven index file, whose name starts
// nexus-maven-repository-index, and any path ending .index, such as P2's
// p2.index (NamingUtils.isSystem, which RepoBase.accepts passes before the
// patterns, in Artifactory OSS 6.20). 7.x documents nothing either way, so
// this follows 6.20 until a capture shows otherwise (#216).
func systemPath(p string) bool {
	name := p[strings.LastIndexByte(p, '/')+1:]
	return strings.HasPrefix(name, "nexus-maven-repository-index") || strings.HasSuffix(p, ".index")
}
