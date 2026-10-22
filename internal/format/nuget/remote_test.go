package nuget_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/nuget"
)

func TestRemoteArchiveAdmission(t *testing.T) {
	good := pkg("Acme.Package", "1.0.0", "")
	for _, tc := range []struct {
		name    string
		body    []byte
		id, key string
		valid   bool
	}{
		{"valid", good, "acme.package", "1.0.0", true},
		{"wrong id", good, "other.package", "1.0.0", false},
		{"wrong version", good, "acme.package", "2.0.0", false},
		{"symbols", symbols("Acme.Package", "1.0.0"), "acme.package", "1.0.0", false},
		{"not zip", []byte("<html>login</html>"), "acme.package", "1.0.0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := nuget.ValidateRemote(
				t.Context(),
				bytes.NewReader(tc.body),
				int64(len(tc.body)),
				tc.id,
				tc.key,
			)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := nuget.ValidateRemote(
		ctx,
		bytes.NewReader(good),
		int64(len(good)),
		"acme.package",
		"1.0.0",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestRemoteArchivePayloadLimitsAndCompression(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method uint16
		size   uint64
	}{
		{"unknown compression", 255, 4},
		{"inflated size", zip.Store, 17 << 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			zw := zip.NewWriter(&b)
			w, _ := zw.Create("Acme.Package.nuspec")
			_, _ = io.WriteString(w, nuspecXML("Acme.Package", "1.0.0", ""))
			w, _ = zw.CreateRaw(
				&zip.FileHeader{
					Name:               "payload",
					Method:             tc.method,
					UncompressedSize64: tc.size,
					CompressedSize64:   4,
				},
			)
			_, _ = io.WriteString(w, "data")
			_ = zw.Close()
			if err := nuget.ValidateRemote(
				t.Context(),
				bytes.NewReader(b.Bytes()),
				int64(b.Len()),
				"acme.package",
				"1.0.0",
			); err == nil {
				t.Fatal("unsafe archive admitted")
			}
		})
	}
}

func TestRemoteArchiveEntryNamesAndTypes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  fs.FileMode
		valid bool
	}{
		{"../../outside.txt", 0o644, false},
		{"lib/%2e%2e/%2e%2e/outside.txt", 0o644, false},
		{"%2foutside.txt", 0o644, false},
		{`..\..\outside.txt`, 0o644, false},
		{"%5c%5cserver/share.txt", 0o644, false},
		{"C:/outside.txt", 0o644, false},
		{"file%00.txt", 0o644, false},
		{"invalid%xy", 0o644, false},
		{".", 0o644, false},
		{"lib/link", fs.ModeSymlink | 0o777, false},
		{"lib/pipe", fs.ModeNamedPipe | 0o600, false},
		{"docs/a%20b.txt", 0o644, true},
		{"lib/", fs.ModeDir | 0o755, true},
		{".signature.p7s", 0o644, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			zw := zip.NewWriter(&b)
			w, _ := zw.Create("Acme.Package.nuspec")
			_, _ = io.WriteString(w, nuspecXML("Acme.Package", "1.0.0", ""))
			h := &zip.FileHeader{Name: tc.name, Method: zip.Store}
			h.SetMode(tc.mode)
			w, _ = zw.CreateHeader(h)
			if !tc.mode.IsDir() {
				_, _ = io.WriteString(w, "payload")
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			err := nuget.ValidateRemote(
				t.Context(),
				bytes.NewReader(b.Bytes()),
				int64(b.Len()),
				"acme.package",
				"1.0.0",
			)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func zip64Package(body []byte) []byte {
	end := body[len(body)-22:]
	record := make([]byte, 56)
	binary.LittleEndian.PutUint32(record, 0x06064b50)
	binary.LittleEndian.PutUint64(record[4:], 44)
	binary.LittleEndian.PutUint64(record[24:], uint64(binary.LittleEndian.Uint16(end[8:])))
	binary.LittleEndian.PutUint64(record[32:], uint64(binary.LittleEndian.Uint16(end[10:])))
	binary.LittleEndian.PutUint64(record[40:], uint64(binary.LittleEndian.Uint32(end[12:])))
	binary.LittleEndian.PutUint64(record[48:], uint64(binary.LittleEndian.Uint32(end[16:])))
	locator := make([]byte, 20)
	binary.LittleEndian.PutUint32(locator, 0x07064b50)
	binary.LittleEndian.PutUint64(locator[8:], uint64(len(body)-22))
	binary.LittleEndian.PutUint32(locator[16:], 1)
	copyEnd := bytes.Clone(end)
	for i := 8; i < 20; i++ {
		copyEnd[i] = 255
	}
	out := append(bytes.Clone(body[:len(body)-22]), record...)
	out = append(out, locator...)
	return append(out, copyEnd...)
}

func TestRemoteArchiveDirectoryBounds(t *testing.T) {
	good := pkg("Acme.Package", "1.0.0", "")
	wide := zip64Package(good)
	for _, body := range [][]byte{good, wide} {
		if err := nuget.ValidateRemote(
			t.Context(),
			bytes.NewReader(body),
			int64(len(body)),
			"acme.package",
			"1.0.0",
		); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string][]byte{"short": []byte("PK"), "no-end": bytes.Repeat([]byte("x"), 30)}
	changes := map[string]func([]byte){
		"disk":                func(e []byte) { e[4] = 1 },
		"different-counts":    func(e []byte) { e[8] = 1 },
		"zero-count":          func(e []byte) { clear(e[8:12]) },
		"size-limit":          func(e []byte) { binary.LittleEndian.PutUint32(e[12:], 33<<20) },
		"offset-overflow":     func(e []byte) { binary.LittleEndian.PutUint32(e[16:], 0xfffffffe) },
		"inconsistent-offset": func(e []byte) { e[16]++ },
		"record-count":        func(e []byte) { e[8]++; e[10]++ },
	}
	for name, edit := range changes {
		b := bytes.Clone(good)
		edit(b[len(b)-22:])
		cases[name] = b
	}
	for name, edit := range map[string]func([]byte){
		"zip64-count-limit": func(b []byte) {
			e := b[len(b)-98:]
			binary.LittleEndian.PutUint64(e[24:], 100001)
			binary.LittleEndian.PutUint64(e[32:], 100001)
		},
		"zip64-bad-locator":   func(b []byte) { b[len(b)-42] = 0 },
		"zip64-multi-disk":    func(b []byte) { b[len(b)-26] = 2 },
		"zip64-offset":        func(b []byte) { binary.LittleEndian.PutUint64(b[len(b)-34:], ^uint64(0)) },
		"zip64-short":         func(b []byte) { binary.LittleEndian.PutUint64(b[len(b)-34:], uint64(len(b)-60)) },
		"zip64-bad-record":    func(b []byte) { b[len(b)-98] = 0 },
		"zip64-record-length": func(b []byte) { b[len(b)-94]++ },
		"zip64-mixed-count":   func(b []byte) { b[len(b)-74]++ },
	} {
		b := bytes.Clone(wide)
		edit(b)
		cases[name] = b
	}
	central := int(binary.LittleEndian.Uint32(good[len(good)-6:]))
	for name, edit := range map[string]func([]byte){
		"central-signature":    func(b []byte) { b[central] = 0 },
		"central-name-overrun": func(b []byte) { binary.LittleEndian.PutUint16(b[central+28:], 65535) },
	} {
		b := bytes.Clone(good)
		edit(b)
		cases[name] = b
	}
	// A forged small EOCD count cannot hide an excessive actual directory.
	header := make([]byte, 46)
	binary.LittleEndian.PutUint32(header, 0x02014b50)
	flood := bytes.Repeat(header, 100001)
	end := make([]byte, 22)
	binary.LittleEndian.PutUint32(end, 0x06054b50)
	binary.LittleEndian.PutUint16(end[8:], 1)
	binary.LittleEndian.PutUint16(end[10:], 1)
	binary.LittleEndian.PutUint32(end[12:], uint32(len(flood)))
	cases["actual-entry-limit"] = append(flood, end...)
	cases["trailing-junk"] = append(bytes.Clone(good), []byte(strings.Repeat("x", 30))...)
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := nuget.ValidateRemote(
				t.Context(),
				bytes.NewReader(body),
				int64(len(body)),
				"acme.package",
				"1.0.0",
			); err == nil {
				t.Fatal("unsafe directory admitted")
			}
		})
	}
}
