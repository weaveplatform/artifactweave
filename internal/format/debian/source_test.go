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
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitReadDpkgSourceFixture(t *testing.T) {
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

func TestUnitReadSourceRejectsInvalidControl(t *testing.T) {
	body, err := os.ReadFile("testdata/alpha_1.0-1.dsc")
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.ReplaceAll(string(body), "\r\n", "\n")
	for _, ending := range []struct{ name, value string }{
		{"lf", "\n"}, {"crlf", "\r\n"},
	} {
		t.Run(ending.name, func(t *testing.T) {
			text := strings.ReplaceAll(raw, "\n", ending.value)
			if _, err := ReadSource(
				t.Context(),
				strings.NewReader(text),
				int64(len(text)),
			); err != nil {
				t.Fatalf("valid %s .dsc rejected: %v", ending.name, err)
			}
			alter := func(section, old, replacement string) string {
				before, after, ok := strings.Cut(text, section)
				if !ok {
					t.Fatalf("fixture lacks %s", section)
				}
				return before + section + strings.Replace(after, old, replacement, 1)
			}
			for name, bad := range map[string]string{
				"missing checksum": strings.Replace(text, "Checksums-Sha256:", "Checksums-Unknown:", 1),
				"duplicate field":  text + "Source: forged" + ending.value,
				"second stanza":    text + ending.value + "Source: forged" + ending.value,
				"path traversal": alter("Checksums-Sha256:", "alpha_1.0.orig.tar.gz",
					"../alpha_1.0.orig.tar.gz"),
				"mismatched size": alter("Files:", " 153 alpha_1.0.orig.tar.gz",
					" 154 alpha_1.0.orig.tar.gz"),
			} {
				t.Run(name, func(t *testing.T) {
					if _, err := ReadSource(
						t.Context(),
						strings.NewReader(bad),
						int64(len(bad)),
					); err == nil {
						t.Fatal("invalid .dsc accepted")
					}
				})
			}
		})
	}
	if _, err := ReadSource(t.Context(), bytes.NewReader(body), maxSourceControl+1); err == nil {
		t.Fatal("oversized .dsc accepted")
	}
}

func TestUnitSourceIndexRequiresEveryStoredArchiveDigest(t *testing.T) {
	st := inmem.New()
	const folder = "pool/main/a/alpha/"
	names := []string{
		"alpha_1.0-1.dsc", "alpha_1.0.orig.tar.gz",
		"alpha_1.0-1.debian.tar.xz",
	}
	items := make([]types.Item, 0, len(names))
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		sha := sha256.Sum256(body)
		md5sum := md5.Sum(body) //nolint:gosec // Debian Sources wire checksum.
		it, err := st.Items().Put(t.Context(), types.Item{
			RepoKey: "deb-local", Name: folder + name, Size: int64(len(body)),
			Actual: types.Checksums{SHA256: fmt.Sprintf("%x", sha), MD5: fmt.Sprintf("%x", md5sum)},
		})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, it)
	}
	if err := st.Items().SetProperties(t.Context(), items[0].ID, types.Properties{
		"dsc.distribution": {"stable"}, "dsc.component": {"main"},
	}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("testdata/alpha_1.0-1.dsc")
	if err != nil {
		t.Fatal(err)
	}
	src, err := ReadSource(t.Context(), bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: st, Inspection: sourceInspection{
		sha: items[0].Actual.SHA256, path: items[0].FullPath(), source: src,
	}}
	key := aptCoordinates{"stable", "main"}
	groups, err := collectSourceGroups(t.Context(), env, items)
	if err != nil || len(groups[key]) != 1 {
		t.Fatalf("complete source not indexed: %v %+v", err, groups)
	}
	index, err := Sources(groups[key])
	if err != nil || !strings.Contains(string(index), "Package: alpha\n") ||
		!strings.Contains(string(index), "Directory: "+strings.TrimSuffix(folder, "/")+"\n") ||
		!strings.Contains(string(index), items[0].Actual.SHA256) {
		t.Fatalf("Sources metadata incomplete: %v %s", err, index)
	}
	rows, err := DecodeSourcesIndex(index)
	if err != nil || len(rows) != 1 || len(rows[0].Files) != 3 {
		t.Fatalf("generated Sources index is not readable: %v %+v", err, rows)
	}
	if _, err := DecodeSourcesIndex(append(bytes.Clone(index), 0)); err == nil {
		t.Fatal("Sources index with trailing invalid bytes was accepted")
	}
	groups, err = collectSourceGroups(t.Context(), env, items[:2])
	if err != nil || len(groups) != 0 {
		t.Fatalf("incomplete source indexed: %v %+v", err, groups)
	}
	corrupt := append([]types.Item(nil), items...)
	corrupt[2].Actual.SHA256 = strings.Repeat("0", 64)
	groups, err = collectSourceGroups(t.Context(), env, corrupt)
	if err != nil || len(groups) != 0 {
		t.Fatalf("corrupt source indexed: %v %+v", err, groups)
	}
}
