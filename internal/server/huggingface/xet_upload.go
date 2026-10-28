package huggingface

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/format/huggingface/xet"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	xetHashProperty        = "artifactweave.xet.hash"
	maxXetFile       int64 = 10 << 30
	maxXetFileChunks       = 1 << 20
)

type xetSegment struct {
	Hash  string `json:"hash"`
	Start uint32 `json:"start"`
	End   uint32 `json:"end"`
	Size  uint32 `json:"size"`
}
type xetRecord struct {
	remoteFile string
	Hash       string       `json:"hash"`
	SHA256     string       `json:"sha256"`
	Size       int64        `json:"size"`
	Segments   []xetSegment `json:"segments"`
}

func (h handler) xetRoutes(r chi.Router) {
	prefix := "/xet/{kind}/{owner}/{name}/{revision}"
	r.Post(prefix+"/v1/xorbs/default/{hash}", h.xetUploadXorb)
	r.Post(prefix+"/v1/shards", h.xetUploadShard)
	r.Post(prefix+"/v2/shards", h.xetUploadShard)
	r.Get(prefix+"/v1/chunks/{prefix}/{hash}", h.xetDedup)
	r.Head(prefix+"/v1/xorbs/default/{hash}", h.xetXorbExists)
	r.Get(prefix+"/v1/reconstructions/{hash}", h.xetReconstruction)
	r.Get(prefix+"/v2/reconstructions/{hash}", h.xetReconstruction)
	r.Get(prefix+"/data/{hash}/{segment}/{start}/{end}", h.xetData)
}

func claimIdentity(c xetClaim) format.Identity {
	return format.Identity{Kind: c.Kind, Owner: c.Owner, Name: c.Name}
}

func xetPath(id format.Identity, kind, hash string) string {
	return id.Prefix() + "/.xet/" + kind + "/" + hash
}

func xetInvalid() error {
	return apierror.New(http.StatusUnprocessableEntity, "invalid Xet content or reconstruction")
}

func (h handler) xetUploadXorb(w http.ResponseWriter, r *http.Request) {
	target, c, err := h.xetAuthority(r, true)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	hash := chi.URLParam(r, "hash")
	data, err := io.ReadAll(io.LimitReader(r.Body, xet.MaxEncodedXorb+1))
	if err != nil || len(data) > xet.MaxEncodedXorb {
		h.k.Error(w, r, xetInvalid())
		return
	}
	if _, err = xet.ParseXorb(data, hash); err != nil {
		h.k.Error(w, r, xetInvalid())
		return
	}
	if _, err = h.storeXet(target, xetPath(claimIdentity(c), "xorbs", hash), data); err != nil {
		h.k.Error(w, r, err)
		return
	}
	h.writeJSON(w, map[string]bool{"was_inserted": true})
}

func (h handler) storeXet(r *http.Request, path string, data []byte) (types.Item, error) {
	r = r.WithContext(format.XetWrite(r.Context()))
	if existing, err := h.k.DB().
		Items().
		Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, path); err == nil {
		old, e := h.readXet(r, path, xet.MaxEncodedXorb)
		if e != nil {
			return types.Item{}, e
		}
		equal := bytes.Equal(old, data)
		if strings.Contains(path, "/.xet/xorbs/") {
			hash := path[strings.LastIndex(path, "/")+1:]
			_, e = xet.ParseXorb(old, hash)
			equal = e == nil
		}
		if strings.Contains(path, "/.xet/files/") {
			var a, b xetRecord
			equal = json.Unmarshal(old, &a) == nil && json.Unmarshal(data, &b) == nil &&
				a.Hash == b.Hash &&
				a.SHA256 == b.SHA256 &&
				a.Size == b.Size
		}
		if !equal {
			return types.Item{}, apierror.New(http.StatusConflict, "immutable Xet record differs")
		}
		return existing, nil
	} else if !errors.Is(
		err,
		store.ErrNotFound,
	) {
		return types.Item{}, fmt.Errorf("huggingface: lookup Xet record: %w", err)
	}
	it, err := h.k.StoreOnce(
		r,
		path,
		bytes.NewReader(data),
		nil,
		types.Checksums{},
		func(existing types.Item) error {
			old, err := h.readXet(r, path, int64(len(data))+1)
			if err != nil || !bytes.Equal(old, data) {
				return apierror.New(http.StatusConflict, "immutable Xet record differs")
			}
			return nil
		},
	)
	if err != nil {
		return types.Item{}, fmt.Errorf("huggingface: publish Xet record: %w", err)
	}
	return it, nil
}

