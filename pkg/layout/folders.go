package layout

import (
	"regexp"
	"strings"
)

// FolderPrefix is the longest complete directory prefix determined by Values.
// Bound contains only tokens determined before its final slash. A token known
// only in the filename cannot authorize a version or module directory.
type FolderPrefix struct {
	Path       string
	Bound      Values
	Descriptor bool
}

// FolderPrefixes renders known values through optional groups, distinguishing
// an explicitly absent optional token (empty value) from an unknown token.
// Unlike Prefixes, its output is always a complete folder, never part of a name.
func (l *Layout) FolderPrefixes(v Values) []FolderPrefix {
	out := []FolderPrefix{folderPrefix(l.artifact.nodes, v)}
	if l.descriptor != nil {
		p := folderPrefix(l.descriptor.nodes, v)
		p.Descriptor = true
		out = append(out, p)
	}
	return out
}

type folderMark struct {
	name, value string
	end         int
}

type folderRenderer struct {
	text  strings.Builder
	marks []folderMark
	v     Values
}

func folderPrefix(nodes []node, v Values) FolderPrefix {
	r := folderRenderer{v: v}
	r.render(nodes)
	s := r.text.String()
	end := strings.LastIndexByte(s, '/')
	out := FolderPrefix{Bound: Values{}}
	if end < 0 {
		return out
	}
	out.Path = s[:end]
	for _, mark := range r.marks {
		if mark.end <= end {
			out.Bound[mark.name] = mark.value
		}
	}
	return out
}

func (r *folderRenderer) render(nodes []node) bool {
	for _, n := range nodes {
		switch {
		case n.dropped != "":
			return false
		case n.group != nil:
			known, absent := r.group(n.group)
			if !known {
				return false
			}
			if !absent && !r.render(n.group) {
				return false
			}
		case n.token != "":
			value, known := folderValue(r.v, n.token)
			if !known || value == "" {
				return false
			}
			if !regexp.MustCompile("^(?:" + n.regex + ")$").MatchString(value) {
				return false
			}
			r.text.WriteString(value)
			r.mark(n.token, value)
		default:
			r.text.WriteString(n.literal)
		}
	}
	return true
}

// Optional absence is a known value. Keep its mark so a release folder can be
// distinguished from an unresolved integration suffix without inventing files.
func (r *folderRenderer) group(nodes []node) (known, absent bool) {
	known = true
	for _, n := range nodes {
		if n.group != nil {
			k, a := r.group(n.group)
			known, absent = known && k, absent || a
		} else if n.token != "" {
			value, ok := folderValue(r.v, n.token)
			known, absent = known && ok, absent || value == ""
			if ok && value == "" {
				r.mark(n.token, "")
			}
		}
	}
	return known, absent
}

func (r *folderRenderer) mark(name, value string) {
	if name == TokenOrgPath {
		name, value = TokenOrg, Org(value)
	}
	r.marks = append(r.marks, folderMark{name, value, r.text.Len()})
}

func folderValue(v Values, token string) (string, bool) {
	if value, ok := v[token]; ok {
		return value, true
	}
	return lookup(v, token)
}
