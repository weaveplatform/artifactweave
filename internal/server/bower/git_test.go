package bower

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitGitArchiveGlobalHeaderAndUnversionedManifest(t *testing.T) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct {
		name, body string
		kind       byte
	}{
		{"pax_global_header", "", tar.TypeXGlobalHeader},
		{"hello-1.2.3/", "", tar.TypeDir},
		{"hello-1.2.3/bower.json", `{"name":"hello"}`, tar.TypeReg},
		{"hello-1.2.3/index.js", "valid", tar.TypeReg},
	} {
		if err := tw.WriteHeader(
			&tar.Header{Name: entry.name, Typeflag: entry.kind, Size: int64(len(entry.body))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateArchive(
		"hello",
		"1.2.3",
		true,
	)(
		t.Context(),
		bytes.NewReader(out.Bytes()),
		int64(out.Len()),
	); err != nil {
		t.Fatalf("authentic Git archive layout refused: %v", err)
	}
	if err := validateArchive(
		"hello",
		"1.2.3",
	)(
		t.Context(),
		bytes.NewReader(out.Bytes()),
		int64(out.Len()),
	); err == nil {
		t.Fatal("local archive admitted an unversioned manifest")
	}
}

func TestUnitGitRegistryPointerAndArchiveSourceBoundaries(t *testing.T) {
	c := repo.Config{
		URL:              "https://github.com/allowed",
		BowerRegistryURL: "https://registry.example/v1",
	}
	for _, raw := range []string{
		"", "ssh://github.com/allowed/team/repo.git", "https://user:pass@github.com/allowed/team/repo.git",
		"https://github.com/allowed/team/repo.git#fragment", "https://github.com/allowed/team/repo.git?token=secret",
		"https://github.com/allowedness/team/repo.git", "https://github.com/allowed/team/repo",
		"https://github.com/allowed/team/../repo.git", "https://github.com/allowed/team/repo%2egit",
	} {
		if _, err := gitPointerURL(c, "safe", raw); err == nil {
			t.Errorf("accepted pointer outside configured source: %q", raw)
		}
	}
	for _, raw := range []string{
		``, `{"name":"other","url":"https://github.com/allowed/team/repo.git"}`,
		`{"name":"safe","url":"https://evil.example/team/repo.git"}`,
		`{"name":"safe","url":"ssh://github.com/allowed/team/repo.git"}`,
		strings.Repeat("x", maxRefsBytes+1),
	} {
		if _, err := decodePointer([]byte(raw), c, "safe"); err == nil {
			t.Errorf("accepted registry pointer %q", raw)
		}
	}
	for _, raw := range []string{``, `ssh://registry.example/`, `https://user:pass@registry.example/`} {
		bad := c
		bad.BowerRegistryURL = raw
		if _, err := registryTarget(bad, "/safe"); err == nil {
			t.Errorf("accepted registry URL %q", raw)
		}
	}
	target, err := registryTarget(c, "/safe")
	if err != nil || target.String() != "https://registry.example/v1/packages/safe" {
		t.Fatalf("registry target = %v, %v", target, err)
	}
	guard := exactTarget(target)
	for _, raw := range []string{
		"https://evil.example/v1/packages/safe", "https://registry.example/v1/packages/other",
		"https://registry.example/v1/packages/safe?token=x", "https://user:pass@registry.example/v1/packages/safe",
	} {
		u, _ := url.Parse(raw)
		if guard(context.Background(), u) == nil {
			t.Errorf("redirect escaped exact registry target: %q", raw)
		}
	}
	p, err := gitPointerURL(c, "safe", "https://github.com/allowed/team/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		provider repo.VCSGitProvider
		expected string
	}{
		{repo.GitHub, "https://github.com/allowed/team/repo/archive/refs/tags/v1.2.3.tar.gz"},
		{repo.Bitbucket, "https://github.com/allowed/team/repo/get/v1.2.3.tar.gz"},
	} {
		cc := c
		cc.VCSGitProvider = tc.provider
		u, check, err := gitArchiveURL(cc, p, "v1.2.3")
		if err != nil || u.String() != tc.expected || check(context.Background(), u) != nil {
			t.Fatalf("provider archive = %v, %v", u, err)
		}
		other, _ := url.Parse("https://attacker.example/archive.tar.gz")
		if check(context.Background(), other) == nil {
			t.Fatal("archive redirect escaped source")
		}
	}
	if _, _, err := gitArchiveURL(c, p, "../main"); err == nil {
		t.Fatal("unsafe tag admitted")
	}
	broken := c
	broken.VCSGitDownloadURL = "https://cdn.example/{unknown}/{2}.tar.gz"
	if _, _, err := gitArchiveURL(broken, p, "v1.2.3"); err == nil {
		t.Fatal("unknown download-template expansion admitted")
	}
	broken.VCSGitDownloadURL = "https://cdn.example/{0}/{1}/{2}.{3}"
	u, check, err := gitArchiveURL(broken, p, "v1.2.3")
	if err != nil || u.String() != "https://cdn.example/team/repo/v1.2.3.tar.gz" ||
		check(context.Background(), u) != nil {
		t.Fatalf("configured archive URL = %v, %v", u, err)
	}
	bad, _ := url.Parse("https://cdn.example/team/other/v1.2.3.tar.gz")
	if check(context.Background(), bad) == nil {
		t.Fatal("configured archive redirect escaped exact path")
	}
	broken.VCSGitDownloadURL = "ssh://cdn.example/{0}/{1}/{2}.{3}"
	if _, _, err := gitArchiveURL(broken, p, "v1.2.3"); err == nil {
		t.Fatal("unsupported configured archive transport was accepted")
	}
}

func TestUnitGitRegistryCatalogRejectsAmbiguousNamesAndFiltersOutsideSource(t *testing.T) {
	c := repo.Config{URL: "https://github.com/allowed"}
	var overLimit strings.Builder
	overLimit.WriteByte('[')
	for i := range 100001 {
		if i > 0 {
			overLimit.WriteByte(',')
		}
		overLimit.WriteString(`{"name":"same","url":"https://github.com/allowed/team/same.git"}`)
	}
	overLimit.WriteByte(']')
	if _, err := parseGitCatalog(c, []byte(overLimit.String())); err == nil {
		t.Fatal("accepted an unbounded Git registry catalog")
	}
	for _, raw := range []string{
		`bad json`, `[{"name":"../escape","url":"https://github.com/allowed/x/y.git"}]`,
		`[{"name":"same","url":"https://github.com/allowed/x/y.git"},{"name":"same","url":"https://github.com/allowed/x/z.git"}]`,
	} {
		if _, err := parseGitCatalog(c, []byte(raw)); err == nil {
			t.Errorf("accepted ambiguous Git catalog %q", raw)
		}
	}
	names, err := parseGitCatalog(c, []byte(`[
		{"name":"zeta","url":"https://github.com/allowed/team/zeta.git"},
		{"name":"outside","url":"https://evil.example/team/secret.git"},
		{"name":"alpha","url":"https://github.com/allowed/team/alpha.git"}
	]`))
	if err != nil || strings.Join(names, ",") != "alpha,zeta" {
		t.Fatalf("source-scoped catalog = %v, %v", names, err)
	}
}

func TestUnitGitSmartRefsRejectsMalformedAndAmbiguousPackets(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, raw := range []string{
		"", "0003", "0009short", "xyz0", "000", // edge framing
		gitPkt(strings.Repeat("z", 40) + " refs/tags/v1.0.0\n"),
		gitPkt(sha+" refs/tags/v1.0.0\n") + gitPkt("short refs/tags/v2.0.0\n"),
		gitPkt(sha+" refs/tags/v1.0.0\n") + "0002" + "00",
	} {
		if _, err := parseGitRefs([]byte(raw)); err == nil {
			t.Errorf("accepted malformed packet line %q", raw)
		}
	}
	refs, err := parseGitRefs([]byte(gitPkt(sha+" refs/heads/main\n") +
		gitPkt(sha+" refs/tags/not-semver\n") + gitPkt(sha+" refs/tags/v1.2.3\n")))
	if err != nil || len(refs) != 1 || refs[0].Version != "1.2.3" {
		t.Fatalf("non-version refs were exposed: %#v, %v", refs, err)
	}
	var tooMany strings.Builder
	for i := range 4097 {
		tooMany.WriteString(gitPkt(sha + fmt.Sprintf(" refs/tags/v1.0.%d\n", i)))
	}
	if _, err := parseGitRefs([]byte(tooMany.String())); err == nil {
		t.Fatal("accepted more than 4096 Git version tags")
	}
	if _, err := parseGitRefs(bytes.Repeat([]byte("0000"), 65537)); err == nil {
		t.Fatal("accepted an unbounded Git packet stream")
	}
	if _, err := gitPointerURL(
		repo.Config{URL: "ssh://host/repo"},
		"safe",
		"https://host/repo/team/safe.git",
	); err == nil {
		t.Fatal("accepted Git pointer with unsupported configured transport")
	}
	if _, err := gitPointerURL(
		repo.Config{URL: "https://host/repo"},
		"../escape",
		"https://host/repo/team/safe.git",
	); err == nil {
		t.Fatal("accepted invalid registry package name")
	}
}

func gitPkt(s string) string { return hexLength(len(s)+4) + s }

func hexLength(n int) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[n>>12&15], hex[n>>8&15], hex[n>>4&15], hex[n&15]})
}

