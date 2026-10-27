package helm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/archive"
	helmfmt "github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func validateRemoteChart(
	entry helmfmt.ChartVersion,
) func(context.Context, io.ReadSeeker, int64) error {
	return func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("helm chart validation: %w", err)
		}
		if err := completeChart(ctx, body, size); err != nil {
			return err
		}
		chart, err := helmfmt.ReadChart(archive.ReaderAt(body), size)
		if err != nil || chart == nil || chart.Name != entry.Name ||
			chart.Version != entry.Version {
			return remoteError()
		}
		return nil
	}
}

// Admission scans every tar entry; the format indexer intentionally ignores
// unsafe paths and non-metadata files. Drain beyond the tar end marker so gzip
// validates its checksum/trailer, and bound all expanded bytes including padding.
func completeChart(ctx context.Context, body io.ReadSeeker, size int64) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = fmt.Errorf("helm chart validation: %w", ctx.Err())
		}
	}()
	if size <= 0 || archive.KindOf(archive.ReaderAt(body), "chart.tgz") != archive.KindTarGz {
		return remoteError()
	}
	if _, err = body.Seek(0, io.SeekStart); err != nil {
		return remoteError()
	}
	gz, err := gzip.NewReader(&chartContextReader{ctx: ctx, reader: body})
	if err != nil {
		return remoteError()
	}
	defer gz.Close()
	const expandedLimit = 100 << 20
	bounded := &io.LimitedReader{R: gz, N: expandedLimit + 1}
	stream := &chartTailReader{reader: bounded}
	tr := tar.NewReader(stream)
	for count := 0; ; count++ {
		entry, readErr := tr.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || count >= 1<<20 {
			return remoteError()
		}
		name, cleanErr := archive.Clean(entry.Name)
		if cleanErr != nil || name == "" && entry.Typeflag != tar.TypeDir {
			return remoteError()
		}
		switch entry.Typeflag {
		case tar.TypeDir:
		case tar.TypeReg:
			if entry.Size > 5<<20 {
				return remoteError()
			}
		default:
			return remoteError()
		}
		// All bytes pass through the decompression budget, including ignored files.
		//nolint:gosec
		if _, err = io.Copy(io.Discard, tr); err != nil {
			return remoteError()
		}
	}
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := stream.Read(buffer)
		if bytes.Count(buffer[:n], []byte{0}) != n {
			return remoteError()
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return remoteError()
		}
	}
	if bounded.N <= 0 || stream.size < 1024 || stream.size%512 != 0 ||
		bytes.Count(stream.tail[:], []byte{0}) != len(stream.tail) {
		return remoteError()
	}
	if _, err = body.Seek(0, io.SeekStart); err != nil {
		return remoteError()
	}
	return nil
}

// archive/tar accepts EOF in end padding. Preserve the last two records to
// distinguish a complete archive from a truncated but recompressed envelope.
type chartTailReader struct {
	reader io.Reader
	size   int64
	tail   [1024]byte
}

func (r *chartTailReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.size += int64(n)
	if n >= len(r.tail) {
		copy(r.tail[:], p[n-len(r.tail):n])
	} else {
		copy(r.tail[:], r.tail[n:])
		copy(r.tail[len(r.tail)-n:], p[:n])
	}
	return n, err //nolint:wrapcheck // io.Reader preserves EOF
}

type chartContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *chartContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("helm chart read: %w", err)
	}
	return r.reader.Read(p) //nolint:wrapcheck // io.Reader preserves EOF
}

// The server validates the provenance envelope and declared chart identity/hash,
// while Helm verifies the signature against the consuming user's trusted keys.
// Neither metadata rewriting nor cache ingestion changes the signed bytes.
func validateRemoteProvenance(
	entry helmfmt.ChartVersion,
	filename string,
) func(context.Context, io.ReadSeeker, int64) error {
	return func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("helm provenance validation: %w", err)
		}
		const limit = 2 << 20
		if size < 0 || size > limit {
			return remoteError()
		}
		data, err := io.ReadAll(io.LimitReader(body, limit+1))
		if err != nil || int64(len(data)) != size ||
			!bytes.HasPrefix(data, []byte("-----BEGIN PGP SIGNED MESSAGE-----")) {
			return remoteError()
		}
		block, rest := clearsign.Decode(data)
		if block == nil || block.ArmoredSignature == nil || len(bytes.TrimSpace(rest)) != 0 {
			return remoteError()
		}
		packets := packet.NewReader(block.ArmoredSignature.Body)
		signature, err := packets.Next()
		if err != nil {
			return remoteError()
		}
		if _, ok := signature.(*packet.Signature); !ok {
			return remoteError()
		}
		if _, err = packets.Next(); !errors.Is(err, io.EOF) {
			return remoteError()
		}
		metadata, hashes, ok := bytes.Cut(block.Plaintext, []byte("\n...\n"))
		if !ok {
			return remoteError()
		}
		var chart helmfmt.Chart
		var sums struct {
			Files map[string]string `yaml:"files"`
		}
		if yaml.Unmarshal(metadata, &chart) != nil || chart.Name != entry.Name ||
			chart.Version != entry.Version ||
			yaml.Unmarshal(hashes, &sums) != nil {
			return remoteError()
		}
		value, ok := strings.CutPrefix(sums.Files[path.Base(filename)], "sha256:")
		if !ok || value == "" {
			return remoteError()
		}
		parsed, err := item.ParseChecksums(types.Checksums{SHA256: value})
		if err != nil || entry.Digest != "" && parsed.SHA256 != entry.Digest {
			return remoteError()
		}
		return nil
	}
}
