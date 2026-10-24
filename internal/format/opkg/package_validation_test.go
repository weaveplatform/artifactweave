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
