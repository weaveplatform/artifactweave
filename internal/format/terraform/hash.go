package terraform

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"

	"golang.org/x/mod/sumdb/dirhash"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format"
)

// maxInflated bounds the content H1 decompresses. The largest Terraform
// provider packages inflate to a few hundred megabytes per platform, and a
// zip that inflates past this is not one; without a bound a small archive
// of repeated bytes would keep a deploy hashing for minutes.
var maxInflated int64 = 16 << 30

// errInflated is an archive that inflates past maxInflated.
var errInflated = errors.New("terraform: the archive inflates past the hashing limit")

// H1 is the h1: hash of the zip in r, of size bytes, as Terraform computes
// it for an archive of a Terraform provider package
// (internal/getproviders/hash.go: dirhash.HashZip with dirhash.Hash1) and
// records it in its lock file: Go's module hash over every entry,
// directories included, by name. ok is false for content that is not a zip
// whose entries can all be read within maxInflated, for which the network
// mirror lists no hash (decision 0040). err is a failure to read r itself.
func H1(r io.ReaderAt, size int64) (h1 string, ok bool, err error) {
	src := &readErrs{r: r}
	z, zerr := zip.NewReader(src, size)
	if zerr != nil {
		return "", false, src.err
	}
	names := make([]string, 0, len(z.File))
	entries := make(map[string]*zip.File, len(z.File))
	for _, f := range z.File {
		names = append(names, f.Name)
		entries[f.Name] = f
	}
	left := maxInflated
	h, herr := dirhash.Hash1(names, func(name string) (io.ReadCloser, error) {
		rc, err := entries[name].Open()
		if err != nil {
			return nil, err //nolint:wrapcheck // dirhash reports it, and H1 only that it failed
		}
		return &budgeted{ReadCloser: rc, left: &left}, nil
	})
	if herr != nil {
		return "", false, src.err
	}
	return h, true, nil
}

// readErrs keeps the first error reading r gave other than io.EOF, so H1
// can tell content that is no zip, which is no failure, from a store that
// could not be read, which is.
type readErrs struct {
	r   io.ReaderAt
	err error
}

func (e *readErrs) ReadAt(p []byte, off int64) (int, error) {
	n, err := e.r.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) && e.err == nil {
		e.err = fmt.Errorf("terraform: read the archive: %w", err)
	}
	return n, err //nolint:wrapcheck // io.ReaderAt contract
}

// budgeted fails a read once the entries of one archive have inflated past
// the shared budget.
type budgeted struct {
	io.ReadCloser
	left *int64
}

func (b *budgeted) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if *b.left -= int64(n); *b.left < 0 {
		return n, errInflated
	}
	return n, err //nolint:wrapcheck // io.Reader contract
}

// storedH1 is the h1: hash of the content stored under sha256, with ok as
// H1 gives it.
func storedH1(ctx context.Context, blobs format.Blobs, sha256 string) (string, bool, error) {
	rc, info, err := blobs.Open(ctx, sha256)
	if err != nil {
		return "", false, fmt.Errorf("terraform: open %s: %w", sha256, err)
	}
	defer func() { _ = rc.Close() }()
	return H1(archive.ReaderAt(rc), info.Size)
}
