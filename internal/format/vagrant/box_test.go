package vagrant

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

type entry struct {
	name, body string
	kind       byte
}

type cancelingReader struct {
	io.Reader
	cancel context.CancelFunc
}

func (r cancelingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.cancel()
	return n, err
}

func TestUnitArchiveMemberCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const size = 1 << 20
	r := cancelingReader{Reader: bytes.NewReader(make([]byte, size)), cancel: cancel}
	state := archiveState{}
	if err := state.read(ctx, r, "disk.img", size); !errors.Is(err, context.Canceled) {
		t.Fatalf("member continued after cancellation: %v", err)
	}
}

func TestUnitTarExpansionIncludesHiddenExtensionHeaders(t *testing.T) {
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	const metadata = `{"provider":"virtualbox"}`
	for _, name := range []string{"metadata.json", "disk.img"} {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Size: int64(len(metadata)), Mode: 0o644, Format: tar.FormatPAX,
			PAXRecords: map[string]string{"comment": string(bytes.Repeat([]byte("x"), 4096))},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, metadata); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readTar(t.Context(), bytes.NewReader(data.Bytes()), maxExpandedBytes); err != nil {
		t.Fatalf("valid PAX archive: %v", err)
	}
	if _, err := readTar(
		t.Context(),
		bytes.NewReader(data.Bytes()),
		4096,
	); !errors.Is(
		err,
		errInvalid,
	) {
		t.Fatalf("hidden extension records bypassed expansion limit: %v", err)
	}
}

func TestUnitTarExpansionBoundsPayloadAndPadding(t *testing.T) {
	metadata := entry{"metadata.json", `{"provider":"virtualbox"}`, 0}
	archive := tarBox(t, []entry{metadata}, false)
	for _, tc := range []struct {
		name  string
		data  []byte
		limit int64
	}{
		{"payload", tarBox(t, []entry{metadata, {"disk.img", string(bytes.Repeat([]byte("x"), 4096)), 0}}, false), 2048},
		{"padding", append(archive, make([]byte, 4096)...), int64(len(archive)) + 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readTar(
				t.Context(),
				bytes.NewReader(tc.data),
				tc.limit,
			); !errors.Is(
				err,
				errInvalid,
			) {
				t.Fatalf("expanded stream accepted beyond limit: %v", err)
			}
		})
	}
}

