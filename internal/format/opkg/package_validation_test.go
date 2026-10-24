package opkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

type ipkEntry struct {
	name string
	body []byte
}

func openWrtMembers(t *testing.T) []ipkEntry {
	t.Helper()
	body, err := os.ReadFile("testdata/464xlat_13_x86_64.ipk")
	if err != nil {
		t.Fatal(err)
	}
	z, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = z.Close() }()
	tr := tar.NewReader(z)
	var entries []ipkEntry
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		member, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, ipkEntry{strings.TrimPrefix(h.Name, "./"), member})
	}
	if len(entries) != 3 {
		t.Fatalf("OpenWrt IPK members: %d", len(entries))
	}
	return entries
}

func tarGzipEntries(t *testing.T, entries []ipkEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	tw := tar.NewWriter(z)
	for _, e := range entries {
		if err := tw.WriteHeader(
			&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func arEntries(t *testing.T, entries []ipkEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("!<arch>\n")
	for _, e := range entries {
		_, err := fmt.Fprintf(&out, "%-16s%-12d%-6d%-6d%-8s%-10d`\n",
			e.name+"/", 0, 0, 0, "100644", len(e.body))
		if err != nil {
			t.Fatal(err)
		}
		out.Write(e.body)
		if len(e.body)%2 != 0 {
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}

func TestReadLegacyArAndRejectMalformedArchives(t *testing.T) {
	t.Parallel()
	entries := openWrtMembers(t)
	valid := arEntries(t, entries)
	if pkg, err := ReadPackage(t.Context(), bytes.NewReader(valid), int64(len(valid)),
		"feed/464xlat_13_x86_64.ipk"); err != nil || pkg.Name != "464xlat" {
		t.Fatalf("legacy ar IPK: %+v %v", pkg, err)
	}
	for _, tc := range []struct {
		name string
		body []byte
		path string
		size int64
	}{
		{"invalid path", valid, "../unsafe.ipk", int64(len(valid))},
		{"zero size", valid, "feed/464xlat_13_x86_64.ipk", 0},
		{"short ar", []byte("!<arch>\n"), "feed/464xlat_13_x86_64.ipk", 8},
		{"missing member", arEntries(t, entries[:2]), "feed/464xlat_13_x86_64.ipk", int64(len(arEntries(t, entries[:2])))},
		{"duplicate member", arEntries(t, append(append([]ipkEntry{}, entries...), entries[0])), "feed/464xlat_13_x86_64.ipk", int64(len(arEntries(t, append(append([]ipkEntry{}, entries...), entries[0]))))},
		{"bad ar header", append(bytes.Clone(valid[:8]), bytes.Repeat([]byte{'x'}, 60)...), "feed/464xlat_13_x86_64.ipk", 68},
		{"wrong identity", valid, "feed/other_13_x86_64.ipk", int64(len(valid))},
		{"missing tar member", tarGzipEntries(t, entries[:2]), "feed/464xlat_13_x86_64.ipk", int64(len(tarGzipEntries(t, entries[:2])))},
		{"duplicate tar member", tarGzipEntries(t, append(append([]ipkEntry{}, entries...), entries[0])), "feed/464xlat_13_x86_64.ipk", int64(len(tarGzipEntries(t, append(append([]ipkEntry{}, entries...), entries[0]))))},
		{"unexpected tar member", tarGzipEntries(t, append(append([]ipkEntry{}, entries...), ipkEntry{"evil", []byte("x")})), "feed/464xlat_13_x86_64.ipk", int64(len(tarGzipEntries(t, append(append([]ipkEntry{}, entries...), ipkEntry{"evil", []byte("x")}))))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadPackage(
				t.Context(),
				bytes.NewReader(tc.body),
				tc.size,
				tc.path,
			); err == nil {
				t.Fatal("invalid IPK accepted")
			}
		})
	}
}

func TestValidatePathsAndControlFields(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"", "/abs/a.ipk", "../a.ipk", "a/../b.ipk", "a space.ipk", "a.tar.gz", "a//b.ipk"} {
		if ValidPath(p) {
			t.Errorf("unsafe path accepted: %q", p)
		}
	}
	for _, p := range []string{"a.ipk", "feed/alpha_1.0_all.ipk"} {
		if !ValidPath(p) {
			t.Errorf("valid path refused: %q", p)
		}
	}
	for _, body := range []string{
		"", " bad\n", "Package: alpha", "Package: alpha\x00\n",
		"Package: alpha\nPackage: beta\n", "Package alpha\n",
		"Package: alpha\r\n", "Package: alpha\n bad\n\x00",
	} {
		if _, err := parseFields([]byte(body)); err == nil {
			t.Errorf("invalid control accepted: %q", body)
		}
	}
	fields, err := parseFields([]byte("Package: alpha\nDescription: first\n second\n"))
	if err != nil || fields["Description"] != "first\n second" {
		t.Fatalf("valid continued description: %+v %v", fields, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	entries := openWrtMembers(t)
	body := tarGzipEntries(t, entries)
	if _, err := ReadPackage(ctx, bytes.NewReader(body), int64(len(body)),
		"feed/464xlat_13_x86_64.ipk"); err == nil {
		t.Fatal("canceled package parse succeeded")
	}
}

func TestReadPackageRejectsNestedCorruption(t *testing.T) {
	t.Parallel()
	entries := openWrtMembers(t)
	member := func(entries []ipkEntry, name string) *[]byte {
		for i := range entries {
			if entries[i].name == name {
				return &entries[i].body
			}
		}
		t.Fatalf("missing fixture member %s", name)
		return nil
	}
	for _, tc := range []struct {
		name   string
		change func([]ipkEntry)
	}{
		{"bad control gzip CRC", func(e []ipkEntry) {
			body := member(e, "control.tar.gz")
			*body = bytes.Clone(*body)
			(*body)[len(*body)-8] ^= 1
		}},
		{"bad data gzip CRC", func(e []ipkEntry) {
			body := member(e, "data.tar.gz")
			*body = bytes.Clone(*body)
			(*body)[len(*body)-8] ^= 1
		}},
		{"control missing", func(e []ipkEntry) {
			*member(e, "control.tar.gz") = tarGzipEntries(t, []ipkEntry{{"not-control", []byte("hello")}})
		}},
		{"data tar traversal", func(e []ipkEntry) {
			*member(e, "data.tar.gz") = tarGzipEntries(t, []ipkEntry{{"../escape", []byte("hello")}})
		}},
		{"data tar absolute", func(e []ipkEntry) {
			*member(e, "data.tar.gz") = tarGzipEntries(t, []ipkEntry{{"/escape", []byte("hello")}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			members := append([]ipkEntry(nil), entries...)
			tc.change(members)
			body := tarGzipEntries(t, members)
			if _, err := ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)),
				"feed/464xlat_13_x86_64.ipk"); err == nil {
				t.Fatal("corrupt nested archive accepted")
			}
		})
	}
	if _, err := ReadPackage(t.Context(), bytes.NewReader([]byte("not gzip")),
		8, "feed/a.ipk"); err == nil {
		t.Fatal("non-IPK upload accepted")
	}
	if _, err := ReadPackage(t.Context(), bytes.NewReader(nil), maxPackageSize+1,
		"feed/a.ipk"); err == nil {
		t.Fatal("oversized IPK accepted")
	}
	if Feed("a.ipk") != "" || IndexPath("", PackagesName) != PackagesName {
		t.Fatal("root feed path mismatch")
	}
}

func TestLegacyArRejectsInvalidMemberHeadersAndPadding(t *testing.T) {
	t.Parallel()
	entries := openWrtMembers(t)
	valid := arEntries(t, entries)
	name := bytes.Clone(valid)
	copy(name[8:24], []byte("unexpected/     "))
	size := bytes.Clone(valid)
	copy(size[8+48:8+58], []byte("9999999999"))
	marker := bytes.Clone(valid)
	marker[8+58] = 'x'
	for _, body := range [][]byte{name, size, marker, valid[:len(valid)-1]} {
		if _, err := ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)),
			"feed/464xlat_13_x86_64.ipk"); err == nil {
			t.Fatal("invalid ar header or truncation accepted")
		}
	}
	position := 8
	for _, entry := range entries {
		position += 60 + len(entry.body)
		if len(entry.body)%2 == 1 {
			bad := bytes.Clone(valid)
			bad[position] = 'x'
			if _, err := ReadPackage(t.Context(), bytes.NewReader(bad), int64(len(bad)),
				"feed/464xlat_13_x86_64.ipk"); err == nil {
				t.Fatal("invalid ar padding accepted")
			}
			return
		}
	}
}

type unseekableIPK struct{ *bytes.Reader }

func (unseekableIPK) Seek(int64, int) (int64, error) { return 0, io.ErrUnexpectedEOF }

func TestIPKArchiveReaderRejectsMalformedBoundaryStreams(t *testing.T) {
	ctx := t.Context()
	path := "feed/464xlat_13_x86_64.ipk"
	if _, err := ReadPackage(ctx, bytes.NewReader([]byte("short")), 5, path); err == nil {
		t.Fatal("short IPK prefix accepted")
	}
	if _, err := ReadPackage(
		ctx,
		unseekableIPK{bytes.NewReader([]byte("!<arch>\n"))},
		8,
		path,
	); err == nil {
		t.Fatal("IPK whose archive cannot be rewound accepted")
	}
	entries := openWrtMembers(t)
	for _, tc := range []struct {
		name, member string
		body         []byte
	}{
		{"empty data archive", "data.tar.gz", nil},
		{"malformed data gzip", "data.tar.gz", []byte("bad")},
		{"malformed control gzip", "control.tar.gz", []byte("bad")},
		{"wrong Debian marker", "debian-binary", []byte("1.0\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			members := append([]ipkEntry(nil), entries...)
			for i := range members {
				if members[i].name == tc.member {
					members[i].body = tc.body
				}
			}
			body := tarGzipEntries(t, members)
			if _, err := ReadPackage(
				ctx,
				bytes.NewReader(body),
				int64(len(body)),
				path,
			); err == nil {
				t.Fatal("malformed upstream IPK accepted")
			}
		})
	}
	if _, err := readControlTar(ctx, []byte("bad")); err == nil {
		t.Fatal("malformed control gzip accepted")
	}
	if err := validateDataTar(ctx, bytes.NewReader([]byte("bad"))); err == nil {
		t.Fatal("malformed data gzip accepted")
	}
	if _, err := readAr(ctx, bytes.NewReader([]byte("wrong!!!")), 8); err == nil {
		t.Fatal("invalid legacy ar magic accepted")
	}
	brokenHeader := append([]byte("!<arch>\n"), bytes.Repeat([]byte{'x'}, 59)...)
	if _, err := readAr(ctx, bytes.NewReader(brokenHeader), int64(len(brokenHeader))); err == nil {
		t.Fatal("short legacy ar header accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	validAr := arEntries(t, entries)
	if _, err := readAr(canceled, bytes.NewReader(validAr), int64(len(validAr))); err == nil {
		t.Fatal("canceled legacy ar read accepted")
	}
}

func TestIPKControlIdentityCannotOverridePackageFilename(t *testing.T) {
	entries := openWrtMembers(t)
	for _, tc := range []struct {
		name, control string
		valid         bool
	}{
		{"matching identity", "Package: 464xlat\nVersion: 13\nArchitecture: x86_64\n", true},
		{"missing name", "Version: 13\nArchitecture: x86_64\n", false},
		{"invalid version", "Package: 464xlat\nVersion: 1 3\nArchitecture: x86_64\n", false},
		{"invalid architecture", "Package: 464xlat\nVersion: 13\nArchitecture: x86 64\n", false},
		{"duplicate name", "Package: 464xlat\nPackage: other\nVersion: 13\nArchitecture: x86_64\n", false},
		{"missing trailing newline", "Package: 464xlat\nVersion: 13\nArchitecture: x86_64", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			members := append([]ipkEntry(nil), entries...)
			for i := range members {
				if members[i].name == "control.tar.gz" {
					members[i].body = tarGzipEntries(t, []ipkEntry{{"control", []byte(tc.control)}})
				}
			}
			archive := tarGzipEntries(t, members)
			pkg, err := ReadPackage(t.Context(), bytes.NewReader(archive), int64(len(archive)),
				"feed/464xlat_13_x86_64.ipk")
			if (err == nil) != tc.valid {
				t.Fatalf("control validity=%t package=%+v err=%v", tc.valid, pkg, err)
			}
		})
	}
}

func TestIPKArchiveMembersRejectSpecialTypesAndTruncation(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name   string
		header tar.Header
		outer  bool
	}{
		{"outer symlink", tar.Header{Name: "control.tar.gz", Typeflag: tar.TypeSymlink, Linkname: "elsewhere"}, true},
		{"oversized metadata", tar.Header{Name: "control.tar.gz", Typeflag: tar.TypeReg, Size: maxControlSize + 1}, true},
		{"data character device", tar.Header{Name: "dev/zero", Typeflag: tar.TypeChar}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw bytes.Buffer
			tw := tar.NewWriter(&raw)
			if err := tw.WriteHeader(&tc.header); err != nil {
				t.Fatal(err)
			}
			if _, err := readTar(ctx, bytes.NewReader(raw.Bytes()), tc.outer); err == nil {
				t.Fatal("unsafe tar member accepted")
			}
		})
	}
	if _, err := readAr(ctx, bytes.NewReader([]byte("short")), 5); err == nil {
		t.Fatal("short legacy ar accepted")
	}
	var truncated bytes.Buffer
	tw := tar.NewWriter(&truncated)
	if err := tw.WriteHeader(
		&tar.Header{Name: "control", Typeflag: tar.TypeReg, Size: 4},
	); err != nil {
		t.Fatal(err)
	}
	truncated.WriteString("ab")
	if _, err := readTar(ctx, bytes.NewReader(truncated.Bytes()), false); err == nil {
		t.Fatal("truncated control entry accepted")
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(truncated.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateDataTar(ctx, bytes.NewReader(compressed.Bytes())); err == nil {
		t.Fatal("truncated data member accepted")
	}
	var invalidData bytes.Buffer
	zw := gzip.NewWriter(&invalidData)
	var special bytes.Buffer
	specialTar := tar.NewWriter(&special)
	if err := specialTar.WriteHeader(&tar.Header{
		Name: "dev/zero", Typeflag: tar.TypeChar,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(special.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateDataTar(ctx, bytes.NewReader(invalidData.Bytes())); err == nil {
		t.Fatal("device entry in data archive accepted")
	}
	members := openWrtMembers(t)
	for i := range members {
		if members[i].name == "control.tar.gz" {
			members[i].body = bytes.Repeat([]byte{'x'}, maxControlSize+1)
		}
	}
	tooLarge := arEntries(t, members)
	if _, err := readAr(ctx, bytes.NewReader(tooLarge), int64(len(tooLarge))); err == nil {
		t.Fatal("oversized legacy ar control accepted")
	}
	for i := range members {
		if members[i].name == "control.tar.gz" {
			members[i].body = []byte("bad")
		}
		if members[i].name == "data.tar.gz" {
			members[i].body = []byte("bad")
		}
	}
	badData := arEntries(t, members)
	if _, err := readAr(ctx, bytes.NewReader(badData), int64(len(badData))); err == nil {
		t.Fatal("malformed legacy ar data accepted")
	}
	validAr := arEntries(t, openWrtMembers(t))
	controlHeader := bytes.Index(validAr, []byte("control.tar.gz/"))
	if controlHeader < 0 {
		t.Fatal("independent OpenWrt ar control member not found")
	}
	if _, err := readAr(
		ctx,
		bytes.NewReader(validAr[:controlHeader+62]),
		int64(len(validAr)),
	); err == nil {
		t.Fatal("truncated legacy ar control body accepted")
	}
	var malformedControl bytes.Buffer
	badZip := gzip.NewWriter(&malformedControl)
	if _, err := badZip.Write(bytes.Repeat([]byte{'x'}, 512)); err != nil {
		t.Fatal(err)
	}
	if err := badZip.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readControlTar(ctx, malformedControl.Bytes()); err == nil {
		t.Fatal("malformed control tar accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := validateDataTar(
		canceled,
		bytes.NewReader(tarGzipEntries(t, []ipkEntry{{"file", []byte("data")}})),
	); err == nil {
		t.Fatal("canceled data archive accepted")
	}
}
