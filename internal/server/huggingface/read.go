package huggingface

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git blob IDs are SHA-1 protocol identifiers, not security digests.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxInfo = 8 << 20

type sibling struct {
	Filename string   `json:"rfilename"`
	BlobID   string   `json:"blobId,omitempty"` //nolint:tagliatelle // Hugging Face Hub wire key.
	Size     int64    `json:"size,omitempty"`
	LFS      *lfsInfo `json:"lfs,omitempty"`
}

type lfsInfo struct {
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	PointerSize int64  `json:"pointerSize"` //nolint:tagliatelle // Hugging Face Hub wire key.
}

type repoInfo struct {
	ID       string    `json:"id"`
	SHA      string    `json:"sha"`
	Siblings []sibling `json:"siblings"`
}

type selected struct {
	request *http.Request
	info    repoInfo
	fence   func(context.Context) error
	check   func(context.Context, string) error
}

func badIdentity() error { return apierror.New(http.StatusBadRequest, "invalid Hugging Face identity") }

func missing() error {
	return apierror.New(http.StatusNotFound, "Hugging Face revision or file not found")
}

func selector(s string) bool { return format.RefName(s) }

func (h handler) info(w http.ResponseWriter, r *http.Request) {
	id, ok := identity(r)
	rev := chi.URLParam(r, "revision")
	if rev == "" {
		rev = "main"
	}
	if !ok || !selector(rev) || r.URL.RawQuery != "" && r.URL.RawQuery != "blobs=true" {
		h.k.Error(w, r, badIdentity())
		return
	}
	chosen, err := h.selectRepo(r, id, rev, map[string]bool{})
	if err != nil {
		if rev != "main" && isNotFound(err) {
			w.Header().Set("X-Error-Code", "RevisionNotFound")
		}
		h.k.Error(w, r, err)
		return
	}
	if chosen.fence != nil {
		if err := chosen.fence(r.Context()); err != nil {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		}
	}
	h.writeJSON(w, chosen.info)
}

// selectRepo chooses one member for an entire revision. File reads use the
// same choice, so a virtual cannot combine files from different models.
func (h handler) selectRepo(
	r *http.Request,
	id format.Identity,
	rev string,
	seen map[string]bool,
) (selected, error) {
	c := pkgroute.RequestFrom(r).Repo
	if seen[c.Key] || len(seen) >= 8 {
		return selected{}, badIdentity()
	}
	seen[c.Key] = true
	defer delete(seen, c.Key)
	if c.Class == repo.Virtual {
		for _, key := range c.Repositories {
			member, err := h.k.DB().Repos().Get(r.Context(), key)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return selected{}, fmt.Errorf("huggingface: virtual member: %w", err)
			}
			if member.PackageType != repo.HuggingFaceML ||
				member.Class == repo.Virtual && seen[key] {
				return selected{}, badIdentity()
			}
			mr, err := h.k.MemberRequest(r, member)
			if err != nil {
				return selected{}, fmt.Errorf("huggingface: member request: %w", err)
			}
			chosen, err := h.selectRepo(mr, id, rev, seen)
			if errors.Is(err, store.ErrNotFound) || isNotFound(err) {
				continue
			}
			if err != nil {
				return selected{}, err
			}
			return chosen, nil
		}
		return selected{}, missing()
	}
	if !h.k.CanFolder(r, id.Prefix(), types.ActionRead) {
		return selected{}, missing()
	}
	if c.Class == repo.Remote {
		return h.remoteInfo(r, id, rev)
	}
	return h.localInfo(r, id, rev)
}

func isNotFound(err error) bool {
	var api *apierror.Error
	return errors.As(err, &api) && api.Status == http.StatusNotFound
}

