package item

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/archive"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// entry is a test archive entry; dir entries end in "/", and link entries
// are symbolic links, which explode skips.
type entry struct {
	name, body string
	link       bool
}

func zipOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name}
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

func (w *world) explode(p string, body []byte, atomic bool) (model.Item, error) {
	return w.svc.Explode(context.Background(), Explode{
		Deploy: Deploy{
			Target: Target{Repo: w.loc, Path: p}, Body: bytes.NewReader(body),
			Properties: model.Properties{"from": {"archive"}}, By: "alice",
		},
		Atomic: atomic, CanOverwrite: true,
	})
}

func TestUnitExplode(t *testing.T) {
	sample := []entry{
		{name: "a.txt", body: "a"},
		{name: "dir/b.txt", body: "b"},
		{name: "dir/empty/"},
		{name: "./dir/c.txt", body: "c"},
		{name: "link", link: true},
	}
	for name, body := range map[string][]byte{
		"zip":    zipOf(t, sample...),
		"tar":    tarOf(t, false, sample...),
		"tar.gz": tarOf(t, true, sample...),
	} {
		for _, atomic := range []bool{false, true} {
			w := newWorld(t)
			folder, err := w.explode("up/archive."+name, body, atomic)
			if err != nil || folder.FullPath() != "up" || !folder.Folder {
				t.Fatalf("%s: %+v %v", name, folder, err)
			}
			for _, p := range []string{"up/a.txt", "up/dir/b.txt", "up/dir/c.txt", "up/dir/empty"} {
				it, ok := w.get(w.loc, p)
				if !ok {
					t.Fatalf("%s: %s missing", name, p)
				}
				if !it.Folder {
					props, _ := w.db.Items().Properties(context.Background(), it.ID)
					if props["from"][0] != "archive" {
						t.Errorf("%s: %s properties %v", name, p, props)
					}
				}
			}
			for _, p := range []string{"up/archive." + name, "up/link"} {
				if _, ok := w.get(w.loc, p); ok {
					t.Errorf("%s: %s must not be stored", name, p)
				}
			}
		}
	}
	// Exploding at the root returns the root.
	w := newWorld(t)
	root, err := w.explode("archive.zip", zipOf(t, entry{name: "r.txt", body: "r"}), false)
	if err != nil || root.FullPath() != "" || !root.Folder {
		t.Fatalf("root: %+v %v", root, err)
	}
}

// unknownMethodZip is a zip whose one entry, a.txt, is compressed with a
// method no reader knows, so it lists but cannot be opened.
func unknownMethodZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name: "a.txt", Method: 99, CompressedSize64: 1, UncompressedSize64: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "a"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUnitExplodeRefused(t *testing.T) {
	ctx := context.Background()
	for name, body := range map[string][]byte{
		"parent":         zipOf(t, entry{name: "ok.txt", body: "ok"}, entry{name: "../evil.txt", body: "x"}),
		"nested":         zipOf(t, entry{name: "a/../../evil.txt", body: "x"}),
		"absolute":       tarOf(t, false, entry{name: "/etc/evil", body: "x"}),
		"drive":          zipOf(t, entry{name: "C:\\evil.txt", body: "x"}),
		"backslash":      zipOf(t, entry{name: "a\\..\\..\\evil.txt", body: "x"}),
		"not archive":    []byte("plain text"),
		"gzip not tar":   gzipOf(t, "plain"),
		"corrupt zip":    []byte("PK\x03\x04garbage"),
		"unknown method": unknownMethodZip(t),
	} {
		w := newWorld(t)
		if _, err := w.explode("x/archive.bin", body, false); !errors.Is(err, ErrBadArchive) {
			t.Errorf("%s: %v", name, err)
		}
		if items, _, _ := w.db.Items().
			Descendants(ctx, w.loc.Key, false, "", noLimit); len(
			items,
		) != 0 {
			t.Errorf("%s: wrote %v", name, items)
		}
	}
}

var noLimit = kernel.Page{Limit: kernel.MaxLimit}

