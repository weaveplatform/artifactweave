package item

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format"
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
var ErrBadArchive = archive.ErrUnusable

// ErrOverwriteDenied is a deploy or an explode that would replace existing
// files the caller may not delete (403).
var ErrOverwriteDenied = errors.New("item: no permission to overwrite")

// errFound ends an archive walk once the wanted entry is served.
var errFound = errors.New("item: archive entry found")

// ArchiveKind is an archive container format.
type ArchiveKind = archive.Kind

// Archive kinds; NotArchive is a file none of them.
const (
	NotArchive = archive.None
	Zip        = archive.Zip
	Tar        = archive.Tar
	TarGz      = archive.TarGz
)

// ParseArchiveKind reads an archiveType parameter (zip, tar, tar.gz, tgz).
func ParseArchiveKind(s string) (ArchiveKind, bool) { return archive.ParseKind(s) }

// Explode is a deploy whose body is an archive: its entries are deployed
// into the folder of Path, and the archive itself is not stored.
type Explode struct {
	Deploy
	// Atomic stores every entry or none.
	Atomic bool
	// CanOverwrite is the caller's right to replace existing files.
	CanOverwrite bool
}

// explodeFile is a validated entry to deploy, with the environment of its
// hooks once the format inspected it.
type explodeFile struct {
	path string
	info blob.Info
	env  format.Env
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
	kind := archive.KindOf(f, d.Path)
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
	// Every entry is inspected before any is written, so an atomic explode
	// holds its transaction for the writes alone.
	for i, fl := range files {
		e := d.Deploy
		e.Path = fl.path
		if files[i].env, err = s.inspected(ctx, e, digestsOf(fl.info), fl.info.Size); err != nil {
			return types.Item{}, err
		}
	}
	now := s.now()
	write := func(ctx context.Context, fl explodeFile) error {
		e := d.Deploy
		e.Path = fl.path
		// The plan checked each entry's own path; a format may rename it.
		e.CanOverwritePath = func(string) bool { return d.CanOverwrite }
		_, err := s.putFile(
			ctx,
			fl.env,
			e,
			digestsOf(fl.info),
			fl.info.Size,
			types.Checksums{},
			now,
		)
		// An entry the format takes without storing, such as Maven's
		// snapshot metadata, is left out as a single upload of it would be.
		var accepted *format.Accepted
		if errors.As(err, &accepted) {
			return nil
		}
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
	err := archive.Walk(f, size, kind, func(e archive.Entry) error {
		entries++
		if entries > maxExplodeEntries {
			return fmt.Errorf("%w: more than %d entries", ErrBadArchive, maxExplodeEntries)
		}
		rel, err := archive.Clean(e.Name)
		if err != nil || rel == "" {
			return err //nolint:wrapcheck // archive's own ErrUnusable
		}
		target := join(dir, rel)
		if e.Dir {
			folders = append(folders, target)
			return nil
		}
		if !e.Regular {
			return nil
		}
		total += max(e.Size, 0)
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
	return folders, err //nolint:wrapcheck // archive's ErrUnusable or ours from the walk
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
	err := archive.Walk(f, size, kind, func(e archive.Entry) error {
		rel, err := archive.Clean(e.Name)
		if err != nil || rel == "" || e.Dir || !e.Regular {
			return err //nolint:wrapcheck // archive's own ErrUnusable
		}
		r, err := e.Open()
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrBadArchive, e.Name, err)
		}
		lr := &io.LimitedReader{R: r, N: budget + 1}
		u, err := s.Blobs.BeginUpload(ctx)
		if err != nil {
			return fmt.Errorf("item: begin upload: %w", err)
		}
		if _, err := u.Append(ctx, lr); err != nil {
			_ = u.Abort(context.WithoutCancel(ctx))
			return fmt.Errorf("%w: %s: %w", ErrBadArchive, e.Name, err)
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
	return files, err //nolint:wrapcheck // archive's ErrUnusable or ours from the walk
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

// ArchiveEntry finds the entry at entry inside the archive file it and
// calls serve with its size and content. ErrNotFound when the file is not an
// archive or has no such entry.
func (s *Service) ArchiveEntry(
	ctx context.Context,
	it types.Item,
	entry string,
	serve func(size int64, r io.Reader) error,
) error {
	want, err := archive.Clean(entry)
	if err != nil || want == "" {
		return ErrNotFound
	}
	content, info, err := s.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return fmt.Errorf("item: open %s: %w", it.FullPath(), err)
	}
	defer func() { _ = content.Close() }()
	ra := archive.ReaderAt(content)
	kind := archive.KindOf(ra, it.Name)
	if kind == NotArchive {
		return ErrNotFound
	}
	err = archive.Walk(ra, info.Size, kind, func(e archive.Entry) error {
		name, err := archive.Clean(e.Name)
		if err != nil || name != want || e.Dir || !e.Regular {
			return nil //nolint:nilerr // an unusable entry is just not the one
		}
		r, err := e.Open()
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrBadArchive, e.Name, err)
		}
		if c, ok := r.(io.Closer); ok {
			defer func() { _ = c.Close() }()
		}
		if err := serve(e.Size, r); err != nil {
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
	return err //nolint:wrapcheck // serve's own error
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
