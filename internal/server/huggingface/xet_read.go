package huggingface

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/format/huggingface/xet"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (h handler) xetRecordBySHA(
	r *http.Request,
	id format.Identity,
	sha string,
) (xetRecord, error) {
	var record xetRecord
	it, err := h.k.DB().
		Items().
		Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, xetPath(id, "sha256", sha))
	if err != nil {
		return record, fmt.Errorf("huggingface: Xet content mapping: %w", err)
	}
	props, err := h.k.DB().Items().Properties(r.Context(), it.ID)
	if err != nil {
		return record, fmt.Errorf("huggingface: Xet content identity: %w", err)
	}
	hashes := props[xetHashProperty]
	if it.Folder || it.Actual.SHA256 != sha || len(hashes) != 1 || !formatSHA256(hashes[0]) {
		return record, xetInvalid()
	}
	raw, err := h.readXet(r, xetPath(id, "files", hashes[0]), xet.MaxShard)
	if err != nil {
		return record, err
	}
	if json.Unmarshal(raw, &record) != nil || record.SHA256 != sha || record.Hash != hashes[0] ||
		record.Size != it.Size ||
		record.Size <= 0 ||
		record.Size > maxXetFile ||
		len(record.Segments) == 0 {
		return record, xetInvalid()
	}
	var total int64
	var chunks uint64
	for _, s := range record.Segments {
		if !formatSHA256(s.Hash) || s.Start >= s.End || s.End > xet.MaxChunks || s.Size == 0 {
			return record, xetInvalid()
		}
		total += int64(s.Size)
		chunks += uint64(s.End - s.Start)
		if chunks > maxXetFileChunks {
			return record, xetInvalid()
		}
	}
	if total != record.Size {
		return record, xetInvalid()
	}
	return record, nil
}

func (h handler) xetPublishedFile(
	r *http.Request,
	c xetClaim,
	hash string,
) (selected, xetRecord, error) {
	id := claimIdentity(c)
	var record xetRecord
	if _, ok := xet.ParseHash(hash); !ok {
		return selected{}, record, badIdentity()
	}
	chosen, err := h.selectRepo(r, id, c.Reference, map[string]bool{})
	if err != nil {
		return chosen, record, err
	}
	if pkgroute.RequestFrom(chosen.request).Repo.Key != c.Member || chosen.info.SHA != c.Revision {
		return chosen, record, missing()
	}
	for _, s := range chosen.info.Siblings {
		if s.LFS == nil || c.File != "" && s.Filename != c.File {
			continue
		}
		path, _ := format.SnapshotPath(id, chosen.info.SHA, s.Filename)
		if !h.k.Can(chosen.request, path, types.ActionRead) {
			continue
		}
		candidate, e := h.xetFileRecord(r.Context(), chosen, id, s)
		if errors.Is(e, store.ErrNotFound) || errors.Is(e, errXetTransient) {
			continue
		}
		if e != nil {
			return chosen, record, e
		}
		if candidate.Hash != hash {
			continue
		}
		if candidate.Size != s.Size {
			return chosen, record, xetInvalid()
		}
		if chosen.fence != nil {
			if e = chosen.fence(r.Context()); e != nil {
				return chosen, record, e
			}
		}
		if chosen.check != nil {
			if e = chosen.check(r.Context(), path); e != nil {
				return chosen, record, e
			}
		}
		return chosen, candidate, nil
	}
	return chosen, record, missing()
}

type xetRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}
type xetTerm struct {
	Hash   string   `json:"hash"`
	Length int64    `json:"unpacked_length"`
	Range  xetRange `json:"range"`
}
type xetFetch struct {
	Range xetRange `json:"range"`
	URL   string   `json:"url"`
	Bytes xetRange `json:"url_range"`
}
type xetMultiRange struct {
	Chunks xetRange `json:"chunks"`
	Bytes  xetRange `json:"bytes"`
}
type xetMultiFetch struct {
	URL    string          `json:"url"`
	Ranges []xetMultiRange `json:"ranges"`
}

