package archive_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/weaveplatform/artifactweave/internal/platform/archive"
)

// entry is a test archive entry; dir entries end in "/", and link entries
// are symbolic links.
type entry struct {
	name, body string
	link       bool
}

func zipOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.link {
			h.SetMode(0o777 | 1<<27) // os.ModeSymlink
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarOf(t *testing.T, gz bool, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var g *gzip.Writer
	if gz {
		g = gzip.NewWriter(&buf)
		w = g
	}
	tw := tar.NewWriter(w)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		switch {
		case strings.HasSuffix(e.name, "/"):
			h.Typeflag, h.Size = tar.TypeDir, 0
		case e.link:
			h.Typeflag, h.Size, h.Linkname = tar.TypeSymlink, 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if g != nil {
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

// kinds builds the same entries as each kind of archive.
func kinds(t *testing.T, entries ...entry) map[archive.Kind][]byte {
	t.Helper()
	return map[archive.Kind][]byte{
		archive.KindZip:   zipOf(t, entries...),
		archive.KindTar:   tarOf(t, false, entries...),
		archive.KindTarGz: tarOf(t, true, entries...),
	}
}

func TestUnitClean(t *testing.T) {
	for in, want := range map[string]string{
		"a/b.txt": "a/b.txt", "./a//b.txt": "a/b.txt", "dir/": "dir", "./": "",
		"a\\b.txt": "a/b.txt",
	} {
		if got, err := archive.Clean(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"../x", "/x", "C:/x", "a/../../x", "a/\x00"} {
		if _, err := archive.Clean(in); !errors.Is(err, archive.ErrUnusable) {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestUnitSniffAndKinds(t *testing.T) {
	ustar := make([]byte, 300)
	copy(ustar[257:], "ustar")
	for name, c := range map[string]struct {
		head     []byte
		file     string
		expected archive.Kind
	}{
		"zip magic":   {[]byte("PK\x03\x04"), "x", archive.KindZip},
		"empty zip":   {[]byte("PK\x05\x06"), "x", archive.KindZip},
		"gzip magic":  {[]byte{0x1f, 0x8b}, "x", archive.KindTarGz},
		"tar magic":   {ustar, "x", archive.KindTar},
		"jar name":    {nil, "x.JAR", archive.KindZip},
		"tar name":    {nil, "x.tar", archive.KindTar},
		"tgz name":    {nil, "x.tgz", archive.KindTarGz},
		"tar.gz name": {nil, "x.tar.gz", archive.KindTarGz},
		"neither":     {[]byte("text"), "x.txt", archive.KindNone},
	} {
		if got := archive.Sniff(c.head, c.file); got != c.expected {
			t.Errorf("%s: %v", name, got)
		}
	}
	if got := archive.KindOf(bytes.NewReader(ustar), "x"); got != archive.KindTar {
		t.Errorf("KindOf: %v", got)
	}
	for s, want := range map[string]archive.Kind{
		"zip": archive.KindZip, "TAR": archive.KindTar, "tar.gz": archive.KindTarGz, "tgz": archive.KindTarGz,
	} {
		if k, ok := archive.ParseKind(s); !ok || k != want {
			t.Errorf("%s: %v", s, k)
		}
	}
	if _, ok := archive.ParseKind("rar"); ok {
		t.Error("rar")
	}
	if archive.KindZip.ContentType() != "application/zip" ||
		archive.KindTar.ContentType() != "application/x-tar" ||
		archive.KindTarGz.ContentType() != "application/x-gzip" || archive.KindZip.Ext() != ".zip" ||
		archive.KindTar.Ext() != ".tar" || archive.KindTarGz.Ext() != ".tar.gz" {
		t.Error("content types and extensions")
	}
}

func TestUnitWalk(t *testing.T) {
	for kind, body := range kinds(t,
		entry{name: "dir/"}, entry{name: "dir/a.txt", body: "a"}, entry{name: "l", link: true},
	) {
		var got []string
		err := archive.Walk(bytes.NewReader(body), int64(len(body)), kind,
			func(e archive.Entry) error {
				line := e.Name
				switch {
				case e.Dir:
					line += " dir"
				case !e.Regular:
					line += " other"
				default:
					r, err := e.Open()
					if err != nil {
						return err
					}
					b, _ := io.ReadAll(r)
					line += " " + string(b)
				}
				got = append(got, line)
				return nil
			})
		if err != nil || strings.Join(got, ",") != "dir/ dir,dir/a.txt a,l other" {
			t.Errorf("%v: %v %v", kind, got, err)
		}
		stop := errors.New("stop")
		if err := archive.Walk(bytes.NewReader(body), int64(len(body)), kind,
			func(archive.Entry) error { return stop }); !errors.Is(err, stop) {
			t.Errorf("%v: the callback's error: %v", kind, err)
		}
	}
	garbage := []byte("not an archive at all")
	for _, kind := range []archive.Kind{archive.KindNone, archive.KindZip, archive.KindTarGz} {
		if err := archive.Walk(bytes.NewReader(garbage), int64(len(garbage)), kind,
			func(archive.Entry) error { return nil }); !errors.Is(err, archive.ErrUnusable) {
			t.Errorf("%v: %v", kind, err)
		}
	}
	// A tar stream cut off mid-header is unusable too.
	cut := tarOf(t, false, entry{name: "a", body: "a"})[:100]
	if err := archive.Walk(bytes.NewReader(cut), int64(len(cut)), archive.KindTar,
		func(archive.Entry) error { return nil }); !errors.Is(err, archive.ErrUnusable) {
		t.Errorf("cut tar: %v", err)
	}
}

var (
	errRead    = errors.New("read failed")
	errPastEnd = errors.New("read past the end")
)

// broken is content every read of which fails with err.
type broken struct{ err error }

func (b broken) ReadAt([]byte, int64) (int, error) { return 0, b.err }

// breakable reads ra until broken is set, then fails every read with
// errRead.
type breakable struct {
	ra     io.ReaderAt
	broken bool
}

func (b *breakable) ReadAt(p []byte, off int64) (int, error) {
	if b.broken {
		return 0, errRead
	}
	return b.ra.ReadAt(p, off)
}

// strict fails a read that reaches past its content with errPastEnd, as a
// store asked for bytes it does not hold may, where bytes.Reader reports
// io.EOF.
type strict []byte

func (s strict) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(s)) {
		return 0, errPastEnd
	}
	return copy(p, s[off:]), nil
}

// A read that fails is no unusable archive: Walk, Scan and ReadFile return
// the read's own error for every kind, a store's io.ErrUnexpectedEOF for a
// read it cut short included.
func TestUnitWalkReadFailures(t *testing.T) {
	short := fmt.Errorf("store: %w", io.ErrUnexpectedEOF)
	for kind, body := range kinds(t, entry{name: "a.txt", body: "a"}) {
		size := int64(len(body))
		for _, failure := range []error{errRead, short} {
			ra := broken{failure}
			got := map[string]error{
				"Walk": archive.Walk(ra, size, kind, func(archive.Entry) error { return nil }),
				"Scan": archive.Scan(ra, size, kind, archive.DefaultLimits,
					func(archive.Entry) error { return nil }),
			}
			_, _, got["ReadFile"] = archive.ReadFile(ra, size, kind, archive.DefaultLimits,
				func(string) bool { return true })
			for name, err := range got {
				if !errors.Is(err, failure) || errors.Is(err, archive.ErrUnusable) {
					t.Errorf("%v %s failing with %v: %v", kind, name, failure, err)
				}
			}
		}
	}
}

// An entry whose read fails once the walk has listed it returns the read's
// error from ReadEntry, and Wrap returns that error for it too.
func TestUnitReadEntryReadFailure(t *testing.T) {
	built := kinds(t, entry{name: "a.txt", body: "a"})
	for _, kind := range []archive.Kind{archive.KindZip, archive.KindTar} {
		ra := &breakable{ra: bytes.NewReader(built[kind])}
		err := archive.Walk(ra, int64(len(built[kind])), kind, func(e archive.Entry) error {
			ra.broken = true
			if _, err := archive.ReadEntry(e, archive.DefaultLimits); !errors.Is(err, errRead) ||
				errors.Is(err, archive.ErrUnusable) {
				t.Errorf("%v ReadEntry: %v", kind, err)
			}
			return e.Wrap(errOpen)
		})
		if !errors.Is(err, errRead) || errors.Is(err, archive.ErrUnusable) {
			t.Errorf("%v Wrap: %v", kind, err)
		}
	}
}

// Content the parsers refuse stays ErrUnusable whatever error they refuse
// it with: an empty tar.gz (io.EOF), a corrupt deflate stream
// (flate.CorruptInputError), a tar.gz cut short or a zip directory that
// lists fewer entries than it claims (io.ErrUnexpectedEOF), and an entry
// whose offset lies past the end of content that a store reads strictly.
func TestUnitWalkCorruptArchives(t *testing.T) {
	tgz := tarOf(t, true, entry{name: "a.txt", body: "a"})
	flipped := bytes.Clone(tgz)
	for i := 10; i < len(flipped)-8; i++ { // the deflate stream, between gzip's header and trailer
		flipped[i] ^= 0xff
	}
	short := zipOf(t, entry{name: "a.txt", body: "a"})
	end := bytes.LastIndex(short, []byte("PK\x05\x06"))
	binary.LittleEndian.PutUint16(short[end+10:], 2) // the directory's entry count
	for name, c := range map[string]struct {
		body []byte
		kind archive.Kind
	}{
		"empty tar.gz":        {nil, archive.KindTarGz},
		"corrupt deflate":     {flipped, archive.KindTarGz},
		"tar.gz cut short":    {tgz[:len(tgz)/2], archive.KindTarGz},
		"zip directory short": {short, archive.KindZip},
	} {
		err := archive.Walk(strict(c.body), int64(len(c.body)), c.kind,
			func(archive.Entry) error { return nil })
		if !errors.Is(err, archive.ErrUnusable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	past := zipOf(t, entry{name: "a.txt", body: "a"})
	dir := bytes.Index(past, []byte("PK\x01\x02"))
	binary.LittleEndian.PutUint32(past[dir+42:], 1<<30) // the entry's local header offset
	read := func(e archive.Entry) error {
		_, err := archive.ReadEntry(e, archive.DefaultLimits)
		return err
	}
	err := archive.Walk(strict(past), int64(len(past)), archive.KindZip, read)
	if !errors.Is(err, archive.ErrUnusable) || errors.Is(err, errPastEnd) {
		t.Errorf("an entry past the end: %v", err)
	}
}

// seeker hides a reader's ReadAt, as an object store's content reader
// lacks one.
type seeker struct{ io.ReadSeeker }

func TestUnitReaderAt(t *testing.T) {
	r := bytes.NewReader([]byte("0123456789"))
	if archive.ReaderAt(r) != io.ReaderAt(r) {
		t.Fatal("a ReaderAt is used as it is")
	}
	ra := archive.ReaderAt(seeker{r})
	buf := make([]byte, 3)
	if n, err := ra.ReadAt(buf, 4); err != nil || n != 3 || string(buf) != "456" {
		t.Fatalf("ReadAt: %d %v %q", n, err, buf)
	}
	if _, err := ra.ReadAt(buf, -1); err == nil {
		t.Fatal("a negative offset must fail")
	}
}

func TestUnitReadFile(t *testing.T) {
	want := func(n string) bool { return strings.HasSuffix(n, "/package.json") }
	for kind, body := range kinds(t,
		entry{name: "package/"},
		entry{name: "package/README.md", body: "readme"},
		entry{name: "package/link.json", link: true},
		entry{name: "./package/package.json", body: `{"name":"x"}`},
		entry{name: "other/package.json", body: "second"},
	) {
		ra := archive.ReaderAt(seeker{bytes.NewReader(body)})
		name, got, err := archive.ReadFile(ra, int64(len(body)), kind, archive.DefaultLimits, want)
		if err != nil || name != "package/package.json" || string(got) != `{"name":"x"}` {
			t.Errorf("%v: %q %q %v", kind, name, got, err)
		}
		if _, _, err := archive.ReadFile(ra, int64(len(body)), kind, archive.DefaultLimits,
			func(string) bool { return false }); !errors.Is(err, archive.ErrNotFound) {
			t.Errorf("%v none: %v", kind, err)
		}
		few := archive.DefaultLimits
		few.Entries = 2
		if _, _, err := archive.ReadFile(ra, int64(len(body)), kind, few, want); !errors.Is(
			err, archive.ErrUnusable) {
			t.Errorf("%v entries: %v", kind, err)
		}
		small := archive.DefaultLimits
		small.File = 4
		if _, _, err := archive.ReadFile(ra, int64(len(body)), kind, small, want); !errors.Is(
			err, archive.ErrUnusable) {
			t.Errorf("%v file size: %v", kind, err)
		}
	}
	garbage := []byte("garbage")
	if _, _, err := archive.ReadFile(bytes.NewReader(garbage), int64(len(garbage)), archive.KindZip,
		archive.DefaultLimits, want); !errors.Is(err, archive.ErrUnusable) {
		t.Errorf("garbage: %v", err)
	}
}

// A tar.gz whose entries before the wanted one inflate past the budget is
// refused before the walk reaches it: the bytes it skips count.
func TestUnitReadFileInflationBudget(t *testing.T) {
	bomb := tarOf(t, true,
		entry{name: "pad", body: strings.Repeat("0", 1<<20)},
		entry{name: "package/package.json", body: "{}"},
	)
	lim := archive.DefaultLimits
	lim.Inflated = 64 << 10
	_, _, err := archive.ReadFile(bytes.NewReader(bomb), int64(len(bomb)), archive.KindTarGz, lim,
		func(n string) bool { return n == "package/package.json" })
	if !errors.Is(err, archive.ErrUnusable) {
		t.Fatalf("inflation budget: %v", err)
	}
	lim.Inflated = 4 << 20
	if _, got, err := archive.ReadFile(bytes.NewReader(bomb), int64(len(bomb)), archive.KindTarGz,
		lim, func(n string) bool { return n == "package/package.json" }); err != nil ||
		string(got) != "{}" {
		t.Fatalf("within budget: %q %v", got, err)
	}
}

// Scan walks every entry within the limits, and stops with ErrUnusable at
// the first it passes, whatever the callback would do.
func TestUnitScan(t *testing.T) {
	body := tarOf(t, true,
		entry{name: "package/a", body: "1"}, entry{name: "package/b", body: "22"})
	var names []string
	err := archive.Scan(bytes.NewReader(body), int64(len(body)), archive.KindTarGz,
		archive.DefaultLimits, func(e archive.Entry) error {
			names = append(names, e.Name)
			return nil
		})
	if err != nil || strings.Join(names, ",") != "package/a,package/b" {
		t.Fatalf("%v %v", names, err)
	}
	few := archive.DefaultLimits
	few.Entries = 1
	if err := archive.Scan(bytes.NewReader(body), int64(len(body)), archive.KindTarGz, few,
		func(archive.Entry) error { return nil }); !errors.Is(err, archive.ErrUnusable) {
		t.Fatalf("entries: %v", err)
	}
}

// ScanTar walks a tar stream the caller decompressed within the same
// limits: the entries it passes and the bytes the stream yields.
func TestUnitScanTar(t *testing.T) {
	body := tarOf(t, false,
		entry{name: "pkg/a", body: "1"}, entry{name: "pkg/b", body: strings.Repeat("2", 256<<10)})
	var names []string
	err := archive.ScanTar(bytes.NewReader(body), archive.DefaultLimits,
		func(e archive.Entry) error {
			names = append(names, e.Name)
			return nil
		})
	if err != nil || strings.Join(names, ",") != "pkg/a,pkg/b" {
		t.Fatalf("%v %v", names, err)
	}
	few := archive.DefaultLimits
	few.Entries = 1
	if err := archive.ScanTar(bytes.NewReader(body), few,
		func(archive.Entry) error { return nil }); !errors.Is(err, archive.ErrUnusable) {
		t.Fatalf("entries: %v", err)
	}
	small := archive.DefaultLimits
	small.Inflated = 1 << 10
	if err := archive.ScanTar(bytes.NewReader(body), small,
		func(archive.Entry) error { return nil }); !errors.Is(err, archive.ErrUnusable) {
		t.Fatalf("inflation budget: %v", err)
	}
	if err := archive.ScanTar(strings.NewReader("not a tar stream"), archive.DefaultLimits,
		func(archive.Entry) error { return nil }); !errors.Is(err, archive.ErrUnusable) {
		t.Fatalf("not tar: %v", err)
	}
	// An error the stream gives is the caller's to judge, between a header
	// and inside an entry alike.
	if err := archive.ScanTar(iotest.ErrReader(errRead), archive.DefaultLimits,
		func(archive.Entry) error { return nil }); !errors.Is(err, errRead) ||
		errors.Is(err, archive.ErrUnusable) {
		t.Fatalf("a failing stream: %v", err)
	}
	header := io.MultiReader(bytes.NewReader(body[:512]), iotest.ErrReader(errRead))
	if err := archive.ScanTar(header, archive.DefaultLimits, func(e archive.Entry) error {
		_, err := archive.ReadEntry(e, archive.DefaultLimits)
		return err
	}); !errors.Is(err, errRead) || errors.Is(err, archive.ErrUnusable) {
		t.Fatalf("an entry the stream fails: %v", err)
	}
}

var errOpen = errors.New("open failed")

// An entry of a walk that cannot be opened or read for what it holds is
// unusable, and the refusal names it: here an unknown compression method
// and a CRC-32 that does not match.
func TestUnitReadEntryCorrupt(t *testing.T) {
	for want, h := range map[error]*zip.FileHeader{
		zip.ErrAlgorithm: {Name: "a.txt", Method: 99},
		zip.ErrChecksum:  {Name: "a.txt", Method: zip.Store, CRC32: crc32.ChecksumIEEE([]byte("a")) ^ 1},
	} {
		h.CompressedSize64, h.UncompressedSize64 = 1, 1
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.CreateRaw(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, "a"); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		err = archive.Walk(bytes.NewReader(buf.Bytes()), int64(buf.Len()), archive.KindZip,
			func(e archive.Entry) error {
				_, err := archive.ReadEntry(e, archive.DefaultLimits)
				return err
			})
		if !errors.Is(err, archive.ErrUnusable) || !errors.Is(err, want) ||
			err.Error() != "archive: unusable archive: a.txt: "+want.Error() {
			t.Errorf("%v: %v", want, err)
		}
	}
}

// An entry a caller made, which reads no archive, is unusable when it
// cannot be opened or read.
func TestUnitReadEntryFailures(t *testing.T) {
	for name, open := range map[string]func() (io.Reader, error){
		"open": func() (io.Reader, error) { return nil, errOpen },
		"read": func() (io.Reader, error) {
			return io.MultiReader(strings.NewReader("x"),
				iotestErr{}), nil
		},
	} {
		_, err := archive.ReadEntry(archive.Entry{Name: name, Open: open}, archive.DefaultLimits)
		if !errors.Is(err, archive.ErrUnusable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

type iotestErr struct{}

func (iotestErr) Read([]byte) (int, error) { return 0, errOpen }
