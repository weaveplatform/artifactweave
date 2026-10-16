package item

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Limits on exploding an archive. Entry sizes in an archive's headers are
// the uploader's claim, so the extracted bytes are counted as they are
// written, which is what stops a zip bomb.
const (
	// MaxExplodeBytes bounds both the uploaded archive and the total of its
	// extracted entries.
	MaxExplodeBytes = 4 << 30
	// MaxExplodeEntries bounds the entries of one archive.
	MaxExplodeEntries = 10_000
)

// The limits in force; variables so tests need not build gigabyte archives.
var (
	maxExplodeBytes   int64 = MaxExplodeBytes
	maxExplodeEntries       = MaxExplodeEntries
)

// ErrBadArchive is an upload that is not a usable archive: an unknown or
// corrupt format, an entry escaping its folder, or limits exceeded (400).
var ErrBadArchive = errors.New("item: unusable archive")

// ErrOverwriteDenied is an explode that would replace existing files the
// caller may not delete (403).
var ErrOverwriteDenied = errors.New("item: no permission to overwrite")

// errFound ends an archive walk once the wanted entry is served.
var errFound = errors.New("item: archive entry found")

// ArchiveKind is an archive container format.
type ArchiveKind int

// Archive kinds; NotArchive is a file none of them.
const (
	NotArchive ArchiveKind = iota
	Zip
	Tar
	TarGz
)

// ParseArchiveKind reads an archiveType parameter (zip, tar, tar.gz, tgz).
func ParseArchiveKind(s string) (ArchiveKind, bool) {
	switch strings.ToLower(s) {
	case "zip":
		return Zip, true
	case "tar":
		return Tar, true
	case "tar.gz", "tgz":
		return TarGz, true
	}
	return NotArchive, false
}

// ContentType is the media type of an archive of the kind.
func (k ArchiveKind) ContentType() string {
	switch k {
	case Zip:
		return "application/zip"
	case Tar:
		return "application/x-tar"
	default:
		return "application/x-gzip"
	}
}

// Ext is the file extension of an archive of the kind.
func (k ArchiveKind) Ext() string {
	switch k {
	case Zip:
		return ".zip"
	case Tar:
		return ".tar"
	default:
		return ".tar.gz"
	}
}

// sniffArchive tells the format from the content's first bytes, falling
// back to the name for what has no signature to read.
func sniffArchive(head []byte, name string) ArchiveKind {
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return Zip
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return TarGz
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		return Tar
	}
	lower := strings.ToLower(name)
	for _, ext := range []string{".zip", ".jar", ".war", ".ear", ".aar", ".nupkg", ".whl"} {
		if strings.HasSuffix(lower, ext) {
			return Zip
		}
	}
	switch {
	case strings.HasSuffix(lower, ".tar"):
		return Tar
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return TarGz
	}
	return NotArchive
}

// archiveEntry is one entry as an archive lists it.
type archiveEntry struct {
	name string
	dir  bool
	// regular is a plain file; links and devices are skipped.
	regular bool
	size    int64
	open    func() (io.Reader, error)
}

// walkArchive calls fn for every entry of the archive in ra, in archive
// order. A tar entry's reader is only valid until fn returns.
func walkArchive(ra io.ReaderAt, size int64, kind ArchiveKind, fn func(archiveEntry) error) error {
	switch kind {
	case Zip:
		zr, err := zip.NewReader(ra, size)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrBadArchive, err)
		}
		for _, f := range zr.File {
			e := archiveEntry{
				name: f.Name, dir: f.FileInfo().IsDir(), regular: f.Mode().IsRegular(),
				size: int64(min(f.UncompressedSize64, 1<<62)), //nolint:gosec // capped
				open: func() (io.Reader, error) { return f.Open() },
			}
			if err := fn(e); err != nil {
				return err
			}
		}
		return nil
	case Tar, TarGz:
		var r io.Reader = io.NewSectionReader(ra, 0, size)
		if kind == TarGz {
			gz, err := gzip.NewReader(r)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrBadArchive, err)
			}
			defer func() { _ = gz.Close() }()
			r = gz
		}
		tr := tar.NewReader(r)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("%w: %w", ErrBadArchive, err)
			}
			e := archiveEntry{
				name: h.Name, dir: h.Typeflag == tar.TypeDir,
				regular: h.Typeflag == tar.TypeReg, size: h.Size,
				open: func() (io.Reader, error) { return tr, nil },
			}
			if err := fn(e); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("%w: not a zip, tar or tar.gz archive", ErrBadArchive)
}