func tarBox(t *testing.T, entries []entry, compressed bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	var out io.Writer = &buf
	var gz *gzip.Writer
	if compressed {
		gz = gzip.NewWriter(&buf)
		out = gz
	}
	tw := tar.NewWriter(out)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		size := int64(len(e.body))
		if kind != tar.TypeReg {
			size = 0
		}
		if err := tw.WriteHeader(
			&tar.Header{Name: e.name, Mode: 0o644, Typeflag: kind, Size: size},
		); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func zipBox(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{
			Name: e.name, Method: zip.Store, CRC32: crc32.ChecksumIEEE([]byte(e.body)),
			CompressedSize64: uint64(len(e.body)), UncompressedSize64: uint64(len(e.body)),
		}
		h.SetMode(0o644)
		if e.kind == tar.TypeSymlink {
			h.SetMode(0o777 | 1<<27)
		}
		f, err := zw.CreateRaw(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUnitBoxArchivesAndIdentity(t *testing.T) {
	entries := []entry{
		{"./", "", tar.TypeDir},
		{"./metadata.json", `{"provider":"virtualbox","architecture":"amd64"}`, 0},
		{"disk.vmdk", "virtual disk", 0},
	}
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"tar", tarBox(t, entries, false)},
		{"gzip", tarBox(t, entries, true)},
		{"zip", zipBox(t, entries[1:])},
		{"tar PK filename", tarBox(t, []entry{{"PKTest", "data", 0}, entries[1]}, false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := ReadBox(t.Context(), bytes.NewReader(tc.body), int64(len(tc.body)))
			if err != nil || b.Provider != "virtualbox" || b.Architecture != "amd64" {
				t.Fatalf("box: %+v %v", b, err)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		valid bool
	}{{"box", true}, {"acme/box", true}, {"../box", false}, {"a/b/c", false}, {"", false}, {"box?token", false}, {"a//b", false}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidName(tc.name); got != tc.valid {
				t.Fatalf("name valid=%t", got)
			}
		})
	}
	p := types.Properties{
		PropName:         {"acme/demo"},
		PropProvider:     {"virtualbox"},
		PropVersion:      {"1.2.3-beta.1"},
		propProvider:     {"virtualbox"},
		propDigest:       {"digest"},
		propArchitecture: {"amd64"},
	}
	if _, _, ok := Verified(p, "digest"); !ok {
		t.Fatal("valid properties rejected")
	}
	for _, key := range []string{PropName, PropVersion, PropProvider, propDigest, propProvider, propArchitecture} {
		t.Run(key, func(t *testing.T) {
			old := p[key]
			p[key] = []string{"../bad"}
			defer func() { p[key] = old }()
			if _, _, ok := Verified(p, "digest"); ok {
				t.Fatal("invalid properties verified")
			}
		})
	}
	p[PropName] = []string{"a", "b"}
	if _, ok := Properties(p); ok {
		t.Fatal("multi-valued identity allowed")
	}
}

func TestUnitBoxArchiveRejectsUnsafeAndCorruptInput(t *testing.T) {
	good := entry{"metadata.json", `{"provider":"virtualbox"}`, 0}
	for _, tc := range []struct {
		name    string
		entries []entry
	}{
		{"no metadata", []entry{{"disk", "data", 0}}},
		{"invalid json", []entry{{"metadata.json", "{", 0}}},
		{"invalid provider", []entry{{"metadata.json", `{"provider":"../x"}`, 0}}},
		{"invalid architecture", []entry{{"metadata.json", `{"provider":"docker","architecture":"../x"}`, 0}}},
		{"parent traversal", []entry{good, {"../disk", "data", 0}}},
		{"absolute path", []entry{good, {"/disk", "data", 0}}},
		{"windows path", []entry{good, {"C:/disk", "data", 0}}},
		{"backslash", []entry{good, {`a\b`, "data", 0}}},
		{"link", []entry{good, {"link", "", tar.TypeSymlink}}},
		{"duplicate", []entry{good, good}},
		{"nested metadata", []entry{{"nested/metadata.json", good.body, 0}}},
		{"oversize metadata", []entry{{"metadata.json", string(bytes.Repeat([]byte(" "), maxMetadataBytes+1)), 0}}},
	} {
		for _, kind := range []string{"tar", "gzip", "zip"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				data := tarBox(t, tc.entries, kind == "gzip")
				if kind == "zip" {
					data = zipBox(t, tc.entries)
				}
				if _, err := ReadBox(
					t.Context(),
					bytes.NewReader(data),
					int64(len(data)),
				); err == nil {
					t.Fatal("unsafe archive accepted")
				}
			})
		}
	}
	valid := tarBox(t, []entry{good}, true)
	corrupt := bytes.Clone(valid)
	corrupt[len(corrupt)-1] ^= 1
	badZip := zipBox(t, []entry{good})
	badZip[bytes.Index(badZip, []byte("virtualbox"))] ^= 1
	for _, tc := range []struct {
		name string
		data []byte
		size int64
	}{
		{"empty", nil, 0},
		{"short", []byte{1}, 1},
		{"too large", valid, MaxArchiveBytes + 1},
		{"corrupt gzip", corrupt, int64(len(corrupt))},
		{"truncated gzip", valid[:len(valid)-4], int64(len(valid) - 4)},
		{"invalid tar", bytes.Repeat([]byte{1}, 1024), 1024},
		{"appended data", append(tarBox(t, []entry{good}, false), 1), int64(len(tarBox(t, []entry{good}, false)) + 1)},
		{"invalid zip", []byte("PKbroken"), 8},
		{"zip length", badZip, int64(len(badZip) + 1)},
		{"zip checksum", badZip, int64(len(badZip))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadBox(t.Context(), bytes.NewReader(tc.data), tc.size); err == nil {
				t.Fatal("corrupt archive accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadBox(
		ctx,
		bytes.NewReader(valid),
		int64(len(valid)),
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestUnitArchiveResourceFailures(t *testing.T) {
	data := zipBox(t, []entry{{"metadata.json", `{"provider":"virtualbox"}`, 0}})
	t.Run("temporary file unavailable", func(t *testing.T) {
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		if _, err := ReadBox(t.Context(), bytes.NewReader(data), int64(len(data))); err == nil {
			t.Fatal("temporary file failure ignored")
		}
	})
	t.Run("unsupported compression", func(t *testing.T) {
		b := bytes.Clone(data)
		offset := int(binary.LittleEndian.Uint32(b[len(b)-6:]))
		binary.LittleEndian.PutUint16(b[offset+10:], 99)
		if _, err := ReadBox(t.Context(), bytes.NewReader(b), int64(len(b))); err == nil {
			t.Fatal("unsupported compression accepted")
		}
	})
	t.Run("gzip header", func(t *testing.T) {
		data := []byte{0x1f, 0x8b, 0, 0, 0, 0, 0, 0, 0, 0}
		if _, err := ReadBox(t.Context(), bytes.NewReader(data), int64(len(data))); err == nil {
			t.Fatal("bad gzip header accepted")
		}
	})
	t.Run("truncated tar disk", func(t *testing.T) {
		b := tarBox(t, []entry{{"disk", string(bytes.Repeat([]byte("x"), 8192)), 0}}, false)
		b = b[:1024]
		if _, err := ReadBox(t.Context(), bytes.NewReader(b), int64(len(b))); err == nil {
			t.Fatal("truncated member accepted")
		}
	})
}

func TestUnitArchiveEntryLimitIncludesRootDirectories(t *testing.T) {
	state := archiveState{seen: map[string]bool{}}
	for range maxEntries {
		if _, err := state.member("./", 0, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.member("./", 0, true); err == nil {
		t.Fatal("root directories bypassed entry limit")
	}
}
