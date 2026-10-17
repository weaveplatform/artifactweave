package pkgroute_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestParsePath(t *testing.T) {
	for _, c := range []struct {
		escaped   string
		segments  []string
		folder    bool
		props     types.Properties
		mandatory []string
	}{
		{"", nil, false, types.Properties{}, nil},
		{"/", nil, true, types.Properties{}, nil},
		{"a/b.txt", []string{"a", "b.txt"}, false, types.Properties{}, nil},
		{"a/b/", []string{"a", "b"}, true, types.Properties{}, nil},
		// An encoded slash stays inside its segment; String makes it a
		// folder, as the item routes do.
		{"@scope%2fname/-/x.tgz", []string{"@scope/name", "-", "x.tgz"}, false, types.Properties{}, nil},
		{"a;k=v/b;k=w", []string{"a", "b"}, false, types.Properties{"k": {"v", "w"}}, nil},
		{"a/b/;k=v", []string{"a", "b"}, true, types.Properties{"k": {"v"}}, nil},
		{"f;k+=v", []string{"f"}, false, types.Properties{"k": {"v"}}, []string{"k"}},
		// Values are form-decoded, comma-separated, and a backslash escapes
		// a comma.
		{"f;k=a+b%2Cc,d%5C,e", []string{"f"}, false, types.Properties{"k": {"a b", "c", "d,e"}}, nil},
		{"f;;k=", []string{"f"}, false, types.Properties{"k": {""}}, nil},
	} {
		p, err := pkgroute.ParsePath(c.escaped)
		if err != nil {
			t.Fatalf("%q: %v", c.escaped, err)
		}
		if !reflect.DeepEqual(p.Segments, c.segments) || p.Folder != c.folder ||
			!reflect.DeepEqual(p.Props, c.props) {
			t.Errorf("%q: %+v", c.escaped, p)
		}
		for _, k := range c.mandatory {
			if !p.Mandatory[k] {
				t.Errorf("%q: %s not mandatory", c.escaped, k)
			}
		}
	}
	if p, _ := pkgroute.ParsePath("@scope%2fname/-/x.tgz"); p.String() != "@scope/name/-/x.tgz" {
		t.Fatalf("String %q", p.String())
	}
	for _, bad := range []string{"a%zz/b", "f;k%zz=v", "f;=v", "f;k=%zz"} {
		if _, err := pkgroute.ParsePath(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestSplitValues(t *testing.T) {
	for in, want := range map[string][]string{
		"":          {""},
		"a":         {"a"},
		"a,b":       {"a", "b"},
		`a\,b,c`:    {"a,b", "c"},
		`trail\`:    {`trail\`},
		`a\\,b`:     {`a\`, "b"},
		",":         {"", ""},
		`x\y`:       {"xy"},
		`one\,`:     {"one,"},
		"a,,b":      {"a", "", "b"},
		`\,\,`:      {",,"},
		`a\, b , c`: {"a, b ", " c"},
	} {
		if got := pkgroute.SplitValues(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestChecksumHeaders(t *testing.T) {
	sha256 := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	sha1 := "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d"
	md5 := "5d41402abc4b2a76b9719d911017c592"
	for _, c := range []struct {
		headers map[string]string
		want    types.Checksums
	}{
		{
			map[string]string{"X-Checksum-Sha1": sha1, "X-Checksum-Sha256": sha256, "X-Checksum-Md5": md5},
			types.Checksums{SHA1: sha1, SHA256: sha256, MD5: md5},
		},
		// X-Checksum names its algorithm by length; jf sends the SHA-256.
		{map[string]string{"X-Checksum": " " + sha256 + " "}, types.Checksums{SHA256: sha256}},
		{map[string]string{"X-Checksum": sha1}, types.Checksums{SHA1: sha1}},
		{map[string]string{"X-Checksum": md5}, types.Checksums{MD5: md5}},
		{map[string]string{}, types.Checksums{}},
	} {
		r := httptest.NewRequest(http.MethodPut, "/", nil)
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		if got := pkgroute.DeclaredChecksums(r.Header); got != c.want {
			t.Errorf("%v: %+v", c.headers, got)
		}
	}
	h := http.Header{}
	pkgroute.SetChecksums(h, types.Checksums{SHA1: sha1, SHA256: sha256, MD5: md5, SHA512: "x"})
	if h.Get("X-Checksum-Sha1") != sha1 || h.Get("X-Checksum-Sha256") != sha256 ||
		h.Get("X-Checksum-Md5") != md5 || len(h) != 3 {
		t.Fatalf("%v", h)
	}
}