func (h handler) localInfo(r *http.Request, id format.Identity, rev string) (selected, error) {
	all, err := h.snapshotItems(r, id, "")
	if err != nil {
		return selected{}, err
	}
	if !format.Revision(rev) {
		refPath, _ := format.RefPath(id, rev)
		marker, getErr := h.k.DB().Items().Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key,
			false, refPath)
		switch {
		case getErr == nil && !marker.Folder:
			body, _, openErr := h.k.Blobs().Open(r.Context(), marker.Actual.SHA256)
			if openErr != nil {
				return selected{}, fmt.Errorf("huggingface: open revision ref: %w", openErr)
			}
			data, readErr := io.ReadAll(io.LimitReader(body, 41))
			_ = body.Close()
			if readErr != nil || !format.Revision(string(data)) {
				return selected{}, source.ErrInvalidResult
			}
			rev = string(data)
		case errors.Is(getErr, store.ErrNotFound) && rev == "main":
			// An older snapshot can predate ref records.
		case errors.Is(getErr, store.ErrNotFound):
			return selected{}, missing()
		case getErr == nil:
			return selected{}, source.ErrInvalidResult
		default:
			return selected{}, fmt.Errorf("huggingface: read revision ref: %w", getErr)
		}
	}
	if rev == "main" {
		var latest time.Time
		for _, it := range all {
			_, candidate, _, ok := format.ParseSnapshotPath(it.FullPath())
			if ok && (rev == "main" || it.Created.After(latest)) {
				rev, latest = candidate, it.Created
			}
		}
	}
	if !format.Revision(rev) {
		return selected{}, missing()
	}
	files := make([]sibling, 0)
	for _, it := range all {
		_, at, file, ok := format.ParseSnapshotPath(it.FullPath())
		if !ok || at != rev || !h.k.Can(r, it.FullPath(), types.ActionRead) {
			continue
		}
		s := sibling{Filename: file, Size: it.Size}
		if it.Size > maxRegularFile {
			pointer := fmt.Sprintf(
				"version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n",
				it.Actual.SHA256,
				it.Size,
			)
			s.LFS = &lfsInfo{
				SHA256:      it.Actual.SHA256,
				Size:        it.Size,
				PointerSize: int64(len(pointer)),
			}
			s.BlobID = gitBlobID([]byte(pointer))
		} else {
			body, _, err := h.k.Blobs().Open(r.Context(), it.Actual.SHA256)
			if err != nil {
				return selected{}, fmt.Errorf("huggingface: read git blob: %w", err)
			}
			data, err := io.ReadAll(io.LimitReader(body, maxRegularFile+1))
			_ = body.Close()
			if err != nil || int64(len(data)) != it.Size {
				return selected{}, source.ErrInvalidResult
			}
			s.BlobID = gitBlobID(data)
		}
		files = append(files, s)
	}
	if len(files) == 0 {
		return selected{}, missing()
	}
	slices.SortFunc(
		files,
		func(a, b sibling) int { return strings.Compare(a.Filename, b.Filename) },
	)
	return selected{request: r, info: repoInfo{ID: id.RepoID(), SHA: rev, Siblings: files}}, nil
}

func gitBlobID(data []byte) string {
	h := sha1.New() //nolint:gosec // Git blob identity is a SHA-1 protocol identifier.
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(data))
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func (h handler) snapshotItems(
	r *http.Request,
	id format.Identity,
	rev string,
) ([]types.Item, error) {
	prefix := id.Prefix() + "/snapshots"
	if rev != "" {
		prefix += "/" + rev
	}
	page := store.Page{Limit: store.MaxLimit}
	var all []types.Item
	for {
		items, next, err := h.k.DB().
			Items().
			Descendants(r.Context(), pkgroute.RequestFrom(r).Repo.Key,
				false, prefix, page)
		if err != nil {
			return nil, fmt.Errorf("huggingface: list snapshot: %w", err)
		}
		for _, it := range items {
			if !it.Folder {
				all = append(all, it)
			}
		}
		if next == "" {
			break
		}
		page.After = next
	}
	return all, nil
}

func (h handler) remoteInfo(r *http.Request, id format.Identity, rev string) (selected, error) {
	c := pkgroute.RequestFrom(r).Repo
	base, err := url.Parse(c.URL)
	if err != nil || base.Scheme == "" || base.Host == "" || base.RawQuery != "" {
		return selected{}, badIdentity()
	}
	rel := "api/" + id.Kind + "/" + id.Owner + "/" + id.Name + "/revision/" + rev
	base.Path = strings.TrimRight(base.Path, "/") + "/" + rel
	base.RawQuery = "blobs=true"
	address := base.String()
	check := func(_ context.Context, next *url.URL) error {
		if next.String() != address {
			return source.ErrDenied
		}
		return nil
	}
	resource := source.Resource{
		Kind:                source.Metadata,
		Path:                id.Prefix() + "/api/" + rev + ".json",
		Method:              http.MethodGet,
		ExternalURL:         address,
		CheckURL:            check,
		PermissionFolder:    true,
		PermissionPath:      id.Prefix(),
		Variant:             "huggingfaceml-repo-info-v1",
		ExpectedContentType: "application/json",
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			_, err := decodeInfo(body, size, id)
			return err
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return selected{}, fmt.Errorf("huggingface: remote info: %w", err)
	}
	data, err := h.resultBytes(r.Context(), result)
	if err != nil {
		return selected{}, err
	}
	info, err := decodeInfo(bytes.NewReader(data), int64(len(data)), id)
	if err != nil {
		return selected{}, err
	}
	filtered := make([]sibling, 0, len(info.Siblings))
	for _, file := range info.Siblings {
		path, ok := format.SnapshotPath(id, info.SHA, file.Filename)
		if !ok || !h.k.Can(r, path, types.ActionRead) {
			continue
		}
		if result.CheckRead != nil && result.CheckRead(r.Context(), path) != nil {
			continue
		}
		filtered = append(filtered, file)
	}
	if len(filtered) == 0 {
		return selected{}, missing()
	}
	info.Siblings = filtered
	return selected{request: r, info: info, fence: result.Revalidate, check: result.CheckRead}, nil
}

