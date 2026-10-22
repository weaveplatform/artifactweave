package nuget

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"strings"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const xmlNS = "http://www.w3.org/XML/1998/namespace"

type remoteXML struct {
	name     xml.Name
	attrs    []xml.Attr
	text     strings.Builder
	children []*remoteXML
}

// A small token tree avoids namespace-prefix confusion and forbids DTDs before
// any document can enter the shared cache. Both bytes and structural work are bounded.
func readRemoteXML(r io.Reader) (*remoteXML, error) {
	raw, err := io.ReadAll(io.LimitReader(r, remoteDocumentLimit+1))
	if err != nil || len(raw) > remoteDocumentLimit {
		return nil, remoteFailure()
	}
	d := xml.NewDecoder(bytes.NewReader(raw))
	var root *remoteXML
	var stack []*remoteXML
	for nodes := 0; ; {
		token, e := d.Token()
		if errors.Is(e, io.EOF) {
			if root == nil || len(stack) != 0 {
				return nil, remoteFailure()
			}
			return root, nil
		}
		if e != nil {
			return nil, remoteFailure()
		}
		switch t := token.(type) {
		case xml.Directive:
			return nil, remoteFailure()
		case xml.ProcInst:
			if t.Target != "xml" || root != nil {
				return nil, remoteFailure()
			}
		case xml.StartElement:
			nodes++
			if nodes > 100000 || len(stack) >= 32 {
				return nil, remoteFailure()
			}
			attrs := make(map[xml.Name]bool, len(t.Attr))
			for _, a := range t.Attr {
				if attrs[a.Name] {
					return nil, remoteFailure()
				}
				attrs[a.Name] = true
			}
			n := &remoteXML{name: t.Name, attrs: t.Attr}
			if len(stack) == 0 {
				if root != nil {
					return nil, remoteFailure()
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(t)) != "" {
					return nil, remoteFailure()
				}
			} else {
				n := stack[len(stack)-1]
				_, _ = n.text.Write(t)
			}
		}
	}
}

