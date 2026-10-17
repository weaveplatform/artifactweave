// Package layout implements Artifactory repository layouts: the path patterns
// that tell the server which part of an artifact path is the organisation,
// module, base revision, integration revision, classifier and extension. A
// layout drives version search, snapshot cleanup and cross-layout copy, so it
// has to parse and build paths exactly as Artifactory does. The pattern syntax
// is documented at https://docs.jfrog.com/artifactory/docs/repository-layouts.md
// and summarised in docs/research/03-repository-configuration-model.md.
package layout

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Definition is a repository layout as stored in the configuration descriptor
// (repoLayouts.<name>).
type Definition struct {
	Name                             string
	ArtifactPathPattern              string
	DistinctiveDescriptorPathPattern bool
	DescriptorPathPattern            string
	FolderIntegrationRevisionRegExp  string
	FileIntegrationRevisionRegExp    string
}

// Values are the token values extracted from, or used to build, a path.
// Custom tokens are keyed by their declared name.
type Values map[string]string

// Token names with a built-in meaning.
const (
	TokenOrg           = "org"
	TokenOrgPath       = "orgPath"
	TokenModule        = "module"
	TokenBaseRev       = "baseRev"
	TokenFolderItegRev = "folderItegRev"
	TokenFileItegRev   = "fileItegRev"
	TokenClassifier    = "classifier"
	TokenExt           = "ext"
	TokenType          = "type"
)

// ErrPattern reports a malformed pattern.
var ErrPattern = errors.New("invalid layout pattern")

// ErrNoMatch reports a path that does not fit the layout.
var ErrNoMatch = errors.New("path does not match layout")

// ErrMissingValue reports a Build call without a mandatory token value.
var ErrMissingValue = errors.New("missing value for layout token")

// Layout is a compiled Definition.
type Layout struct {
	def        Definition
	artifact   *pattern
	descriptor *pattern
}

// Compile parses the definition's patterns. The integration-revision regexps
// are substituted into [folderItegRev] and [fileItegRev] at compile time, so
// a layout with an empty regexp gets Artifactory's permissive ".+" and still
// matches.
func Compile(def Definition) (*Layout, error) {
	l := &Layout{def: def}
	var err error
	if l.artifact, err = compilePattern(def.ArtifactPathPattern, def); err != nil {
		return nil, fmt.Errorf("layout %q artifact pattern: %w", def.Name, err)
	}
	if def.DescriptorPathPattern != "" {
		if l.descriptor, err = compilePattern(def.DescriptorPathPattern, def); err != nil {
			return nil, fmt.Errorf("layout %q descriptor pattern: %w", def.Name, err)
		}
	}
	return l, nil
}

// Definition returns the source definition.
func (l *Layout) Definition() Definition { return l.def }

// versionTokens are the version tokens a download path may carry where an
// integration revision goes.
const versionTokens = `|\[INTEGRATION\]|\[RELEASE\]`

// VersionTokens returns the layout a download path that may ask for a
// version token is read with: [INTEGRATION] and [RELEASE] also stand for
// either integration revision, as Artifactory reads a request
// (RepoLayoutUtils.getTokenRegExp with version tokens). A base revision
// already reads [RELEASE]. Without this, the lazy base revision would take
// in a following -[INTEGRATION], and the request would read as a release.
// The definition stays the layout's own.
func (l *Layout) VersionTokens() (*Layout, error) {
	def := l.def
	def.FolderIntegrationRevisionRegExp = orDefault(
		def.FolderIntegrationRevisionRegExp,
	) + versionTokens
	def.FileIntegrationRevisionRegExp = orDefault(def.FileIntegrationRevisionRegExp) + versionTokens
	out, err := Compile(def)
	if err != nil {
		return nil, err
	}
	out.def = l.def
	return out, nil
}

// Match extracts token values from an artifact path. When the layout has a
// distinctive descriptor pattern that pattern is tried first, because a POM
// also matches the artifact pattern with ext=pom and the descriptor match is
// the more specific one.
func (l *Layout) Match(path string) (Values, error) {
	if l.descriptor != nil && l.def.DistinctiveDescriptorPathPattern {
		if v, ok := l.descriptor.match(path); ok {
			return v, nil
		}
	}
	if v, ok := l.artifact.match(path); ok {
		return v, nil
	}
	return nil, fmt.Errorf("%w %q: %s", ErrNoMatch, l.def.Name, path)
}

