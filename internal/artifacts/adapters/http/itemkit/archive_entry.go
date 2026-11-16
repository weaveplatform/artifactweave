package itemkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sync"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// getArchiveEntry serves one entry of an archive file, the part of an item
// path after "!/" (decision 0002), as any downloaded file is served.
// 7.161.26 answers the ZipEntryResource it builds for the entry through
// DownloadServiceImpl.respond and RequestResponseHelper, as it answers a
// file, and setBasicHeaders sends the resource's:
//   - Last-Modified, the archive's: ZipEntryResourceImpl copies its time
//     from the archive's info;
//   - the entry's own SHA-1 as the ETag, bare, and its X-Checksum-Sha1,
//     -Sha256 and -Md5, which 7.x computes from the entry ("those are the
//     checksum of the file inside the zip", DbStoringRepoMixin). Which
//     algorithms it computes is not in the OSS sources; these are its
//     ChecksumType.BASE_CHECKSUM_TYPES [I];
//   - Accept-Ranges, X-Artifactory-Filename and the item route's
//     Content-Disposition, by the entry's name (getZipEntryName; RTFACT-23485
//     dropped its folders on 7.12.5) and the content browsing flag of the
//     repository that holds the archive (FileDisposition).
//
// It is typed by its name (ZipEntryResourceImpl.getMimeType), a browser
// gets a file's view by the entry's path (getModifiedMimeType), and the
// entry's SHA-1 and the archive's time decide a 304 (isNotModified) and an
// If-Range (createRangeAwareContext), as for a file. An entry is served
// whatever the content browsing flag: JFrog's Archive Entry Download asks
// only for Read, and RTFACT-23485 shows an entry answered with the
// attachment, which setBasicHeaders sends only without the flag. It is
// never redirected (decision 0029). A complete GET counts as a download of
// the archive: DbStoringRepoMixin.getResourceStreamHandle updates the
// archive's statistics for the entry's handle as for the archive's own.
//
// 7.x's setHeaderForNonAscii reads the name of the resource's info, which
// for an entry is the archive's, where setBasicHeaders names the entry; the
// entry is the file meant, so a name outside printable ASCII is named by
// the entry's filename* [I].
//
// The entry is read once for its checksums before anything is sent, so an
// archive whose blob is lost or fails a read answers 500, as its own
// download does, and an entry too corrupt to read through answers 404 as a
// corrupt archive does. A GET then reads it again, as 7.x streams the entry
// it has checksummed; a failure there, once the status is sent, can only
// be logged.
func (s *Kit) getArchiveEntry(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	f Found,
	entry string,
) {
	ctx := r.Context()
	if f.Item.Folder {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	sums, size, err := s.entryDigests(ctx, st, f.Item, entry)
	switch {
	case errors.Is(err, item.ErrNotFound):
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	case err != nil:
		httpkit.WriteErr(w, r, s.InternalError(ctx, "archive entry", err))
		return
	}
	// An overlay's read under a trace records the archive and serves nothing.
	if traceServed(r, "Returning found resource from "+
		traceRepoPath(f.Repo, f.Item.Cached, f.Item.FullPath())) {
		return
	}
	r = downloadRequest(r)
	name := path.Base(entry)
	h := w.Header()
	pkgroute.SetChecksums(h, sums)
	h.Set("X-Artifactory-Filename", FilenameHeader(name))
	h.Set("Content-Type", item.MimeType(entry))
	// Quoted for ServeContent's If-Range; the client receives it bare, as a
	// file's (bareETagWriter).
	h.Set("ETag", `"`+sums.SHA1+`"`)
	w = &bareETagWriter{ResponseWriter: w, sha1: sums.SHA1}
	if fileNotModified(r, sums, f.Item.Modified) {
		// Without Last-Modified, which a 304 beside an ETag drops
		// (WriteNotModified), as a file's does.
		setDisposition(h, notModifiedDisposition(f.Repo, name, ItemDisposition))
		pkgroute.WriteNotModified(w)
		return
	}
	setDisposition(h, FileDisposition(f.Repo, name, ItemDisposition))
	r = quoteETag(withoutHeaders(r, "If-None-Match", "If-Modified-Since"), sums.SHA1)
	browserView(h, r, f.Repo, name)
	content := &entryContent{
		size: size,
		open: func() io.ReadCloser { return s.openEntry(ctx, st, f.Item, entry) },
	}
	defer func() { _ = content.Close() }()
	// A range that does not overlap answers 416 as a file's does: Content-Range
	// alone.
	sw := &StatusWriter{ResponseWriter: &pkgroute.RangeRefusalWriter{ResponseWriter: w}}
	http.ServeContent(sw, r, name, f.Item.Modified, content)
	if err := content.failure(); err != nil {
		_ = s.InternalError(ctx, "archive entry", err)
		return
	}
	if sw.Complete(r) {
		s.recordCacheUse(ctx, f)
		if countsDownload(r, sw.status) {
			p, _ := httpkit.Principal(ctx)
			recordDownload(ctx, st, f, p)
		}
	}
}

// entryDigests reads entry of the archive it through once, for the
// checksums and the length of its content: the length the archive's
// header claims may differ, and 7.x's ZipEntryResourceImpl takes the one
// it counts while it computes the checksums.
func (s *Kit) entryDigests(
	ctx context.Context,
	st Store,
	it model.Item,
	entry string,
) (kernel.Checksums, int64, error) {
	var sums kernel.Checksums
	var size int64
	err := s.Items(st).ArchiveEntry(ctx, it, entry, func(_ int64, rd io.Reader) error {
		hasher := blob.NewHasher()
		n, err := io.Copy(hasher, rd)
		if err != nil {
			return fmt.Errorf("read entry: %w", err)
		}
		d := hasher.Sum()
		sums, size = kernel.Checksums{SHA256: d.SHA256, SHA1: d.SHA1, MD5: d.MD5}, n
		return nil
	})
	return sums, size, err //nolint:wrapcheck // ArchiveEntry's errors are the answers
}

// openEntry reads entry of the archive it from its start. The archive is
// walked in its own goroutine, because a tar entry can be read only while
// the walk stands on it; closing the reader ends the walk.
func (s *Kit) openEntry(ctx context.Context, st Store, it model.Item, entry string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		err := s.Items(st).ArchiveEntry(ctx, it, entry, func(_ int64, rd io.Reader) error {
			_, err := io.Copy(pw, rd)
			return err //nolint:wrapcheck // the pipe's reader receives it as it is
		})
		_ = pw.CloseWithError(err)
	}()
	return pr
}

