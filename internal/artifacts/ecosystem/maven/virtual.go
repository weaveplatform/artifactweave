package maven

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// POMCleanupPolicy returns the policy for a POM downloaded through v, or
// false outside the 7.x interceptor's Maven group and generic virtuals.
func POMCleanupPolicy(v repo.Config, path string) (string, bool) {
	if v.Class != repo.Virtual || !strings.HasSuffix(strings.ToLower(path), ".pom") {
		return "", false
	}
	switch v.PackageType {
	case repo.Maven, repo.Gradle, repo.Ivy, repo.SBT, repo.Generic:
	default:
		return "", false
	}
	policy := "discard_active_reference"
	if v.PackageType != repo.Generic && v.PomRepositoryReferencesCleanupPolicy != "" {
		policy = string(v.PomRepositoryReferencesCleanupPolicy)
	}
	return policy, policy != "nothing"
}

// CleanPOM implements 7.161.26's PomTransformer. An unchanged or unparsable
// document retains every original byte. Changed documents use JDOM2's raw
// XML form: UTF-8 declaration, CRLF text line endings and empty-element space.
func CleanPOM(body []byte, policy string) ([]byte, bool) {
	if policy == "nothing" {
		return body, false
	}
	doc, root, ok := readPOMTree(body)
	if !ok {
		return body, false
	}
	changed := root.remove("repositories", root.namespace)
	changed = root.remove("pluginRepositories", root.namespace) || changed
	if profiles := root.child("profiles", root.namespace); profiles != nil {
		for _, p := range profiles.children {
			if p.kind != 'e' {
				continue
			}
			if policy == "discard_active_reference" {
				activation := p.child("activation", root.namespace)
				if activation == nil {
					continue
				}
				active := activation.child("activeByDefault", root.namespace)
				// Java Boolean.parseBoolean ignores case, but does not trim whitespace.
				if active == nil || !strings.EqualFold(active.text(), "true") {
					continue
				}
			}
			changed = p.remove("repositories", root.namespace) || changed
			changed = p.remove("pluginRepositories", root.namespace) || changed
		}
	}
	if !changed {
		return body, false
	}
	var out strings.Builder
	out.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n")
	for _, n := range doc {
		n.write(&out, map[string]string{"xml": "http://www.w3.org/XML/1998/namespace"})
	}
	out.WriteString("\r\n")
	return []byte(out.String()), true
}

type pomNode struct {
	kind      byte
	name      xml.Name
	namespace string
	attrs     []xml.Attr
	children  []*pomNode
	value     string
	scope     map[string]string
}

func readPOMTree(body []byte) ([]*pomNode, *pomNode, bool) {
	d := xml.NewDecoder(bytes.NewReader(body))
	// 7.x reads the bytes as UTF-8 before parsing a StringReader. An XML
	// declaration therefore cannot reinterpret that string as another charset.
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	var doc, stack []*pomNode
	var root *pomNode
	for {
		start := d.InputOffset()
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			return doc, root, root != nil && len(stack) == 0
		}
		if err != nil {
			return nil, nil, false
		}
		var n *pomNode
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 0 && root != nil {
				return nil, nil, false
			}
			var parent *pomNode
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			var valid bool
			t = normalizedPOMAttributes(t, body[start:d.InputOffset()])
			n, valid = newPOMElement(t, parent)
			if !valid {
				return nil, nil, false
			}
			if len(stack) == 0 {
				root = n
			}
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].name != t.Name {
				return nil, nil, false
			}
			stack = stack[:len(stack)-1]
			continue
		case xml.CharData:
			if len(stack) == 0 {
				if len(bytes.TrimSpace(t)) != 0 {
					return nil, nil, false
				}
				continue
			}
			kind := byte('t')
			if bytes.HasPrefix(body[start:d.InputOffset()], []byte("<![CDATA[")) {
				kind = 'c'
			}
			n = &pomNode{kind: kind, value: string(t)}
		case xml.Comment:
			n = &pomNode{kind: '!', value: string(t)}
		case xml.ProcInst:
			if strings.EqualFold(t.Target, "xml") {
				continue
			}
			n = &pomNode{kind: '?', value: t.Target}
			if len(t.Inst) > 0 {
				n.value += " " + string(t.Inst)
			}
		case xml.Directive:
			n = &pomNode{kind: 'd', value: string(t)}
		}
		if n == nil {
			continue
		}
		if len(stack) == 0 {
			doc = append(doc, n)
		} else {
			p := stack[len(stack)-1]
			p.children = append(p.children, n)
		}
		if n.kind == 'e' {
			stack = append(stack, n)
		}
	}
}

func (n *pomNode) child(name, ns string) *pomNode {
	for _, c := range n.children {
		if c.kind == 'e' && c.name.Local == name && c.namespace == ns {
			return c
		}
	}
	return nil
}