func gzipOf(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	g := gzip.NewWriter(&buf)
	_, _ = io.WriteString(g, s)
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUnitExplodeLimits(t *testing.T) {
	defer func(b int64, n int) { maxExplodeBytes, maxExplodeEntries = b, n }(
		maxExplodeBytes, maxExplodeEntries)
	maxExplodeEntries = 2
	w := newWorld(t)
	three := zipOf(
		t,
		entry{name: "1", body: "1"},
		entry{name: "2", body: "2"},
		entry{name: "3", body: "3"},
	)
	if _, err := w.explode("x.zip", three, false); !errors.Is(err, ErrBadArchive) {
		t.Fatalf("entries: %v", err)
	}
	maxExplodeEntries = 100
	maxExplodeBytes = 10
	big := zipOf(
		t,
		entry{name: "big", body: strings.Repeat("x", 6)},
		entry{name: "big2", body: strings.Repeat("x", 6)},
	)
	if _, err := w.explode("x.zip", big, false); !errors.Is(err, ErrBadArchive) {
		t.Fatalf("declared bytes: %v", err)
	}
	maxExplodeBytes = 100
	if _, err := w.explode(
		"x.zip",
		bytes.Repeat([]byte("x"), 200),
		false,
	); !errors.Is(
		err,
		ErrBadArchive,
	) {
		t.Fatalf("archive size: %v", err)
	}
	// The extracted bytes are counted too, in case a header understates its
	// entry; Go's readers refuse such entries, so this budget is a second
	// line, exercised here directly.
	maxExplodeBytes = 4000
	f, size, err := spool(bytes.NewReader(zipOf(t, entry{name: "e", body: "0123456789"})),
		kernel.Checksums{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	maxExplodeBytes = 5
	if _, err := w.svc.uploadEntries(context.Background(), f, size, Zip,
		Explode{Deploy: Deploy{Target: Target{Repo: w.loc}}}, ""); !errors.Is(err, ErrBadArchive) {
		t.Fatalf("extracted bytes: %v", err)
	}
}

// closing closes f when an upload begins, so the read of the entry about
// to be stored fails.
type closing struct {
	blob.Store
	f *os.File
}

func (c closing) BeginUpload(ctx context.Context, o ...blob.UploadOption) (blob.Upload, error) {
	_ = c.f.Close()
	return c.Store.BeginUpload(ctx, o...)
}

// A spooled archive that cannot be read is no unusable archive: explode's
// walks return the read's error, before they list the entries and while an
// entry is being stored. A tar.gz is left out of the second, because gzip
// has read so small an archive whole before its entry is stored.
func TestUnitExplodeReadFailures(t *testing.T) {
	ctx := context.Background()
	sample := entry{name: "a.txt", body: "a"}
	for _, c := range []struct {
		name string
		kind ArchiveKind
		body []byte
	}{
		{"zip", Zip, zipOf(t, sample)},
		{"tar", Tar, tarOf(t, false, sample)},
		{"tar.gz", TarGz, tarOf(t, true, sample)},
	} {
		w := newWorld(t)
		d := Explode{Deploy: Deploy{Target: Target{Repo: w.loc}}, CanOverwrite: true}
		f, size, err := spool(bytes.NewReader(c.body), kernel.Checksums{}, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(f.Name()) })
		if c.kind != TarGz {
			w.svc.Blobs = closing{Store: w.svc.Blobs, f: f}
			_, err = w.svc.uploadEntries(ctx, f, size, c.kind, d, "")
			if !errors.Is(err, os.ErrClosed) || errors.Is(err, ErrBadArchive) {
				t.Errorf("%s while storing an entry: %v", c.name, err)
			}
		}
		_ = f.Close()
		_, err = w.svc.validateEntries(ctx, f, size, c.kind, d, "")
		if !errors.Is(err, os.ErrClosed) || errors.Is(err, ErrBadArchive) {
			t.Errorf("%s before listing: %v", c.name, err)
		}
	}
}

func TestUnitExplodeChecksumsAndConflicts(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	body := zipOf(t, entry{name: "a.txt", body: "a"})
	_, err := w.svc.Explode(ctx, Explode{Deploy: Deploy{
		Target: Target{Repo: w.loc, Path: "x.zip"}, Body: bytes.NewReader(body),
		Declared: kernel.Checksums{SHA1: strings.Repeat("0", 40)},
	}})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := w.svc.Explode(ctx, Explode{Deploy: Deploy{
		Target: Target{Repo: w.loc, Path: "x.zip"}, Body: bytes.NewReader(body),
		Declared: kernel.Checksums{SHA1: "zz"},
	}}); !errors.Is(err, ErrBadChecksum) {
		t.Fatalf("malformed: %v", err)
	}
	sums := sums(string(body))
	if _, err := w.svc.Explode(ctx, Explode{Deploy: Deploy{
		Target: Target{Repo: w.loc, Path: "x.zip"}, Body: bytes.NewReader(body), Declared: sums,
	}}); err != nil {
		t.Fatalf("matching checksums: %v", err)
	}
	// Overwriting needs the right, and the refusal names the entry in the
	// way; a folder in the way is a conflict.
	_, err = w.svc.Explode(ctx, Explode{Deploy: Deploy{
		Target: Target{Repo: w.loc, Path: "x.zip"}, Body: bytes.NewReader(body), By: "bob",
	}})
	var denied *OverwriteDeniedError
	if !errors.As(err, &denied) ||
		*denied != (OverwriteDeniedError{Repo: "libs", Path: "a.txt", User: "bob"}) {
		t.Fatalf("overwrite: %v", err)
	}
	w.must("f/a.txt/inside", "x")
	if _, err := w.explode("f/x.zip", body, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	// Under server-generated-checksums a declared mismatch is not checked.
	srv := w.addRepo(repo.Config{
		Key: "srv", Class: repo.Local, ChecksumPolicyType: repo.ServerGeneratedChecksums,
	})
	if _, err := w.svc.Explode(ctx, Explode{Deploy: Deploy{
		Target: Target{Repo: srv, Path: "x.zip"}, Body: bytes.NewReader(body),
		Declared: kernel.Checksums{SHA1: strings.Repeat("0", 40)},
	}}); err != nil {
		t.Fatalf("server policy: %v", err)
	}
}

func TestUnitExplodeAtomicity(t *testing.T) {
	body := zipOf(t, entry{name: "1.txt", body: "1"}, entry{name: "2.txt", body: "2"})
	for _, atomic := range []bool{true, false} {
		w := newWorld(t)
		w.svc.Store = faulty{w.db, "put:2.txt"}
		if _, err := w.explode("x.zip", body, atomic); !errors.Is(err, errBoom) {
			t.Fatalf("atomic=%v: %v", atomic, err)
		}
		_, kept := w.get(w.loc, "1.txt")
		if kept == atomic {
			t.Errorf("atomic=%v: 1.txt stored=%v", atomic, kept)
		}
	}
	// Failures before anything is stored.
	for _, fail := range []string{"begin", "commit", "get", "putFolder"} {
		w := newWorld(t)
		w.svc.Store = faulty{w.db, fail}
		w.svc.Blobs = faultyBlobStore{w.svc.Blobs, fail}
		b := zipOf(t, entry{name: "d/", body: ""}, entry{name: "d/1.txt", body: "1"})
		for _, atomic := range []bool{true, false} {
			if _, err := w.explode("x.zip", b, atomic); !errors.Is(err, errBoom) {
				t.Errorf("%s atomic=%v: %v", fail, atomic, err)
			}
		}
	}
}

func TestUnitArchiveWriterKinds(t *testing.T) {
	if _, err := newArchiveWriter(io.Discard, NotArchive); !errors.Is(err, ErrBadArchive) {
		t.Error(err)
	}
	if k, ok := ParseArchiveKind("tgz"); !ok || k != TarGz {
		t.Errorf("tgz: %v", k)
	}
}

// noReaderAt hides an fs blob's ReadAt, as object-store readers lack one.
type noReaderAt struct{ blob.Store }

type plainRSC struct{ io.ReadSeekCloser }

func (n noReaderAt) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	r, info, err := n.Store.Open(ctx, sha)
	return plainRSC{r}, info, err
}

func TestUnitArchiveEntry(t *testing.T) {
	ctx := context.Background()
	sample := []entry{
		{name: "dir/", body: ""},
		{name: "dir/in.txt", body: "inside"},
		{name: "top", body: "t"},
	}
	for name, body := range map[string][]byte{
		"zip":    zipOf(t, sample...),
		"tar":    tarOf(t, false, sample...),
		"tar.gz": tarOf(t, true, sample...),
	} {
		for _, hide := range []bool{false, true} {
			w := newWorld(t)
			it := w.must("a/archive."+name, string(body))
			if hide {
				w.svc.Blobs = noReaderAt{w.svc.Blobs}
			}
			var got bytes.Buffer
			var size int64
			err := w.svc.ArchiveEntry(ctx, it, "dir/in.txt", func(n int64, r io.Reader) error {
				size = n
				_, err := io.Copy(&got, r)
				return err
			})
			if err != nil || got.String() != "inside" || size != 6 {
				t.Fatalf("%s hide=%v: %q %d %v", name, hide, got.String(), size, err)
			}
			for _, missing := range []string{"dir", "nope", "../top", ""} {
				if err := w.svc.ArchiveEntry(ctx, it, missing, func(int64, io.Reader) error {
					return nil
				}); !errors.Is(err, ErrNotFound) {
					t.Errorf("%s %q: %v", name, missing, err)
				}
			}
			if err := w.svc.ArchiveEntry(ctx, it, "top", func(int64, io.Reader) error {
				return errBoom
			}); !errors.Is(err, errBoom) {
				t.Errorf("%s serve error: %v", name, err)
			}
		}
	}
	w := newWorld(t)
	plain := w.must("plain.txt", "not an archive")
	if err := w.svc.ArchiveEntry(ctx, plain, "x", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("plain: %v", err)
	}
	gone := plain
	gone.Actual.SHA256 = strings.Repeat("0", 64)
	if err := w.svc.ArchiveEntry(ctx, gone, "x", nil); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("missing blob: %v", err)
	}
}

var errRead = errors.New("read failed")

// unreadable opens content that serves its first sound reads at offsets
// and fails every other read with errRead, as a store whose connection
// drops does.
type unreadable struct {
	blob.Store
	sound int
}

func (u unreadable) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	r, info, err := u.Store.Open(ctx, sha)
	return &failing{ReadSeekCloser: r, sound: u.sound}, info, err
}

