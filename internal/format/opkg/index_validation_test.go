package opkg

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

func validIndexPackage() Package {
	return Package{
		Name: "alpha", Version: "1.0", Architecture: "all",
		Path: "feed/alpha_1.0_all.ipk", Size: 123,
		SHA256: strings.Repeat("a", 64), MD5: strings.Repeat("b", 32),
		Description: "A test package", Depends: "libc", License: "MIT",
	}
}

func TestIndexRoundTripAndRelease(t *testing.T) {
	t.Parallel()
	if feed, ok := FeedPath("Packages"); !ok || feed != "" {
		t.Fatal("root Packages path was refused")
	}
	if _, ok := FeedPath("bad%/Packages"); ok {
		t.Fatal("unsafe feed path was accepted")
	}
	first := validIndexPackage()
	second := first
	second.Name, second.Path = "beta", "feed/beta_1.0_all.ipk"
	files, err := EncodeIndex([]Package{second, first}, "feed")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(files[PackagesName], []byte("Package: alpha\n")) {
		t.Fatal("index did not sort by package path")
	}
	for _, name := range []string{PackagesName, PackagesGzipName} {
		got, err := DecodeIndex(files[name], "feed/"+name)
		if err != nil || len(got) != 2 || got[0].Path != first.Path ||
			got[0].SHA256 != first.SHA256 || got[0].MD5 != first.MD5 {
			t.Fatalf("%s round trip: %+v, %v", name, got, err)
		}
	}
	if err := ValidateRelease(files[ReleaseName]); err != nil {
		t.Fatal(err)
	}
	empty, err := EncodeIndex(nil, "feed")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeIndex(
		empty[PackagesGzipName],
		"feed/Packages.gz",
	); err != nil ||
		len(got) != 0 {
		t.Fatalf("empty feed: %+v %v", got, err)
	}
}

func TestDecodeIndexRejectsUnsafeRecords(t *testing.T) {
	t.Parallel()
	files, err := EncodeIndex([]Package{validIndexPackage()}, "feed")
	if err != nil {
		t.Fatal(err)
	}
	valid := string(files[PackagesName])
	for _, tc := range []struct{ name, body, path string }{
		{"unsafe feed", valid, "../Packages"},
		{"empty body", "", "feed/Packages"},
		{"NUL", strings.Replace(valid, "Package: alpha", "Package: al\x00pha", 1), "feed/Packages"},
		{"invalid UTF8", string([]byte{0xff, '\n', '\n'}), "feed/Packages"},
		{"no terminator", strings.TrimSuffix(valid, "\n"), "feed/Packages"},
		{"duplicate", valid + valid, "feed/Packages"},
		{"orphan continuation", " alpha\n" + valid, "feed/Packages"},
		{"missing name", strings.Replace(valid, "Package: alpha", "Name: alpha", 1), "feed/Packages"},
		{"traversal filename", strings.Replace(valid, "alpha_1.0_all.ipk", "../alpha_1.0_all.ipk", 1), "feed/Packages"},
		{"missing SHA", strings.Replace(valid, "SHA256sum: "+strings.Repeat("a", 64), "SHA256sum: ", 1), "feed/Packages"},
		{"bad SHA", strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("z", 64), 1), "feed/Packages"},
		{"size zero", strings.Replace(valid, "Size: 123", "Size: 0", 1), "feed/Packages"},
		{"size huge", strings.Replace(valid, "Size: 123", "Size: 99999999999999", 1), "feed/Packages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeIndex([]byte(tc.body), tc.path); err == nil {
				t.Fatal("invalid index accepted")
			}
		})
	}
	if _, err := DecodeIndex([]byte("not gzip"), "feed/Packages.gz"); err == nil {
		t.Fatal("invalid compressed index accepted")
	}
	broken := bytes.Clone(files[PackagesGzipName])
	broken[len(broken)-8] ^= 0xff
	if _, err := DecodeIndex(broken, "feed/Packages.gz"); err == nil {
		t.Fatal("corrupt gzip CRC accepted")
	}
	var expanded bytes.Buffer
	z := gzip.NewWriter(&expanded)
	if _, err := z.Write(bytes.Repeat([]byte("x"), maxIndexBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeIndex(expanded.Bytes(), "feed/Packages.gz"); err == nil {
		t.Fatal("oversized expanded index accepted")
	}
}

func TestEncodeIndexAndReleaseRejectInvalidInput(t *testing.T) {
	t.Parallel()
	valid := validIndexPackage()
	for _, tc := range []struct {
		name    string
		entries []Package
	}{
		{"wrong feed", []Package{valid}},
		{"duplicate", []Package{valid, valid}},
		{"bad digest", []Package{func() Package { p := valid; p.SHA256 = "bad"; return p }()}},
		{"bad metadata", []Package{func() Package { p := valid; p.Depends = "libc\nInjected: yes"; return p }()}},
		{"zero size", []Package{func() Package { p := valid; p.Size = 0; return p }()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feed := "feed"
			if tc.name == "wrong feed" {
				feed = "other"
			}
			if _, err := EncodeIndex(tc.entries, feed); err == nil {
				t.Fatal("invalid package record accepted")
			}
		})
	}
	files, err := EncodeIndex([]Package{valid}, "feed")
	if err != nil {
		t.Fatal(err)
	}
	good := string(files[ReleaseName])
	releaseSize := strings.Fields(strings.Split(good, "\n")[0])[2]
	for _, release := range []string{
		"", "not a release\n", strings.TrimSuffix(good, "\n"),
		strings.Replace(good, "SHA256:", "MD5:", 1),
		strings.Replace(good, "Packages.gz", "../Packages.gz", 1),
		strings.Replace(good, " "+releaseSize+" Packages", " 0 Packages", 1),
		good + good,
	} {
		if err := ValidateRelease([]byte(release)); err == nil {
			t.Fatalf("invalid Release accepted: %q", release)
		}
	}
}

func TestIndexDigestAndReleaseTrustBoundaries(t *testing.T) {
	valid := validIndexPackage()
	if _, ok := FeedPath("feed/alpha_1.0_all.ipk"); ok {
		t.Fatal("package path classified as feed metadata")
	}
	files, err := EncodeIndex([]Package{valid}, "feed")
	if err != nil {
		t.Fatal(err)
	}
	badHex := strings.Replace(
		string(files[PackagesName]),
		strings.Repeat("a", 64),
		strings.Repeat("g", 64),
		1,
	)
	if _, err := DecodeIndex([]byte(badHex), "feed/Packages"); err == nil {
		t.Fatal("non-hexadecimal package digest accepted")
	}
	valid.SHA256 = strings.Repeat("g", 64)
	if _, err := EncodeIndex([]Package{valid}, "feed"); err == nil {
		t.Fatal("non-hexadecimal package digest encoded")
	}
	goodRelease := string(files[ReleaseName])
	firstDigest := strings.Fields(strings.SplitN(goodRelease, "\n", 2)[0])[1]
	for _, bad := range []string{
		strings.Replace(goodRelease, firstDigest, strings.Repeat("g", 64), 1),
		strings.SplitN(goodRelease, "\n", 2)[0] + "\n",
	} {
		if err := ValidateRelease([]byte(bad)); err == nil {
			t.Fatal("Release lacking trusted digests accepted")
		}
	}
}
