package debian

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

// formsIndex is a Packages index with one stanza.
var formsIndex = []byte("Package: alpha\nVersion: 1.0\nArchitecture: amd64\n" +
	"Filename: pool/alpha.deb\nSize: 4\nSHA256: " + strings.Repeat("a", 64) + "\n\n")

// decompressForm reads body back with the decoder of its own format from
// outside this package's code: the standard library for gzip and bzip2,
// ulikunitz/xz for xz and LZMA.
func decompressForm(t *testing.T, suffix string, body []byte) []byte {
	t.Helper()
	var r io.Reader
	var err error
	switch suffix {
	case "":
		return body
	case ".gz":
		r, err = gzip.NewReader(bytes.NewReader(body))
	case ".bz2":
		r = bzip2.NewReader(bytes.NewReader(body))
	case ".lzma":
		r, err = lzma.NewReader(bytes.NewReader(body))
	case ".xz":
		r, err = xz.NewReader(bytes.NewReader(body))
	default:
		t.Fatalf("no decoder for %q", suffix)
	}
	if err != nil {
		t.Fatalf("%s reader: %v", suffix, err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("%s read: %v", suffix, err)
	}
	return out
}

func suffixes(forms []IndexForm) []string {
	out := make([]string, 0, len(forms))
	for _, form := range forms {
		out = append(out, form.Suffix)
	}
	return out
}

// TestUnitIndexFormsWritesTheOptionalCompressionFormats: an index is
// written plain and gzipped whatever the setting says, and once in each of
// bz2, lzma and xz that optionalIndexCompressionFormats names, in a fixed
// order. Each form decompresses, with its format's own decoder, to the
// plain index, and DecodePackagesIndex reads each. Any other value, gz and
// zst among them, adds nothing.
func TestUnitIndexFormsWritesTheOptionalCompressionFormats(t *testing.T) {
	for _, tc := range []struct {
		name    string
		formats []string
		want    []string
	}{
		{"no setting", nil, []string{"", ".gz"}},
		{"empty setting", []string{}, []string{"", ".gz"}},
		{"every format", []string{"bz2", "lzma", "xz"}, []string{"", ".gz", ".bz2", ".lzma", ".xz"}},
		{
			"repeats, other order and other values",
			[]string{"xz", "gz", "zst", "BZ2", "xz", "bz2"},
			[]string{"", ".gz", ".bz2", ".xz"},
		},
		{"lzma alone", []string{"lzma"}, []string{"", ".gz", ".lzma"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forms, err := IndexForms(formsIndex, tc.formats)
			if err != nil {
				t.Fatal(err)
			}
			if got := suffixes(forms); !slices.Equal(got, tc.want) {
				t.Fatalf("forms %q, want %q", got, tc.want)
			}
			for _, form := range forms {
				if got := decompressForm(t, form.Suffix, form.Body); !bytes.Equal(got, formsIndex) {
					t.Errorf("%q decompresses to %q", form.Suffix, got)
				}
				rows, err := DecodePackagesIndex(form.Body)
				if err != nil || len(rows) != 1 || rows[0].Name != "alpha" {
					t.Errorf("%q decodes to %+v, %v", form.Suffix, rows, err)
				}
			}
		})
	}
	empty, err := IndexForms(nil, []string{"bz2", "lzma", "xz"})
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range empty {
		if got := decompressForm(t, form.Suffix, form.Body); len(got) != 0 {
			t.Errorf("empty index's %q form decompresses to %q", form.Suffix, got)
		}
		if rows, err := DecodePackagesIndex(form.Body); err != nil || len(rows) != 0 {
			t.Errorf("empty index's %q form decodes to %+v, %v", form.Suffix, rows, err)
		}
	}
}

// TestUnitIndexFormsPassTheSystemTools: bzip2, xz and xz's LZMA mode, the
// tools whose libraries apt decompresses an index with, test each written
// form as sound. A tool that is not installed skips its form.
func TestUnitIndexFormsPassTheSystemTools(t *testing.T) {
	forms, err := IndexForms(formsIndex, []string{"bz2", "lzma", "xz"})
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string][]string{
		".bz2":  {"bzip2", "-t"},
		".lzma": {"xz", "--format=lzma", "-t"},
		".xz":   {"xz", "--format=xz", "-t"},
	}
	dir := t.TempDir()
	for _, form := range forms {
		tool, ok := tools[form.Suffix]
		if !ok {
			continue
		}
		t.Run(form.Suffix, func(t *testing.T) {
			bin, err := exec.LookPath(tool[0])
			if err != nil {
				t.Skipf("%s is not installed", tool[0])
			}
			file := filepath.Join(dir, "Packages"+form.Suffix)
			if err := os.WriteFile(file, form.Body, 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.CommandContext(t.Context(), bin, append(tool[1:], file)...).
				CombinedOutput()
			if err != nil {
				t.Fatalf("%s rejected the form: %v: %s", strings.Join(tool, " "), err, out)
			}
		})
	}
}

// lzmaHeader is a .lzma header: a properties byte, a dictionary size and
// an uncompressed size.
func lzmaHeader(props byte, dict uint32, size uint64) []byte {
	h := make([]byte, lzma.HeaderLen)
	h[0] = props
	binary.LittleEndian.PutUint32(h[1:5], dict)
	binary.LittleEndian.PutUint64(h[5:13], size)
	return h
}

// TestUnitLzmaAloneHeaderTellsLzmaFromText: the .lzma form, which has no
// magic number, is told from a plain index by the checks xz's format
// detection makes, and a header that asks for a dictionary above the
// index's own bound is refused before one is allocated.
func TestUnitLzmaAloneHeaderTellsLzmaFromText(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
		want bool
	}{
		{"plain index", formsIndex, false},
		{"short body", []byte{0x5d, 0, 0, 0x80}, false},
		{"properties out of range", lzmaHeader(225, 1<<23, 10), false},
		{"no dictionary", lzmaHeader(0x5d, 0, 10), false},
		{"dictionary of another size", lzmaHeader(0x5d, 5<<20, 10), false},
		{"size above 2^38", lzmaHeader(0x5d, 1<<23, 1<<38+1), false},
		{"power of two dictionary", lzmaHeader(0x5d, 1<<23, 10), true},
		{"three times a power of two", lzmaHeader(0x5d, 3<<22, 10), true},
		{"unknown size", lzmaHeader(0x5d, 1<<16, ^uint64(0)), true},
	} {
		if got := lzmaAloneHeader(tc.body); got != tc.want {
			t.Errorf("%s: %t, want %t", tc.name, got, tc.want)
		}
	}
	if _, err := DecodePackagesIndex(lzmaHeader(0x5d, 1<<30, ^uint64(0))); err == nil {
		t.Error("an LZMA header asking for a 1 GiB dictionary was decoded")
	}
}