// entryPath cleans an entry name into a relative path, refusing names that
// would leave the folder the archive is exploded into: absolute paths,
// drive letters and ".." segments. Backslashes count as separators, as
// Windows tools write them.
func entryPath(name string) (string, error) {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || (len(n) > 1 && n[1] == ':') {
		return "", fmt.Errorf("%w: the entry %q is an absolute path", ErrBadArchive, name)
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." {
			return "", fmt.Errorf(
				"%w: the entry %q leaves the archive's folder",
				ErrBadArchive,
				name,
			)
		}
	}
	n = strings.Trim(path.Clean("/"+n), "/")
	if n == "" || n == "." {
		return "", nil
	}
	if _, _, _, err := types.SplitItemPath(n); err != nil {
		return "", fmt.Errorf("%w: the entry %q is not a valid path", ErrBadArchive, name)
	}
	return n, nil
}

// Explode is a deploy whose body is an archive: its entries are deployed
// into the folder of Path, and the archive itself is not stored.
type Explode struct {
	Deploy
	// Atomic stores every entry or none.
	Atomic bool
	// CanOverwrite is the caller's right to replace existing files.
	CanOverwrite bool
}

// explodeFile is a validated entry to deploy.
type explodeFile struct {
	path string
	info blob.Info
}

// Explode deploys the entries of the archive in d.Body and returns the
// folder they landed in. The archive is spooled to a temporary file, checked
// against d.Declared, and read twice: once to validate every entry name and
// the limits, so nothing is written for an archive that would be refused,
// then to store the entries.
func (s *Service) Explode(ctx context.Context, d Explode) (types.Item, error) {
	declared, err := ParseChecksums(d.Declared)
	if err != nil {
		return types.Item{}, err
	}
	f, size, err := spool(d.Body, declared, clientPolicy(d.Repo))
	if err != nil {
		return types.Item{}, err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	head := make([]byte, 512)
	n, _ := f.ReadAt(head, 0)
	kind := sniffArchive(head[:n], d.Path)
	dir := path.Dir(strings.Trim(d.Path, "/"))
	if dir == "." {
		dir = ""
	}
	folders, err := s.validateEntries(ctx, f, size, kind, d, dir)
	if err != nil {
		return types.Item{}, err
	}
	files, err := s.uploadEntries(ctx, f, size, kind, d, dir)
	if err != nil {
		return types.Item{}, err
	}
	now := s.now()
	write := func(ctx context.Context, fl explodeFile) error {
		e := d.Deploy
		e.Path = fl.path
		_, err := s.putFile(ctx, e, digestsOf(fl.info), fl.info.Size, types.Checksums{}, now)
		return err
	}
	if d.Atomic {
		err = s.Store.Tx(ctx, func(ctx context.Context) error {
			for _, p := range folders {
				if err := s.ensureFolders(ctx, d.Target, p, d.By, now); err != nil {
					return err
				}
			}
			for _, fl := range files {
				if err := write(ctx, fl); err != nil {
					return err
				}
			}
			return nil
		})
	} else {
		for _, p := range folders {
			if _, err = s.MakeFolder(ctx, Target{Repo: d.Repo, Path: p}, nil, d.By); err != nil {
				break
			}
		}
		for _, fl := range files {
			if err != nil {
				break
			}
			err = s.Store.Tx(ctx, func(ctx context.Context) error { return write(ctx, fl) })
		}
	}
	if err != nil {
		return types.Item{}, err //nolint:wrapcheck // ours, from the transaction
	}
	if dir == "" {
		return types.Item{RepoKey: d.Repo.Key, Folder: true, Created: now, CreatedBy: d.By}, nil
	}
	folder, err := s.Store.Items().Get(ctx, d.Repo.Key, false, dir)
	if err != nil {
		return types.Item{}, fmt.Errorf("item: get %s: %w", dir, err)
	}
	return folder, nil
}

// validateEntries checks every entry before anything is stored, and returns
// the folders the archive lists (with no file inside, they would otherwise
// not appear).
func (s *Service) validateEntries(
	ctx context.Context,
	f *os.File,
	size int64,
	kind ArchiveKind,
	d Explode,
	dir string,
) ([]string, error) {
	var folders []string
	var entries int
	var total int64
	err := walkArchive(f, size, kind, func(e archiveEntry) error {
		entries++
		if entries > maxExplodeEntries {
			return fmt.Errorf("%w: more than %d entries", ErrBadArchive, maxExplodeEntries)
		}
		rel, err := entryPath(e.name)
		if err != nil || rel == "" {
			return err
		}
		target := join(dir, rel)
		if e.dir {
			folders = append(folders, target)
			return nil
		}
		if !e.regular {
			return nil
		}
		total += max(e.size, 0)
		if total > maxExplodeBytes {
			return fmt.Errorf("%w: the entries exceed %d bytes", ErrBadArchive, maxExplodeBytes)
		}
		existing, err := s.Store.Items().Get(ctx, d.Repo.Key, false, target)
		switch {
		case err == nil && existing.Folder:
			return fmt.Errorf("%w: %s is a folder", ErrConflict, target)
		case err == nil && !d.CanOverwrite:
			return fmt.Errorf("%w: %s", ErrOverwriteDenied, target)
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return fmt.Errorf("item: get %s: %w", target, err)
		}
		return nil
	})
	return folders, err
}

// uploadEntries stores the content of every file entry in the blob store,
// counting the bytes actually extracted against MaxExplodeBytes.
func (s *Service) uploadEntries(
	ctx context.Context,
	f *os.File,
	size int64,
	kind ArchiveKind,
	d Explode,
	dir string,
) ([]explodeFile, error) {
	var files []explodeFile
	budget := maxExplodeBytes
	err := walkArchive(f, size, kind, func(e archiveEntry) error {
		rel, err := entryPath(e.name)
		if err != nil || rel == "" || e.dir || !e.regular {
			return err
		}
		r, err := e.open()
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrBadArchive, e.name, err)
		}
		lr := &io.LimitedReader{R: r, N: budget + 1}
		u, err := s.Blobs.BeginUpload(ctx)
		if err != nil {
			return fmt.Errorf("item: begin upload: %w", err)
		}
		if _, err := u.Append(ctx, lr); err != nil {
			_ = u.Abort(context.WithoutCancel(ctx))
			return fmt.Errorf("%w: %s: %w", ErrBadArchive, e.name, err)
		}
		if lr.N == 0 {
			_ = u.Abort(context.WithoutCancel(ctx))
			return fmt.Errorf("%w: the entries exceed %d bytes", ErrBadArchive, maxExplodeBytes)
		}
		info, err := u.Commit(ctx, blob.Digests{})
		if err != nil {
			_ = u.Abort(context.WithoutCancel(ctx))
			return fmt.Errorf("item: commit upload: %w", err)
		}
		budget -= info.Size
		files = append(files, explodeFile{path: join(dir, rel), info: info})
		return nil
	})
	return files, err
}

