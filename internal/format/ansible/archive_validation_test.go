package ansible_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/weaveplatform/artifactweave/internal/format/ansible"
)

func TestUnitArchiveRejectsUnsafeOrInconsistentContent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]entry) []entry
	}{
		{"manifest missing", func(e []entry) []entry { return e[1:] }},
		{"files missing", func(e []entry) []entry { return append(e[:1], e[2:]...) }},
		{"invalid manifest", func(e []entry) []entry { e[0].body = []byte("{"); return e }},
		{"manifest wrong digest", func(e []entry) []entry { e[1].body = []byte(`{"files":[]}`); return e }},
		{"checksum", func(e []entry) []entry { e[2].body = []byte("tampered"); return e }},
		{"unlisted file", func(e []entry) []entry { return append(e, entry{name: "extra", body: []byte("secret")}) }},
		{"duplicate", func(e []entry) []entry { return append(e, e[2]) }},
		{"parent", func(e []entry) []entry { return append(e, entry{name: "../escape"}) }},
		{"absolute", func(e []entry) []entry { return append(e, entry{name: "/escape"}) }},
		{"backslash", func(e []entry) []entry { return append(e, entry{name: `a\b`}) }},
		{"noncanonical", func(e []entry) []entry { return append(e, entry{name: "a/../b"}) }},
		{"symlink", func(e []entry) []entry { return append(e, entry{name: "link", kind: tar.TypeSymlink}) }},
		{"hardlink", func(e []entry) []entry { return append(e, entry{name: "link", kind: tar.TypeLink}) }},
		{"oversized metadata", func(e []entry) []entry { e[0].body = bytes.Repeat([]byte("x"), (1<<20)+1); return e }},
		{"too many entries", func(e []entry) []entry {
			for i := 0; i < 4097; i++ {
				e = append(e, entry{name: strings.Repeat("a", i/100+1) + "/" + strings.Repeat("b", i%100+1), kind: tar.TypeDir})
			}
			return e
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := archive(t, tc.mutate(collectionEntries(t, "weave", "sample", "1.0.0", 0)))
			if _, err := ansible.ReadArchive(bytes.NewReader(data), int64(len(data))); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestUnitArchiveRejectsMalformedFileManifest(t *testing.T) {
	for _, tc := range []struct{ name, files string }{
		{"json", "{"},
		{"bad hash", `{"files":[{"name":"README.md","ftype":"file","chksum_sha256":"no"}]}`},
		{"unknown type", `{"files":[{"name":"README.md","ftype":"special"}]}`},
		{"unsafe directory", `{"files":[{"name":"../bad","ftype":"dir"}]}`},
		{"missing directory", `{"files":[{"name":"absent","ftype":"dir"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := collectionEntries(t, "weave", "sample", "1.0.0", 0)
			e[1].body = []byte(tc.files)
			var m map[string]any
			if err := json.Unmarshal(e[0].body, &m); err != nil {
				t.Fatal(err)
			}
			m["file_manifest_file"] = map[string]string{
				"name":          "FILES.json",
				"chksum_sha256": sum(e[1].body),
			}
			e[0].body, _ = json.Marshal(m)
			b := archive(t, e)
			if _, err := ansible.ReadArchive(bytes.NewReader(b), int64(len(b))); err == nil {
				t.Fatal("invalid file manifest accepted")
			}
		})
	}
}

func TestUnitArchiveBoundsAndReaderFailure(t *testing.T) {
	data := archive(t, collectionEntries(t, "weave", "sample", "1.0.0", 0))
	for _, tc := range []struct {
		name string
		body io.Reader
		size int64
	}{
		{"zero", bytes.NewReader(data), 0},
		{"too large", bytes.NewReader(data), ansible.MaxArchiveBytes + 1},
		{"wrong length", bytes.NewReader(data), int64(len(data) + 1)},
		{"broken", iotest.ErrReader(io.ErrUnexpectedEOF), 1},
		{"gzip trailer", bytes.NewReader(data[:len(data)-4]), int64(len(data) - 4)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ansible.ReadArchive(tc.body, tc.size); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}

func TestUnitDependencyAndIdentityGrammar(t *testing.T) {
	for _, tc := range []struct {
		name, constraint string
		valid            bool
	}{
		{"weave.good", "*", true},
		{"weave.good", ">=1.0.0, <2.0.0,!=1.1.0", true},
		{"weave.good", "", false},
		{"weave.good", "garbage", false},
		{"weave.good", ">=1.0.0,", false},
		{"../bad", "*", false},
		{"weave.UPPER", "*", false},
		{"weave.good", strings.Repeat("x", 1025), false},
	} {
		t.Run(tc.name+tc.constraint[:min(20, len(tc.constraint))], func(t *testing.T) {
			if ansible.ValidDependencies(map[string]string{tc.name: tc.constraint}) != tc.valid {
				t.Fatal("dependency grammar mismatch")
			}
		})
	}
	many := map[string]string{}
	for i := 0; i < 257; i++ {
		many[string(rune(1000+i))] = "*"
	}
	if ansible.ValidDependencies(many) {
		t.Fatal("unbounded dependencies")
	}
	for _, v := range []string{"../1.0.0", "v1.0.0", "1.0.0/", ""} {
		if ansible.ValidVersion(v) {
			t.Fatalf("accepted %q", v)
		}
	}
}
