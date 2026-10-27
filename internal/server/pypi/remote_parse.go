package pypi

import (
	"bytes"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"

	htmlparse "golang.org/x/net/html"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

const (
	remoteMetadataLimit = 64 << 20
	remoteEntryLimit    = 1000000
)

type remoteDoc map[string]json.RawMessage

type remoteFile struct {
	doc                                   remoteDoc
	name, version, path, target, fragment string
	hashes                                map[string]string
	metadata                              map[string]string
	hasCoreMetadata                       bool
	attrs                                 map[string]string
}
type remoteIndex struct {
	doc      remoteDoc
	files    []remoteFile
	projects []string
}

func remoteError() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream Python metadata")
}

func remoteBytes(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, remoteMetadataLimit+1))
	if err != nil || len(b) > remoteMetadataLimit {
		return nil, remoteError()
	}
	return b, nil
}

func parseRemoteIndex(r io.Reader, base, name string) (remoteIndex, error) {
	if _, _, err := remoteLink(base, base); err != nil {
		return remoteIndex{}, err
	}
	b, err := remoteBytes(r)
	if err != nil {
		return remoteIndex{}, err
	}
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte("{")) {
		return parseIndexJSON(b, base, name)
	}
	return parseIndexHTML(b, base, name)
}

func validSimpleAPIVersion(version string) bool {
	major, minor, ok := strings.Cut(version, ".")
	return ok && major == "1" && minor != "" && strings.Trim(minor, "0123456789") == ""
}

func parseIndexJSON(b []byte, base, name string) (remoteIndex, error) {
	var out remoteIndex
	if json.Unmarshal(b, &out.doc) != nil || out.doc == nil {
		return out, remoteError()
	}
	var meta map[string]json.RawMessage
	var version string
	if json.Unmarshal(out.doc["meta"], &meta) != nil ||
		json.Unmarshal(meta["api-version"], &version) != nil ||
		!validSimpleAPIVersion(version) {
		return out, remoteError()
	}
	if name == "" {
		var projects []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(out.doc["projects"], &projects) != nil || projects == nil ||
			len(projects) > remoteEntryLimit {
			return out, remoteError()
		}
		for _, p := range projects {
			if !pypifmt.ValidName(p.Name) {
				return out, remoteError()
			}
			out.projects = append(out.projects, pypifmt.Normalize(p.Name))
		}
		return out, nil
	}
	var got string
	var files []remoteDoc
	if json.Unmarshal(out.doc["name"], &got) != nil || pypifmt.Normalize(got) != name ||
		json.Unmarshal(out.doc["files"], &files) != nil ||
		files == nil ||
		len(files) > remoteEntryLimit {
		return out, remoteError()
	}
	for _, d := range files {
		f, err := parseJSONFile(d, base, name)
		if err != nil {
			return out, err
		}
		out.files = append(out.files, f)
	}
	return out, uniqueFiles(out.files)
}

func parseJSONFile(d remoteDoc, base, name string) (remoteFile, error) {
	var filename, href string
	var hashes map[string]string
	if json.Unmarshal(d["filename"], &filename) != nil ||
		json.Unmarshal(d["url"], &href) != nil ||
		json.Unmarshal(d["hashes"], &hashes) != nil ||
		hashes == nil {
		return remoteFile{}, remoteError()
	}
	f, err := newRemoteFile(base, name, filename, href, hashes)
	if err != nil {
		return remoteFile{}, err
	}
	f.doc = d
	if raw, ok := d["gpg-sig"]; ok {
		var advertised bool
		if string(raw) == "null" || json.Unmarshal(raw, &advertised) != nil {
			return remoteFile{}, remoteError()
		}
		f.attrs["data-gpg-sig"] = strconv.FormatBool(advertised)
	}
	if raw, ok := d["requires-python"]; ok && string(raw) != "null" {
		var v string
		if json.Unmarshal(raw, &v) != nil {
			return remoteFile{}, remoteError()
		}
		f.attrs["data-requires-python"] = v
	}
	if raw, ok := d["yanked"]; ok {
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return remoteFile{}, remoteError()
		}
		switch x := v.(type) {
		case bool:
			if x {
				f.attrs["data-yanked"] = ""
			}
		case string:
			f.attrs["data-yanked"] = x
		default:
			return remoteFile{}, remoteError()
		}
	}
	raw, ok := d["core-metadata"]
	if !ok {
		raw = d["dist-info-metadata"]
	}
	if len(raw) > 0 {
		if err = f.setCore(raw); err != nil {
			return remoteFile{}, err
		}
	}
	return f, nil
}

