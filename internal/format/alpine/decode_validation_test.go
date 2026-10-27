package alpine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

const testIndexPath = "v3.20/main/x86_64/APKINDEX.tar.gz"

func indexArchive(t *testing.T, entries ...struct {
	name string
	body string
},
) []byte {
	t.Helper()
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	tw := tar.NewWriter(z)
	for _, entry := range entries {
		if err := tw.WriteHeader(
			&tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.body))},
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
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestUnitDecodeIndexRejectsInvalidContainers(t *testing.T) {
	valid := indexArchive(t,
		struct{ name, body string }{"DESCRIPTION", "test"},
		struct{ name, body string }{"APKINDEX", ""},
	)
	for _, tc := range []struct {
		name, path string
		body       []byte
	}{
		{"invalid path", "../main/x86_64/APKINDEX.tar.gz", valid},
		{"empty", testIndexPath, nil},
		{"not gzip", testIndexPath, []byte("not an index")},
		{"truncated gzip", testIndexPath, valid[:len(valid)-2]},
		{"missing index", testIndexPath, indexArchive(t, struct{ name, body string }{"DESCRIPTION", "test"})},
		{"unknown tar member", testIndexPath, indexArchive(t, struct{ name, body string }{"extra", "bad"})},
		{"duplicate tar member", testIndexPath, indexArchive(t,
			struct{ name, body string }{"DESCRIPTION", "test"},
			struct{ name, body string }{"DESCRIPTION", "again"})},
		{"invalid signature", testIndexPath, append(indexArchive(t,
			struct{ name, body string }{".SIGN.BAD", "sig"}), valid...)},
		{"signature without index", testIndexPath, indexArchive(t,
			struct{ name, body string }{".SIGN.RSA.test.rsa.pub", "sig"})},
		{"trailing stream", testIndexPath, append(append([]byte(nil), valid...), valid...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeIndex(t.Context(), tc.body, tc.path); err == nil {
				t.Fatal("invalid Alpine index accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecodeIndex(ctx, valid, testIndexPath); err == nil {
		t.Fatal("canceled index parse succeeded")
	}
}

func TestUnitDecodeIndexRejectsInvalidRecords(t *testing.T) {
	checksum := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 20))
	valid := "C:Q1" + checksum + "\nP:hello\nV:1.0-r0\nA:x86_64\nS:10\nI:5\n\n"
	for _, tc := range []struct{ name, record string }{
		{"invalid utf8", string([]byte{0xff})},
		{"nul", "P:hello\x00\n\n"},
		{"missing record terminator", strings.TrimSuffix(valid, "\n")},
		{"empty record", valid + "\n\n"},
		{"malformed field", strings.Replace(valid, "P:hello", "Phello", 1)},
		{"duplicate field", strings.Replace(valid, "P:hello", "P:hello\nP:other", 1)},
		{"invalid checksum", strings.Replace(valid, "C:Q1"+checksum, "C:Q1bad", 1)},
		{"wrong architecture", strings.Replace(valid, "A:x86_64", "A:aarch64", 1)},
		{"zero archive size", strings.Replace(valid, "S:10", "S:0", 1)},
		{"negative installed size", strings.Replace(valid, "I:5", "I:-1", 1)},
		{"duplicate package", valid + valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := indexArchive(t,
				struct{ name, body string }{"DESCRIPTION", "test"},
				struct{ name, body string }{"APKINDEX", tc.record},
			)
			if _, err := DecodeIndex(t.Context(), body, testIndexPath); err == nil {
				t.Fatal("invalid Alpine index record accepted")
			}
		})
	}
}

func TestUnitParseIndexPathRejectsUnsafeLayouts(t *testing.T) {
	for _, path := range []string{
		"v3.20/main/x86_64", "v3.20/main/../APKINDEX.tar.gz",
		"v3.20/main/x86_64/OTHER", "/main/x86_64/APKINDEX.tar.gz",
	} {
		if _, _, _, ok := ParseIndexPath(path); ok {
			t.Fatalf("invalid index path accepted: %q", path)
		}
	}
}