func (h handler) readXet(r *http.Request, path string, limit int64) ([]byte, error) {
	it, err := h.k.DB().Items().Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, path)
	if err != nil {
		return nil, fmt.Errorf("huggingface: Xet item: %w", err)
	}
	if it.Folder || it.Size > limit {
		return nil, xetInvalid()
	}
	body, info, err := h.k.Blobs().Open(r.Context(), it.Actual.SHA256)
	if err != nil {
		return nil, fmt.Errorf("huggingface: Xet blob: %w", err)
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || int64(len(data)) != it.Size || info.Size != it.Size ||
		int64(len(data)) > limit {
		return nil, xetInvalid()
	}
	return data, nil
}

func (h handler) readXorb(r *http.Request, id format.Identity, hash string) (xet.Xorb, error) {
	data, err := h.readXet(r, xetPath(id, "xorbs", hash), xet.MaxEncodedXorb)
	if err != nil {
		return xet.Xorb{}, err
	}
	result, err := xet.ParseXorb(data, hash)
	if err != nil {
		return xet.Xorb{}, xetInvalid()
	}
	return result, nil
}

func (h handler) xetDedup(w http.ResponseWriter, r *http.Request) {
	_, _, err := h.xetAuthority(r, true)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	_, valid := xet.ParseHash(chi.URLParam(r, "hash"))
	prefix := chi.URLParam(r, "prefix")
	if !valid || prefix != "default" && prefix != "default-merkledb" {
		h.k.Error(w, r, badIdentity())
		return
	}
	// Global cross-model deduplication would reveal uncommitted or denied chunks.
	// A miss is the protocol's supported request to upload the client's own data.
	h.k.Error(w, r, missing())
}

func (h handler) xetXorbExists(w http.ResponseWriter, r *http.Request) {
	target, c, err := h.xetAuthority(r, true)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	hash := chi.URLParam(r, "hash")
	if _, ok := xet.ParseHash(hash); !ok {
		h.k.Error(w, r, badIdentity())
		return
	}
	//nolint:contextcheck // The selected request retains restored caller authority.
	_, err = h.k.DB().
		Items().
		Get(target.Context(), pkgroute.RequestFrom(target).Repo.Key, false, xetPath(claimIdentity(c), "xorbs", hash))

	if errors.Is(err, store.ErrNotFound) {
		h.k.Error(w, r, missing())
		return
	}
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("huggingface: Xet existence: %w", err))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h handler) xetUploadShard(w http.ResponseWriter, r *http.Request) {
	target, c, err := h.xetAuthority(r, true)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, xet.MaxShard+1))
	if err != nil || len(data) > xet.MaxShard {
		h.k.Error(w, r, xetInvalid())
		return
	}
	shard, err := xet.ParseShard(data)
	if err != nil {
		h.k.Error(w, r, xetInvalid())
		return
	}
	id := claimIdentity(c)
	for _, claimed := range shard.Xorbs {
		actual, e := h.readXorb(target, id, claimed.Hash.String())
		if e != nil || !sameParts(xet.Parts(actual.Chunks), claimed.Chunks) {
			h.k.Error(w, r, xetInvalid())
			return
		}
	}
	records := make([]xetRecord, 0, len(shard.Files))
	infos := make([]blob.Info, 0, len(shard.Files))
	for _, file := range shard.Files {
		record, info, e := h.materializeXet(target, id, file)
		if e != nil {
			h.k.Error(w, r, e)
			return
		}
		records = append(records, record)
		infos = append(infos, info)
	}
	// Publish all file mappings together only after every reconstruction has
	// passed its Xet root, per-segment verification and SHA256 checks.
	err = h.k.DB().
		Tx(target.Context(), func(ctx context.Context) error { //nolint:contextcheck // target inherits r.Context and restored authority.
			target := target.WithContext(format.XetWrite(ctx))
			for i, record := range records {
				raw, _ := json.Marshal(record)
				if _, err := h.storeXet(
					target,
					xetPath(id, "files", record.Hash),
					raw,
				); err != nil {
					return err
				}
				existing, e := h.k.DB().
					Items().
					Get(ctx, pkgroute.RequestFrom(target).Repo.Key, false, xetPath(id, "sha256", record.SHA256))
				switch {
				case e == nil:
					if existing.Folder || existing.Size != record.Size ||
						existing.Actual.SHA256 != record.SHA256 {
						return xetInvalid()
					}
				case !errors.Is(e, store.ErrNotFound):
					return fmt.Errorf("huggingface: lookup reconstructed Xet file: %w", e)
				default:
					_, err := h.k.StoreNew(
						target,
						xetPath(id, "sha256", record.SHA256),
						infos[i],
						types.Properties{xetHashProperty: {record.Hash}},
						types.Checksums{SHA256: record.SHA256},
						func() error {
							existing, e := h.k.DB().
								Items().
								Get(ctx, pkgroute.RequestFrom(target).Repo.Key, false, xetPath(id, "sha256", record.SHA256))
							if e != nil || existing.Size != record.Size ||
								existing.Actual.SHA256 != record.SHA256 {
								return xetInvalid()
							}
							return nil
						},
					)
					if err != nil {
						return fmt.Errorf("huggingface: pin reconstructed Xet file: %w", err)
					}
				}

				if _, err := h.storeXet(
					target,
					xetStagePath(id, c.Subject, record.SHA256),
					[]byte(record.SHA256),
				); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if strings.Contains(pkgroute.RequestFrom(r).Rest, "/v2/") {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, "{\"type\":\"result\"}\n")
	} else {
		h.writeJSON(w, map[string]int{"result": 1})
	}
}

func sameParts(a, b []xet.HashPart) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type xetReader struct {
	ctx     context.Context
	next    func() (io.Reader, error)
	current io.Reader
}

func (r *xetReader) Read(p []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, fmt.Errorf("huggingface: Xet reader cancelled: %w", err)
		}
		if r.current == nil {
			next, err := r.next()
			if err != nil {
				return 0, err
			}
			r.current = next
		}
		n, err := r.current.Read(p)
		if errors.Is(err, io.EOF) {
			r.current = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			return n, fmt.Errorf("huggingface: read Xet segment: %w", err)
		}
		return n, nil
	}
}

