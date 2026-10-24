package debian

import (
	"bytes"
	"crypto/md5" //nolint:gosec // Debian .dsc Files wire checksum.
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadDpkgSourceFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/alpha_1.0-1.dsc")
	if err != nil {
		t.Fatal(err)
	}
	src, err := ReadSource(t.Context(), bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if src.Name != "alpha" || src.Version != "1.0-1" ||
		src.Format != "3.0 (quilt)" || len(src.Parts) != 2 {
		t.Fatalf("dpkg-source coordinates: %+v", src)
	}
	for _, p := range src.Parts {
		archive, err := os.ReadFile(filepath.Join("testdata", p.Name))
		if err != nil {
			t.Fatal(err)
		}
		sha := sha256.Sum256(archive)
		md5sum := md5.Sum(archive) //nolint:gosec // Debian .dsc Files wire checksum.
		if p.Size != int64(len(archive)) || p.SHA256 != fmt.Sprintf("%x", sha) ||
			p.MD5 != fmt.Sprintf("%x", md5sum) {
			t.Fatalf("dpkg-source archive declaration mismatch: %+v", p)
		}
	}
}

func TestReadSourceRejectsInvalidControl(t *testing.T) {
	body, err := os.ReadFile("testdata/alpha_1.0-1.dsc")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	alter := func(section, old, replacement string) string {
		before, after, ok := strings.Cut(text, section)
		if !ok {
			t.Fatalf("fixture lacks %s", section)
		}
		return before + section + strings.Replace(after, old, replacement, 1)
	}
	for name, bad := range map[string]string{
		"missing checksum": strings.Replace(text, "Checksums-Sha256:", "Checksums-Unknown:", 1),
		"duplicate field":  text + "Source: forged\n",
		"second stanza":    text + "\nSource: forged\n",
		"path traversal": alter("Checksums-Sha256:\n", "alpha_1.0.orig.tar.gz",
			"../alpha_1.0.orig.tar.gz"),
		"mismatched size": alter("Files:\n", " 153 alpha_1.0.orig.tar.gz",
			" 154 alpha_1.0.orig.tar.gz"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadSource(t.Context(), strings.NewReader(bad), int64(len(bad))); err == nil {
				t.Fatal("invalid .dsc accepted")
			}
		})
	}
	if _, err := ReadSource(t.Context(), bytes.NewReader(body), maxSourceControl+1); err == nil {
		t.Fatal("oversized .dsc accepted")
	}
}