// IsDescriptor reports whether the path matches the descriptor pattern.
func (l *Layout) IsDescriptor(path string) bool {
	if l.descriptor == nil {
		return false
	}
	_, ok := l.descriptor.match(path)
	return ok
}

// Build renders an artifact path from token values. Optional groups are
// emitted only when every token inside them has a value; mandatory tokens
// without a value are an error.
func (l *Layout) Build(v Values) (string, error) {
	return l.artifact.build(v)
}

// BuildDescriptor renders a descriptor path, or an error when the layout has
// no descriptor pattern.
func (l *Layout) BuildDescriptor(v Values) (string, error) {
	if l.descriptor == nil {
		return "", fmt.Errorf("%w: layout %q has no descriptor pattern", ErrPattern, l.def.Name)
	}
	return l.descriptor.build(v)
}

// Has reports whether the artifact pattern uses token; org and orgPath
// stand for each other, as they do in Match and Build.
func (l *Layout) Has(token string) bool {
	if token == TokenOrg || token == TokenOrgPath {
		return hasToken(l.artifact.nodes, TokenOrg) || hasToken(l.artifact.nodes, TokenOrgPath)
	}
	return hasToken(l.artifact.nodes, token)
}

func hasToken(nodes []node, name string) bool {
	for _, n := range nodes {
		if n.token == name || hasToken(n.group, name) {
			return true
		}
	}
	return false
}

// Prefixes return the text every path the layout matches with the tokens in
// v starts with: the artifact pattern's, and the descriptor pattern's when
// there is one, each rendered up to its first optional group or token
// without a value. Values are copied as given, so one with * or ? makes a
// prefix a glob. A search narrows by them before Match decides.
func (l *Layout) Prefixes(v Values) []string {
	out := []string{prefix(l.artifact.nodes, v)}
	if l.descriptor != nil {
		if p := prefix(l.descriptor.nodes, v); p != out[0] {
			out = append(out, p)
		}
	}
	return out
}

func prefix(nodes []node, v Values) string {
	var sb strings.Builder
	for _, n := range nodes {
		switch {
		case n.group != nil:
			return sb.String()
		case n.token != "":
			val, ok := lookup(v, n.token)
			if !ok {
				return sb.String()
			}
			sb.WriteString(val)
		default:
			sb.WriteString(n.literal)
		}
	}
	return sb.String()
}

// OrgPath converts a dotted organisation into the slash form used by
// [orgPath]; Artifactory derives one from the other the same way.
func OrgPath(org string) string {
	return strings.ReplaceAll(org, ".", "/")
}

// Org is the inverse of OrgPath.
func Org(orgPath string) string {
	return strings.ReplaceAll(orgPath, "/", ".")
}

// node is one element of a parsed pattern: a literal, a token or an optional
// group of nodes.
type node struct {
	literal string
	token   string
	regex   string
	group   []node
}

type pattern struct {
	nodes []node
	re    *regexp.Regexp
	names []string
	// repeated is set when a token appears more than once. RE2 has no
	// back-references, so such patterns are matched in two passes: the first
	// captures the first occurrence of each token, the second pins the later
	// occurrences to those values and must still match.
	repeated bool
}

// tokenRegexps are the expressions Artifactory uses for the built-in tokens
// (RepoLayoutUtils.getTokenRegExp in 6.20). Organisation, module, revision and
// classifier stop at a slash; the extension additionally must not start with a
// digit or contain a hyphen so that "1.0-sources.jar" splits into baseRev,
// classifier and ext rather than swallowing the classifier into the extension.
// "7z" is the one documented exception to the leading-digit rule. Module alone
// is greedy: where it appears only in a file name, as in maven-1-default and
// build-default, commons-lang-2.1.jar is module commons-lang, not commons.
var tokenRegexps = map[string]string{
	TokenOrg:        `[^/]+?`,
	TokenOrgPath:    `.+?`,
	TokenModule:     `[^/]+`,
	TokenBaseRev:    `[^/]+?`,
	TokenClassifier: `[^/]+?`,
	TokenExt:        `(?:(?:[^\d/\-][^/\-]*)|7z)`,
	TokenType:       `[^/]+?`,
}

