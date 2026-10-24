package gems

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"strings"
	"time"
)

// Ruby Marshal 4.8 is used by gem's legacy source index. These values are
// deliberately small and contain only trusted, validated gemspec fields.
type (
	rubyVersion     string
	rubyRequirement string
	rubyDependency  Dependency
	rubySpec        struct {
		Package Package
		Date    time.Time
	}
)

type marshalWriter struct{ bytes.Buffer }

func (m *marshalWriter) number(n int) {
	switch {
	case n == 0:
		m.WriteByte(0)
	case n > 0 && n < 123:
		m.WriteByte(byte(n + 5))
	case n < 0 && n > -124:
		m.WriteByte(byte(int8(n - 5)))
	default:
		var b [8]byte
		size := 0
		v := n
		for v != 0 && v != -1 && size < 8 {
			b[size] = byte(v)
			v >>= 8
			size++
		}
		if n > 0 {
			m.WriteByte(byte(size))
		} else {
			m.WriteByte(byte(int8(-size)))
		}
		m.Write(b[:size])
	}
}
func (m *marshalWriter) rawString(s string) { m.WriteByte('"'); m.number(len(s)); m.WriteString(s) }
func (m *marshalWriter) symbol(s string)    { m.WriteByte(':'); m.number(len(s)); m.WriteString(s) }
func (m *marshalWriter) value(v any) {
	switch x := v.(type) {
	case nil:
		m.WriteByte('0')
	case bool:
		if x {
			m.WriteByte('T')
		} else {
			m.WriteByte('F')
		}
	case int:
		m.WriteByte('i')
		m.number(x)
	case string:
		m.rawString(x)
	case []any:
		m.WriteByte('[')
		m.number(len(x))
		for _, a := range x {
			m.value(a)
		}
	case []string:
		m.WriteByte('[')
		m.number(len(x))
		for _, a := range x {
			m.value(a)
		}
	case rubyVersion:
		m.WriteByte('U')
		m.symbol("Gem::Version")
		m.value([]any{string(x)})
	case rubyRequirement:
		m.WriteByte('U')
		m.symbol("Gem::Requirement")
		m.value([]any{requirementPairs(string(x))})
	case rubyDependency:
		m.WriteByte('o')
		m.symbol("Gem::Dependency")
		m.number(5)
		m.symbol("@name")
		m.value(x.Name)
		m.symbol("@requirement")
		m.value(rubyRequirement(x.Requirement))
		m.symbol("@type")
		m.symbol("runtime")
		m.symbol("@prerelease")
		m.value(false)
		m.symbol("@version_requirements")
		m.value(rubyRequirement(x.Requirement))
	case rubySpec:
		inner := marshalWriter{}
		inner.Write([]byte{4, 8})
		deps := make([]any, 0, len(x.Package.Dependencies))
		for _, d := range x.Package.Dependencies {
			deps = append(deps, rubyDependency(d))
		}
		platform := x.Package.Platform
		if platform == "" {
			platform = "ruby"
		}
		date := x.Date.UTC().Format("2006-01-02")
		inner.value([]any{
			"3.0.3",
			4,
			x.Package.Name,
			rubyVersion(x.Package.Version),
			date,
			x.Package.Summary,
			rubyRequirement(x.Package.Ruby),
			rubyRequirement(x.Package.RubyGems),
			platform,
			deps,
			"",
			nil,
			x.Package.Authors,
			x.Package.Description,
			x.Package.Homepage,
			true,
			platform,
			x.Package.Licenses,
			map[string]string{},
		})
		m.WriteByte('u')
		m.symbol("Gem::Specification")
		m.number(inner.Len())
		m.Write(inner.Bytes())
	case map[string]string:
		m.WriteByte('{')
		m.number(len(x))
		for k, v := range x {
			m.value(k)
			m.value(v)
		}
	default:
		panic(fmt.Sprintf("unsupported marshal value %T", v))
	}
}

func requirementPairs(s string) []any {
	if s == "" {
		s = ">= 0"
	}
	parts := strings.Split(s, ",")
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		fields := strings.Fields(strings.TrimSpace(p))
		if len(fields) != 2 {
			continue
		}
		out = append(out, []any{fields[0], rubyVersion(fields[1])})
	}
	if len(out) == 0 {
		out = append(out, []any{">=", rubyVersion("0")})
	}
	return out
}

func Marshal(
	v any,
) []byte {
	m := marshalWriter{}
	m.Write([]byte{4, 8})
	m.value(v)
	return m.Bytes()
}

func GzipMarshal(v any) ([]byte, error) {
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	if _, err := z.Write(Marshal(v)); err != nil {
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func Specs(packages []Package, latest, prerelease bool) ([]byte, error) {
	rows := make([]any, 0, len(packages))
	for _, p := range packages {
		isPre := strings.IndexFunc(
			p.Version,
			func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' },
		) >= 0
		if isPre != prerelease {
			continue
		}
		if latest {
			newer := false
			for _, q := range packages {
				if q.Name == p.Name && q.Platform == p.Platform &&
					CompareVersions(q.Version, p.Version) > 0 {
					newer = true
					break
				}
			}
			if newer {
				continue
			}
		}
		rows = append(rows, []any{p.Name, rubyVersion(p.Version), p.Platform})
	}
	return GzipMarshal(rows)
}

func QuickSpec(p Package, at time.Time) ([]byte, error) {
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	if _, err := io.Copy(z, bytes.NewReader(Marshal(rubySpec{Package: p, Date: at}))); err != nil {
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
