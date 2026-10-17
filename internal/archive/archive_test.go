package archive_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/archive"
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
		archive.Zip:   zipOf(t, entries...),
		archive.Tar:   tarOf(t, false, entries...),
		archive.TarGz: tarOf(t, true, entries...),
	}
}

func TestClean(t *testing.T) {
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

func TestSniffAndKinds(t *testing.T) {
	ustar := make([]byte, 300)
	copy(ustar[257:], "ustar")
	for name, c := range map[string]struct {
		head []byte
		file string
		want archive.Kind
	}{
		"zip magic":   {[]byte("PK\x03\x04"), "x", archive.Zip},
		"empty zip":   {[]byte("PK\x05\x06"), "x", archive.Zip},
		"gzip magic":  {[]byte{0x1f, 0x8b}, "x", archive.TarGz},
		"tar magic":   {ustar, "x", archive.Tar},
		"jar name":    {nil, "x.JAR", archive.Zip},
		"tar name":    {nil, "x.tar", archive.Tar},
		"tgz name":    {nil, "x.tgz", archive.TarGz},
		"tar.gz name": {nil, "x.tar.gz", archive.TarGz},
		"neither":     {[]byte("text"), "x.txt", archive.None},
	} {
		if got := archive.Sniff(c.head, c.file); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
	if got := archive.KindOf(bytes.NewReader(ustar), "x"); got != archive.Tar {
		t.Errorf("KindOf: %v", got)
	}
	for s, want := range map[string]archive.Kind{
		"zip": archive.Zip, "TAR": archive.Tar, "tar.gz": archive.TarGz, "tgz": archive.TarGz,
	} {
		if k, ok := archive.ParseKind(s); !ok || k != want {
			t.Errorf("%s: %v", s, k)
		}
	}
	if _, ok := archive.ParseKind("rar"); ok {
		t.Error("rar")
	}
	if archive.Zip.ContentType() != "application/zip" ||
		archive.Tar.ContentType() != "application/x-tar" ||
		archive.TarGz.ContentType() != "application/x-gzip" || archive.Zip.Ext() != ".zip" ||
		archive.Tar.Ext() != ".tar" || archive.TarGz.Ext() != ".tar.gz" {
		t.Error("content types and extensions")
	}
}

func TestWalk(t *testing.T) {
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
	for _, kind := range []archive.Kind{archive.None, archive.Zip, archive.TarGz} {
		if err := archive.Walk(bytes.NewReader(garbage), int64(len(garbage)), kind,
			func(archive.Entry) error { return nil }); !errors.Is(err, archive.ErrUnusable) {
			t.Errorf("%v: %v", kind, err)
		}
	}
	// A tar stream cut off mid-header is unusable too.
	cut := tarOf(t, false, entry{name: "a", body: "a"})[:100]
	if err := archive.Walk(bytes.NewReader(cut), int64(len(cut)), archive.Tar,
		func(archive.Entry) error { return nil }); !errors.Is(err, archive.ErrUnusable) {
		t.Errorf("cut tar: %v", err)
	}
}

// seeker hides a reader's ReadAt, as an object store's content reader
// lacks one.
type seeker struct{ io.ReadSeeker }

func TestReaderAt(t *testing.T) {
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

func TestReadFile(t *testing.T) {
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
	if _, _, err := archive.ReadFile(bytes.NewReader(garbage), int64(len(garbage)), archive.Zip,
		archive.DefaultLimits, want); !errors.Is(err, archive.ErrUnusable) {
		t.Errorf("garbage: %v", err)
	}
}

// A tar.gz whose entries before the wanted one inflate past the budget is
// refused before the walk reaches it: the bytes it skips count.
func TestReadFileInflationBudget(t *testing.T) {
	bomb := tarOf(t, true,
		entry{name: "pad", body: strings.Repeat("0", 1<<20)},
		entry{name: "package/package.json", body: "{}"},
	)
	lim := archive.DefaultLimits
	lim.Inflated = 64 << 10
	_, _, err := archive.ReadFile(bytes.NewReader(bomb), int64(len(bomb)), archive.TarGz, lim,
		func(n string) bool { return n == "package/package.json" })
	if !errors.Is(err, archive.ErrUnusable) {
		t.Fatalf("inflation budget: %v", err)
	}
	lim.Inflated = 4 << 20
	if _, got, err := archive.ReadFile(bytes.NewReader(bomb), int64(len(bomb)), archive.TarGz,
		lim, func(n string) bool { return n == "package/package.json" }); err != nil ||
		string(got) != "{}" {
		t.Fatalf("within budget: %q %v", got, err)
	}
}
