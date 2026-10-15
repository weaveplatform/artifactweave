package types

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// ErrInvalidScope reports a token scope string the grammar does not accept.
var ErrInvalidScope = errors.New("invalid token scope")

// MaxScopeLength is the longest scope string Access accepts on token create.
const MaxScopeLength = 500

// ScopeKind is the kind of one scope entry (research 05, scope grammar).
type ScopeKind int

// Scope kinds.
const (
	ScopeUser     ScopeKind = iota + 1 // applied-permissions/user
	ScopeAdmin                         // applied-permissions/admin
	ScopeGroups                        // applied-permissions/groups:"a","b"
	ScopeRoles                         // applied-permissions/roles:<project>:<role>,...
	ScopeArtifact                      // artifact:<repo|*>[/<ant path>]:<actions>
	ScopeRepo                          // repo:<key>:<actions>
	ScopeProject                       // project:<key>/<area>:<actions>
	ScopeSystem                        // system:<area>[@<qualifier>]:<actions>
	ScopeBuild                         // build:<build-info repo|*>[/<ant path>]:<actions>
)

// ScopeActions are the resource-scope action letters r w d a x s m, or all.
type ScopeActions uint16

// Scope actions. ScopeAll is "*".
const (
	ScopeRead ScopeActions = 1 << iota
	ScopeWrite
	ScopeDelete
	ScopeAnnotate
	ScopeExecute
	ScopeScan
	ScopeManage
	ScopeAll
)

const scopeLetters = "rwdaxsm"

// Covers reports whether a grants everything b does.
func (a ScopeActions) Covers(b ScopeActions) bool {
	if a&ScopeAll != 0 {
		return true
	}
	return b&ScopeAll == 0 && b&^a == 0
}

func (a ScopeActions) String() string {
	if a&ScopeAll != 0 {
		return "*"
	}
	var parts []string
	for i, l := range scopeLetters {
		if a&(1<<i) != 0 {
			parts = append(parts, string(l))
		}
	}
	return strings.Join(parts, ",")
}

func parseScopeActions(s string) (ScopeActions, error) {
	if s == "*" {
		return ScopeAll, nil
	}
	var a ScopeActions
	for _, part := range strings.Split(s, ",") {
		i := strings.Index(scopeLetters, part)
		if len(part) != 1 || i < 0 {
			return 0, fmt.Errorf("%w: action %q", ErrInvalidScope, part)
		}
		a |= 1 << i
	}
	return a, nil
}

// ScopeEntry is one space-separated element of a scope string. Which fields
// are set depends on Kind: Names for groups and roles, Project for roles and
// project scopes, Target for the repository (artifact, build, repo), the
// project area or the system area, Path for an artifact or build ant path
// (a build's path is its document's, <name>/<number>-<started>.json),
// Qualifier for the
// part of a system scope after '@'.
type ScopeEntry struct {
	Kind      ScopeKind
	Names     []string
	Project   string
	Target    string
	Path      string
	Qualifier string
	Actions   ScopeActions
}

// Scope is a parsed token scope.
type Scope struct {
	Entries []ScopeEntry
}

// UserScope is the default scope of an identity token.
func UserScope() Scope { return Scope{Entries: []ScopeEntry{{Kind: ScopeUser}}} }

// ParseScope reads a scope string. Entries are separated by whitespace
// outside double quotes, because group and role names may be quoted and may
// contain spaces ("Project Admin").
func ParseScope(s string) (Scope, error) {
	if len(s) > MaxScopeLength {
		return Scope{}, fmt.Errorf("%w: longer than %d characters", ErrInvalidScope, MaxScopeLength)
	}
	// A scope arrives as a JSON string or a form value, so invalid UTF-8 is
	// never legitimate; refusing it also keeps the rendered form byte-for-byte
	// identical to what was parsed.
	if !utf8.ValidString(s) {
		return Scope{}, fmt.Errorf("%w: not valid UTF-8", ErrInvalidScope)
	}
	tokens, err := scopeTokens(s)
	if err != nil {
		return Scope{}, err
	}
	if len(tokens) == 0 {
		return Scope{}, fmt.Errorf("%w: empty", ErrInvalidScope)
	}
	sc := Scope{Entries: make([]ScopeEntry, 0, len(tokens))}
	for _, t := range tokens {
		e, err := parseScopeEntry(t)
		if err != nil {
			return Scope{}, err
		}
		sc.Entries = append(sc.Entries, e)
	}
	return sc, nil
}

