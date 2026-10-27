package luarocks

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	luafmt "github.com/weaveplatform/artifactweave/internal/format/luarocks"
)

const (
	remoteManifestLimit  = 32 << 20
	remoteManifestFields = 200000
	remoteManifestDepth  = 24
)

var errRemoteManifest = errors.New("luarocks: invalid upstream luarocks manifest")

type luaField struct {
	key   string
	value luaValue
}

type luaValue struct {
	kind   byte // '{' for table, 's' for string, 'n' for number, 'b' for boolean
	text   string
	fields []luaField
}

type manifestParser struct {
	data   []byte
	pos    int
	fields int
}

func (p *manifestParser) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		case '-':
			if p.pos+1 >= len(p.data) || p.data[p.pos+1] != '-' {
				return
			}
			p.pos += 2
			for p.pos < len(p.data) && p.data[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

func (p *manifestParser) take(c byte) bool {
	p.space()
	if p.pos < len(p.data) && p.data[p.pos] == c {
		p.pos++
		return true
	}
	return false
}

func (p *manifestParser) ident() string {
	p.space()
	start := p.pos
	if start >= len(p.data) || !letter(p.data[start]) {
		return ""
	}
	p.pos++
	for p.pos < len(p.data) && (letter(p.data[p.pos]) || digit(p.data[p.pos])) {
		p.pos++
	}
	return string(p.data[start:p.pos])
}

func letter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' }
func digit(c byte) bool  { return c >= '0' && c <= '9' }

// Quoted Lua strings are decoded without executing Lua. Reject long strings,
// byte escapes and control bytes: none are valid in a package identity or
// useful in the remote metadata fields we forward to native clients.
func (p *manifestParser) quoted() (string, error) {
	p.space()
	if p.pos >= len(p.data) || p.data[p.pos] != '\'' && p.data[p.pos] != '"' {
		return "", errRemoteManifest
	}
	quote := p.data[p.pos]
	p.pos++
	var out strings.Builder
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++
		if c == quote {
			if !utf8.ValidString(out.String()) {
				return "", errRemoteManifest
			}
			return out.String(), nil
		}
		if c == '\\' {
			if p.pos >= len(p.data) {
				return "", errRemoteManifest
			}
			c = p.data[p.pos]
			p.pos++
			switch c {
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case '\\', '\'', '"':
			default:
				return "", errRemoteManifest
			}
		}
		if c < 0x20 || c == 0x7f || out.Len() >= remoteManifestLimit {
			return "", errRemoteManifest
		}
		out.WriteByte(c)
	}
	return "", errRemoteManifest
}

func (p *manifestParser) value(depth int) (luaValue, error) {
	p.space()
	if depth > remoteManifestDepth || p.pos >= len(p.data) {
		return luaValue{}, errRemoteManifest
	}
	if p.take('{') {
		v := luaValue{kind: '{'}
		for {
			p.space()
			if p.take('}') {
				return v, nil
			}
			p.fields++
			if p.fields > remoteManifestFields {
				return luaValue{}, errRemoteManifest
			}
			f := luaField{}
			if p.take('[') {
				key, err := p.quoted()
				if err != nil || !p.take(']') || !p.take('=') {
					return luaValue{}, errRemoteManifest
				}
				f.key = key
			} else {
				saved := p.pos
				id := p.ident()
				if id != "" && p.take('=') {
					f.key = id
				} else {
					p.pos = saved
				}
			}
			var err error
			f.value, err = p.value(depth + 1)
			if err != nil {
				return luaValue{}, err
			}
			v.fields = append(v.fields, f)
			p.space()
			if p.take(',') || p.take(';') {
				continue
			}
			if p.take('}') {
				return v, nil
			}
			return luaValue{}, errRemoteManifest
		}
	}
	if p.data[p.pos] == '\'' || p.data[p.pos] == '"' {
		s, err := p.quoted()
		return luaValue{kind: 's', text: s}, err
	}
	if id := p.ident(); id != "" {
		if id == "true" || id == "false" {
			return luaValue{kind: 'b', text: id}, nil
		}
		return luaValue{}, errRemoteManifest
	}
	start := p.pos
	if p.data[p.pos] == '-' {
		p.pos++
	}
	for p.pos < len(p.data) && (digit(p.data[p.pos]) || p.data[p.pos] == '.') {
		p.pos++
	}
	if p.pos == start {
		return luaValue{}, errRemoteManifest
	}
	n := string(p.data[start:p.pos])
	if _, err := strconv.ParseFloat(n, 64); err != nil {
		return luaValue{}, errRemoteManifest
	}
	return luaValue{kind: 'n', text: n}, nil
}

type remoteManifest struct {
	repository luaValue
	files      map[string]bool
}

func parseRemoteManifest(data []byte) (remoteManifest, error) {
	if len(data) == 0 || len(data) > remoteManifestLimit {
		return remoteManifest{}, errRemoteManifest
	}
	root, err := parseManifestRoot(data)
	if err != nil || len(root) != 3 || root["repository"].kind != '{' ||
		root["commands"].kind != '{' || len(root["commands"].fields) != 0 ||
		root["modules"].kind != '{' || len(root["modules"].fields) != 0 {
		return remoteManifest{}, errRemoteManifest
	}
	return validateManifestRepository(root["repository"])
}