func decodeInfo(body io.Reader, size int64, id format.Identity) (repoInfo, error) {
	if size <= 0 || size > maxInfo {
		return repoInfo{}, source.ErrInvalidResult
	}
	b, err := io.ReadAll(io.LimitReader(body, maxInfo+1))
	if err != nil || int64(len(b)) != size {
		return repoInfo{}, source.ErrInvalidResult
	}
	var info repoInfo
	if json.Unmarshal(b, &info) != nil || info.ID != id.RepoID() || !format.Revision(info.SHA) ||
		len(info.Siblings) == 0 || len(info.Siblings) > 10000 {
		return repoInfo{}, source.ErrInvalidResult
	}
	seen := map[string]bool{}
	for _, s := range info.Siblings {
		if !format.FilePath(s.Filename) || seen[s.Filename] || s.Size < 0 || s.Size > 1<<42 ||
			s.LFS == nil && s.BlobID == "" ||
			s.LFS != nil && (!formatSHA256(s.LFS.SHA256) || s.LFS.Size != s.Size) {
			return repoInfo{}, source.ErrInvalidResult
		}
		if s.BlobID != "" && (len(s.BlobID) != 40 || !hex40(s.BlobID)) {
			return repoInfo{}, source.ErrInvalidResult
		}
		seen[s.Filename] = true
	}
	return info, nil
}

func hex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

func (h handler) resultBytes(ctx context.Context, result source.Result) ([]byte, error) {
	if result.Stream != nil {
		defer result.Stream.Body.Close()
		b, err := io.ReadAll(io.LimitReader(result.Stream.Body, maxInfo+1))
		if err != nil {
			return nil, fmt.Errorf("huggingface: read upstream metadata: %w", err)
		}
		return b, nil
	}
	if result.Item == nil {
		return nil, source.ErrInvalidResult
	}
	rc, _, err := h.k.Blobs().Open(ctx, result.Item.Actual.SHA256)
	if err != nil {
		return nil, fmt.Errorf("huggingface: cached info: %w", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxInfo+1))
	if err != nil {
		return nil, fmt.Errorf("huggingface: read cached metadata: %w", err)
	}
	return b, nil
}

//nolint:gocyclo // One resolution path validates identity, member selection, destination and bytes.
func (h handler) resolve(w http.ResponseWriter, r *http.Request) {
	id, ok := identity(r)
	rev := chi.URLParam(r, "revision")
	file, err := pkgroute.ParsePath(chi.URLParam(r, "*"))
	if !ok || !selector(rev) || err != nil || !format.FilePath(file.String()) ||
		(r.URL.RawQuery != "" && r.URL.RawQuery != "download=true") {
		h.k.Error(w, r, badIdentity())
		return
	}
	chosen, err := h.selectRepo(r, id, rev, map[string]bool{})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	var target *sibling
	for i := range chosen.info.Siblings {
		if chosen.info.Siblings[i].Filename == file.String() {
			target = &chosen.info.Siblings[i]
			break
		}
	}
	if target == nil {
		h.k.Error(w, r, missing())
		return
	}
	path, _ := format.SnapshotPath(id, chosen.info.SHA, file.String())
	w.Header().Set("X-Repo-Commit", chosen.info.SHA)
	if target.LFS != nil {
		w.Header().Set("ETag", `"`+target.LFS.SHA256+`"`)
	} else if target.BlobID != "" {
		w.Header().Set("ETag", `"`+target.BlobID+`"`)
	}
	if pkgroute.RequestFrom(chosen.request).Repo.Class != repo.Remote {
		h.k.ServeItem(w, chosen.request, path)
		return
	}
	upstream := id.Owner + "/" + id.Name + "/resolve/" + rev + "/" +
		(&url.URL{Path: file.String()}).EscapedPath()
	if id.Kind == "datasets" {
		upstream = "datasets/" + upstream
	}
	base, err := url.Parse(pkgroute.RequestFrom(chosen.request).Repo.URL)
	if err != nil || base.Scheme == "" || base.Host == "" || base.RawQuery != "" {
		h.k.Error(w, r, badIdentity())
		return
	}
	address := strings.TrimRight(base.String(), "/") + "/" + upstream
	check := func(_ context.Context, next *url.URL) error {
		if next.User != nil || next.Fragment != "" {
			return source.ErrDenied
		}
		if next.Scheme == base.Scheme && next.Host == base.Host {
			return nil
		}
		if base.Scheme == "https" && base.Hostname() == "huggingface.co" &&
			next.Scheme == "https" && next.Port() == "" && huggingFaceCDN(next.Hostname()) {
			return nil
		}
		return source.ErrDenied
	}
	resource := source.Resource{
		Kind: source.Artifact, Path: path, ExternalURL: address, CheckURL: check,
		Method: http.MethodGet, Range: r.Header.Get("Range"), IfRange: r.Header.Get("If-Range"),
		Variant: "huggingfaceml-asset-v2",
	}
	if target.LFS != nil {
		resource.Checksums.SHA256 = target.LFS.SHA256
	}
	resource.Validate = func(_ context.Context, body io.ReadSeeker, size int64) error {
		if size != target.Size {
			return source.ErrInvalidResult
		}
		if target.LFS == nil {
			hash := sha1.New() //nolint:gosec // verify the Git wire blob identifier.
			_, _ = fmt.Fprintf(hash, "blob %d\x00", size)
			if _, err := io.Copy(
				hash,
				body,
			); err != nil ||
				hex.EncodeToString(hash.Sum(nil)) != target.BlobID {
				return source.ErrInvalidResult
			}
		}
		return nil
	}
	result, err := h.k.Read(chosen.request, resource)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if chosen.fence != nil {
		original := result.Revalidate
		result.Revalidate = func(ctx context.Context) error {
			if err := chosen.fence(ctx); err != nil {
				return err
			}
			if chosen.check != nil {
				if err := chosen.check(ctx, path); err != nil {
					return err
				}
			}
			if original != nil {
				return original(ctx)
			}
			return nil
		}
	}
	h.k.ServeResult(w, r, result)
}