func TestUnitGitSmartRefsAndPointerPolicy(t *testing.T) {
	sha := strings.Repeat("a", 40)
	packet := gitPkt("# service=git-upload-pack\n") + "0000" +
		gitPkt(sha+" HEAD\x00multi_ack\n") +
		gitPkt(sha+" refs/tags/v1.2.3\n") +
		gitPkt(sha+" refs/tags/v1.2.3^{}\n") + "0000"
	refs, err := parseGitRefs([]byte(packet))
	if err != nil || len(refs) != 1 || refs[0].Version != "1.2.3" || refs[0].Tag != "v1.2.3" {
		t.Fatalf("parse Git refs = %#v, %v", refs, err)
	}
	for _, bad := range []string{"0005x", "gggg", gitPkt("not-a-ref\n")} {
		if _, err := parseGitRefs([]byte(bad)); err == nil {
			t.Errorf("accepted malformed Git refs %q", bad)
		}
	}
	aliases, err := parseGitRefs(
		[]byte(gitPkt(sha+" refs/tags/1.2.3\n") + gitPkt(sha+" refs/tags/v1.2.3\n")),
	)
	if err != nil || len(aliases) != 1 || aliases[0].Tag != "v1.2.3" {
		t.Fatalf("Git tag aliases = %#v, %v", aliases, err)
	}
	c := repo.Config{URL: "https://github.com/"}
	for _, raw := range []string{
		"https://attacker.example/o/repo.git", "http://github.com/o/repo.git",
		"https://github.com/o/other/third.git", "ssh://github.com/o/repo.git",
		"https://github.com/o/repo.git?token=x", "https://github.com/o/%2e%2e/repo.git",
	} {
		if _, err := gitPointerURL(c, "hello", raw); err == nil {
			t.Errorf("accepted untrusted Git pointer %q", raw)
		}
	}
	p, err := gitPointerURL(c, "hello", "https://github.com/o/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	archive, guard, err := gitArchiveURL(c, p, "v1.2.3")
	if err != nil ||
		archive.String() != "https://github.com/o/repo/archive/refs/tags/v1.2.3.tar.gz" {
		t.Fatalf("archive URL = %v, %v", archive, err)
	}
	good, _ := url.Parse("https://codeload.github.com/o/repo/tar.gz/refs/tags/v1.2.3")
	bad, _ := url.Parse("https://codeload.github.com/o/other/tar.gz/refs/tags/v1.2.3")
	if guard(context.Background(), good) != nil || guard(context.Background(), bad) == nil {
		t.Fatal("codeload redirect scope")
	}
}