func (h handler) materializeXet(
	r *http.Request,
	id format.Identity,
	file xet.File,
) (xetRecord, blob.Info, error) {
	record := xetRecord{Hash: file.Hash.String()}
	var parts []xet.HashPart
	at := 0
	reader := &xetReader{ctx: r.Context()}
	reader.next = func() (io.Reader, error) {
		if at == len(file.Segments) {
			if xet.FileHash(parts) != file.Hash {
				return nil, xetInvalid()
			}
			return nil, io.EOF
		}
		segment := file.Segments[at]
		at++
		xorb, err := h.readXorb(r, id, segment.Xorb.String())
		if err != nil || int(segment.End) > len(xorb.Chunks) {
			return nil, xetInvalid()
		}
		chunks := xorb.Chunks[segment.Start:segment.End]
		hashes := xet.Parts(chunks)
		var size uint64
		var readers []io.Reader
		for _, chunk := range chunks {
			size += uint64(len(chunk.Data))
			readers = append(readers, bytes.NewReader(chunk.Data))
		}
		if size != uint64(segment.Size) ||
			file.Verified && xet.RangeHash(hashes) != segment.Verification ||
			record.Size+int64(size) > maxXetFile {
			return nil, xetInvalid()
		}
		record.Size += int64(size)
		record.Segments = append(
			record.Segments,
			xetSegment{
				Hash:  segment.Xorb.String(),
				Start: segment.Start,
				End:   segment.End,
				Size:  segment.Size,
			},
		)
		if len(hashes) > maxXetFileChunks-len(parts) {
			return nil, xetInvalid()
		}
		parts = append(parts, hashes...)
		return io.MultiReader(readers...), nil
	}
	info, err := h.k.Upload(r, reader)
	if err != nil {
		return record, info, fmt.Errorf("huggingface: reconstruct Xet file: %w", err)
	}
	if info.Size != record.Size || file.SHA256 != "" && file.SHA256 != info.Digests.SHA256 {
		return record, info, xetInvalid()
	}
	record.SHA256 = info.Digests.SHA256
	return record, info, nil
}
