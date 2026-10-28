package archivecheck

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"testing"
)

func layoutBox(t *testing.T, descriptor bool, extra []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	body := []byte(`{"provider":"virtualbox"}`)
	h := &zip.FileHeader{
		Name: "metadata.json", Method: zip.Store, Extra: extra,
		CRC32: crc32.ChecksumIEEE(body), CompressedSize64: uint64(len(body)),
		UncompressedSize64: uint64(len(body)),
	}
	var entry io.Writer
	var err error
	if descriptor {
		entry, err = w.CreateHeader(h)
	} else {
		entry, err = w.CreateRaw(h)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if descriptor {
		binary.LittleEndian.PutUint32(out[18:], uint32(len(body)))
		binary.LittleEndian.PutUint32(out[22:], uint32(len(body)))
	}
	return out
}

func layoutDirectory(data []byte) int {
	return int(binary.LittleEndian.Uint32(data[len(data)-6:]))
}

func checkLayout(t *testing.T, data []byte) error {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("fixture central directory failed: %v", err)
	}
	return validateZIPLayout(t.Context(), bytes.NewReader(data), r, int64(layoutDirectory(data)))
}

func TestUnitZIPLocalLayoutMatchesNativeExtraction(t *testing.T) {
	for _, descriptor := range []bool{false, true} {
		data := layoutBox(t, descriptor, nil)
		if err := checkLayout(t, data); err != nil {
			t.Fatalf("descriptor=%t: %v", descriptor, err)
		}
		for _, tc := range []struct {
			name string
			edit func([]byte)
		}{
			{"local traversal name", func(b []byte) { copy(b[30:43], "../evilxx.txt") }},
			{"local flags", func(b []byte) { b[6] ^= 1 }},
			{"encrypted plaintext", func(b []byte) {
				b[6] |= 1
				b[layoutDirectory(b)+8] |= 1
			}},
			{"reserved flags", func(b []byte) {
				b[7] |= 0x40
				b[layoutDirectory(b)+9] |= 0x40
			}},
			{"store deflate options", func(b []byte) {
				b[6] |= 2
				b[layoutDirectory(b)+8] |= 2
			}},
			{"local method", func(b []byte) { b[8] = 8 }},
			{"local CRC", func(b []byte) { binary.LittleEndian.PutUint32(b[14:], 1234) }},
			{"local compressed size", func(b []byte) { binary.LittleEndian.PutUint32(b[18:], 1234) }},
			{"local expanded size", func(b []byte) { binary.LittleEndian.PutUint32(b[22:], 1234) }},
			{"stored stream without sizes", func(b []byte) {
				b[6] |= 8
				b[layoutDirectory(b)+8] |= 8
				binary.LittleEndian.PutUint32(b[18:], 0)
				binary.LittleEndian.PutUint32(b[22:], 0)
			}},
			{"missing ZIP64 size", func(b []byte) { binary.LittleEndian.PutUint32(b[18:], 0xffffffff) }},
			{"central payload overlaps directory", func(b []byte) {
				binary.LittleEndian.PutUint32(b[layoutDirectory(b)+20:], uint32(len(b)))
				binary.LittleEndian.PutUint32(b[18:], uint32(len(b)))
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := bytes.Clone(data)
				tc.edit(bad)
				if err := checkLayout(t, bad); err == nil {
					t.Fatal("local/central inconsistency accepted")
				}
			})
		}
	}
	var multiple bytes.Buffer
	w := zip.NewWriter(&multiple)
	for _, entry := range []struct{ name, body string }{
		{"metadata.json", `{"provider":"virtualbox"}`}, {"disk.img", "VM fixture"},
	} {
		f, err := w.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := multiple.Bytes()
	if err := checkLayout(t, data); err != nil {
		t.Fatal(err)
	}
	// A streaming extractor sees both local entries even when the central
	// directory deliberately omits the first one.
	directory := layoutDirectory(data)
	firstLength := 46 + len("metadata.json")
	orphan := append(bytes.Clone(data[:directory]), data[directory+firstLength:]...)
	end := orphan[len(orphan)-22:]
	binary.LittleEndian.PutUint16(end[8:], 1)
	binary.LittleEndian.PutUint16(end[10:], 1)
	binary.LittleEndian.PutUint32(end[12:], uint32(len(orphan)-22-directory))
	if err := checkLayout(t, orphan); err == nil {
		t.Fatal("unindexed local entry accepted")
	}
	gap := append(
		append(bytes.Clone(data[:directory]), []byte("hidden local data")...),
		data[directory:]...)
	binary.LittleEndian.PutUint32(gap[len(gap)-6:], uint32(directory+len("hidden local data")))
	if err := checkLayout(t, gap); err == nil {
		t.Fatal("local-record gap accepted")
	}
	duplicate := bytes.Clone(data)
	binary.LittleEndian.PutUint32(duplicate[directory+firstLength+42:], 0)
	if err := checkLayout(t, duplicate); err == nil {
		t.Fatal("duplicate local record accepted")
	}
}

func descriptorBox(t *testing.T, width int, signed bool) []byte {
	t.Helper()
	data := layoutBox(t, true, nil)
	directory := layoutDirectory(data)
	old := data[directory-16 : directory]
	var descriptor []byte
	if signed {
		descriptor = append(descriptor, old[:4]...)
	}
	descriptor = append(descriptor, old[4:8]...)
	for _, off := range []int{8, 12} {
		var value [8]byte
		binary.LittleEndian.PutUint64(value[:], uint64(binary.LittleEndian.Uint32(old[off:])))
		descriptor = append(descriptor, value[:width]...)
	}
	out := append(append(bytes.Clone(data[:directory-16]), descriptor...), data[directory:]...)
	binary.LittleEndian.PutUint32(out[len(out)-6:], uint32(directory-16+len(descriptor)))
	return out
}

func TestUnitZIPDataDescriptorForms(t *testing.T) {
	for _, width := range []int{4, 8} {
		for _, signed := range []bool{false, true} {
			data := descriptorBox(t, width, signed)
			if err := checkLayout(t, data); err != nil {
				t.Fatalf("width=%d signed=%t: %v", width, signed, err)
			}
			directory := layoutDirectory(data)
			length := 4 + 2*width
			if signed {
				length += 4
			}
			for i := directory - length; i < directory; i++ {
				bad := bytes.Clone(data)
				bad[i] ^= 1
				if err := checkLayout(t, bad); err == nil {
					t.Fatalf("corrupt descriptor byte %d accepted", i)
				}
			}
		}
	}
}

func zipExtra(kind uint16, data []byte) []byte {
	out := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint16(out, kind)
	binary.LittleEndian.PutUint16(out[2:], uint16(len(data)))
	copy(out[4:], data)
	return out
}

func TestUnitZIPExtraFieldsCannotChangeNativePathsOrTypes(t *testing.T) {
	unicode := append([]byte{1, 0, 0, 0, 0}, []byte("metadata.json")...)
	binary.LittleEndian.PutUint32(unicode[1:], crc32.ChecksumIEEE([]byte("metadata.json")))
	for _, extra := range [][]byte{
		zipExtra(0x7075, unicode), zipExtra(0x5455, []byte{1, 0, 0, 0, 0}),
		zipExtra(0x7875, []byte{1, 1, 0, 1, 0}),
	} {
		if err := checkLayout(t, layoutBox(t, false, extra)); err != nil {
			t.Fatal(err)
		}
	}
	alternate := bytes.Clone(unicode)
	copy(alternate[5:], "../evilxx.txt")
	for _, extra := range [][]byte{
		zipExtra(0x7075, alternate), zipExtra(0x7075, []byte{1}),
		zipExtra(0x756e, make([]byte, 14)), zipExtra(0x000d, make([]byte, 12)),
		append(zipExtra(0x0001, nil), zipExtra(0x0001, nil)...),
	} {
		if err := checkLayout(t, layoutBox(t, false, extra)); err == nil {
			t.Fatal("ambiguous path/type metadata accepted")
		}
	}
	for _, extra := range [][]byte{{1}, {1, 0, 2, 0, 0}} {
		if _, _, err := zipLocalSizes(extra, "metadata.json", 0, 0); err == nil {
			t.Fatal("malformed extra accepted")
		}
	}
	wide := make([]byte, 16)
	binary.LittleEndian.PutUint64(wide, 1<<34)
	binary.LittleEndian.PutUint64(wide[8:], 1<<33)
	compressed, expanded, err := zipLocalSizes(
		zipExtra(1, wide),
		"disk.img",
		0xffffffff,
		0xffffffff,
	)
	if err != nil || compressed != 1<<33 || expanded != 1<<34 {
		t.Fatalf("ZIP64 sizes truncated: %d %d %v", compressed, expanded, err)
	}
}

type layoutReadFailure struct{}

func (r layoutReadFailure) ReadAt([]byte, int64) (int, error) { return 0, io.ErrUnexpectedEOF }

type layoutFailAtReader struct {
	io.ReaderAt
	offset int64
}

func (r layoutFailAtReader) ReadAt(p []byte, offset int64) (int, error) {
	if offset == r.offset {
		return 0, io.ErrUnexpectedEOF
	}
	return r.ReaderAt.ReadAt(p, offset)
}

func TestUnitZIPLocalLayoutReadFailuresAndCancellation(t *testing.T) {
	data := layoutBox(t, true, nil)
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := validateZIPLayout(
		ctx,
		bytes.NewReader(data),
		z,
		int64(layoutDirectory(data)),
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("cancellation: %v", err)
	}
	for _, offset := range []int64{0, 30, int64(layoutDirectory(data) - 16)} {
		if err := validateZIPLayout(
			t.Context(),
			layoutFailAtReader{bytes.NewReader(data), offset},
			z,
			int64(layoutDirectory(data)),
		); err == nil {
			t.Fatalf("read failure at %d ignored", offset)
		}
	}
	if zipNextRecord(layoutReadFailure{}, 0, 100, 50) {
		t.Fatal("unreadable next record accepted")
	}
	bad := bytes.Clone(data)
	bad[0] = 0
	if err := checkLayout(t, bad); err == nil {
		t.Fatal("missing local header accepted")
	}
}

func TestUnitZIPDirectoryMayReorderPhysicalEntries(t *testing.T) {
	t.Parallel()
	data := zipFixture(t, []zipFixtureEntry{{"metadata.json", `{}`}, {"disk.img", "disk content"}})
	directory := layoutDirectory(data)
	firstEnd := directory + 46 + len("metadata.json")
	reordered := append(bytes.Clone(data[:directory]), data[firstEnd:len(data)-22]...)
	reordered = append(reordered, data[directory:firstEnd]...)
	reordered = append(reordered, data[len(data)-22:]...)
	archive, err := checkZIPFixture(t.Context(), bytes.NewReader(reordered), int64(len(reordered)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 2 {
		t.Fatalf("file count = %d, want 2", len(archive.File))
	}
	want := map[string]string{"metadata.json": `{}`, "disk.img": "disk content"}
	for _, file := range archive.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		closeErr := r.Close()
		if err != nil || closeErr != nil || string(body) != want[file.Name] {
			t.Fatalf("entry %q: body=%q read=%v close=%v", file.Name, body, err, closeErr)
		}
		delete(want, file.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing entries: %v", want)
	}
}

func TestUnitZIPRejectsUnindexedOrRepeatedStoredRecords(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"unindexed local bytes", func(b []byte) []byte {
			directory := layoutDirectory(b)
			out := append(append(bytes.Clone(b[:directory]), []byte("unindexed")...), b[directory:]...)
			binary.LittleEndian.PutUint32(out[len(out)-6:], uint32(directory+len("unindexed")))
			return out
		}},
		{"duplicate indexed record", func(b []byte) []byte {
			directory := layoutDirectory(b)
			record := b[directory : len(b)-22]
			out := append(append(bytes.Clone(b[:len(b)-22]), record...), b[len(b)-22:]...)
			end := out[len(out)-22:]
			binary.LittleEndian.PutUint16(end[8:], 2)
			binary.LittleEndian.PutUint16(end[10:], 2)
			binary.LittleEndian.PutUint32(end[12:], uint32(2*len(record)))
			return out
		}},
		{"local data offset beyond directory", func(b []byte) []byte {
			binary.LittleEndian.PutUint16(b[28:], 0xffff)
			return b
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := tc.edit(layoutBox(t, false, nil))
			archive, err := checkZIPFixture(t.Context(), bytes.NewReader(data), int64(len(data)))
			if archive != nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("inconsistent stored layout: archive=%v error=%v", archive, err)
			}
		})
	}
}

