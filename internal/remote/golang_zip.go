package remote

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"

	goformat "github.com/weaveplatform/artifactweave/internal/format/golang"
)

var ErrGoResponse = errors.New("remote: invalid upstream go module response")

// x/mod's canonical ZIP validation takes a filename. Stage a bounded private
// copy so every blob backend receives the same validation outside SQL, without
// depending on the implementation of its ReadSeeker. No archive is extracted.
func validateGoZip(ctx context.Context, f goformat.File, body io.Reader, size int64) error {
	if size < 0 || size > modzip.MaxZipFile {
		return ErrGoResponse
	}
	tmp, err := os.CreateTemp("", "artifactweave-go-zip-*")
	if err != nil {
		return fmt.Errorf("stage go archive validation: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, &goContextReader{ctx: ctx, body: io.LimitReader(body, size+1)})
	if err != nil {
		return fmt.Errorf("stage go archive bytes: %w", err)
	}
	if n != size {
		return ErrGoResponse
	}
	if _, err = modzip.CheckZip(
		module.Version{Path: f.Module, Version: f.Version},
		tmp.Name(),
	); err != nil {
		return ErrGoResponse
	}
	archive, err := zip.NewReader(tmp, size)
	if err != nil {
		return ErrGoResponse
	}
	// CheckZip validates paths, collisions and declared sizes; consuming every
	// member also checks decompression/CRC before a corrupt archive can be cached.
	for _, file := range archive.File {
		if err = checkGoZipMember(ctx, file); err != nil {
			return err
		}
	}
	return nil
}

func checkGoZipMember(ctx context.Context, file *zip.File) error {
	if file.UncompressedSize64 > modzip.MaxZipFile {
		return ErrGoResponse
	}
	if file.FileInfo().IsDir() {
		return nil
	}
	r, err := file.Open()
	if err != nil {
		return ErrGoResponse
	}
	defer r.Close()
	n, err := io.Copy(
		io.Discard,
		&goContextReader{ctx: ctx, body: io.LimitReader(r, modzip.MaxZipFile+1)},
	)
	if ctx.Err() != nil {
		return fmt.Errorf("validate go archive member: %w", ctx.Err())
	}
	if err != nil || n != int64(file.UncompressedSize64) {
		return ErrGoResponse
	}
	return nil
}

type goContextReader struct {
	ctx  context.Context
	body io.Reader
}

func (r *goContextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("go response canceled: %w", err)
	}
	return r.body.Read(b) //nolint:wrapcheck // internal copy reports a safe protocol error
}