func (n *pomNode) remove(name, ns string) bool {
	for i, c := range n.children {
		if c.kind == 'e' && c.name.Local == name && c.namespace == ns {
			n.children = slices.Delete(n.children, i, i+1)
			return true
		}
	}
	return false
}

func (n *pomNode) text() string {
	var out strings.Builder
	for _, c := range n.children {
		if c.kind == 't' || c.kind == 'c' {
			out.WriteString(c.value)
		}
	}
	return out.String()
}

func xmlName(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

func (n *pomNode) write(out *strings.Builder, inherited map[string]string) {
	switch n.kind {
	case 't':
		out.WriteString(pomEscape(n.value, false))
	case 'c':
		out.WriteString("<![CDATA[" + n.value + "]]>")
	case '!':
		out.WriteString("<!--" + n.value + "-->")
	case '?':
		out.WriteString("<?" + n.value + "?>")
	case 'd':
		out.WriteString("<!" + n.value + ">")
	case 'e':
		n.writeElement(out, inherited)
	}
}

func (n *pomNode) writeElement(out *strings.Builder, inherited map[string]string) {
	out.WriteString("<" + xmlName(n.name))
	scope := maps.Clone(inherited)
	// JDOM emits the element's namespace first, then additional declarations
	// in prefix order, before ordinary attributes in their parsed order.
	prefixes := make([]string, 0, len(n.scope))
	for prefix, uri := range n.scope {
		if inherited[prefix] != uri {
			prefixes = append(prefixes, prefix)
		}
	}
	slices.Sort(prefixes)
	if slices.Contains(prefixes, n.name.Space) {
		prefixes = slices.DeleteFunc(prefixes, func(p string) bool { return p == n.name.Space })
		prefixes = append([]string{n.name.Space}, prefixes...)
	}
	for _, prefix := range prefixes {
		out.WriteString(" xmlns")
		if prefix != "" {
			out.WriteString(":" + prefix)
		}
		out.WriteString("=\"" + pomEscape(n.scope[prefix], true) + "\"")
		scope[prefix] = n.scope[prefix]
	}
	for _, a := range n.attrs {
		if a.Name.Space == "xmlns" || a.Name.Space == "" && a.Name.Local == "xmlns" {
			continue
		}
		out.WriteString(" " + xmlName(a.Name) + "=\"" + pomEscape(a.Value, true) + "\"")
	}
	if len(n.children) == 0 {
		out.WriteString(" />")
		return
	}
	out.WriteString(">")
	for _, c := range n.children {
		c.write(out, scope)
	}
	out.WriteString("</" + xmlName(n.name) + ">")
}

func pomEscape(s string, attr bool) string {
	s = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#xD;").Replace(s)
	if attr {
		return strings.NewReplacer("\"", "&quot;", "\t", "&#x9;", "\n", "&#xA;").Replace(s)
	}
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func newPOMElement(t xml.StartElement, parent *pomNode) (*pomNode, bool) {
	scope := map[string]string{"xml": "http://www.w3.org/XML/1998/namespace"}
	if parent != nil {
		scope = maps.Clone(parent.scope)
	}
	for _, a := range t.Attr {
		switch {
		case a.Name.Space == "xmlns":
			scope[a.Name.Local] = a.Value
		case a.Name.Space == "" && a.Name.Local == "xmlns":
			scope[""] = a.Value
		}
	}
	if t.Name.Space != "" && scope[t.Name.Space] == "" {
		return nil, false
	}
	seen := map[xml.Name]bool{}
	for _, a := range t.Attr {
		name := a.Name
		if name.Space != "" && name.Space != "xmlns" {
			if scope[name.Space] == "" {
				return nil, false
			}
			name.Space = scope[name.Space]
		}
		if seen[name] {
			return nil, false
		}
		seen[name] = true
	}
	return &pomNode{kind: 'e', name: t.Name, namespace: scope[t.Name.Space], attrs: t.Attr, scope: scope}, true
}

// encoding/xml retains literal attribute tabs and newlines, while the SAX
// parser JDOM uses normalizes them to spaces. Normalize the raw start tag
// before decoding its values so character references still retain their
// distinct values (and serialize as &#xA;, &#x9; and &#xD;).
func normalizedPOMAttributes(original xml.StartElement, raw []byte) xml.StartElement {
	normalized := make([]byte, 0, len(raw))
	var quote byte
	changed := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if quote == 0 {
			if c == '\'' || c == '"' {
				quote = c
			}
		} else if c == quote {
			quote = 0
		} else if c == '\r' || c == '\n' || c == '\t' {
			if c == '\r' && i+1 < len(raw) && raw[i+1] == '\n' {
				i++
			}
			c = ' '
			changed = true
		}
		normalized = append(normalized, c)
	}
	if !changed {
		return original
	}
	decoder := xml.NewDecoder(bytes.NewReader(normalized))
	token, err := decoder.RawToken()
	if err == nil {
		if start, ok := token.(xml.StartElement); ok {
			return start
		}
	}
	return original
}
