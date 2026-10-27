package archivecheck

import (
	"archive/zip"
	"bufio"
	"compress/flate"
	"errors"
	"fmt"
	"io"
)

// Registration belongs only to this archive; other readers retain their own
// decompression policy.
func registerStrictDeflate(archive *zip.Reader) {
	archive.RegisterDecompressor(zip.Deflate, strictZIPDeflate)
}

// A flate stream must consume the entire compressed entry: otherwise a streaming
// native extractor could see records hidden after its end but before its declared
// boundary. flate's ByteReader support makes this check possible in one pass.
func strictZIPDeflate(r io.Reader) io.ReadCloser {
	compressed := bufio.NewReader(r)
	return &zipDeflateReader{decoded: flate.NewReader(compressed), compressed: compressed}
}

type zipDeflateReader struct {
	decoded    io.ReadCloser
	compressed *bufio.Reader
}

func (r *zipDeflateReader) Read(p []byte) (int, error) {
	n, err := r.decoded.Read(p)
	if !errors.Is(err, io.EOF) {
		if err != nil {
			return n, fmt.Errorf("decode ZIP deflate: %w", err)
		}
		return n, nil
	}
	if r.compressed.Buffered() != 0 {
		return n, invalid("unconsumed ZIP deflate bytes")
	}
	if _, extra := r.compressed.ReadByte(); !errors.Is(extra, io.EOF) {
		if extra != nil {
			return n, fmt.Errorf("read ZIP deflate trailer: %w", extra)
		}
		return n, invalid("unconsumed ZIP deflate bytes")
	}
	return n, io.EOF
}

func (r *zipDeflateReader) Close() error {
	if err := r.decoded.Close(); err != nil {
		return fmt.Errorf("close ZIP deflate: %w", err)
	}
	return nil
}
