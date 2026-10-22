package terraform

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	tf "github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestRemoteModuleSourceGrammar(t *testing.T) {
	c := repo.Config{
		URL:                  "https://up.example/storage",
		TerraformRegistryURL: "https://up.example/registry",
	}
	base := "https://up.example/registry/ns/name/aws/1.0.0/download"
	for _, tc := range []struct{ raw, archive, subdir, target string }{
		{"/storage/archive.zip", "zip", "", "https://up.example/storage/archive.zip"},
		{"../../../../../storage/archive.tgz//root/demo", "tar.gz", "root/demo", "https://up.example/storage/archive.tgz"},
		{"https::https://up.example/storage/archive?archive=tar.xz&token=abc", "tar.xz", "", "https://up.example/storage/archive?token=abc"},
		{"/storage/archive.tbz2//*", "tar.bz2", "*", "https://up.example/storage/archive.tbz2"},
		{"/storage/archive.txz", "tar.xz", "", "https://up.example/storage/archive.txz"},
		{"/storage/archive.tar.tbz2", "tar.bz2", "", "https://up.example/storage/archive.tar.tbz2"},
	} {
		s, e := parseModuleSource(tc.raw, base, c)
		if e != nil || s.Archive != tc.archive || s.Subdir != tc.subdir || s.Target != tc.target {
			t.Fatalf("%s: %+v %v", tc.raw, s, e)
		}
	}
	for _, raw := range []string{"", "\n", strings.Repeat("x", 8193), "/storage\\archive.zip", "file::/archive.zip", "ssh::git@github.com:repo", "file:///tmp/archive.zip", "https://user:pass@up.example/storage/archive.zip", "https://up.example/storage/archive.zip#fragment", "http://up.example/storage/archive.zip", "https://other.example/archive.zip", "/storage/archive.zip?archive=zip&archive=tar", "/storage/archive.zip?checksum=xx&checksum=yy", "/storage/archive.zip?bad=%zz", "/storage/archive.zip?archive=unsupported", "/storage/unknown", "/storage/archive.zip?checksum=sha256:123", "/storage/archive.zip//../bad", "/storage/archive.zip//[bad", "/storage/archive.zip//root/%2e%2e", "/storage/archive.zip//root#fragment", "/storage/archive.zip//root//child", "/storage/%2e%2e/archive.zip", "git::https://gitlab.com/org/repo?ref=v1"} {
		if s, e := parseModuleSource(raw, base, c); e == nil {
			t.Errorf("admitted %q: %+v", raw, s)
		}
	}
	for _, kind := range []string{"zip", "tar", "tar.gz", "tar.bz2", "tar.xz", "gz", "bz2", "xz"} {
		if s, e := parseModuleSource(
			"/storage/module?archive="+kind,
			base,
			c,
		); e != nil ||
			s.Archive != kind {
			t.Fatal(kind, s, e)
		}
	}
}

func TestRemoteGitHubModuleSources(t *testing.T) {
	c := repo.Config{
		URL:                  "https://api.github.com",
		TerraformRegistryURL: "https://registry.terraform.io",
	}
	base := "https://registry.terraform.io/v1/modules/ns/name/aws/1.0.0/download"
	for _, raw := range []string{"git::https://github.com/org/repo.git//modules/demo?ref=v1.0&depth=1", "github.com/org/repo//modules/demo?ref=v1.0"} {
		s, e := parseModuleSource(raw, base, c)
		if e != nil || s.Target != "https://github.com/org/repo/archive/v1.0.tar.gz" ||
			s.Subdir != "*/modules/demo" {
			t.Fatal(s, e)
		}
	}
	s, e := parseModuleSource("git::https://github.com/org/repo", base, c)
	if e != nil || !strings.HasSuffix(s.Target, "/HEAD.tar.gz") || s.Subdir != "*" {
		t.Fatal(s, e)
	}
	for _, raw := range []string{"git::http://github.com/org/repo", "git::https://github.com/org/repo/more", "git::https://github.com/org/.git", "git::https://github.com/org/repo?ref=../x", "git::https://github.com/org/repo?ref=-x", "git::https://github.com/org/repo?ref=x//y", "git::https://github.com/org/repo?depth=0", "git::https://github.com/org/repo?depth=no", "git::https://github.com/org/repo?depth=1&depth=2", "git::https://github.com/org/repo?other=1"} {
		if _, e = parseModuleSource(raw, base, c); e == nil {
			t.Error(raw)
		}
	}
	c.URL = "https://storage.example"
	if _, e = parseModuleSource("github.com/org/repo", base, c); e == nil {
		t.Fatal("unconfigured github allowed")
	}
	on := true
	c.ExternalDependenciesEnabled = &on
	c.ExternalDependenciesPatterns = []string{"", "https://github.com/org/**"}
	if _, e = parseModuleSource("github.com/org/repo", base, c); e != nil {
		t.Fatal(e)
	}
}

