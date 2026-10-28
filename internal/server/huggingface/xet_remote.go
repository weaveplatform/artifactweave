package huggingface

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/format/huggingface/xet"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

var errXetTransient = errors.New("huggingface: Xet requires a persistent verified remote artifact")

// A remote keeps the P9 immutable artifact as its source of truth. Xet's edge
// representation derives only from that verified file, never from an upstream
// CAS URL or a caller's credentials. No extra cache entries can outlive it.
func (h handler) xetFileRecord(
	ctx context.Context,
	chosen selected,
	id format.Identity,
	s sibling,
) (xetRecord, error) {
	if pkgroute.RequestFrom(chosen.request).Repo.Class != repo.Remote {
		return h.xetRecordBySHA(chosen.request, id, s.LFS.SHA256)
	}
	var record xetRecord
	if c := pkgroute.RequestFrom(
		chosen.request,
	).Repo; c.StoreArtifactsLocally != nil &&
		!*c.StoreArtifactsLocally {
		return record, errXetTransient
	}
	if s.Size <= 0 || s.Size > maxXetFile {
		return record, xetInvalid()
	}
	body, result, err := h.xetRemoteBody(ctx, chosen, id, s)
	if err != nil {
		return record, err
	}
	defer body.Close()
	key := xetCacheKey{
		Source: result.Identity,
		Path:   s.Filename,
		SHA256: s.LFS.SHA256,
		Size:   s.Size,
		ItemID: result.Item.ID,
	}
	if record, ok := h.xetCache.get(key); ok {
		return record, h.xetSourceFence(ctx, chosen, id, s, result)
	}
	record.SHA256 = s.LFS.SHA256
	record.Size = s.Size
	record.remoteFile = s.Filename
	hasher := sha256.New()
	reader := io.TeeReader(io.LimitReader(body, maxXetFile+1), hasher)
	var all []xet.HashPart
	var total int64
	for total < s.Size {
		var parts []xet.HashPart
		var size, count uint32
		for len(parts) < xet.MaxXorb/xet.MaxChunk && total < s.Size {
			if err = ctx.Err(); err != nil {
				return record, fmt.Errorf("huggingface: derive Xet metadata: %w", err)
			}
			chunk := make([]byte, min(int64(xet.MaxChunk), s.Size-total))
			if _, err = io.ReadFull(reader, chunk); err != nil {
				return record, xetInvalid()
			}
			part := xet.HashPart{Hash: xet.DataHash(chunk), Size: uint64(len(chunk))}
			parts = append(parts, part)
			count++
			all = append(all, part)
			size += uint32(len(chunk)) //nolint:gosec // Each decoded chunk is at most 128 KiB.
			total += int64(len(chunk))
		}
		record.Segments = append(
			record.Segments,
			xetSegment{
				Hash:  xet.Aggregate(parts).String(),
				Start: 0,
				End:   count,
				Size:  size,
			},
		)
	}
	var extra [1]byte
	if n, e := reader.Read(
		extra[:],
	); n != 0 || e != io.EOF ||
		hex.EncodeToString(hasher.Sum(nil)) != s.LFS.SHA256 {
		return record, xetInvalid()
	}
	record.Hash = xet.FileHash(all).String()
	if err = h.xetSourceFence(ctx, chosen, id, s, result); err != nil {
		return record, err
	}
	h.xetCache.put(key, record)
	return record, nil
}

func (h handler) xetRemoteBody(
	ctx context.Context,
	chosen selected,
	id format.Identity,
	s sibling,
) (io.ReadSeekCloser, source.Result, error) {
	result, err := h.remoteFile(ctx, chosen, id, s, "", "")
	if err != nil {
		return nil, result, err
	}
	if result.Stream != nil {
		_ = result.Stream.Body.Close()
		return nil, result, errXetTransient
	}
	if result.Item == nil {
		return nil, result, source.ErrInvalidResult
	}
	body, _, err := h.k.Blobs().Open(ctx, result.Item.Actual.SHA256)
	if err != nil {
		return nil, result, fmt.Errorf("huggingface: open Xet source: %w", err)
	}
	return body, result, nil
}

func (h handler) xetSegment(
	ctx context.Context,
	chosen selected,
	id format.Identity,
	file xetRecord,
	index int,
) (xet.Xorb, error) {
	segment := file.Segments[index]
	if file.remoteFile == "" {
		return h.readXorb(chosen.request, id, segment.Hash)
	}
	s := sibling{
		Filename: file.remoteFile,
		Size:     file.Size,
		LFS:      &lfsInfo{SHA256: file.SHA256, Size: file.Size},
	}
	body, result, err := h.xetRemoteBody(ctx, chosen, id, s)
	if err != nil {
		return xet.Xorb{}, err
	}
	defer body.Close()
	var offset int64
	for _, previous := range file.Segments[:index] {
		offset += int64(previous.Size)
	}
	_, err = body.Seek(offset, io.SeekStart)
	if err != nil {
		return xet.Xorb{}, xetInvalid()
	}
	raw := make([]byte, segment.Size)
	if _, err = io.ReadFull(body, raw); err != nil {
		return xet.Xorb{}, xetInvalid()
	}
	var chunks []xet.Chunk
	for len(raw) > 0 {
		if err = ctx.Err(); err != nil {
			return xet.Xorb{}, fmt.Errorf("huggingface: derive Xet segment: %w", err)
		}
		n := min(len(raw), xet.MaxChunk)
		data := bytes.Clone(raw[:n])
		chunks = append(chunks, xet.Chunk{Hash: xet.DataHash(data), Data: data})
		raw = raw[n:]
	}
	hash := xet.Aggregate(xet.Parts(chunks))
	if hash.String() != segment.Hash {
		return xet.Xorb{}, xetInvalid()
	}
	if err = h.xetSourceFence(ctx, chosen, id, s, result); err != nil {
		return xet.Xorb{}, err
	}
	return xet.Xorb{Hash: hash, Chunks: chunks}, nil
}

func (h handler) xetSourceFence(
	ctx context.Context,
	chosen selected,
	id format.Identity,
	s sibling,
	result source.Result,
) error {
	if chosen.fence != nil {
		if err := chosen.fence(ctx); err != nil {
			return fmt.Errorf("huggingface: revalidate Xet metadata: %w", err)
		}
	}
	if chosen.check != nil {
		path, _ := format.SnapshotPath(id, chosen.info.SHA, s.Filename)
		if err := chosen.check(ctx, path); err != nil {
			return fmt.Errorf("huggingface: revalidate Xet path: %w", err)
		}
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(ctx); err != nil {
			return fmt.Errorf("huggingface: revalidate Xet artifact: %w", err)
		}
	}
	return nil
}

func (h handler) xetChunkSizes(
	ctx context.Context,
	chosen selected,
	id format.Identity,
	file xetRecord,
	index int,
) ([]int, error) {
	segment := file.Segments[index]
	if file.remoteFile != "" {
		sizes := make([]int, segment.End)
		left := int(segment.Size)
		for i := range sizes {
			sizes[i] = min(left, xet.MaxChunk)
			left -= sizes[i]
		}
		return sizes, nil
	}
	xorb, err := h.readXorb(chosen.request, id, segment.Hash)
	if err != nil {
		return nil, err
	}
	sizes := make([]int, len(xorb.Chunks))
	for i, c := range xorb.Chunks {
		sizes[i] = len(c.Data)
	}
	return sizes, nil
}