// spool copies body to a temporary file, refusing more than MaxExplodeBytes,
// and checks the declared checksums under the client-checksums policy.
func spool(body io.Reader, declared types.Checksums, verify bool) (*os.File, int64, error) {
	f, err := os.CreateTemp("", "artifactweave-explode-*")
	if err != nil {
		return nil, 0, fmt.Errorf("item: spool archive: %w", err)
	}
	fail := func(err error) (*os.File, int64, error) {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, 0, err
	}
	h := blob.NewHasher()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, maxExplodeBytes+1))
	if err != nil {
		return fail(fmt.Errorf("item: spool archive: %w", err))
	}
	if n > maxExplodeBytes {
		return fail(fmt.Errorf("%w: the archive exceeds %d bytes", ErrBadArchive, maxExplodeBytes))
	}
	if verify {
		got := h.Sum()
		for _, c := range [][2]string{
			{declared.SHA256, got.SHA256},
			{declared.SHA512, got.SHA512},
			{declared.SHA1, got.SHA1},
			{declared.MD5, got.MD5},
		} {
			if c[0] != "" && c[0] != c[1] {
				return fail(ErrChecksumMismatch)
			}
		}
	}
	return f, n, nil
}

func digestsOf(info blob.Info) types.Checksums {
	d := info.Digests
	return types.Checksums{SHA256: d.SHA256, SHA512: d.SHA512, SHA1: d.SHA1, MD5: d.MD5}
}

// readerAt gives random access over a blob reader for zip, which reads its
// central directory from the end. Reads are serialised because they share
// one seek position.
type readerAt struct {
	mu sync.Mutex
	rs io.ReadSeeker
}

func (r *readerAt) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.rs.Seek(off, io.SeekStart); err != nil {
		return 0, err //nolint:wrapcheck // io.ReaderAt contract
	}
	return io.ReadFull(r.rs, p) //nolint:wrapcheck // io.ReaderAt contract
}

// ArchiveEntry finds the entry at entry inside the archive file it and
// calls serve with its size and content. ErrNotFound when the file is not an
// archive or has no such entry.
func (s *Service) ArchiveEntry(
	ctx context.Context,
	it types.Item,
	entry string,
	serve func(size int64, r io.Reader) error,
) error {
	want, err := entryPath(entry)
	if err != nil || want == "" {
		return ErrNotFound
	}
	content, info, err := s.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return fmt.Errorf("item: open %s: %w", it.FullPath(), err)
	}
	defer func() { _ = content.Close() }()
	var ra io.ReaderAt = &readerAt{rs: content}
	if direct, ok := content.(io.ReaderAt); ok {
		ra = direct
	}
	head := make([]byte, 512)
	n, _ := ra.ReadAt(head, 0)
	kind := sniffArchive(head[:n], it.Name)
	if kind == NotArchive {
		return ErrNotFound
	}
	err = walkArchive(ra, info.Size, kind, func(e archiveEntry) error {
		name, err := entryPath(e.name)
		if err != nil || name != want || e.dir || !e.regular {
			return nil //nolint:nilerr // an unusable entry is just not the one
		}
		r, err := e.open()
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrBadArchive, e.name, err)
		}
		if c, ok := r.(io.Closer); ok {
			defer func() { _ = c.Close() }()
		}
		if err := serve(e.size, r); err != nil {
			return err
		}
		return errFound
	})
	switch {
	case errors.Is(err, errFound):
		return nil
	case err == nil, errors.Is(err, ErrBadArchive):
		return ErrNotFound
	}
	return err
}

