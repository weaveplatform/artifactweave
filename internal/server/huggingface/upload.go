package huggingface

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // the Hub revision is a Git-compatible protocol identifier.
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	maxUploadFiles = 256
	maxCommitBody  = 64 << 20
	maxRegularFile = 1 << 20
)

type preuploadFile struct {
	Path   string `json:"path"`
	Sample string `json:"sample"`
	Size   int64  `json:"size"`
}

type preuploadRequest struct {
	Files     []preuploadFile `json:"files"`
	GitIgnore string          `json:"gitIgnore,omitempty"` //nolint:tagliatelle // Hub preupload wire key.
}

func (h handler) targetRequest(r *http.Request, id format.Identity) (*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	if !h.k.CanFolder(r, id.Prefix(), types.ActionWrite) {
		return nil, apierror.New(http.StatusForbidden, "model deployment is not authorized")
	}
	if c.Class == repo.Remote {
		return nil, apierror.New(
			http.StatusMethodNotAllowed,
			"cannot deploy to a remote model repository",
		)
	}
	if c.Class != repo.Virtual {
		return r, nil
	}
	if c.DefaultDeploymentRepo == "" || !slices.Contains(c.Repositories, c.DefaultDeploymentRepo) {
		return nil, apierror.New(
			http.StatusMethodNotAllowed,
			"virtual model repository has no deployment target",
		)
	}
	member, err := h.k.DB().Repos().Get(r.Context(), c.DefaultDeploymentRepo)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apierror.New(http.StatusNotFound, "model deployment target not found")
		}
		return nil, fmt.Errorf("huggingface: deployment member: %w", err)
	}
	if member.Class != repo.Local && member.Class != repo.Federated ||
		member.PackageType != repo.HuggingFaceML {
		return nil, apierror.New(http.StatusBadRequest, "invalid virtual model deployment target")
	}
	mr, err := h.k.MemberRequest(r, member)
	if err != nil {
		return nil, fmt.Errorf("huggingface: member request: %w", err)
	}
	if !h.k.CanFolder(mr, id.Prefix(), types.ActionWrite) {
		return nil, apierror.New(http.StatusForbidden, "model deployment target is not authorized")
	}
	return mr, nil
}

func (h handler) preupload(w http.ResponseWriter, r *http.Request) {
	id, ok := identity(r)
	rev := chi.URLParam(r, "revision")
	if !ok || !format.RefName(rev) || r.URL.RawQuery != "" {
		h.k.Error(w, r, badIdentity())
		return
	}
	target, err := h.targetRequest(r, id)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	var request preuploadRequest
	if err := boundedJSON(r, 1<<20, &request); err != nil || len(request.Files) == 0 ||
		len(request.Files) > maxUploadFiles {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid preupload request"))
		return
	}
	seen := map[string]bool{}
	response := make([]map[string]any, 0, len(request.Files))
	for _, f := range request.Files {
		pathRev := rev
		if !format.Revision(pathRev) {
			pathRev = strings.Repeat("0", 40)
		}
		path, valid := format.SnapshotPath(id, pathRev, f.Path)
		decoded, err := base64.StdEncoding.DecodeString(f.Sample)
		if !valid || seen[f.Path] || f.Size < 0 || f.Size > 10<<30 || err != nil ||
			len(decoded) > 512 || !h.k.Can(target, path, types.ActionWrite) {
			h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid preupload file"))
			return
		}
		seen[f.Path] = true
		mode := "regular"
		if f.Size > maxRegularFile {
			mode = "lfs"
		}
		response = append(response, map[string]any{
			"path": f.Path, "uploadMode": mode, "shouldIgnore": false,
		})
	}
	h.writeJSON(w, map[string]any{"files": response})
}

type commitLine struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type commitFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	Algo     string `json:"algo"`
	OID      string `json:"oid"`
	Size     int64  `json:"size"`
}

type commitOp struct {
	path string
	data []byte
	oid  string
	size int64
}