func TestUnitZIPRejectsCentralOnlyNativePathMetadata(t *testing.T) {
	t.Parallel()
	// A benign local timestamp must not hide a central ASi Unix field, which
	// native extractors may interpret as a symlink target or different file type.
	data := layoutBox(t, false, zipExtra(0x5455, make([]byte, 14)))
	extra := layoutDirectory(data) + 46 + len("metadata.json")
	binary.LittleEndian.PutUint16(data[extra:], 0x756e)
	archive, err := checkZIPFixture(t.Context(), bytes.NewReader(data), int64(len(data)))
	if archive != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("central path/type metadata: archive=%v error=%v", archive, err)
	}
}

func TestUnitZIPRejectsUnrepresentablePayloadSpan(t *testing.T) {
	t.Parallel()
	wide := make([]byte, 16)
	binary.LittleEndian.PutUint64(wide, uint64(len(`{"provider":"virtualbox"}`)))
	binary.LittleEndian.PutUint64(wide[8:], 1<<63)
	data := layoutBox(t, false, zipExtra(1, wide))
	for _, off := range []int{18, 22, layoutDirectory(data) + 20, layoutDirectory(data) + 24} {
		binary.LittleEndian.PutUint32(data[off:], 0xffffffff)
	}
	archive, err := checkZIPFixture(t.Context(), bytes.NewReader(data), int64(len(data)))
	if archive != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("unrepresentable payload span: archive=%v error=%v", archive, err)
	}
}