func scopeTokens(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			quoted = !quoted
			cur.WriteByte(c)
		case !quoted && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if quoted {
		return nil, fmt.Errorf("%w: unterminated quote", ErrInvalidScope)
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
}

const appliedPrefix = "applied-permissions/"

func parseScopeEntry(t string) (ScopeEntry, error) {
	if rest, ok := strings.CutPrefix(t, appliedPrefix); ok {
		return parseApplied(rest)
	}
	// Resource scopes are <type>:<middle>:<actions>. The type ends at the
	// first colon and the actions start after the last one, because the
	// middle may itself contain colons (a webhook scope's qualifier is a
	// URL).
	first, last := strings.Index(t, ":"), strings.LastIndex(t, ":")
	if first < 0 || first == last {
		return ScopeEntry{}, fmt.Errorf("%w: %q", ErrInvalidScope, t)
	}
	kind, middle := t[:first], t[first+1:last]
	actions, err := parseScopeActions(t[last+1:])
	if err != nil {
		return ScopeEntry{}, err
	}
	e := ScopeEntry{Actions: actions}
	switch kind {
	case "artifact":
		e.Kind = ScopeArtifact
		e.Target, e.Path, _ = strings.Cut(middle, "/")
	case "build":
		// build:artifactory-build-info/<pattern>:r lets a non-admin run
		// AQL builds queries (research 22 §1.3).
		e.Kind = ScopeBuild
		e.Target, e.Path, _ = strings.Cut(middle, "/")
	case "repo":
		e.Kind = ScopeRepo
		e.Target = middle
	case "project":
		e.Kind = ScopeProject
		var ok bool
		if e.Project, e.Target, ok = strings.Cut(middle, "/"); !ok || e.Target == "" {
			return ScopeEntry{}, fmt.Errorf(
				"%w: project scope needs <key>/<area>: %q",
				ErrInvalidScope,
				t,
			)
		}
	case "system":
		e.Kind = ScopeSystem
		e.Target, e.Qualifier, _ = strings.Cut(middle, "@")
	default:
		return ScopeEntry{}, fmt.Errorf("%w: unknown resource %q", ErrInvalidScope, kind)
	}
	if e.Target == "" || (e.Kind == ScopeProject && e.Project == "") {
		return ScopeEntry{}, fmt.Errorf("%w: missing target in %q", ErrInvalidScope, t)
	}
	return e, nil
}

func parseApplied(rest string) (ScopeEntry, error) {
	switch {
	case rest == "user":
		return ScopeEntry{Kind: ScopeUser}, nil
	case rest == "admin":
		return ScopeEntry{Kind: ScopeAdmin}, nil
	case strings.HasPrefix(rest, "groups:"):
		names, err := parseNameList(strings.TrimPrefix(rest, "groups:"))
		if err != nil {
			return ScopeEntry{}, err
		}
		return ScopeEntry{Kind: ScopeGroups, Names: names}, nil
	case strings.HasPrefix(rest, "roles:"):
		project, list, ok := strings.Cut(strings.TrimPrefix(rest, "roles:"), ":")
		if !ok || project == "" || strings.Contains(project, `"`) {
			return ScopeEntry{}, fmt.Errorf(
				"%w: roles scope needs <project>:<roles>",
				ErrInvalidScope,
			)
		}
		names, err := parseNameList(list)
		if err != nil {
			return ScopeEntry{}, err
		}
		return ScopeEntry{Kind: ScopeRoles, Project: project, Names: names}, nil
	default:
		return ScopeEntry{}, fmt.Errorf("%w: %q", ErrInvalidScope, appliedPrefix+rest)
	}
}

// parseNameList reads a comma-separated list of names, each optionally in
// double quotes. Quotes cannot be escaped, so a name never contains one.
func parseNameList(s string) ([]string, error) {
	var names []string
	for {
		var name string
		if strings.HasPrefix(s, `"`) {
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				return nil, fmt.Errorf("%w: unterminated quote", ErrInvalidScope)
			}
			name, s = s[1:1+end], s[2+end:]
		} else {
			i := strings.IndexByte(s, ',')
			if i < 0 {
				i = len(s)
			}
			name, s = s[:i], s[i:]
			if strings.Contains(name, `"`) {
				return nil, fmt.Errorf("%w: quote inside name %q", ErrInvalidScope, name)
			}
		}
		if name == "" {
			return nil, fmt.Errorf("%w: empty name", ErrInvalidScope)
		}
		names = append(names, name)
		if s == "" {
			return names, nil
		}
		if s[0] != ',' {
			return nil, fmt.Errorf("%w: text after quoted name", ErrInvalidScope)
		}
		s = s[1:]
	}
}