func compilePattern(src string, def Definition) (*pattern, error) {
	nodes, rest, err := parseNodes(src, def, false)
	if err != nil {
		return nil, err
	}
	if rest != "" {
		return nil, fmt.Errorf("%w: unexpected %q", ErrPattern, rest)
	}
	p := &pattern{nodes: nodes}
	p.re, p.repeated, err = compileRegexp(nodes, nil, &p.names)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// compileRegexp builds the anchored expression for nodes. When pinned is
// non-nil, repeated occurrences of a token whose value is known are emitted
// as the quoted literal instead of the token expression.
func compileRegexp(nodes []node, pinned Values, names *[]string) (*regexp.Regexp, bool, error) {
	var sb strings.Builder
	sb.WriteString("^")
	seen := map[string]bool{}
	repeated := writeRegexp(&sb, nodes, seen, names, pinned)
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrPattern, err)
	}
	return re, repeated, nil
}

// parseNodes consumes the pattern until the end or, inside a group, the
// closing parenthesis. It returns the unconsumed remainder so the caller can
// detect a stray ")".
func parseNodes(src string, def Definition, inGroup bool) ([]node, string, error) {
	var nodes []node
	for src != "" {
		switch src[0] {
		case '[':
			end := tokenEnd(src)
			if end < 0 {
				return nil, "", fmt.Errorf("%w: unterminated token in %q", ErrPattern, src)
			}
			tok, err := parseToken(src[1:end], def)
			if err != nil {
				return nil, "", err
			}
			nodes = append(nodes, tok)
			src = src[end+1:]
		case '(':
			inner, rest, err := parseNodes(src[1:], def, true)
			if err != nil {
				return nil, "", err
			}
			if !strings.HasPrefix(rest, ")") {
				return nil, "", fmt.Errorf("%w: unterminated group", ErrPattern)
			}
			nodes = append(nodes, node{group: inner})
			src = rest[1:]
		case ')':
			if inGroup {
				return nodes, src, nil
			}
			return nil, "", fmt.Errorf("%w: unexpected \")\"", ErrPattern)
		default:
			end := strings.IndexAny(src, "[()")
			if end < 0 {
				end = len(src)
			}
			nodes = append(nodes, node{literal: src[:end]})
			src = src[end:]
		}
	}
	return nodes, "", nil
}

// tokenEnd finds the "]" that closes the token starting at src[0]. A custom
// token's regexp may itself contain "]" (as in [^/]), so when a "<" appears
// before the first "]" the token ends at the first ">]" instead.
func tokenEnd(src string) int {
	first := strings.IndexByte(src, ']')
	lt := strings.IndexByte(src, '<')
	if lt < 0 || (first >= 0 && lt > first) {
		return first
	}
	close := strings.Index(src[lt:], ">]")
	if close < 0 {
		return -1
	}
	return lt + close + 1
}

// parseToken turns the text between brackets into a token node. A custom
// token is "name<regex>"; the regexp is used as written, wrapped in a
// non-capturing group, which is why Artifactory tells layout authors not to
// put capturing groups inside it.
func parseToken(body string, def Definition) (node, error) {
	if lt := strings.IndexByte(body, '<'); lt >= 0 {
		if !strings.HasSuffix(body, ">") {
			return node{}, fmt.Errorf("%w: malformed custom token %q", ErrPattern, body)
		}
		name := body[:lt]
		if name == "" {
			return node{}, fmt.Errorf("%w: custom token without a name", ErrPattern)
		}
		return node{token: name, regex: body[lt+1 : len(body)-1]}, nil
	}
	switch body {
	case TokenFolderItegRev:
		return node{token: body, regex: orDefault(def.FolderIntegrationRevisionRegExp)}, nil
	case TokenFileItegRev:
		return node{token: body, regex: orDefault(def.FileIntegrationRevisionRegExp)}, nil
	}
	re, ok := tokenRegexps[body]
	if !ok {
		return node{}, fmt.Errorf("%w: unknown token %q", ErrPattern, body)
	}
	return node{token: body, regex: re}, nil
}