// entryContent is an archive entry, whose length is known, as
// http.ServeContent reads it: a Seek only moves the position, and a Read
// opens the entry, skipping forward to the position, or opens it again
// when a range asks for bytes before those already read. A failed read is
// kept, as ServeContent returns none (failure).
//
// ServeContent reads several ranges in a goroutine of its own, which can
// still be reading when it returns early, after a client went away. So
// Close, from the handler, ends the current read without waiting for it,
// and no Read after Close opens the entry again: a walk opened then would
// never be closed.
type entryContent struct {
	open func() io.ReadCloser
	size int64
	// pos and at, the position and how far the current read has come, are
	// only used by the goroutine that reads.
	pos int64
	at  int64

	mu     sync.Mutex
	r      io.ReadCloser
	closed bool
	err    error
}

var (
	errEntrySeek   = errors.New("itemkit: seek outside an archive entry")
	errEntryClosed = errors.New("itemkit: archive entry closed")
)

func (c *entryContent) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		offset += c.pos
	case io.SeekEnd:
		offset += c.size
	}
	if offset < 0 {
		return c.pos, errEntrySeek
	}
	c.pos = offset
	return offset, nil
}

func (c *entryContent) Read(p []byte) (int, error) {
	r, err := c.reader()
	if err != nil {
		return 0, err
	}
	if c.at < c.pos {
		n, err := io.CopyN(io.Discard, r, c.pos-c.at)
		c.at += n
		if err != nil {
			return 0, c.fail(err)
		}
	}
	n, err := r.Read(p)
	c.at += int64(n)
	c.pos = c.at
	if err != nil && !errors.Is(err, io.EOF) {
		return n, c.fail(err)
	}
	return n, err //nolint:wrapcheck // io.EOF, as io.Reader asks
}

// reader is the read to continue from, opened again from the start when
// the position is behind it.
func (c *entryContent) reader() (io.Reader, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errEntryClosed
	}
	if c.r == nil || c.at > c.pos {
		if c.r != nil {
			_ = c.r.Close()
		}
		c.r, c.at = c.open(), 0
	}
	return c.r, nil
}

// fail keeps err, an entry that ends early included, as the read's
// failure, unless Close ended the read.
func (c *entryContent) fail(err error) error {
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil && !c.closed {
		c.err = err
	}
	return err
}

// failure is the first read that failed, nil when none did.
func (c *entryContent) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the walk of the entry's current read, if any. A pipe reader
// may be closed while another goroutine reads it, which then fails.
func (c *entryContent) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.r == nil {
		return nil
	}
	return c.r.Close() //nolint:wrapcheck // a pipe's close, which never fails
}