func parseHTMLBase(attrs []htmlparse.Attribute, base string) (string, error) {
	href := ""
	for _, a := range attrs {
		if a.Key == "href" {
			if href != "" {
				return "", remoteError()
			}
			href = a.Val
		}
	}
	resolved, fragment, err := remoteLink(base, href)
	if err != nil || fragment != "" {
		return "", remoteError()
	}
	return resolved, nil
}

func htmlAPIVersion(attrs []htmlparse.Attribute, prior string) (string, error) {
	values := map[string]string{}
	for _, a := range attrs {
		if _, ok := values[a.Key]; ok {
			return "", remoteError()
		}
		values[a.Key] = a.Val
	}
	if !strings.EqualFold(values["name"], "pypi:repository-version") {
		return prior, nil
	}
	version := values["content"]
	if !validSimpleAPIVersion(version) || (prior != "" && prior != version) {
		return "", remoteError()
	}
	return version, nil
}

func parseIndexHTML(b []byte, base, name string) (remoteIndex, error) {
	out := remoteIndex{doc: remoteDoc{}}
	z := htmlparse.NewTokenizer(bytes.NewReader(b))
	seenHTML := false
	version := ""
	seenBase, seenLink := false, false
	var anchor map[string]string
	var text strings.Builder
	count := 0
	for {
		switch z.Next() {
		case htmlparse.ErrorToken:
			if !errors.Is(z.Err(), io.EOF) || !seenHTML || anchor != nil {
				return out, remoteError()
			}
			return out, uniqueFiles(out.files)
		case htmlparse.StartTagToken, htmlparse.SelfClosingTagToken:
			t := z.Token()
			count++
			if count > remoteEntryLimit*8 {
				return out, remoteError()
			}
			if t.Data == "html" {
				seenHTML = true
			}
			if t.Data == "meta" {
				var err error
				version, err = htmlAPIVersion(t.Attr, version)
				if err != nil {
					return out, err
				}
			}
			if t.Data == "base" {
				if seenBase || seenLink {
					return out, remoteError()
				}
				seenBase = true
				resolved, err := parseHTMLBase(t.Attr, base)
				if err != nil {
					return out, err
				}
				base = resolved
				continue
			}
			if t.Data != "a" {
				continue
			}
			if anchor != nil {
				return out, remoteError()
			}
			seenLink = true
			anchor = map[string]string{}
			text.Reset()
			for _, a := range t.Attr {
				if _, exists := anchor[a.Key]; exists {
					return out, remoteError()
				}
				anchor[a.Key] = a.Val
			}
		case htmlparse.TextToken:
			if anchor != nil {
				text.Write(z.Text())
			}
		case htmlparse.EndTagToken:
			t := z.Token()
			if t.Data != "a" {
				continue
			}
			if anchor == nil {
				return out, remoteError()
			}
			label := strings.TrimSpace(text.String())
			href := anchor["href"]
			if name == "" {
				if !pypifmt.ValidName(label) {
					return out, remoteError()
				}
				target, _, err := remoteLink(base, href)
				if err != nil {
					return out, err
				}
				u, _ := url.Parse(target)
				if pypifmt.Normalize(
					path.Base(strings.TrimSuffix(u.Path, "/")),
				) != pypifmt.Normalize(
					label,
				) {
					return out, remoteError()
				}
				out.projects = append(out.projects, pypifmt.Normalize(label))
			} else {
				f, err := parseHTMLFile(base, name, label, anchor)
				if err != nil {
					return out, err
				}
				out.files = append(out.files, f)
			}
			anchor = nil
		}
	}
}

func parseHTMLFile(base, name, label string, attrs map[string]string) (remoteFile, error) {
	f, err := newRemoteFile(base, name, label, attrs["href"], nil)
	if err != nil {
		return f, err
	}
	f.attrs = attrs
	if v, ok := attrs["data-gpg-sig"]; ok && v != "true" && v != "false" {
		return f, remoteError()
	}
	for _, key := range []string{"data-core-metadata", "data-dist-info-metadata"} {
		if v, ok := attrs[key]; ok {
			var raw []byte
			if v == "true" || v == "false" {
				raw = []byte(v)
			} else {
				k, d, ok := strings.Cut(v, "=")
				if !ok {
					return f, remoteError()
				}
				raw = encode(map[string]string{k: d})
			}
			if err = f.setCore(raw); err != nil {
				return f, err
			}
			break
		}
	}
	return f, nil
}