func (n *remoteXML) attr(space, name string) string {
	for _, a := range n.attrs {
		if a.Name.Space == space && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n *remoteXML) child(space, name string) *remoteXML {
	for _, c := range n.children {
		if c.name.Space == space && c.name.Local == name {
			return c
		}
	}
	return nil
}

func (n *remoteXML) value(space, name string) string {
	if c := n.child(space, name); c != nil {
		return c.text.String()
	}
	return ""
}

func (n *remoteXML) base(c repo.Config, parent string) (string, error) {
	if own := n.attr(xmlNS, "base"); own != "" {
		return remoteXMLURL(c, parent, own)
	}
	return parent, nil
}

func remoteXMLURL(c repo.Config, base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil || b.Host == "" {
		return "", remoteFailure()
	}
	u, err := url.Parse(ref)
	if err != nil || ref == "" {
		return "", remoteFailure()
	}
	target := b.ResolveReference(u).String()
	check := c
	check.V3FeedURL = c.URL
	if resourceURL(check, target, false) != nil {
		return "", remoteFailure()
	}
	return target, nil
}

type remoteAtomEntry struct {
	atom   atomEntry
	values map[string]string
	meta   nugetfmt.Metadata
	checks types.Checksums
}
type remoteAtomPage struct {
	entries []remoteAtomEntry
	next    string
}

func parseRemoteAtom(c repo.Config, root *remoteXML, base, id string) (remoteAtomPage, error) {
	var out remoteAtomPage
	if root.name.Space != atomNS || (root.name.Local != "feed" && root.name.Local != "entry") {
		return out, remoteFailure()
	}
	if root.name.Local == "entry" {
		e, err := parseRemoteEntry(c, root, base, id)
		if err != nil {
			return out, err
		}
		out.entries = []remoteAtomEntry{e}
		return out, nil
	}
	base, err := root.base(c, base)
	if err != nil {
		return out, err
	}
	entries := root.children
	seen := make(map[string]bool)
	for _, n := range entries {
		if n.name.Space != atomNS {
			continue
		}
		if n.name.Local == "link" {
			own, e := n.base(c, base)
			if e != nil {
				return out, e
			}
			href, e := remoteXMLURL(c, own, n.attr("", "href"))
			if e != nil {
				return out, e
			}
			if strings.EqualFold(n.attr("", "rel"), "next") {
				if out.next != "" {
					return out, remoteFailure()
				}
				out.next = href
			}
		}
		if n.name.Local != "entry" {
			continue
		}
		e, err := parseRemoteEntry(c, n, base, id)
		if err != nil {
			return out, err
		}
		key := nugetfmt.PackageName(e.meta.ID) + "/" + e.meta.Version
		if seen[key] {
			return out, remoteFailure()
		}
		seen[key] = true
		out.entries = append(out.entries, e)
		if len(out.entries) > 10000 {
			return out, remoteFailure()
		}
	}
	return out, nil
}

func parseRemoteEntry(c repo.Config, n *remoteXML, base, id string) (remoteAtomEntry, error) {
	out := remoteAtomEntry{values: map[string]string{}}
	base, err := n.base(c, base)
	if err != nil {
		return out, err
	}
	out.values, err = remoteEntryProperties(n)
	if err != nil {
		return out, err
	}
	out.meta.ID = out.values["Id"]
	if out.meta.ID == "" {
		out.meta.ID = n.value(atomNS, "title")
	}
	key, valid := versionKey(out.values["Version"])
	if !nugetfmt.ValidID(out.meta.ID) || !valid ||
		(id != "" && !strings.EqualFold(id, out.meta.ID)) {
		return out, remoteFailure()
	}
	out.meta.Version = key
	version, _ := nugetfmt.ParseVersion(out.values["Version"])
	parsed := nugetfmt.Nuspec{Version: version}
	for _, dep := range strings.Split(out.values["Dependencies"], "|") {
		if dep == "" {
			continue
		}
		fields := strings.SplitN(dep, ":", 3)
		if len(fields) < 2 || !nugetfmt.ValidID(fields[0]) {
			return out, remoteFailure()
		}
		parsed.DependencyGroups = append(
			parsed.DependencyGroups,
			nugetfmt.DependencyGroup{
				Dependencies: []nugetfmt.Dependency{{ID: fields[0], Range: fields[1]}},
			},
		)
	}
	out.meta.SemVer2 = parsed.IsSemVer2()
	out.meta.Title, out.meta.Description, out.meta.Tags = out.values["Title"], out.values["Description"], out.values["Tags"]
	for _, e := range n.children {
		if e.name.Space != atomNS {
			continue
		}
		switch e.name.Local {
		case "content", "link":
			own, e2 := e.base(c, base)
			if e2 != nil {
				return out, e2
			}
			field := "href"
			if e.name.Local == "content" {
				field = "src"
			}
			if _, e2 = remoteXMLURL(c, own, e.attr("", field)); e2 != nil {
				return out, e2
			}
		}
	}
	if n.child(atomNS, "content") == nil {
		return out, remoteFailure()
	}
	if out.checks, err = v2Checksums(out.values); err != nil {
		return out, err
	}
	out.atom = atomEntry{
		XMLNS:      atomNS,
		DataNS:     dataNS,
		MetadataNS: metadataNS,
		Title:      n.value(atomNS, "title"),
		Summary:    n.value(atomNS, "summary"),
		Updated:    n.value(atomNS, "updated"),
		Category: atomCategory{
			Term:   "NuGetGallery.V2FeedPackage",
			Scheme: "http://schemas.microsoft.com/ado/2007/08/dataservices/scheme",
		},
	}
	for _, a := range n.children {
		if a.name.Space == atomNS && a.name.Local == "author" {
			out.atom.Authors = append(out.atom.Authors, atomAuthor{Name: a.value(atomNS, "name")})
		}
	}
	return out, nil
}

func remoteEntryProperties(n *remoteXML) (map[string]string, error) {
	values := map[string]string{}
	p := n.child(metadataNS, "properties")
	if p == nil {
		return nil, remoteFailure()
	}
	properties, content := 0, 0
	for _, child := range n.children {
		if child.name == (xml.Name{Space: metadataNS, Local: "properties"}) {
			properties++
		}
		if child.name == (xml.Name{Space: atomNS, Local: "content"}) {
			content++
		}
	}
	if properties != 1 || content != 1 {
		return nil, remoteFailure()
	}
	for _, v := range p.children {
		if v.name.Space != dataNS || len(v.children) != 0 {
			return nil, remoteFailure()
		}
		if _, exists := values[v.name.Local]; exists {
			return nil, remoteFailure()
		}
		values[v.name.Local] = v.text.String()
	}
	return values, nil
}

func v2Checksums(values map[string]string) (types.Checksums, error) {
	hash, algorithm := values["PackageHash"], strings.ToUpper(values["PackageHashAlgorithm"])
	var out types.Checksums
	if hash == "" && algorithm == "" {
		return out, nil
	}
	raw, err := base64.StdEncoding.DecodeString(hash)
	if err != nil {
		return out, remoteFailure()
	}
	switch {
	case algorithm == "SHA512" && len(raw) == 64:
		out.SHA512 = hex.EncodeToString(raw)
	case algorithm == "SHA256" && len(raw) == 32:
		out.SHA256 = hex.EncodeToString(raw)
	case algorithm == "SHA1" && len(raw) == 20:
		out.SHA1 = hex.EncodeToString(raw)
	default:
		return out, remoteFailure()
	}
	return out, nil
}
