package nuget_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format/nuget"
)

const symbolGUID = "030201000504070608090A0B0C0D0E0F"

func portablePDB() []byte {
	b := make([]byte, 120)
	copy(b, "BSJB")
	binary.LittleEndian.PutUint16(b[4:], 1)
	binary.LittleEndian.PutUint16(b[6:], 1)
	binary.LittleEndian.PutUint32(b[12:], 12)
	copy(b[16:], "PDB v1.0\x00")
	binary.LittleEndian.PutUint16(b[30:], 2)
	binary.LittleEndian.PutUint32(b[32:], 64)
	binary.LittleEndian.PutUint32(b[36:], 32)
	copy(b[40:], "#Pdb\x00")
	binary.LittleEndian.PutUint32(b[48:], 96)
	binary.LittleEndian.PutUint32(b[52:], 24)
	copy(b[56:], "#~\x00")
	for i := range 20 {
		b[64+i] = byte(i)
	}
	return b
}

func windowsPDB() []byte {
	b := make([]byte, 6*512)
	copy(b, "Microsoft C/C++ MSF 7.00\r\n\x1aDS\x00\x00\x00")
	for off, v := range map[int]uint32{32: 512, 36: 1, 40: 6, 44: 16, 52: 2, 1024: 3, 1536: 2, 1540: 0, 1544: 28, 1548: 5, 2560: 20000404, 2568: 42} {
		binary.LittleEndian.PutUint32(b[off:], v)
	}
	for i := range 16 {
		b[2572+i] = byte(i)
	}
	return b
}

func TestUnitPDBKeys(t *testing.T) {
	for _, c := range []struct {
		name     string
		body     []byte
		expected string
	}{
		{"portable", portablePDB(), symbolGUID + "FFFFFFFF"},
		{"windows", windowsPDB(), symbolGUID + "2A"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := nuget.PDBKey(c.body)
			if !ok || got != c.expected {
				t.Fatalf("key=%q,%v want %s", got, ok, c.expected)
			}
			for i := 0; i < 56; i++ {
				if _, ok := nuget.PDBKey(c.body[:i]); ok {
					t.Fatalf("truncated header %d accepted", i)
				}
			}
		})
	}
	for _, c := range []struct {
		name  string
		build func() []byte
		off   int
		value uint32
	}{
		{"portable-version-overflow", portablePDB, 12, 0xffffffff},
		{"portable-header-truncated", portablePDB, 12, 101},
		{"portable-offset-overflow", portablePDB, 32, 0xffffffff},
		{"portable-size-overflow", portablePDB, 36, 0xffffffff},
		{"portable-short-id", portablePDB, 36, 19},
		{"msf-small-block", windowsPDB, 32, 1},
		{"msf-non-power", windowsPDB, 32, 513},
		{"msf-large-block", windowsPDB, 32, 0xffffffff},
		{"msf-block-count", windowsPDB, 40, 0xffffffff},
		{"msf-short-directory", windowsPDB, 44, 11},
		{"msf-long-directory", windowsPDB, 44, 0xffffffff},
		{"msf-bad-map", windowsPDB, 52, 99},
		{"msf-bad-directory-block", windowsPDB, 1024, 99},
		{"msf-short-stream-count", windowsPDB, 1536, 1},
		{"msf-large-stream-count", windowsPDB, 1536, 0xffffffff},
		{"msf-short-stream", windowsPDB, 1544, 27},
		{"msf-absent-stream", windowsPDB, 1544, 0xffffffff},
		{"msf-bad-skipped-stream", windowsPDB, 1540, 0xfffffffe},
		{"msf-bad-info-block", windowsPDB, 1548, 99},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := c.build()
			binary.LittleEndian.PutUint32(b[c.off:], c.value)
			if k, ok := nuget.PDBKey(b); ok {
				t.Fatalf("malformed key %s", k)
			}
		})
	}
	b := portablePDB()
	copy(b[40:44], "#Bad")
	binary.LittleEndian.PutUint16(b[30:], 1)
	if _, ok := nuget.PDBKey(b); ok {
		t.Fatal("missing #Pdb accepted")
	}
	b = portablePDB()
	for i := 40; i < 72; i++ {
		b[i] = 'x'
	}
	if _, ok := nuget.PDBKey(b); ok {
		t.Fatal("unterminated name accepted")
	}
	b = windowsPDB()
	binary.LittleEndian.PutUint32(b[1540:], 0xffffffff)
	if k, ok := nuget.PDBKey(b); !ok || k != symbolGUID+"2A" {
		t.Fatal("absent stream zero", k, ok)
	}
	if _, ok := nuget.PDBKey([]byte("legacy or unknown")); ok {
		t.Fatal("unknown PDB accepted")
	}
}