//nolint:gocyclo // The commit is one atomic validation and immutable publication path.
func (h handler) commit(w http.ResponseWriter, r *http.Request) {
	id, ok := identity(r)
	rev := chi.URLParam(r, "revision")
	if !ok || !format.RefName(rev) || r.URL.RawQuery != "" {
		h.k.Error(w, r, badIdentity())
		return
	}
	target, err := h.targetRequest(r, id)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	media := r.Header.Get("Content-Type")
	if !strings.HasPrefix(media, "application/x-ndjson") || r.ContentLength > maxCommitBody {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Hugging Face commit media"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCommitBody+1))
	if err != nil || len(body) == 0 || len(body) > maxCommitBody {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusRequestEntityTooLarge, "Hugging Face commit too large"),
		)
		return
	}
	storedRev := rev
	if !format.Revision(storedRev) {
		hash := sha1.New() //nolint:gosec // 40-hex Git-compatible revision identifier.
		_, _ = io.WriteString(hash, id.Kind+"/"+id.RepoID()+"\n")
		_, _ = hash.Write(body)
		storedRev = fmt.Sprintf("%x", hash.Sum(nil))
	}
	ops, err := parseCommit(body, id, storedRev)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, op := range ops {
		if !h.k.Can(target, op.path, types.ActionWrite) {
			h.k.Error(w, r, apierror.New(http.StatusForbidden, "model file is not writable"))
			return
		}
	}
	refPath := ""
	if !format.Revision(rev) {
		refPath, _ = format.RefPath(id, rev)
		if !h.k.Can(target, refPath, types.ActionWrite) {
			h.k.Error(w, r, apierror.New(http.StatusForbidden, "model revision is not writable"))
			return
		}
	}
	// MemberRequest derives target from r; the transaction must retain its
	// selected repository in the request context for virtual deployments.
	//nolint:contextcheck // target.Context inherits r.Context with the selected member.
	err = h.k.DB().Tx(target.Context(), func(ctx context.Context) error {
		target := target.WithContext(ctx)
		for _, op := range ops {
			if op.oid == "" {
				_, err := h.k.StoreOnce(target, op.path, bytes.NewReader(op.data), nil,
					types.Checksums{}, func(types.Item) error {
						return apierror.New(http.StatusConflict, "immutable model revision differs")
					})
				if err != nil {
					return fmt.Errorf("huggingface: store regular file: %w", err)
				}
				continue
			}
			existing, getErr := h.k.DB().Items().Get(ctx,
				pkgroute.RequestFrom(target).Repo.Key, false, op.path)
			switch {
			case getErr == nil && existing.Actual.SHA256 == op.oid && existing.Size == op.size:
				continue
			case getErr == nil:
				return apierror.New(http.StatusConflict, "immutable model revision differs")
			case !errors.Is(getErr, store.ErrNotFound):
				return fmt.Errorf("huggingface: check existing LFS file: %w", getErr)
			}
			info, err := h.k.Blobs().Stat(ctx, op.oid)
			if err != nil || info.Size != op.size || info.Digests.SHA256 != op.oid {
				return apierror.New(http.StatusConflict, "large model file is missing or corrupt")
			}
			_, err = h.k.StoreNew(target, op.path, info, nil,
				types.Checksums{SHA256: op.oid}, func() error {
					it, getErr := h.k.DB().Items().Get(ctx,
						pkgroute.RequestFrom(target).Repo.Key, false, op.path)
					if getErr == nil && it.Actual.SHA256 == op.oid {
						return nil
					}
					return apierror.New(http.StatusConflict, "immutable model revision differs")
				})
			if err != nil {
				return fmt.Errorf("huggingface: store LFS file: %w", err)
			}
		}
		if refPath != "" {
			current, getErr := h.k.DB().Items().Get(ctx,
				pkgroute.RequestFrom(target).Repo.Key, false, refPath)
			refDigest := sha256.Sum256([]byte(storedRev))
			if getErr == nil && current.Actual.SHA256 == fmt.Sprintf("%x", refDigest) {
				return nil
			}
			if getErr != nil && !errors.Is(getErr, store.ErrNotFound) {
				return fmt.Errorf("huggingface: check existing revision ref: %w", getErr)
			}
			if _, err := h.k.Store(target, refPath, bytes.NewReader([]byte(storedRev)), nil,
				types.Checksums{}); err != nil {
				return fmt.Errorf("huggingface: move revision ref: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// Hub 2.x derives the repository URL by removing /commit/{oid} from
	// commitUrl. Return a repository page URL, not the API revision route.
	repoPath := id.RepoID()
	if id.Kind == "datasets" {
		repoPath = "datasets/" + repoPath
	}
	repoURL := baseurl.From(r.Context()) + "/artifactory/api/huggingfaceml/" +
		url.PathEscape(pkgroute.RequestFrom(r).Repo.Key) + "/" + repoPath
	_ = json.NewEncoder(w).Encode(map[string]any{
		"commitOid": storedRev,
		"commitUrl": repoURL + "/commit/" + storedRev,
	})
}

func parseCommit(body []byte, id format.Identity, rev string) ([]commitOp, error) {
	lines := bytes.Split(bytes.TrimSpace(body), []byte{'\n'})
	if len(lines) < 2 || len(lines) > maxUploadFiles+1 {
		return nil, apierror.New(http.StatusBadRequest, "invalid Hugging Face commit")
	}
	var header commitLine
	if json.Unmarshal(lines[0], &header) != nil || header.Key != "header" ||
		len(header.Value) == 0 {
		return nil, apierror.New(http.StatusBadRequest, "missing Hugging Face commit header")
	}
	seen := map[string]bool{}
	ops := make([]commitOp, 0, len(lines)-1)
	for _, line := range lines[1:] {
		var entry commitLine
		var file commitFile
		if json.Unmarshal(line, &entry) != nil ||
			(entry.Key != "file" && entry.Key != "lfsFile") ||
			json.Unmarshal(entry.Value, &file) != nil {
			return nil, apierror.New(http.StatusBadRequest, "invalid model file operation")
		}
		path, ok := format.SnapshotPath(id, rev, file.Path)
		if !ok || seen[path] {
			return nil, apierror.New(http.StatusBadRequest, "invalid model file path")
		}
		seen[path] = true
		op := commitOp{path: path}
		if entry.Key == "file" {
			if file.Encoding != "base64" || len(file.Content) > 2*maxRegularFile {
				return nil, apierror.New(http.StatusBadRequest, "invalid regular model file")
			}
			data, err := base64.StdEncoding.DecodeString(file.Content)
			if err != nil || len(data) > maxRegularFile {
				return nil, apierror.New(http.StatusBadRequest, "invalid regular model bytes")
			}
			op.data = data
		} else {
			if file.Algo != "sha256" || !formatSHA256(file.OID) || file.Size <= maxRegularFile ||
				file.Size > 10<<30 {
				return nil, apierror.New(http.StatusBadRequest, "invalid LFS model reference")
			}
			op.oid, op.size = file.OID, file.Size
		}
		ops = append(ops, op)
	}
	return ops, nil
}

var errInvalidJSON = errors.New("invalid Hugging Face JSON request")

func boundedJSON(r *http.Request, limit int64, dest any) error {
	media := r.Header.Get("Content-Type")
	if r.ContentLength > limit ||
		!strings.HasPrefix(media, "application/json") &&
			!strings.HasPrefix(media, "application/vnd.git-lfs+json") {
		return errInvalidJSON
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return errInvalidJSON
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil || !errors.Is(dec.Decode(new(any)), io.EOF) {
		return errInvalidJSON
	}
	return nil
}

type lfsObject struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type lfsBatchRequest struct {
	Operation string      `json:"operation"`
	Objects   []lfsObject `json:"objects"`
	Transfers []string    `json:"transfers"`
	HashAlgo  string      `json:"hash_algo"`
	Ref       struct {
		Name string `json:"name"`
	} `json:"ref"`
}

func (h handler) lfsBatch(w http.ResponseWriter, r *http.Request) {
	id, ok := lfsIdentity(r)
	if !ok || r.URL.RawQuery != "" {
		h.k.Error(w, r, badIdentity())
		return
	}
	if _, err := h.targetRequest(r, id); err != nil {
		h.k.Error(w, r, err)
		return
	}
	var request lfsBatchRequest
	if boundedJSON(r, 1<<20, &request) != nil || request.Operation != "upload" ||
		len(request.Objects) == 0 || len(request.Objects) > maxUploadFiles || h.o.Keys == nil {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid LFS batch"))
		return
	}
	repoKey := pkgroute.RequestFrom(r).Repo.Key
	exp := h.now().Add(5 * time.Minute).Unix()
	objects := make([]map[string]any, 0, len(request.Objects))
	for _, obj := range request.Objects {
		if !formatSHA256(obj.OID) || obj.Size <= maxRegularFile || obj.Size > 10<<30 {
			h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid LFS object"))
			return
		}
		entry := map[string]any{"oid": obj.OID, "size": obj.Size}
		info, err := h.k.Blobs().Stat(r.Context(), obj.OID)
		if err == nil && info.Size == obj.Size {
			objects = append(objects, entry)
			continue
		}
		if err != nil && !errors.Is(err, blob.ErrNotFound) {
			h.k.Error(w, r, fmt.Errorf("huggingface: check LFS object: %w", err))
			return
		}
		query := url.Values{}
		query.Set("size", strconv.FormatInt(obj.Size, 10))
		query.Set("exp", strconv.FormatInt(exp, 10))
		query.Set("sig", h.signedUpload(repoKey, obj.OID, obj.Size, exp))
		href := baseurl.From(r.Context()) + "/artifactory/api/huggingfaceml/" +
			url.PathEscape(repoKey) + "/lfs/upload/" + obj.OID + "?" + query.Encode()
		entry["actions"] = map[string]any{"upload": map[string]any{"href": href}}
		objects = append(objects, entry)
	}
	h.writeJSON(w, map[string]any{"transfer": "basic", "objects": objects})
}

func lfsIdentity(r *http.Request) (format.Identity, bool) {
	kind := "models"
	if strings.HasPrefix(pkgroute.RequestFrom(r).Rest, "datasets/") {
		kind = "datasets"
	}
	name := strings.TrimSuffix(chi.URLParam(r, "name"), ".git")
	return format.ParseIdentity(kind, chi.URLParam(r, "owner"), name)
}

func (h handler) lfsUpload(w http.ResponseWriter, r *http.Request) {
	oid := chi.URLParam(r, "oid")
	size, ok := h.verifyUpload(r, oid)
	if !ok || r.ContentLength != size {
		h.k.Error(w, r, apierror.New(http.StatusForbidden, "invalid LFS upload authority"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, size+1)
	info, err := h.k.Upload(r, r.Body)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if info.Digests.SHA256 != oid || info.Size != size {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusUnprocessableEntity, "LFS object integrity mismatch"),
		)
		return
	}
	w.WriteHeader(http.StatusOK)
}