type failing struct {
	io.ReadSeekCloser
	sound int
}

func (f *failing) ReadAt(p []byte, off int64) (int, error) {
	if f.sound == 0 {
		return 0, errRead
	}
	f.sound--
	return archive.ReaderAt(f.ReadSeekCloser).ReadAt(p, off)
}

func (f *failing) Read([]byte) (int, error) { return 0, errRead }

// An archive whose blob opens but cannot be read is no corrupt archive:
// ArchiveEntry returns the read's error, not ErrNotFound, whether the read
// that sniffs the kind fails or the walk's after it, through ReadAt or a
// store without it, and for a name that tells no kind as well. Content
// that is no usable archive is still ErrNotFound.
func TestUnitArchiveEntryReadFailures(t *testing.T) {
	ctx := context.Background()
	sample := entry{name: "dir/in.txt", body: "inside"}
	for name, body := range map[string][]byte{
		"zip":    zipOf(t, sample),
		"tar":    tarOf(t, false, sample),
		"tar.gz": tarOf(t, true, sample),
	} {
		for _, p := range []string{"a/archive." + name, "a/archive.bin"} {
			w := newWorld(t)
			it := w.must(p, string(body))
			fs := w.svc.Blobs
			for i, blobs := range []blob.Store{
				unreadable{Store: fs},
				unreadable{Store: fs, sound: 1},
				noReaderAt{unreadable{Store: fs}},
			} {
				w.svc.Blobs = blobs
				err := w.svc.ArchiveEntry(ctx, it, "dir/in.txt", func(int64, io.Reader) error {
					t.Errorf("%s %d: served", p, i)
					return nil
				})
				if !errors.Is(err, errRead) || errors.Is(err, ErrNotFound) {
					t.Errorf("%s %d: %v", p, i, err)
				}
			}
		}
	}
	w := newWorld(t)
	for p, body := range map[string]string{
		"corrupt.zip":    "PK\x03\x04garbage",
		"corrupt.tar":    strings.Repeat("x", 1024),
		"corrupt.tar.gz": "\x1f\x8bgarbage",
		"method.zip":     string(unknownMethodZip(t)),
	} {
		it := w.must(p, body)
		if err := w.svc.ArchiveEntry(ctx, it, "a.txt", func(int64, io.Reader) error {
			t.Errorf("%s: served", p)
			return nil
		}); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestUnitArchiveFiles(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a := w.must("f/a.txt", "a")
	b := w.must("f/sub/b.txt", "bb")
	for _, kind := range []ArchiveKind{Zip, Tar, TarGz} {
		var buf bytes.Buffer
		if err := w.svc.ArchiveFiles(ctx, []model.Item{a, b}, "f", kind, true, &buf); err != nil {
			t.Fatal(err)
		}
		var names []string
		data := buf.Bytes()
		if err := archive.Walk(
			bytes.NewReader(data),
			int64(len(data)),
			kind,
			func(e archive.Entry) error {
				names = append(names, e.Name)
				return nil
			},
		); err != nil {
			t.Fatal(err)
		}
		sort.Strings(names)
		want := "a.txt a.txt.md5 a.txt.sha1 a.txt.sha256 sub/b.txt sub/b.txt.md5 sub/b.txt.sha1 sub/b.txt.sha256"
		if got := strings.Join(names, " "); got != want {
			t.Errorf("%v: %s", kind, got)
		}
	}
	gone := a
	gone.Actual.SHA256 = strings.Repeat("0", 64)
	if err := w.svc.ArchiveFiles(ctx, []model.Item{gone}, "f", Zip, false, io.Discard); err == nil {
		t.Fatal("a missing blob must fail")
	}
	for _, kind := range []ArchiveKind{Zip, Tar, TarGz} {
		if err := w.svc.ArchiveFiles(
			ctx,
			[]model.Item{a},
			"f",
			kind,
			false,
			failWriter{},
		); err == nil {
			t.Errorf("%v: a failing writer must fail", kind)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errBoom }

func TestUnitFolderDownloadSetting(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	fd, err := FolderDownload(ctx, w.db)
	if err != nil || fd.Enabled || fd.MaxFiles != 5000 || fd.MaxDownloadSizeMB != 1024 {
		t.Fatalf("defaults %+v %v", fd, err)
	}
	if err := w.db.Settings().Put(ctx, settings.SettingFolderDownload,
		model.FolderDownloadSettings{Enabled: true, MaxFiles: 3}); err != nil {
		t.Fatal(err)
	}
	if fd, _ := FolderDownload(
		ctx,
		w.db,
	); !fd.Enabled || fd.MaxFiles != 3 ||
		fd.MaxDownloadSizeMB != 1024 {
		t.Fatalf("stored %+v", fd)
	}
	if err := w.db.Settings().
		Put(ctx, settings.SettingFolderDownload, "not an object"); err != nil {
		t.Fatal(err)
	}
	if _, err := FolderDownload(ctx, w.db); err == nil {
		t.Fatal("an unreadable setting must fail")
	}
}

func TestUnitTree(t *testing.T) {
	w := newWorld(t)
	w.must("d/1", "1")
	w.must("d/s/2", "2")
	tree, err := w.svc.Tree(context.Background(), Target{Repo: w.loc, Path: "/d/"})
	if err != nil || len(tree) != 4 || tree[0].FullPath() != "d" {
		var names []string
		for _, it := range tree {
			names = append(names, it.FullPath())
		}
		t.Fatalf("%v %v", names, err)
	}
}