func TestUnitPDBFragmentedDirectoryAndSkippedStream(t *testing.T) {
	b := windowsPDB()
	// The directory crosses non-contiguous file blocks: block 4 then 3.
	dir := make([]byte, 528)
	binary.LittleEndian.PutUint32(dir, 130)
	binary.LittleEndian.PutUint32(dir[8:], 28)
	binary.LittleEndian.PutUint32(dir[524:], 5)
	binary.LittleEndian.PutUint32(b[44:], uint32(len(dir)))
	binary.LittleEndian.PutUint32(b[1024:], 4)
	binary.LittleEndian.PutUint32(b[1028:], 3)
	copy(b[2048:], dir[:512])
	copy(b[1536:], dir[512:])
	if key, ok := nuget.PDBKey(b); !ok || key != symbolGUID+"2A" {
		t.Fatal(key, ok)
	}
	b = windowsPDB()
	binary.LittleEndian.PutUint32(b[44:], 20)
	binary.LittleEndian.PutUint32(b[1540:], 512)
	binary.LittleEndian.PutUint32(b[1548:], 4)
	binary.LittleEndian.PutUint32(b[1552:], 5)
	if key, ok := nuget.PDBKey(b); !ok || key != symbolGUID+"2A" {
		t.Fatal(key, ok)
	}
	// Portable metadata can list another heap before its PDB stream.
	b = portablePDB()
	first, second := append([]byte(nil), b[32:48]...), append([]byte(nil), b[48:60]...)
	copy(b[32:44], second)
	copy(b[44:60], first)
	if key, ok := nuget.PDBKey(b); !ok || key != symbolGUID+"FFFFFFFF" {
		t.Fatal(key, ok)
	}
}

func TestUnitSymbolPaths(t *testing.T) {
	want := ".symbols/app.pdb/" + symbolGUID + "FFFFFFFF/app.pdb"
	if got, ok := nuget.SymbolPath(
		"App.PDB",
		strings.ToLower(symbolGUID)+"ffffffff",
		"APP.pdb",
	); !ok ||
		got != want {
		t.Fatal(got, ok)
	}
	for _, c := range []struct{ a, k, b string }{
		{"a.pdb", symbolGUID + "1", "b.pdb"},
		{"a.dll", symbolGUID + "1", "a.dll"},
		{".pdb", symbolGUID + "1", ".pdb"},
		{"a.pdb", "1", "a.pdb"},
		{"a.pdb", symbolGUID + "123456789", "a.pdb"},
		{"a.pdb", symbolGUID + "G", "a.pdb"},
		{"../a.pdb", symbolGUID + "1", "../a.pdb"},
		{"a/b.pdb", symbolGUID + "1", "a/b.pdb"},
		{`a\b.pdb`, symbolGUID + "1", `a\b.pdb`},
		{"/a.pdb", symbolGUID + "1", "/a.pdb"},
	} {
		if p, ok := nuget.SymbolPath(c.a, c.k, c.b); ok {
			t.Fatal("invalid path", p)
		}
	}
}

func TestUnitSymbolArchiveBoundsAndUnsupported(t *testing.T) {
	pdb := portablePDB()
	body := zipOf(
		zipFile{"../../escape.pdb", string(pdb)},
		zipFile{"lib/a.pdb", string(pdb)},
		zipFile{"lib/b.pdb", "bad"},
		zipFile{"src/a.cs", "text"},
	)
	lim := archive.Limits{Entries: 10, File: 128, Inflated: 256}
	got, err := nuget.ReadSymbolArchive(t.Context(), bytes.NewReader(body), int64(len(body)), lim)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v err %v", got, err)
	}
	for _, l := range []archive.Limits{{Entries: 1, File: 128, Inflated: 256}, {Entries: 10, File: 119, Inflated: 256}, {Entries: 10, File: 128, Inflated: 119}} {
		if _, err := nuget.ReadSymbolArchive(
			t.Context(),
			bytes.NewReader(body),
			int64(len(body)),
			l,
		); !errors.Is(
			err,
			archive.ErrUnusable,
		) {
			t.Fatal("limit", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := nuget.ReadSymbolArchive(
		ctx,
		bytes.NewReader(body),
		int64(len(body)),
		lim,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	if _, err := nuget.ReadSymbolArchive(
		t.Context(),
		bytes.NewReader([]byte("bad")),
		3,
		lim,
	); !errors.Is(
		err,
		archive.ErrUnusable,
	) {
		t.Fatal(err)
	}
}

func FuzzPDBKey(f *testing.F) {
	f.Add(portablePDB())
	f.Add(windowsPDB())
	f.Add([]byte("BSJB"))
	f.Fuzz(func(t *testing.T, b []byte) {
		k, ok := nuget.PDBKey(b)
		if ok {
			if _, valid := nuget.SymbolPath("a.pdb", k, "a.pdb"); !valid {
				t.Fatalf("invalid generated key %q", k)
			}
		}
	})
}
