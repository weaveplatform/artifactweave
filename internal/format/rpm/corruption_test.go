package rpm

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixtureHeader struct {
	start, data int
	entries     map[uint32]int
}

func rpmMainHeader(t *testing.T, body []byte) fixtureHeader {
	t.Helper()
	_, end, err := readHeader(bytes.NewReader(body), leadSize, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	start := int((end + 7) &^ 7)
	count := int(binary.BigEndian.Uint32(body[start+8 : start+12]))
	h := fixtureHeader{
		start: start, data: start + headerIntroLen + count*indexEntryLen,
		entries: map[uint32]int{},
	}
	for i := range count {
		entry := start + headerIntroLen + i*indexEntryLen
		h.entries[binary.BigEndian.Uint32(body[entry:entry+4])] = entry
	}
	return h
}

func TestUnitReadPackageRejectsCorruptedRealRPMHeaders(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	header := rpmMainHeader(t, body)
	const pkgPath = "pool/" + fixtureName
	for _, tc := range []struct {
		name, expected string
		mutate         func([]byte)
	}{
		{"old lead version", "lead magic or version", func(b []byte) { b[4] = 2 }},
		{"empty signature index", "header bounds", func(b []byte) {
			binary.BigEndian.PutUint32(b[leadSize+8:], 0)
		}},
		{"huge signature index", "header bounds", func(b []byte) {
			binary.BigEndian.PutUint32(b[leadSize+8:], headerEntries+1)
		}},
		{"wrong main header magic", "header magic", func(b []byte) { b[header.start] = 0 }},
		{"empty main header index", "header bounds", func(b []byte) {
			binary.BigEndian.PutUint32(b[header.start+8:], 0)
		}},
		{"duplicate main tag", "header entry", func(b []byte) {
			first := header.start + headerIntroLen
			second := first + indexEntryLen
			copy(b[second:second+4], b[first:first+4])
		}},
		{"main tag outside data", "header entry", func(b []byte) {
			entry := header.entries[1000]
			binary.BigEndian.PutUint32(b[entry+8:], uint32(len(b)))
		}},
		{"name has wrong tag type", "invalid string tag", func(b []byte) {
			entry := header.entries[1000]
			binary.BigEndian.PutUint32(b[entry+4:], 4)
		}},
		{"version has wrong tag type", "invalid string tag", func(b []byte) {
			binary.BigEndian.PutUint32(b[header.entries[1001]+4:], 4)
		}},
		{"release has wrong tag type", "invalid string tag", func(b []byte) {
			binary.BigEndian.PutUint32(b[header.entries[1002]+4:], 4)
		}},
		{"architecture has wrong tag type", "invalid string tag", func(b []byte) {
			binary.BigEndian.PutUint32(b[header.entries[1022]+4:], 4)
		}},
		{"summary has wrong tag type", "invalid string tag", func(b []byte) {
			binary.BigEndian.PutUint32(b[header.entries[1004]+4:], 4)
		}},
		{"build time has wrong tag type", "integer tag", func(b []byte) {
			binary.BigEndian.PutUint32(b[header.entries[1006]+4:], 6)
		}},
		{"installed size has wrong tag type", "integer tag", func(b []byte) {
			binary.BigEndian.PutUint32(b[header.entries[1009]+4:], 6)
		}},
		{"provides names have wrong tag type", "string array tag", func(b []byte) {
			binary.BigEndian.PutUint32(b[header.entries[1047]+4:], 4)
		}},
		{"requires versions have wrong tag type", "string array tag", func(b []byte) {
			binary.BigEndian.PutUint32(b[header.entries[1050]+4:], 4)
		}},
		{"requires flags have wrong tag type", "integer array tag", func(b []byte) {
			binary.BigEndian.PutUint32(b[header.entries[1048]+4:], 8)
		}},
		{"requires arrays have inconsistent lengths", "dependency tag lengths", func(b []byte) {
			binary.BigEndian.PutUint32(b[header.entries[1048]+12:], 3)
		}},
		{"name differs from path", "header identity differs", func(b []byte) {
			entry := header.entries[1000]
			offset := binary.BigEndian.Uint32(b[entry+8:])
			b[header.data+int(offset)] = 'x'
		}},
		{"payload digest wrong type", "payload SHA-256 tag", func(b []byte) {
			entry := header.entries[5092]
			binary.BigEndian.PutUint32(b[entry+4:], 4)
		}},
		{"payload digest wrong encoding", "payload SHA-256 encoding", func(b []byte) {
			entry := header.entries[5092]
			offset := binary.BigEndian.Uint32(b[entry+8:])
			b[header.data+int(offset)] = 'z'
		}},
		{"payload digest wrong length", "payload SHA-256 length", func(b []byte) {
			entry := header.entries[5092]
			offset := binary.BigEndian.Uint32(b[entry+8:])
			b[header.data+int(offset)] = 0
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corrupt := bytes.Clone(body)
			tc.mutate(corrupt)
			_, err := ReadPackage(
				t.Context(),
				bytes.NewReader(corrupt),
				int64(len(corrupt)),
				pkgPath,
			)
			if err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("invalid RPM got %v; want %q", err, tc.expected)
			}
		})
	}
	if _, ok := header.entries[5092]; !ok {
		t.Fatal("fixture lacks RPMTAG_PAYLOADSHA256")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadPackage(ctx, bytes.NewReader(body), int64(len(body)), pkgPath); err == nil ||
		!strings.Contains(err.Error(), "canceled payload read") {
		t.Fatalf("canceled validation returned %v", err)
	}
	for _, path := range []string{
		"/" + pkgPath, "../" + pkgPath, "pool/./" + fixtureName,
		"pool/not an rpm.rpm",
	} {
		if _, err := ReadPackage(
			t.Context(),
			bytes.NewReader(body),
			int64(len(body)),
			path,
		); err == nil {
			t.Errorf("unsafe package path %q accepted", path)
		}
	}
}