func orDefault(re string) string {
	if re == "" {
		return ".+"
	}
	return re
}

// writeRegexp emits named groups for the first occurrence of each token.
// Artifactory honours only the first occurrence's expression and treats later
// ones as back-references; here they are matched with the same expression (or
// the pinned literal) and the first value wins when extracting. It reports
// whether any token repeated.
func writeRegexp(
	sb *strings.Builder,
	nodes []node,
	seen map[string]bool,
	names *[]string,
	pinned Values,
) bool {
	repeated := false
	for _, n := range nodes {
		switch {
		case n.group != nil:
			sb.WriteString("(?:")
			if writeRegexp(sb, n.group, seen, names, pinned) {
				repeated = true
			}
			sb.WriteString(")?")
		case n.token != "":
			name := n.token
			if seen[name] {
				repeated = true
				if val, ok := pinned[name]; ok {
					sb.WriteString(regexp.QuoteMeta(val))
				} else {
					sb.WriteString("(?:" + n.regex + ")")
				}
				continue
			}
			seen[name] = true
			if names != nil {
				*names = append(*names, name)
			}
			sb.WriteString("(?P<" + name + ">" + n.regex + ")")
		default:
			sb.WriteString(regexp.QuoteMeta(n.literal))
		}
	}
	return repeated
}

func (p *pattern) match(path string) (Values, bool) {
	v, ok := capture(p.re, path)
	if !ok {
		return nil, false
	}
	if p.repeated {
		// Second pass: the first-occurrence values are now literals, so a
		// path such as com/acme/app/1.0/other-1.0.jar, whose folder and file
		// disagree on the module, is rejected and an ambiguous file name is
		// split the way the folder dictates.
		re, _, err := compileRegexp(p.nodes, v, nil)
		if err != nil {
			return nil, false
		}
		if v, ok = capture(re, path); !ok {
			return nil, false
		}
	}
	// org and orgPath are two spellings of one value; callers matching a
	// Maven path want both available.
	if org, ok := v[TokenOrgPath]; ok {
		if _, has := v[TokenOrg]; !has {
			v[TokenOrg] = Org(org)
		}
	} else if org, ok := v[TokenOrg]; ok {
		v[TokenOrgPath] = OrgPath(org)
	}
	return v, true
}

func capture(re *regexp.Regexp, path string) (Values, bool) {
	m := re.FindStringSubmatch(path)
	if m == nil {
		return nil, false
	}
	v := Values{}
	for i, name := range re.SubexpNames() {
		if name != "" && m[i] != "" {
			v[name] = m[i]
		}
	}
	return v, true
}

func (p *pattern) build(v Values) (string, error) {
	var sb strings.Builder
	if err := buildNodes(&sb, p.nodes, v); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// buildNodes renders nodes into sb. A group whose tokens lack a value is
// skipped, which is what makes it optional; a bare token without a value is
// an error, and that error propagates out of any enclosing group so the group
// as a whole is skipped rather than half-rendered.
func buildNodes(sb *strings.Builder, nodes []node, v Values) error {
	for _, n := range nodes {
		switch {
		case n.group != nil:
			var inner strings.Builder
			if err := buildNodes(&inner, n.group, v); err != nil {
				continue
			}
			sb.WriteString(inner.String())
		case n.token != "":
			val, ok := lookup(v, n.token)
			if !ok {
				return fmt.Errorf("%w %q", ErrMissingValue, n.token)
			}
			sb.WriteString(val)
		default:
			sb.WriteString(n.literal)
		}
	}
	return nil
}

// lookup resolves a token value, deriving org from orgPath and vice versa so
// callers can supply either spelling.
func lookup(v Values, token string) (string, bool) {
	if val, ok := v[token]; ok && val != "" {
		return val, true
	}
	switch token {
	case TokenOrgPath:
		if org, ok := v[TokenOrg]; ok && org != "" {
			return OrgPath(org), true
		}
	case TokenOrg:
		if orgPath, ok := v[TokenOrgPath]; ok && orgPath != "" {
			return Org(orgPath), true
		}
	}
	return "", false
}