// TestUnitIndexNamesAndInstallerIndex: an index name may carry .lzma as it
// does .gz, .xz, .bz2 and .zst; an installer's Packages index is named only
// at dists/<d>/<c>/debian-installer/binary-<arch>/, and it is not one of
// the binary-<arch> indexes; its rows name .udeb files.
func TestUnitIndexNamesAndInstallerIndex(t *testing.T) {
	for _, p := range []string{
		"dists/stable/main/binary-amd64/Packages.lzma",
		"dists/stable/main/source/Sources.lzma",
	} {
		if !PackagesIndexPath(p) && !SourcesIndexPath(p) {
			t.Errorf("%s is not an index name", p)
		}
	}
	for _, p := range []string{
		"dists/stable/main/debian-installer/binary-amd64/Packages",
		"dists/stable/main/debian-installer/binary-amd64/Packages.gz",
		"dists/stable/main/debian-installer/binary-arm64/Packages.lzma",
	} {
		if !InstallerPackagesIndexPath(p) || PackagesIndexPath(p) {
			t.Errorf("%s: installer index %t, binary index %t", p,
				InstallerPackagesIndexPath(p), PackagesIndexPath(p))
		}
	}
	for _, p := range []string{
		"dists/stable/main/debian-installer/binary-amd64/Packages.lz4",
		"dists/stable/main/debian-installer/Packages",
		"dists/stable/main/debian-installer/source/Packages",
		"dists/stable/main/debian-installer/binary-AMD64/Packages",
		"dists/stable/main/other/binary-amd64/Packages",
		"pool/stable/main/debian-installer/binary-amd64/Packages",
		"dists/stable/main/debian-installer/binary-amd64/x/Packages",
		"dists/stable/main/binary-amd64/Packages",
	} {
		if InstallerPackagesIndexPath(p) {
			t.Errorf("%s named an installer index", p)
		}
	}
	udeb := []byte(strings.Replace(string(formsIndex), "pool/alpha.deb", "pool/alpha.udeb", 1))
	rows, err := DecodeInstallerPackagesIndex(udeb)
	if err != nil || len(rows) != 1 || rows[0].Filename != "pool/alpha.udeb" {
		t.Errorf("installer index: %+v, %v", rows, err)
	}
	if _, err := DecodeInstallerPackagesIndex(formsIndex); err == nil {
		t.Error("an installer index naming a .deb was accepted")
	}
	if _, err := DecodePackagesIndex(udeb); err == nil {
		t.Error("a binary index naming a .udeb was accepted")
	}
	if _, err := DecodeInstallerPackagesIndex([]byte{0x1f, 0x8b, 0}); err == nil {
		t.Error("a broken gzip installer index was accepted")
	}
}