func (h handler) xetReconstruction(w http.ResponseWriter, r *http.Request) {
	authorized, c, err := h.xetAuthority(r, false)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	chosen, file, err := h.xetPublishedFile(authorized, c, chi.URLParam(r, "hash"))
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	start, end, ok := xetRequestedRange(r.Header.Get("Range"), file.Size)
	if !ok {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusRequestedRangeNotSatisfiable, "invalid Xet file range"),
		)
		return
	}
	terms := make([]xetTerm, 0, len(file.Segments))
	fetch := map[string][]xetFetch{}
	multi := map[string][]xetMultiFetch{}
	chunkSizes := map[string]map[int64]int64{}
	segmentIndex := map[string]int{}
	var fileOffset, offset int64
	for index, segment := range file.Segments {
		if fileOffset+int64(segment.Size) <= start {
			fileOffset += int64(segment.Size)
			continue
		}
		if fileOffset > end {
			break
		}
		sizes, e := h.xetChunkSizes(r.Context(), chosen, claimIdentity(c), file, index)
		if e != nil || int(segment.End) > len(sizes) {
			h.k.Error(w, r, xetInvalid())
			return
		}
		first, last := int64(-1), int64(-1)
		var length int64
		if chunkSizes[segment.Hash] == nil {
			chunkSizes[segment.Hash] = map[int64]int64{}
			segmentIndex[segment.Hash] = index
		}
		for chunk := segment.Start; chunk < segment.End; chunk++ {
			size := int64(sizes[chunk])
			if fileOffset+size > start && fileOffset <= end {
				if first < 0 {
					first = int64(chunk)
					if len(terms) == 0 {
						offset = start - fileOffset
					}
				}
				last = int64(chunk) + 1
				length += size
				chunkSizes[segment.Hash][int64(chunk)] = size + 8
			}
			fileOffset += size
		}
		if first < 0 {
			continue
		}
		chunkRange := xetRange{Start: first, End: last}
		terms = append(terms, xetTerm{Hash: segment.Hash, Length: length, Range: chunkRange})
	}
	// Fetch ranges for a xorb must be disjoint. Repeated file terms may refer
	// to overlapping chunks; duplicate fetch entries confuse the native cache.
	for hash, sizes := range chunkSizes {
		var indices []int64
		for index := range sizes {
			indices = append(indices, index)
		}
		slices.Sort(indices)
		for at := 0; at < len(indices); {
			first, last := indices[at], indices[at]+1
			packed := sizes[first]
			at++
			for at < len(indices) && indices[at] == last {
				packed += sizes[last]
				last++
				at++
			}
			chunkRange := xetRange{Start: first, End: last}
			byteRange := xetRange{Start: 0, End: packed - 1}
			address := h.xetEndpoint(
				authorized,
				c,
			) + "/data/" + file.Hash + "/" + strconv.Itoa(
				segmentIndex[hash],
			) + "/" + strconv.FormatInt(
				first,
				10,
			) + "/" + strconv.FormatInt(
				last,
				10,
			) + "?token=" + url.QueryEscape(
				h.signXet(c),
			)
			fetch[hash] = append(
				fetch[hash],
				xetFetch{Range: chunkRange, URL: address, Bytes: byteRange},
			)
			multi[hash] = append(
				multi[hash],
				xetMultiFetch{
					URL:    address,
					Ranges: []xetMultiRange{{Chunks: chunkRange, Bytes: byteRange}},
				},
			)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	if strings.Contains(pkgroute.RequestFrom(r).Rest, "/v2/") {
		h.writeJSON(
			w,
			map[string]any{"offset_into_first_range": offset, "terms": terms, "xorbs": multi},
		)
	} else {
		h.writeJSON(
			w,
			map[string]any{"offset_into_first_range": offset, "terms": terms, "fetch_info": fetch},
		)
	}
}

func xetRequestedRange(value string, size int64) (int64, int64, bool) {
	if size <= 0 {
		return 0, 0, false
	}
	if value == "" {
		return 0, size - 1, true
	}
	if !strings.HasPrefix(value, "bytes=") {
		return 0, 0, false
	}
	a, b, ok := strings.Cut(strings.TrimPrefix(value, "bytes="), "-")
	if !ok || a == "" {
		return 0, 0, false
	}
	start, e := strconv.ParseInt(a, 10, 64)
	if e != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end := size - 1
	if b != "" {
		upper, err := strconv.ParseUint(b, 10, 64)
		if err != nil || upper < uint64(start) {
			return 0, 0, false
		}
		//nolint:gosec // Bounded by the positive int64 file size.
		end = int64(min(upper, uint64(size-1)))
	}
	return start, end, true
}

func (h handler) xetData(w http.ResponseWriter, r *http.Request) {
	authorized, c, err := h.xetAuthority(r, false)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	chosen, file, err := h.xetPublishedFile(authorized, c, chi.URLParam(r, "hash"))
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	index, e1 := strconv.Atoi(chi.URLParam(r, "segment"))
	start, e2 := strconv.Atoi(chi.URLParam(r, "start"))
	end, e3 := strconv.Atoi(chi.URLParam(r, "end"))
	if e1 != nil || e2 != nil || e3 != nil || index < 0 || index >= len(file.Segments) ||
		start < 0 ||
		start >= end {
		h.k.Error(w, r, badIdentity())
		return
	}
	segment := file.Segments[index]
	if !xetContainsChunks(file, segment.Hash, start, end) {
		h.k.Error(w, r, badIdentity())
		return
	}
	//nolint:contextcheck // The selected request inherits cancellation and adds restored capability authority.
	xorb, err := h.xetSegment(chosen.request.Context(), chosen, claimIdentity(c), file, index)
	if err != nil || end > len(xorb.Chunks) {
		h.k.Error(w, r, xetInvalid())
		return
	}
	chunks := make([][]byte, 0, end-start)
	for _, chunk := range xorb.Chunks[start:end] {
		chunks = append(chunks, chunk.Data)
	}
	data, err := xet.EncodeChunks(chunks)
	if err != nil {
		h.k.Error(w, r, xetInvalid())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", h.now(), bytes.NewReader(data))
}

func (h handler) xetResolveHeaders(
	w http.ResponseWriter,
	r *http.Request,
	chosen selected,
	id format.Identity,
	s sibling,
) error {
	if !h.o.XetEnabled || s.LFS == nil {
		return nil
	}
	record, err := h.xetFileRecord(r.Context(), chosen, id, s)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, errXetTransient) {
		return nil
	}
	if err != nil {
		return err
	}
	// Keep the original virtual repository URL: its membership and permissions
	// must still be checked when the client exchanges its own credential.
	endpoint := baseurl.From(
		r.Context(),
	) + "/artifactory/api/huggingfaceml/" + url.PathEscape(
		pkgroute.RequestFrom(r).Repo.Key,
	)
	w.Header().Set("X-Xet-Hash", record.Hash)
	w.Header().
		Set("X-Xet-Refresh-Route", endpoint+"/api/"+id.Kind+"/"+id.RepoID()+"/xet-read-token/"+chi.URLParam(r, "revision")+"?file="+url.QueryEscape(s.Filename))
	return nil
}

func xetContainsChunks(file xetRecord, hash string, start, end int) bool {
	if start < 0 || start >= end || end > xet.MaxChunks {
		return false
	}
	var ranges []xetRange
	for _, segment := range file.Segments {
		if segment.Hash == hash {
			ranges = append(ranges, xetRange{Start: int64(segment.Start), End: int64(segment.End)})
		}
	}
	slices.SortFunc(ranges, func(a, b xetRange) int {
		if a.Start < b.Start {
			return -1
		}
		if a.Start > b.Start {
			return 1
		}
		return 0
	})
	cursor := int64(start)
	for _, r := range ranges {
		if r.Start > cursor {
			return false
		}
		cursor = max(cursor, r.End)
		if cursor >= int64(end) {
			return true
		}
	}
	return false
}
