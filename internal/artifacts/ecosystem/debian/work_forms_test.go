package debian

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // Debian Release wire checksum.
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// rebuildWithFormats stores a repository of class with formats as its
// optionalIndexCompressionFormats, one amd64 package in stable/main and,
// when withSource is set, the dpkg-built alpha source package beside it,
// runs the metadata rebuild and returns what it derived by path.
func rebuildWithFormats(
	t *testing.T, class repo.Class, formats []string, withSource bool,
) map[string][]byte {
	t.Helper()
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{
		Key: "deb-forms", Class: class, PackageType: repo.Debian,
		OptionalIndexCompressionFormats: formats,
	})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	body := fixture(t, validControl)
	b, err := ReadBinary(t.Context(), bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	const path = "pool/main/h/hello/hello_1.0-1_amd64.deb"
	it, err := st.Items().Put(t.Context(), model.Item{
		RepoKey: c.Key, Name: path, Size: int64(len(body)),
		Actual: kernel.Checksums{
			SHA256: fmt.Sprintf("%x", sha256.Sum256(body)),
			MD5:    fmt.Sprintf("%x", md5.Sum(body)), //nolint:gosec // Packages wire checksum.
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Items().SetProperties(t.Context(), it.ID, model.Properties{
		PropDistribution: {"stable"}, PropComponent: {"main"}, PropArchitecture: {"amd64"},
	}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	env := format.Env{
		Store:      st,
		Now:        func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) },
		Inspection: inspection{sha: it.Actual.SHA256, path: path, binary: b},
	}
	if withSource {
		entry := dpkgSourceIndexEntry(t)
		var descriptor model.Item
		for _, name := range []string{
			"alpha_1.0-1.dsc", "alpha_1.0.orig.tar.gz", "alpha_1.0-1.debian.tar.xz",
		} {
			part, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			stored, err := st.Items().Put(t.Context(), model.Item{
				RepoKey: c.Key, Name: "pool/main/a/alpha/" + name, Size: int64(len(part)),
				Actual: sourceTestChecksums(part),
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(name, ".dsc") {
				descriptor = stored
			}
		}
		if err := st.Items().SetProperties(t.Context(), descriptor.ID, model.Properties{
			"dsc.distribution": {"stable"}, "dsc.component": {"main"},
		}, time.Time{}); err != nil {
			t.Fatal(err)
		}
		// The rebuild inspects one item; the binary is given by its
		// properties above and read through the source's inspection here.
		env.Inspection = sourceInspection{
			sha: descriptor.Actual.SHA256, path: descriptor.FullPath(), source: entry.Source,
		}
		env.Blobs = debianFixtureBlobs{body: body}
	}
	generated := map[string][]byte{}
	env.Derive = func(_ context.Context, d format.Derived) (model.Item, error) {
		got, err := io.ReadAll(d.Body)
		if err != nil {
			return model.Item{}, err
		}
		generated[d.Path] = got
		return model.Item{}, nil
	}
	env.Underive = func(context.Context, repo.Config, bool, string) error { return nil }
	if err := (Handler{}).Work(
		t.Context(),
		env,
		work.WorkItem{Kind: KindIndex, Key: c.Key},
	); err != nil {
		t.Fatal(err)
	}
	return generated
}

// releaseRows returns the paths a Release's SHA256 section lists, each
// with its size and digest as the row gives them.
func releaseRows(release []byte) map[string]string {
	out := map[string]string{}
	_, section, _ := strings.Cut(string(release), "SHA256:\n")
	for _, line := range strings.Split(strings.TrimSpace(section), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 {
			out[f[2]] = f[0] + " " + f[1]
		}
	}
	return out
}

// TestUnitWorkWritesTheOptionalIndexForms: a local or federated
// repository configured with bz2, lzma and xz writes each Packages index in
// those forms beside Packages and Packages.gz, each decompressing to the
// Packages index; its Release lists every form with its size and SHA-256,
// and each form has a by-hash copy (#1274). The JFrog Debian guide's
// Optional Index Compression Formats creates them "in addition to the
// default Gzip".
func TestUnitWorkWritesTheOptionalIndexForms(t *testing.T) {
	const dir = "dists/stable/main/binary-amd64/"
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			generated := rebuildWithFormats(t, class, []string{"bz2", "lzma", "xz"}, false)
			plain := generated[dir+"Packages"]
			if len(plain) == 0 {
				t.Fatalf("no Packages index: %v", keys(generated))
			}
			rows := releaseRows(generated["dists/stable/Release"])
			for _, suffix := range []string{"", ".gz", ".bz2", ".lzma", ".xz"} {
				body, ok := generated[dir+"Packages"+suffix]
				if !ok {
					t.Errorf("Packages%s not written", suffix)
					continue
				}
				if got := decompressForm(t, suffix, body); !bytes.Equal(got, plain) {
					t.Errorf("Packages%s decompresses to %q", suffix, got)
				}
				sum := fmt.Sprintf("%x", sha256.Sum256(body))
				want := fmt.Sprintf("%s %d", sum, len(body))
				if got := rows["main/binary-amd64/Packages"+suffix]; got != want {
					t.Errorf("Release lists Packages%s as %q, want %q", suffix, got, want)
				}
				if !bytes.Equal(generated[dir+"by-hash/SHA256/"+sum], body) {
					t.Errorf("Packages%s has no by-hash copy", suffix)
				}
			}
			release := string(generated["dists/stable/Release"])
			if strings.Count(release, "main/binary-amd64/Packages") != 10 {
				t.Errorf("Release lists other forms than five, twice each:\n%s", release)
			}
		})
	}
}

// TestUnitWorkWithoutTheSettingWritesPlainAndGzipOnly: an empty or unset
// optionalIndexCompressionFormats keeps the Packages and Packages.gz the
// rebuild wrote before #1274, and a Sources index keeps its plain and
// gzip forms whatever the setting names [I].
func TestUnitWorkWithoutTheSettingWritesPlainAndGzipOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		formats []string
		source  bool
	}{
		{"unset", nil, false},
		{"empty", []string{}, false},
		{"Sources with every format", []string{"bz2", "lzma", "xz"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			generated := rebuildWithFormats(t, repo.Local, tc.formats, tc.source)
			var indexes []string
			for _, p := range keys(generated) {
				if strings.Contains(p, "/Packages") || strings.Contains(p, "/Sources") {
					indexes = append(indexes, p)
				}
			}
			want := []string{
				"dists/stable/main/binary-amd64/Packages",
				"dists/stable/main/binary-amd64/Packages.gz",
			}
			if len(tc.formats) != 0 {
				want = append(want,
					"dists/stable/main/binary-amd64/Packages.bz2",
					"dists/stable/main/binary-amd64/Packages.lzma",
					"dists/stable/main/binary-amd64/Packages.xz")
			}
			if tc.source {
				want = append(want,
					"dists/stable/main/source/Sources", "dists/stable/main/source/Sources.gz")
			}
			slices.Sort(want)
			if !slices.Equal(indexes, want) {
				t.Errorf("indexes %q, want %q", indexes, want)
			}
		})
	}
}
