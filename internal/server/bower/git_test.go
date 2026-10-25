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
	for _, tc := range []struct {
		key  string
		want bool
	}{{"comment", true}, {"path", false}, {"linkpath", false}} {
		content := tc.key + "=danger\n"
		n := len(content) + 2
		for len(fmt.Sprint(n))+1+len(content) != n {
			n = len(fmt.Sprint(n)) + 1 + len(content)
		}
		if got := safePAXGlobal([]byte(fmt.Sprintf("%d %s", n, content))); got != tc.want {
			t.Errorf("PAX global %s allowed=%t, want %t", tc.key, got, tc.want)
		}
	}
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