func newRemoteFile(
	base, project, filename, href string,
	hashes map[string]string,
) (remoteFile, error) {
	f := remoteFile{name: filename, attrs: map[string]string{}, hashes: hashes}
	if filename == "" || filename != path.Base(filename) ||
		strings.ContainsAny(filename, "\\\r\n\x00") ||
		!pypifmt.IsDistribution(filename) {
		return f, remoteError()
	}
	version, err := fileVersion(project, filename)
	if err != nil {
		return f, err
	}
	f.version = version
	f.path = pypifmt.CanonicalPath(project, version, filename)
	f.target, f.fragment, err = remoteLink(base, href)
	if err != nil {
		return f, err
	}
	u, _ := url.Parse(f.target)
	if path.Base(u.Path) != filename {
		return f, remoteError()
	}
	if f.hashes == nil {
		f.hashes = map[string]string{}
	}
	if f.fragment != "" {
		k, v, ok := strings.Cut(f.fragment, "=")
		if !ok {
			return f, remoteError()
		}
		if prior, ok := f.hashes[k]; ok && !strings.EqualFold(prior, v) {
			return f, remoteError()
		}
		f.hashes[k] = v
	}
	if err = validateHashes(f.hashes); err != nil {
		return f, err
	}
	return f, nil
}

func fileVersion(project, filename string) (string, error) {
	stem := filename
	for _, ext := range []string{".tar.gz", ".tar.bz2", ".whl", ".egg", ".tgz", ".tar", ".zip"} {
		if s, ok := strings.CutSuffix(filename, ext); ok {
			stem = s
			break
		}
	}
	parts := strings.Split(stem, "-")
	if strings.HasSuffix(filename, ".whl") {
		if (len(parts) != 5 && len(parts) != 6) || pypifmt.Normalize(parts[0]) != project ||
			!validRemoteVersion(parts[1]) {
			return "", remoteError()
		}
		return parts[1], nil
	}
	for i := len(parts) - 1; i > 0; i-- {
		if pypifmt.Normalize(strings.Join(parts[:i], "-")) == project &&
			validRemoteVersion(strings.Join(parts[i:], "-")) {
			return strings.Join(parts[i:], "-"), nil
		}
	}
	return "", remoteError()
}

func remoteLink(base, raw string) (string, string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", "", remoteError()
	}
	r, err := url.Parse(raw)
	if err != nil || raw == "" || r.User != nil || r.Opaque != "" ||
		strings.ContainsAny(raw, "\\\r\n\x00") {
		return "", "", remoteError()
	}
	u := b.ResolveReference(r)
	fragment := u.Fragment
	u.Fragment = ""
	u.RawFragment = ""
	if u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") ||
		(b.Scheme == "https" && u.Scheme != "https") {
		return "", "", remoteError()
	}
	for _, p := range strings.Split(u.Path, "/") {
		if p == "." || p == ".." || strings.ContainsAny(p, "\\\x00\r\n") {
			return "", "", remoteError()
		}
	}
	return u.String(), fragment, nil
}

func (f *remoteFile) setCore(raw []byte) error {
	if string(raw) == "false" {
		return nil
	}
	f.hasCoreMetadata = true
	if string(raw) == "true" {
		return nil
	}
	if json.Unmarshal(raw, &f.metadata) != nil || len(f.metadata) == 0 {
		return remoteError()
	}
	return validateHashes(f.metadata)
}

func uniqueFiles(files []remoteFile) error {
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.path] {
			return remoteError()
		}
		seen[f.path] = true
	}
	return nil
}

func remoteHTML(files []remoteFile, base string, core bool) []byte {
	var b strings.Builder
	b.WriteString(pageHead)
	for _, f := range files {
		b.WriteString(
			`<a href="` + html.EscapeString(
				base+"/"+escapePath(f.path),
			) + html.EscapeString(
				hashFragment(f),
			) + `"`,
		)
		for _, k := range []string{"data-requires-python", "data-yanked", "data-gpg-sig"} {
			if v, ok := f.attrs[k]; ok {
				b.WriteString(" " + k + `="` + html.EscapeString(v) + `"`)
			}
		}
		if core && f.hasCoreMetadata {
			v := "true"
			for _, k := range hashNames {
				if d, ok := f.metadata[k]; ok {
					v = k + "=" + d
					break
				}
			}
			b.WriteString(
				` data-core-metadata="` + html.EscapeString(
					v,
				) + `" data-dist-info-metadata="` + html.EscapeString(
					v,
				) + `"`,
			)
		}
		b.WriteString(">" + html.EscapeString(f.name) + "</a>\n")
	}
	b.WriteString(pageTail)
	return []byte(b.String())
}

func hashFragment(f remoteFile) string {
	if f.fragment != "" {
		return "#" + f.fragment
	}
	for _, k := range hashNames {
		if d, ok := f.hashes[k]; ok {
			return "#" + k + "=" + d
		}
	}
	return ""
}

func validRemoteVersion(v string) bool {
	return v != "" && v != "." && v != ".." && len(v) <= 256 &&
		!strings.ContainsFunc(
			v,
			func(r rune) bool { return r == '/' || r == '\\' || unicode.IsSpace(r) || !unicode.IsPrint(r) },
		)
}