func renderNames(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		if strings.ContainsAny(n, " ,\t\r\n") {
			out[i] = `"` + n + `"`
		} else {
			out[i] = n
		}
	}
	return strings.Join(out, ",")
}

// String renders the entry in canonical form: names quoted only when they
// need it, actions in r,w,d,a,x,s,m order.
func (e ScopeEntry) String() string {
	switch e.Kind {
	case ScopeUser:
		return appliedPrefix + "user"
	case ScopeAdmin:
		return appliedPrefix + "admin"
	case ScopeGroups:
		return appliedPrefix + "groups:" + renderNames(e.Names)
	case ScopeRoles:
		return appliedPrefix + "roles:" + e.Project + ":" + renderNames(e.Names)
	case ScopeArtifact, ScopeBuild:
		target := e.Target
		if e.Path != "" {
			target += "/" + e.Path
		}
		kind := "artifact:"
		if e.Kind == ScopeBuild {
			kind = "build:"
		}
		return kind + target + ":" + e.Actions.String()
	case ScopeRepo:
		return "repo:" + e.Target + ":" + e.Actions.String()
	case ScopeProject:
		return "project:" + e.Project + "/" + e.Target + ":" + e.Actions.String()
	default:
		target := e.Target
		if e.Qualifier != "" {
			target += "@" + e.Qualifier
		}
		return "system:" + target + ":" + e.Actions.String()
	}
}

// String renders the scope in canonical form, entries separated by a space.
func (s Scope) String() string {
	parts := make([]string, len(s.Entries))
	for i, e := range s.Entries {
		parts[i] = e.String()
	}
	return strings.Join(parts, " ")
}

// IsAdmin reports whether the scope applies administrator permissions.
func (s Scope) IsAdmin() bool {
	return slices.ContainsFunc(s.Entries, func(e ScopeEntry) bool { return e.Kind == ScopeAdmin })
}

// Narrows reports whether s grants nothing beyond parent, which is the rule
// for issuing a token from another token or for a non-admin creating one:
// scopes only narrow (decision 0005). Resource scopes (artifact, build,
// repo, project) intersect the subject's applied permissions, so any applied entry
// covers them; system scopes grant capabilities the subject's permissions do
// not, so only admin or a matching system scope covers them.
func (s Scope) Narrows(parent Scope) bool {
	for _, e := range s.Entries {
		if !slices.ContainsFunc(
			parent.Entries,
			func(p ScopeEntry) bool { return scopeCovers(p, e) },
		) {
			return false
		}
	}
	return true
}

func scopeCovers(p, e ScopeEntry) bool {
	if p.Kind == ScopeAdmin {
		return true
	}
	switch e.Kind {
	case ScopeAdmin:
		return false
	case ScopeUser:
		return p.Kind == ScopeUser
	case ScopeGroups:
		return p.Kind == ScopeGroups && subset(e.Names, p.Names)
	case ScopeRoles:
		return p.Kind == ScopeRoles && p.Project == e.Project && subset(e.Names, p.Names)
	case ScopeSystem:
		return p.Kind == ScopeSystem && areaCovers(p.Target, e.Target) &&
			(p.Qualifier == "" || p.Qualifier == e.Qualifier) && p.Actions.Covers(e.Actions)
	}
	if p.Kind == ScopeUser || p.Kind == ScopeGroups || p.Kind == ScopeRoles {
		return true
	}
	if p.Kind != e.Kind || !p.Actions.Covers(e.Actions) {
		return false
	}
	switch e.Kind {
	case ScopeArtifact, ScopeBuild:
		return (p.Target == "*" || p.Target == e.Target) && pathCovers(p.Path, e.Path)
	case ScopeRepo:
		return p.Target == "*" || p.Target == e.Target
	default: // ScopeProject
		return p.Project == e.Project && areaCovers(p.Target, e.Target)
	}
}

func subset(a, b []string) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

func areaCovers(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, parent+"/")
}

// pathCovers compares ant paths conservatively: an empty or "**" parent
// covers everything, "dir/**" covers anything under dir, and otherwise only
// an identical path is covered. Anything subtler is refused rather than
// guessed, which can only make a narrowing request fail, never widen it.
func pathCovers(parent, child string) bool {
	if parent == "" || parent == "**" || parent == child {
		return true
	}
	if prefix, ok := strings.CutSuffix(parent, "/**"); ok && child != "" {
		return child == prefix || strings.HasPrefix(child, prefix+"/")
	}
	return false
}
