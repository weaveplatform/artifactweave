package archivecheck

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func deflatedBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := flate.NewWriter(&out, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

type zipByteReader struct{ io.Reader }

func (r zipByteReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:min(1, len(p))]) }

type zipFaultReader struct{}

func (zipFaultReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestUnitStrictZIPDeflateConsumesOnlyExactStream(t *testing.T) {
	body := bytes.Repeat([]byte("virtual machine content"), 1000)
	compressed := deflatedBytes(t, body)
	for _, buffered := range []bool{false, true} {
		var input io.Reader = bytes.NewReader(compressed)
		if !buffered {
			input = zipByteReader{input}
		}
		r := strictZIPDeflate(input)
		got, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("exact stream: %d %v", len(got), err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(append(bytes.Clone(compressed), 0))
		if !buffered {
			input = zipByteReader{input}
		}
		r = strictZIPDeflate(input)
		if _, err := io.ReadAll(r); !errors.Is(err, ErrInvalid) {
			t.Fatalf("trailing bytes: %v", err)
		}
		_ = r.Close()
	}
	r := strictZIPDeflate(io.MultiReader(bytes.NewReader(compressed), zipFaultReader{}))
	if _, err := io.ReadAll(r); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("trailer read: %v", err)
	}
	_ = r.Close()
	r = strictZIPDeflate(bytes.NewReader([]byte{0xff}))
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("corrupt deflate accepted")
	}
	if err := r.Close(); err == nil {
		t.Fatal("corrupt stream close error discarded")
	}
}

func TestUnitZIPCannotHideLocalRecordsInsideDeflateSpan(t *testing.T) {
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	f, err := w.Create("metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(f, `{"provider":"virtualbox"}`); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := out.Bytes()
	limits := ZIPLimits{ArchiveBytes: 1 << 20, DirectoryBytes: 1 << 16, Entries: 10}
	z, err := ZIP(t.Context(), bytes.NewReader(data), int64(len(data)), limits)
	if err != nil {
		t.Fatal(err)
	}
	r, err := z.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// Keep a correct final descriptor for the central directory's inflated size,
	// but place the original descriptor and a hidden local record immediately
	// after the actual deflate stream. Streaming native tar sees the hidden name.
	directory := layoutDirectory(data)
	descriptor := directory - 16
	orphan := layoutBox(t, false, nil)
	orphan = orphan[:layoutDirectory(orphan)]
	copy(orphan[30:43], "../evilxx.txt")
	hidden := append(bytes.Clone(data[descriptor:directory]), orphan...)
	attack := append(append(bytes.Clone(data[:descriptor]), hidden...), data[descriptor:]...)
	directory += len(hidden)
	binary.LittleEndian.PutUint32(attack[len(attack)-6:], uint32(directory))
	compressed := binary.LittleEndian.Uint32(attack[directory+20:]) + uint32(len(hidden))
	binary.LittleEndian.PutUint32(attack[directory+20:], compressed)
	binary.LittleEndian.PutUint32(attack[directory-8:], compressed)
	z, err = ZIP(t.Context(), bytes.NewReader(attack), int64(len(attack)), limits)
	if err != nil {
		t.Fatalf("layout should be structurally consistent: %v", err)
	}
	r, err = z.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("hidden compressed records accepted: %v", err)
	}
	_ = r.Close()
}
