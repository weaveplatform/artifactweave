package remote

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/weaveplatform/artifactweave/internal/source"
)

const (
	cocoaPodsPackBytes    = 64 << 20
	cocoaPodsObjectBytes  = 64 << 20
	cocoaPodsPackObjects  = 10000
	cocoaPodsDecodedBytes = 256 << 20
)

// decodeCocoaPodsPack bounds inflation before go-git's parser allocates objects.
// Parser observers run after inflation and cannot enforce this boundary alone.
// remainingBytes is the caller's budget after retaining ancestor repositories;
// delta instructions and their reconstructed objects both consume this budget.
func decodeCocoaPodsPack(
	ctx context.Context,
	data []byte,
	remainingBytes int64,
	remainingObjects int,
) (*memory.Storage, error) {
	if err := checkCocoaPodsPackContext(ctx); err != nil {
		return nil, err
	}
	if len(data) > cocoaPodsPackBytes || remainingBytes <= 0 || remainingObjects <= 0 {
		return nil, source.ErrInvalidResult
	}
	remainingBytes = min(remainingBytes, cocoaPodsDecodedBytes)
	if err := preflightCocoaPodsPack(ctx, data, remainingBytes, remainingObjects); err != nil {
		return nil, cocoaPodsPackError(ctx, err)
	}
	storage := memory.NewStorage()
	scanner := packfile.NewScanner(&cocoaPodsPackReader{ctx: ctx, Reader: bytes.NewReader(data)})
	parser, err := packfile.NewParserWithStorage(scanner, storage, cocoaPodsPackObserver{ctx: ctx})
	if err == nil {
		_, err = parser.Parse()
	}
	if err != nil {
		return nil, cocoaPodsPackError(ctx, err)
	}
	if err := checkCocoaPodsPackContext(ctx); err != nil {
		return nil, err
	}
	return storage, nil
}

func preflightCocoaPodsPack(
	ctx context.Context,
	data []byte,
	remainingBytes int64,
	remainingObjects int,
) error {
	scanner := packfile.NewScanner(&cocoaPodsPackReader{ctx: ctx, Reader: bytes.NewReader(data)})
	_, count, err := scanner.Header()
	if err != nil {
		return fmt.Errorf("read git pack header: %w", err)
	}
	if count == 0 || count > cocoaPodsPackObjects || int64(count) > int64(remainingObjects) {
		return source.ErrInvalidResult
	}
	for range count {
		header, err := scanner.NextObjectHeader()
		if err != nil {
			return fmt.Errorf("read git object header: %w", err)
		}
		if header.Length < 0 || header.Length > cocoaPodsObjectBytes ||
			header.Length > remainingBytes {
			return source.ErrInvalidResult
		}
		remainingBytes -= header.Length
		capture := cocoaPodsDeltaHeader{ctx: ctx}
		switch header.Type {
		case plumbing.CommitObject, plumbing.TreeObject, plumbing.BlobObject, plumbing.TagObject:
		case plumbing.OFSDeltaObject, plumbing.REFDeltaObject:
			capture.delta = true
		default:
			return source.ErrInvalidResult
		}
		n, _, err := scanner.NextObject(&capture)
		if err != nil {
			return fmt.Errorf("inflate git object: %w", err)
		}
		if n != header.Length {
			return source.ErrInvalidResult
		}
		if capture.delta {
			target, err := capture.targetSize()
			if err != nil || target > remainingBytes {
				return source.ErrInvalidResult
			}
			remainingBytes -= target
		}
	}
	if _, err := scanner.Checksum(); err != nil {
		return fmt.Errorf("verify git pack checksum: %w", err)
	}
	// SeekFromStart reports the logical position, including bytes buffered by
	// the scanner; bytes.Reader.Len alone would miss trailing buffered data.
	end, err := scanner.SeekFromStart(0)
	if err != nil {
		return fmt.Errorf("check git pack trailer: %w", err)
	}
	if end != int64(len(data)) {
		return source.ErrInvalidResult
	}
	return nil
}

type cocoaPodsDeltaHeader struct {
	ctx   context.Context
	delta bool
	data  [2 * binary.MaxVarintLen64]byte
	n     int
}

func (h *cocoaPodsDeltaHeader) Write(p []byte) (int, error) {
	if err := checkCocoaPodsPackContext(h.ctx); err != nil {
		return 0, err
	}
	if h.delta {
		h.n += copy(h.data[h.n:], p)
	}
	return len(p), nil
}

func (h *cocoaPodsDeltaHeader) targetSize() (int64, error) {
	base, n := binary.Uvarint(h.data[:h.n])
	if n <= 0 || base > cocoaPodsObjectBytes {
		return 0, source.ErrInvalidResult
	}
	target, n := binary.Uvarint(h.data[n:h.n])
	if n <= 0 || target > cocoaPodsObjectBytes {
		return 0, source.ErrInvalidResult
	}
	return int64(target), nil
}

type cocoaPodsPackReader struct {
	ctx context.Context
	*bytes.Reader
}

func (r *cocoaPodsPackReader) Read(p []byte) (int, error) {
	if err := checkCocoaPodsPackContext(r.ctx); err != nil {
		return 0, err
	}
	return r.Reader.Read(p) //nolint:wrapcheck // io.Reader must preserve EOF for the pack scanner.
}

// The parser can satisfy object reads from its own memory cache; checking each
// observer callback also notices cancellation without another transport read.
type cocoaPodsPackObserver struct{ ctx context.Context }

func (o cocoaPodsPackObserver) OnHeader(uint32) error { return checkCocoaPodsPackContext(o.ctx) }

func (o cocoaPodsPackObserver) OnInflatedObjectHeader(plumbing.ObjectType, int64, int64) error {
	return checkCocoaPodsPackContext(o.ctx)
}

func (o cocoaPodsPackObserver) OnInflatedObjectContent(plumbing.Hash, int64, uint32, []byte) error {
	return checkCocoaPodsPackContext(o.ctx)
}

func (o cocoaPodsPackObserver) OnFooter(plumbing.Hash) error {
	return checkCocoaPodsPackContext(o.ctx)
}

func checkCocoaPodsPackContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("cocoapods git pack: %w", err)
	}
	return nil
}

func cocoaPodsPackError(ctx context.Context, err error) error {
	if contextErr := checkCocoaPodsPackContext(ctx); contextErr != nil {
		return contextErr
	}
	return fmt.Errorf("cocoapods git pack: %w: %w", source.ErrInvalidResult, err)
}

var _ io.ReadSeeker = (*cocoaPodsPackReader)(nil)