func TestUnitZIPRejectsOverlappingStoredEntries(t *testing.T) {
	t.Parallel()
	first := layoutBox(t, false, nil)
	second := layoutBox(t, false, nil)
	firstEnd, secondEnd := layoutDirectory(first), layoutDirectory(second)
	data := append(bytes.Clone(first[:firstEnd]), second[:secondEnd]...)
	directory := len(data)
	data = append(data, first[firstEnd:len(first)-22]...)
	secondRecord := len(data)
	data = append(data, second[secondEnd:]...)
	binary.LittleEndian.PutUint32(data[secondRecord+42:], uint32(firstEnd))
	end := data[len(data)-22:]
	binary.LittleEndian.PutUint16(end[8:], 2)
	binary.LittleEndian.PutUint16(end[10:], 2)
	binary.LittleEndian.PutUint32(end[12:], uint32(len(data)-22-directory))
	binary.LittleEndian.PutUint32(end[16:], uint32(directory))
	// Both headers claim that the first payload consumes part of the second
	// local header. A central-directory-only reader can still find both entries.
	span := uint32(firstEnd + 14 - 30 - len("metadata.json"))
	for _, off := range []int{18, 22, directory + 20, directory + 24} {
		binary.LittleEndian.PutUint32(data[off:], span)
	}
	archive, err := checkZIPFixture(t.Context(), bytes.NewReader(data), int64(len(data)))
	if archive != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("overlapping stored entries: archive=%v error=%v", archive, err)
	}
}