// Hugging Face's current model/dataset download guide names these HTTPS
// storage hosts. The source transport still applies DNS and credential-origin
// checks. A custom upstream only permits its own origin.
func huggingFaceCDN(host string) bool {
	switch host {
	case "cas-server.xethub.hf.co", "cas-server.xethub-eu.hf.co",
		"transfer.xethub.hf.co", "transfer.xethub-eu.hf.co",
		"us.aws.cdn.hf.co", "us.gcp.cdn.hf.co",
		"cdn-lfs-us-1.hf.co", "cdn-lfs-eu-1.hf.co":
		return true
	}
	return false
}

func (h handler) tree(w http.ResponseWriter, r *http.Request) {
	id, ok := identity(r)
	rev := chi.URLParam(r, "revision")
	prefix := chi.URLParam(r, "*")
	if prefix != "" {
		parsed, err := pkgroute.ParsePath(prefix)
		if err != nil || !format.FilePath(parsed.String()) {
			h.k.Error(w, r, badIdentity())
			return
		}
		prefix = parsed.String()
	}
	q := r.URL.Query()
	if !ok || !selector(rev) || len(q) > 2 {
		h.k.Error(w, r, badIdentity())
		return
	}
	for key, values := range q {
		if key != "recursive" && key != "expand" || len(values) != 1 ||
			!strings.EqualFold(values[0], "true") && !strings.EqualFold(values[0], "false") {
			h.k.Error(w, r, badIdentity())
			return
		}
	}
	chosen, err := h.selectRepo(r, id, rev, map[string]bool{})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if chosen.fence != nil {
		if err := chosen.fence(r.Context()); err != nil {
			h.k.Error(w, r, err)
			return
		}
	}
	files := make([]map[string]any, 0, len(chosen.info.Siblings))
	seenFolders := map[string]bool{}
	recursive := strings.EqualFold(q.Get("recursive"), "true")
	for _, s := range chosen.info.Siblings {
		path := s.Filename
		if prefix != "" {
			if !strings.HasPrefix(path, prefix+"/") {
				continue
			}
			path = strings.TrimPrefix(path, prefix+"/")
		}
		if !recursive {
			folder, _, nested := strings.Cut(path, "/")
			if nested {
				full := folder
				if prefix != "" {
					full = prefix + "/" + folder
				}
				if !seenFolders[full] {
					files = append(files, map[string]any{"type": "directory", "path": full})
					seenFolders[full] = true
				}
				continue
			}
		}
		entry := map[string]any{"type": "file", "path": s.Filename, "size": s.Size, "oid": s.BlobID}
		if s.LFS != nil {
			entry["lfs"] = map[string]any{
				"oid": s.LFS.SHA256, "size": s.LFS.Size, "pointerSize": s.LFS.PointerSize,
			}
		}
		files = append(files, entry)
	}
	h.writeJSON(w, files)
}

func (h handler) writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(w).Encode(data)
}