// ArchiveFiles writes files to w as one archive of kind, each named by its
// path relative to base, with ".md5", ".sha1" and ".sha256" sidecars when
// sums is set. The archive is streamed: nothing is buffered beyond one
// file's copy buffer.
func (s *Service) ArchiveFiles(
	ctx context.Context,
	files []types.Item,
	base string,
	kind ArchiveKind,
	sums bool,
	w io.Writer,
) error {
	aw, err := newArchiveWriter(w, kind)
	if err != nil {
		return err
	}
	for _, f := range files {
		name := strings.TrimPrefix(strings.TrimPrefix(f.FullPath(), base), "/")
		content, _, err := s.Blobs.Open(ctx, f.Actual.SHA256)
		if err != nil {
			return fmt.Errorf("item: open %s: %w", f.FullPath(), err)
		}
		err = aw.add(name, f.Size, f.Modified, content)
		_ = content.Close()
		if err != nil {
			return err
		}
		if !sums {
			continue
		}
		for _, sc := range []struct{ ext, v string }{
			{".md5", f.Actual.MD5}, {".sha1", f.Actual.SHA1}, {".sha256", f.Actual.SHA256},
		} {
			if sc.v == "" {
				continue
			}
			if err := aw.add(name+sc.ext, int64(len(sc.v)), f.Modified,
				strings.NewReader(sc.v)); err != nil {
				return err
			}
		}
	}
	return aw.close()
}

// archiveWriter writes one archive of any kind.
type archiveWriter struct {
	zw *zip.Writer
	tw *tar.Writer
	gz *gzip.Writer
}

func newArchiveWriter(w io.Writer, kind ArchiveKind) (*archiveWriter, error) {
	switch kind {
	case Zip:
		return &archiveWriter{zw: zip.NewWriter(w)}, nil
	case Tar:
		return &archiveWriter{tw: tar.NewWriter(w)}, nil
	case TarGz:
		gz := gzip.NewWriter(w)
		return &archiveWriter{tw: tar.NewWriter(gz), gz: gz}, nil
	}
	return nil, fmt.Errorf("%w: unknown archive kind", ErrBadArchive)
}

func (a *archiveWriter) add(name string, size int64, mod time.Time, r io.Reader) error {
	if a.zw != nil {
		fw, err := a.zw.CreateHeader(&zip.FileHeader{
			Name: name, Method: zip.Deflate, Modified: mod.UTC(),
		})
		if err != nil {
			return fmt.Errorf("item: archive %s: %w", name, err)
		}
		if _, err := io.Copy(fw, r); err != nil {
			return fmt.Errorf("item: archive %s: %w", name, err)
		}
		return nil
	}
	if err := a.tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: size, ModTime: mod.UTC(), Typeflag: tar.TypeReg,
		Format: tar.FormatPAX,
	}); err != nil {
		return fmt.Errorf("item: archive %s: %w", name, err)
	}
	if _, err := io.Copy(a.tw, r); err != nil {
		return fmt.Errorf("item: archive %s: %w", name, err)
	}
	return nil
}

func (a *archiveWriter) close() error {
	if a.zw != nil {
		return a.zw.Close() //nolint:wrapcheck // the end of the stream
	}
	if err := a.tw.Close(); err != nil {
		return fmt.Errorf("item: archive: %w", err)
	}
	if a.gz != nil {
		return a.gz.Close() //nolint:wrapcheck // the end of the stream
	}
	return nil
}

// FolderDownload reads the folder download setting, with Artifactory's
// defaults for what is unset: off, 1024 MB and 5000 files.
func FolderDownload(ctx context.Context, st store.Store) (types.FolderDownloadSettings, error) {
	fd := types.FolderDownloadSettings{}
	err := st.Settings().Get(ctx, types.SettingFolderDownload, &fd)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fd, fmt.Errorf("item: folder download setting: %w", err)
	}
	if fd.MaxDownloadSizeMB <= 0 {
		fd.MaxDownloadSizeMB = 1024
	}
	if fd.MaxFiles <= 0 {
		fd.MaxFiles = 5000
	}
	return fd, nil
}