// TestUnitByHashPathNamesAnyChecksumAddressedFile: a by-hash file is named
// by the shape of its last three segments wherever it is, and its
// SHA-256 is returned in lower case; ByHashMetadataPath still names only a
// binary-<arch> or source index's copy.
func TestUnitByHashPathNamesAnyChecksumAddressedFile(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	for _, p := range []string{
		"dists/stable/main/i18n/by-hash/SHA256/" + sum,
		"dists/stable/main/by-hash/SHA256/" + sum,
		"dists/stable/main/debian-installer/binary-amd64/by-hash/SHA256/" + sum,
		"dists/stable/main/binary-amd64/by-hash/SHA256/" + strings.ToUpper(sum),
		"pool/by-hash/SHA256/" + sum,
	} {
		if got, ok := ByHashPath(p); !ok || got != sum {
			t.Errorf("%s: %q, %t", p, got, ok)
		}
	}
	for _, p := range []string{
		"by-hash/SHA256/" + sum,
		"dists/stable/main/by-hash/SHA256/" + sum[:62],
		"dists/stable/main/by-hash/SHA256/" + strings.Repeat("zz", 32),
		"dists/stable/main/by-hash/MD5Sum/" + sum,
		"dists/stable/main/by-hash/SHA256/" + sum + "/x",
		"../stable/main/by-hash/SHA256/" + sum,
		"dists/stable/../main/by-hash/SHA256/" + sum,
		"dists/sta ble/main/by-hash/SHA256/" + sum,
	} {
		if got, ok := ByHashPath(p); ok {
			t.Errorf("%s named a by-hash file %q", p, got)
		}
	}
	if _, ok := ByHashMetadataPath("dists/stable/main/i18n/by-hash/SHA256/" + sum); ok {
		t.Error("a translation's copy was named an index copy")
	}
}

// TestUnitReleaseSelectsAnLzmaIndexWhenItIsTheOnlyOne: a member Release
// that declares only Packages.lzma has that index selected for a virtual
// merge, as one declaring only Packages.gz has the gzip index.
func TestUnitReleaseSelectsAnLzmaIndexWhenItIsTheOnlyOne(t *testing.T) {
	forms, err := IndexForms(formsIndex, []string{"lzma"})
	if err != nil {
		t.Fatal(err)
	}
	release, err := Release("stable", []string{"main"}, []string{"amd64"},
		[]ReleaseEntry{{Path: "main/binary-amd64/Packages.lzma", Body: forms[2].Body}},
		timeZero())
	if err != nil {
		t.Fatal(err)
	}
	refs, err := ReleasePackageIndexes("stable", release)
	if err != nil || len(refs) != 1 ||
		refs[0].Path != "dists/stable/main/binary-amd64/Packages.lzma" ||
		refs[0].BasePath() != "dists/stable/main/binary-amd64/Packages" {
		t.Fatalf("selected %+v, %v", refs, err)
	}
}