func TestRemoteModuleChecksums(t *testing.T) {
	for _, tc := range []struct {
		algorithm string
		length    int
	}{{"md5", 32}, {"sha1", 40}, {"sha256", 64}, {"sha512", 128}} {
		for _, prefix := range []string{"", tc.algorithm + ":"} {
			sum, e := moduleChecksum(prefix + strings.Repeat("A", tc.length))
			if e != nil || !strings.Contains(sum.MD5+sum.SHA1+sum.SHA256+sum.SHA512, "aaa") {
				t.Fatal(sum, e)
			}
		}
	}
	for _, raw := range []string{"sha384:" + strings.Repeat("a", 96), "sha256:bad", "xxx", "file:/checksum", "sha1:" + strings.Repeat("g", 40)} {
		if _, e := moduleChecksum(raw); e == nil {
			t.Fatal(raw)
		}
	}
	if _, e := moduleChecksum(""); e != nil {
		t.Fatal(e)
	}
}

func TestRemoteModuleDocuments(t *testing.T) {
	m, _ := tf.ParseModule("ns/name/aws/1.0.0.zip")
	for _, raw := range []string{`null`, `{}`, `{"modules":[]}`, `{"modules":[{},{}]}`, `{"modules":[{}]}`, `{"modules":[{"versions":[{"version":"v1.0.0"}]}]}`, `{"modules":[{"versions":[{"version":"1.0.0"},{"version":"1.0.0"}]}]}`, `{"modules":[{"versions":[]}] } trailing`} {
		if _, e := parseModuleVersions(strings.NewReader(raw), m); e == nil {
			t.Fatal(raw)
		}
	}
	if versions, e := parseModuleVersions(
		strings.NewReader(`{"modules":[{"versions":[]}]}`),
		m,
	); e != nil ||
		len(versions) != 0 {
		t.Fatal(versions, e)
	}
	for _, raw := range []string{`null`, `{}`, `{"modules.v1":3}`, `{"modules.v1":"/v1/modules"}`, `{"modules.v1":"/v1/modules/?q=1"}`, `{"modules.v1":"http://example/v1/"}`} {
		if _, e := parseModuleDiscovery(
			strings.NewReader(raw),
			"https://up.example/discovery",
		); e == nil {
			t.Fatal(raw)
		}
	}
	if target, e := parseModuleDiscovery(
		strings.NewReader(`{"modules.v1":"../v1/modules/","future":true}`),
		"https://up.example/actual/discovery",
	); e != nil ||
		target != "https://up.example/v1/modules/" {
		t.Fatal(target, e)
	}
	c := repo.Config{URL: "https://up.example"}
	for _, raw := range []string{`null`, `{}`, `{"source":"/archive.zip","unexpected":"field"}`, `{"source":2}`} {
		if _, e := parseModuleLocation(strings.NewReader(raw), c.URL, c); e == nil {
			t.Fatal(raw)
		}
	}
	if _, e := parseModuleLocation(
		strings.NewReader(`{"source":"/archive.zip"}`),
		c.URL,
		c,
	); e != nil {
		t.Fatal(e)
	}
	var doc any
	if readModuleJSON(
		bytes.NewReader(bytes.Repeat([]byte(" "), moduleMetadataLimit+1)),
		&doc,
	) == nil {
		t.Fatal("oversize")
	}
	if readModuleJSON(brokenModuleReader{}, &doc) == nil {
		t.Fatal("read error")
	}
	for _, base := range []string{"https://%zz", ":bad"} {
		if _, e := moduleURL(base, "/archive.zip"); e == nil {
			t.Fatal(base)
		}
	}
}

type brokenModuleReader struct{}

func (brokenModuleReader) Read([]byte) (int, error) { return 0, errors.New("fixture read failure") }

var _ io.Reader = brokenModuleReader{}