func parseManifestRoot(data []byte) (map[string]luaValue, error) {
	p := manifestParser{data: data}
	root := map[string]luaValue{}
	for {
		p.space()
		if p.pos == len(data) {
			return root, nil
		}
		id := p.ident()
		if id == "return" {
			v, err := p.value(0)
			if err != nil || v.kind != '{' {
				return nil, errRemoteManifest
			}
			for _, f := range v.fields {
				if f.key == "" || root[f.key].kind != 0 {
					return nil, errRemoteManifest
				}
				root[f.key] = f.value
			}
			p.space()
			if p.pos != len(data) {
				return nil, errRemoteManifest
			}
			return root, nil
		}
		if id == "" || root[id].kind != 0 || !p.take('=') {
			return nil, errRemoteManifest
		}
		v, err := p.value(0)
		if err != nil {
			return nil, err
		}
		root[id] = v
	}
}

func recordArch(record luaValue) (string, error) {
	if record.kind != '{' {
		return "", errRemoteManifest
	}
	arch := ""
	for _, attr := range record.fields {
		if attr.key != "arch" {
			continue
		}
		if arch != "" || attr.value.kind != 's' || attr.value.text == "" {
			return "", errRemoteManifest
		}
		arch = attr.value.text
	}
	if arch == "" {
		return "", errRemoteManifest
	}
	return arch, nil
}

func manifestFilename(name, version, arch string) (string, error) {
	filename := name + "-" + version + "." + arch
	if arch == "rockspec" {
		filename = name + "-" + version + ".rockspec"
	} else {
		filename += ".rock"
	}
	id, ok := luafmt.Parse(filename)
	if !ok || id.Name != name || id.Version != version || id.Arch != arch {
		return "", errRemoteManifest
	}
	return filename, nil
}

// The public LuaRocks index still contains a few historical rockspecs whose
// version has no -revision. They cannot pass the shared package identity
// validation, so omit them without letting one old record hide every current
// package in the remote repository. The synthetic revision checks that the
// name, version and architecture are otherwise safe root-level components.
func legacyManifestVersion(name, version, arch string) bool {
	if strings.Contains(version, "-") {
		return false
	}
	_, err := manifestFilename(name, version+"-1", arch)
	return err == nil
}

func validateManifestRepository(repository luaValue) (remoteManifest, error) {
	out := remoteManifest{repository: repository, files: map[string]bool{}}
	packages := map[string]bool{}
	for _, pkg := range repository.fields {
		if pkg.key == "" || pkg.value.kind != '{' || packages[pkg.key] {
			return remoteManifest{}, errRemoteManifest
		}
		packages[pkg.key] = true
		versions := map[string]bool{}
		for _, version := range pkg.value.fields {
			if version.key == "" || version.value.kind != '{' || versions[version.key] {
				return remoteManifest{}, errRemoteManifest
			}
			versions[version.key] = true
			for _, record := range version.value.fields {
				if record.key != "" {
					return remoteManifest{}, errRemoteManifest
				}
				arch, err := recordArch(record.value)
				if err != nil {
					return remoteManifest{}, err
				}
				filename, err := manifestFilename(pkg.key, version.key, arch)
				if err != nil && legacyManifestVersion(pkg.key, version.key, arch) {
					continue
				}
				if err != nil || out.files[filename] {
					return remoteManifest{}, errRemoteManifest
				}
				out.files[filename] = true
			}
		}
	}
	return out, nil
}

func (m remoteManifest) has(path string) bool { return m.files[path] }

func (m remoteManifest) filtered(canRead func(string) bool) ([]byte, []string, error) {
	filtered := luaValue{kind: '{'}
	var paths []string
	for _, pkg := range m.repository.fields {
		versions := luaValue{kind: '{'}
		for _, version := range pkg.value.fields {
			records := luaValue{kind: '{'}
			for _, record := range version.value.fields {
				arch, err := recordArch(record.value)
				if err != nil {
					return nil, nil, err
				}
				name, err := manifestFilename(pkg.key, version.key, arch)
				if err != nil && legacyManifestVersion(pkg.key, version.key, arch) {
					continue
				}
				if err != nil {
					return nil, nil, err
				}
				if canRead(name) {
					records.fields = append(records.fields, record)
					paths = append(paths, name)
				}
			}
			if len(records.fields) > 0 {
				versions.fields = append(
					versions.fields,
					luaField{key: version.key, value: records},
				)
			}
		}
		if len(versions.fields) > 0 {
			filtered.fields = append(filtered.fields, luaField{key: pkg.key, value: versions})
		}
	}
	var b bytes.Buffer
	b.WriteString("commands = {}\nmodules = {}\nrepository = ")
	if err := writeLuaValue(&b, filtered, 0); err != nil {
		return nil, nil, err
	}
	b.WriteByte('\n')
	if b.Len() > remoteManifestLimit {
		return nil, nil, errRemoteManifest
	}
	return b.Bytes(), paths, nil
}

func writeLuaValue(b *bytes.Buffer, v luaValue, depth int) error {
	if depth > remoteManifestDepth || b.Len() > remoteManifestLimit {
		return errRemoteManifest
	}
	if v.kind != '{' {
		if v.kind == 's' {
			fmt.Fprintf(b, "%q", v.text)
		} else {
			b.WriteString(v.text)
		}
		return nil
	}
	b.WriteByte('{')
	for _, f := range v.fields {
		if f.key != "" {
			fmt.Fprintf(b, "[%q]=", f.key)
		}
		if err := writeLuaValue(b, f.value, depth+1); err != nil {
			return err
		}
		b.WriteByte(',')
	}
	b.WriteByte('}')
	return nil
}
