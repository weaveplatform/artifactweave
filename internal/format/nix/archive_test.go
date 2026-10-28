package nix

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

const fixturePath = "fyli4q2yj6vh01bs0jxqbvj5hir5zvqc.narinfo"

func fixture(t *testing.T) (Info, []byte, []byte) {
	t.Helper()
	data, err := os.ReadFile("testdata/cache/" + fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := Parse(data, fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile("testdata/cache/" + info.URL)
	if err != nil {
		t.Fatal(err)
	}
	r, err := xz.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	nar, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return info, archive, nar
}

func token(b *bytes.Buffer, s string) {
	_ = binary.Write(b, binary.LittleEndian, uint64(len(s)))
	b.WriteString(s)
	b.Write(make([]byte, (8-len(s)%8)%8))
}

func regular(body string) []byte {
	var b bytes.Buffer
	for _, s := range []string{"nix-archive-1", "(", "type", "regular", "contents", body, ")"} {
		token(&b, s)
	}
	return b.Bytes()
}

func TestUnitRealNixArchiveAndCompression(t *testing.T) {
	info, compressed, nar := fixture(t)
	a, err := ReadArchive(bytes.NewReader(compressed), int64(len(compressed)), "xz")
	if err != nil || !a.Matches(info) {
		t.Fatalf("native NAR invalid: %#v %v", a, err)
	}
	for _, compression := range []string{"none", "gzip", "zstd"} {
		t.Run(compression, func(t *testing.T) {
			data := nar
			if compression == "gzip" {
				var b bytes.Buffer
				w := gzip.NewWriter(&b)
				_, _ = w.Write(nar)
				_ = w.Close()
				data = b.Bytes()
			}
			if compression == "zstd" {
				w, _ := zstd.NewWriter(nil)
				data = w.EncodeAll(nar, nil)
				w.Close()
			}
			got, err := ReadArchive(bytes.NewReader(data), int64(len(data)), compression)
			if err != nil || got.NarHash != a.NarHash || got.NarSize != a.NarSize {
				t.Fatalf("NAR integrity: %#v %v", got, err)
			}
		})
	}
	for _, compression := range []string{"none", "xz", "bzip2", "gzip", "zstd", "unsupported"} {
		if _, err := ReadArchive(strings.NewReader("bad"), 3, compression); err == nil {
			t.Errorf("bad %s accepted", compression)
		}
	}
	for _, size := range []int64{0, -1, MaxArchiveBytes + 1} {
		if _, err := ReadArchive(bytes.NewReader(nar), size, "none"); err == nil {
			t.Errorf("size %d", size)
		}
	}
	for _, data := range [][]byte{nar[:len(nar)-1], append(bytes.Clone(nar), 1), append(bytes.Clone(nar), make([]byte, 8)...)} {
		if _, err := ReadArchive(bytes.NewReader(data), int64(len(data)), "none"); err == nil {
			t.Fatal("truncated or trailing NAR accepted")
		}
	}
}

func TestUnitNARGrammarRejectsMalformedAndUnsafeNodes(t *testing.T) {
	cases := []struct {
		name   string
		tokens []string
	}{
		{"header", []string{"bad"}},
		{"missing type", []string{"nix-archive-1", "(", "contents"}},
		{"unknown node", []string{"nix-archive-1", "(", "type", "other"}},
		{
			"symlink target",
			[]string{"nix-archive-1", "(", "type", "symlink", "target", "\x00", ")"},
		},
		{"symlink empty", []string{"nix-archive-1", "(", "type", "symlink", "target", "", ")"}},
		{
			"executable value",
			[]string{
				"nix-archive-1",
				"(",
				"type",
				"regular",
				"executable",
				"yes",
				"contents",
				"hello",
				")",
			},
		},
		{
			"directory traversal",
			[]string{"nix-archive-1", "(", "type", "directory", "entry", "(", "name", ".."},
		},
		{
			"directory slash",
			[]string{"nix-archive-1", "(", "type", "directory", "entry", "(", "name", "a/b"},
		},
		{"directory wrong field", []string{"nix-archive-1", "(", "type", "directory", "file"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			for _, s := range tc.tokens {
				token(&b, s)
			}
			if _, err := ReadArchive(
				bytes.NewReader(b.Bytes()),
				int64(b.Len()),
				"none",
			); err == nil {
				t.Fatal("invalid grammar accepted")
			}
		})
	}
	for _, tokens := range [][]string{{"nix-archive-1", "(", "type", "regular", "executable", "", "contents", "hello", ")"}, {"nix-archive-1", "(", "type", "symlink", "target", "../valid-link", ")"}} {
		var b bytes.Buffer
		for _, s := range tokens {
			token(&b, s)
		}
		if _, err := ReadArchive(bytes.NewReader(b.Bytes()), int64(b.Len()), "none"); err != nil {
			t.Fatal(err)
		}
	}
	good := regular("fixture")
	bad := bytes.Clone(good)
	bad[21] = 1
	if _, err := ReadArchive(bytes.NewReader(bad), int64(len(bad)), "none"); err == nil {
		t.Fatal("nonzero padding accepted")
	}
	bad = bytes.Clone(good)
	binary.LittleEndian.PutUint64(bad[:8], 5000)
	if _, err := ReadArchive(bytes.NewReader(bad), int64(len(bad)), "none"); err == nil {
		t.Fatal("oversized token accepted")
	}
}

func TestUnitNARStructuralResourceLimits(t *testing.T) {
	var b bytes.Buffer
	token(&b, "nix-archive-1")
	token(&b, "(")
	token(&b, "type")
	token(&b, "regular")
	token(&b, "contents")
	_ = binary.Write(&b, binary.LittleEndian, uint64(MaxNarBytes+1))
	if _, err := ReadArchive(bytes.NewReader(b.Bytes()), int64(b.Len()), "none"); err == nil {
		t.Fatal("oversized content")
	}
	for _, tokens := range [][]string{{"nix-archive-1", "(", "type"}, {"nix-archive-1", "(", "type", "regular"}, {"nix-archive-1", "(", "type", "symlink", "wrong"}, {"nix-archive-1", "(", "type", "directory"}} {
		b.Reset()
		for _, s := range tokens {
			token(&b, s)
		}
		if _, err := ReadArchive(bytes.NewReader(b.Bytes()), int64(b.Len()), "none"); err == nil {
			t.Fatal("truncated grammar")
		}
	}
	b.Reset()
	token(&b, "nix-archive-1")
	for range 130 {
		for _, s := range []string{"(", "type", "directory", "entry", "(", "name", "x", "node"} {
			token(&b, s)
		}
	}
	if _, err := ReadArchive(bytes.NewReader(b.Bytes()), int64(b.Len()), "none"); err == nil {
		t.Fatal("unbounded recursion")
	}
}
