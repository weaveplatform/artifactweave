package gitlfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	baseformat "github.com/weaveplatform/artifactweave/internal/format"
	format "github.com/weaveplatform/artifactweave/internal/format/gitlfs"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxBatchResponse = 64 << 10

func objectResource(obj batchObject) source.Resource {
	path, _ := format.ObjectPath(obj.OID)
	return source.Resource{
		Kind: source.Artifact, Path: path, Method: http.MethodGet,
		Variant: "gitlfs-object-v1", Checksums: types.Checksums{SHA256: obj.OID},
		Validate: func(_ context.Context, body io.ReadSeeker, size int64) error {
			if size != obj.Size {
				return source.ErrInvalidResult
			}
			hash := sha256.New()
			if _, err := io.Copy(hash, io.LimitReader(body, maxObjectBytes+1)); err != nil ||
				hex.EncodeToString(hash.Sum(nil)) != obj.OID {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
}

func (h handler) readSource(
	r *http.Request,
	resource source.Resource,
	cacheOnly bool,
) (source.Result, error) {
	if h.o.RemoteSources == nil {
		return source.Result{}, source.ErrUnsupported
	}
	c := pkgroute.RequestFrom(r).Repo
	p, ok := auth.FromContext(r.Context())
	if !ok {
		return source.Result{}, source.ErrDenied
	}
	result, err := h.o.RemoteSources.Read(r.Context(), source.Request{
		Repo: c, Subject: p.Subject(), Access: p.Access(), Resource: resource,
		CacheOnly: cacheOnly, Project: c.ProjectKey,
	})
	if err != nil {
		return source.Result{}, fmt.Errorf(
			"gitlfs: remote source: %w",
			pkgroute.SourceError(err, p.Anonymous || p.Name == ""),
		)
	}
	return result, nil
}

func resultIdentity(result source.Result, obj batchObject) error {
	if result.Item != nil {
		if result.Item.Folder || result.Item.Size != obj.Size ||
			result.Item.Actual.SHA256 != obj.OID {
			return source.ErrInvalidResult
		}
		return nil
	}
	if result.Stream == nil || result.Stream.Size != obj.Size ||
		result.Stream.Checksums.SHA256 != obj.OID {
		return source.ErrInvalidResult
	}
	return nil
}

func closeSource(result source.Result) {
	if result.Stream != nil && result.Stream.Body != nil {
		_ = result.Stream.Body.Close()
	}
}

func (h handler) cachedObject(r *http.Request, obj batchObject) (source.Result, error) {
	result, err := h.readSource(r, objectResource(obj), true)
	if err != nil {
		return source.Result{}, err
	}
	if err = resultIdentity(result, obj); err != nil {
		closeSource(result)
		return source.Result{}, err
	}
	return result, nil
}

func (h handler) remoteAvailable(r *http.Request, obj batchObject) error {
	path, _ := format.ObjectPath(obj.OID)
	if !h.k.Can(r, path, types.ActionRead) {
		return apierror.New(http.StatusForbidden, "Git LFS read denied")
	}
	if cached, err := h.cachedObject(r, obj); err == nil {
		closeSource(cached)
		return nil
	} else if !missing(err) {
		return err
	}
	_, err := h.remoteBatch(r, obj)
	return err
}

func (h handler) remoteObject(r *http.Request, obj batchObject) (source.Result, error) {
	path, _ := format.ObjectPath(obj.OID)
	if !h.k.Can(r, path, types.ActionRead) {
		return source.Result{}, apierror.New(http.StatusForbidden, "Git LFS read denied")
	}
	if cached, err := h.cachedObject(r, obj); err == nil {
		return cached, nil
	} else if !missing(err) {
		return source.Result{}, err
	}
	href, err := h.remoteBatch(r, obj)
	if err != nil {
		return source.Result{}, err
	}
	resource := objectResource(obj)
	resource.ExternalURL = href
	resource.NoCrossOriginAuth = true
	resource.CheckURL = actionURLGuard(href)
	result, err := h.readSource(r, resource, false)
	if err != nil {
		return source.Result{}, err
	}
	if err = resultIdentity(result, obj); err != nil {
		closeSource(result)
		return source.Result{}, err
	}
	return result, nil
}

func missing(err error) bool {
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	var api *apierror.Error
	if errors.As(err, &api) && api.Status == http.StatusNotFound {
		return true
	}
	var rejected *baseformat.Rejection
	return errors.As(err, &rejected) && rejected.Status == http.StatusNotFound
}

func (h handler) remoteBatch(r *http.Request, obj batchObject) (string, error) {
	path, _ := format.ObjectPath(obj.OID)
	body, _ := json.Marshal(batchRequest{
		Operation: "download", Transfers: []string{"basic"}, Objects: []batchObject{obj},
		HashAlgo: "sha256",
	})
	resource := source.Resource{
		Kind:                source.Metadata,
		Path:                path,
		UpstreamPath:        "objects/batch",
		Method:              http.MethodPost,
		PostBody:            body,
		Transient:           true,
		Variant:             "gitlfs-batch-v1",
		Accept:              mediaType,
		ExpectedContentType: mediaType,
		ValidateMetadata: func(_ context.Context, content io.ReadSeeker, size int64, response source.MetadataResponse) error {
			_, _, err := parseRemoteBatch(content, size, response.BaseURL, obj)
			return err
		},
	}
	result, err := h.readSource(r, resource, false)
	if err != nil {
		return "", err
	}
	if result.Stream == nil {
		return "", source.ErrInvalidResult
	}
	defer closeSource(result)
	href, missing, err := parseRemoteBatch(
		result.Stream.Body,
		result.Stream.Size,
		result.Metadata.BaseURL,
		obj,
	)
	if err != nil {
		return "", err
	}
	if missing {
		return "", store.ErrNotFound
	}
	return href, nil
}

type upstreamBatch struct {
	Transfer string `json:"transfer"`
	Objects  []struct {
		OID     string `json:"oid"`
		Size    int64  `json:"size"`
		Actions map[string]struct {
			Href   string            `json:"href"`
			Header map[string]string `json:"header,omitempty"`
		} `json:"actions,omitempty"`
		Error *objectError `json:"error,omitempty"`
	} `json:"objects"`
}

func parseRemoteBatch(
	body io.Reader,
	size int64,
	base string,
	obj batchObject,
) (string, bool, error) {
	if size <= 0 || size > maxBatchResponse {
		return "", false, source.ErrInvalidResult
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBatchResponse+1))
	if err != nil || int64(len(data)) != size {
		return "", false, source.ErrInvalidResult
	}
	var response upstreamBatch
	if json.Unmarshal(data, &response) != nil ||
		response.Transfer != "" && response.Transfer != "basic" ||
		len(response.Objects) != 1 || response.Objects[0].OID != obj.OID ||
		response.Objects[0].Size != obj.Size {
		return "", false, source.ErrInvalidResult
	}
	entry := response.Objects[0]
	if entry.Error != nil {
		if entry.Error.Code == http.StatusNotFound || entry.Error.Code == http.StatusGone {
			return "", true, nil
		}
		return "", false, source.ErrInvalidResult
	}
	if len(entry.Actions) != 1 || entry.Actions["download"].Href == "" ||
		len(entry.Actions["download"].Header) != 0 {
		return "", false, source.ErrInvalidResult
	}
	href := entry.Actions["download"].Href
	if !validActionURL(base, href, obj.OID) {
		return "", false, source.ErrInvalidResult
	}
	return href, false, nil
}

func validActionURL(base, href, oid string) bool {
	parent, err := url.Parse(base)
	if err != nil || parent == nil || parent.Host == "" {
		return false
	}
	u, err := url.Parse(href)
	if err != nil || u == nil || u.User != nil || u.Fragment != "" ||
		u.Scheme != parent.Scheme || !strings.EqualFold(u.Host, parent.Host) ||
		(u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	apiRoot := strings.TrimSuffix(parent.Path, "/objects/batch")
	if apiRoot == parent.Path || apiRoot == "" {
		return false
	}
	path, _ := format.ObjectPath(oid)
	browseRoot := apiRoot
	if before, repoKey, ok := strings.Cut(
		apiRoot,
		"/api/lfs/",
	); ok && repoKey != "" &&
		!strings.Contains(repoKey, "/") {
		browseRoot = before + "/" + repoKey
	}
	return u.EscapedPath() == apiRoot+"/objects/"+oid ||
		u.EscapedPath() == apiRoot+"/"+path ||
		u.EscapedPath() == browseRoot+"/"+path
}

func actionURLGuard(href string) func(context.Context, *url.URL) error {
	target, _ := url.Parse(href)
	return func(_ context.Context, next *url.URL) error {
		if next == nil || next.User != nil || next.Fragment != "" ||
			(next.Scheme != "http" && next.Scheme != "https") {
			return source.ErrDenied
		}
		if next.String() == target.String() {
			return nil
		}
		if next.Scheme == target.Scheme && strings.EqualFold(next.Host, target.Host) {
			return nil
		}
		// Cross-origin redirects must use HTTPS. The shared remote transport
		// strips credentials, checks DNS, and enforces redirect policy; the
		// pinned SHA-256 prevents substituted bytes.
		if next.Scheme == "https" {
			return nil
		}
		return source.ErrDenied
	}
}
