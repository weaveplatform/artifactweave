package itemkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// pomLookupRequest fetches the complete owner before a HEAD, range,
// conditional or sidecar can describe its transformed representation.
func pomLookupRequest(ctx context.Context, r *http.Request, v repo.Config, req ItemRequest) (*http.Request, ItemRequest) {
	path := req.Path
	if owner, _, ok := item.Sidecar(path); ok {
		path = owner
	}
	if _, ok := maven.POMCleanupPolicy(v, path); !ok {
		return r, req
	}
	r = withoutHeaders(r, "Range", "If-Range", "If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since")
	r = r.Clone(ctx)
	r.Method = http.MethodGet
	req.Path = path
	return r, req
}

// pomBeforeReturn runs after member permissions and download policy. A
// transformed file is a virtual-owned resource, but retains the remote
// source's delivery fence and successful-use callback.
func (s *Kit) pomBeforeReturn(r *http.Request, st Store, v repo.Config, path string, f Found) (Found, error) {
	policy, ok := maven.POMCleanupPolicy(v, path)
	if !ok {
		return f, nil
	}
	if f.remote != nil && f.remote.err != nil {
		return f, nil
	}
	original := source.Result{Used: f.Used}
	var it model.Item
	if f.remote != nil {
		original = f.remote.result
		if original.Stream != nil {
			defer original.Stream.Body.Close()
		}
		if original.Revalidate != nil {
			if err := original.Revalidate(r.Context()); err != nil {
				return Found{}, s.pomError("Failed to transform pom file", err)
			}
		}
		if original.Item != nil {
			it = *original.Item
		} else {
			it = model.Item{Actual: original.Stream.Checksums, Modified: original.Stream.Modified}
		}
	} else {
		it = f.Item
	}
	if f.Repo.Class != repo.Remote {
		cached, err := st.VirtualCopies().Get(r.Context(), v.Key, path)
		switch {
		case err == nil && cached.Member == f.Repo.Key && cached.SourceSHA256 == it.Actual.SHA256 && cached.Policy == policy && !cached.Item.Modified.Before(it.Modified):
			original = s.pomCopyUsage(r, st, f, it, original)
			return pomCopyFound(v, cached.Item, original), nil
		case err != nil && !errors.Is(err, kernel.ErrNotFound):
			return Found{}, s.pomError("Failed to transform pom file", err)
		}
	}
	data, err := s.pomBytes(r.Context(), it, original)
	if err != nil {
		return Found{}, s.pomError("Failed to transform pom file", err)
	}
	cleaned, changed := maven.CleanPOM(data, policy)
	if !changed {
		if original.Stream != nil {
			// The source stream was consumed for inspection. Keep its validators
			// and authority hooks while replacing only its reader.
			stream := *original.Stream
			stream.Body = io.NopCloser(bytes.NewReader(data))
			original.Stream = &stream
			read := *f.remote
			read.result = original
			f.remote = &read
		}
		return f, nil
	}
	original = s.pomCopyUsage(r, st, f, it, original)
	copy, err := s.savePOMCopy(r.Context(), st, v, path, f.Repo.Key, it.Actual.SHA256, policy, cleaned)
	if err != nil {
		if kernel.StorageRefusal(err) {
			// Optional retention has rolled back. The transformed bytes are
			// already owned by this request, so serve them without a CAS
			// reference while retaining the source's final delivery fence.
			return pomTransientFound(v, cleaned, it.Modified, original), nil
		}
		return Found{}, s.pomError("Failed to import file to local storage", err)
	}
	return pomCopyFound(v, copy, original), nil
}

type pomTransientBody struct{ *bytes.Reader }

func (pomTransientBody) Close() error { return nil }

func pomTransientFound(v repo.Config, data []byte, modified time.Time, original source.Result) Found {
	// There is no retained copy creation time to reuse. Keep the source's
	// modification time stable across reads rather than inventing a new date
	// on each response and invalidating If-Modified-Since/If-Range.
	hasher := blob.NewHasher()
	_, _ = hasher.Write(data)
	digests := hasher.Sum()
	sums := kernel.Checksums{SHA256: digests.SHA256, SHA512: digests.SHA512, SHA1: digests.SHA1, MD5: digests.MD5}
	return Found{Repo: v, remote: &remoteRead{result: source.Result{
		Stream: &source.Stream{
			Status: http.StatusOK, Body: pomTransientBody{bytes.NewReader(data)},
			Representation: source.Representation{Size: int64(len(data)), MediaType: "application/xml", Modified: modified, Checksums: sums, OriginalChecksums: sums},
		},
		Revalidate: original.Revalidate, Used: original.Used,
	}}}
}

func pomCopyFound(v repo.Config, it model.Item, original source.Result) Found {
	return Found{Repo: v, Item: it, remote: &remoteRead{result: source.Result{Item: &it, Revalidate: original.Revalidate, Used: original.Used}}}
}

func (s *Kit) pomBytes(ctx context.Context, it model.Item, result source.Result) ([]byte, error) {
	var reader io.ReadCloser
	if result.Stream != nil {
		reader = result.Stream.Body
	} else {
		body, _, err := s.Blobs.Open(ctx, it.Actual.SHA256)
		if err != nil {
			return nil, fmt.Errorf("open POM: %w", err)
		}
		reader = body
		defer reader.Close()
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read POM: %w", err)
	}
	return data, nil
}

func (s *Kit) savePOMCopy(ctx context.Context, st Store, v repo.Config, path, member, sha, policy string, data []byte) (model.Item, error) {
	upload, err := s.Blobs.BeginUpload(ctx)
	if err != nil {
		return model.Item{}, fmt.Errorf("begin POM copy: %w", err)
	}
	defer func() { _ = upload.Abort(ctx) }()
	if _, err = upload.Append(ctx, bytes.NewReader(data)); err != nil {
		return model.Item{}, fmt.Errorf("write POM copy: %w", err)
	}
	info, err := upload.Commit(ctx, blob.Digests{})
	if err != nil {
		return model.Item{}, fmt.Errorf("commit POM copy: %w", err)
	}
	folder, name, depth, err := kernel.SplitItemPath(path)
	if err != nil {
		return model.Item{}, fmt.Errorf("POM path: %w", err)
	}
	now := time.Now().UTC()
	sums := kernel.Checksums{SHA256: info.Digests.SHA256, SHA512: info.Digests.SHA512, SHA1: info.Digests.SHA1, MD5: info.Digests.MD5}
	it := model.Item{
		RepoKey:  v.Key,
		Path:     folder,
		Name:     name,
		Depth:    depth,
		Size:     info.Size,
		Actual:   sums,
		Original: sums,
		MimeType: "application/xml",
		Created:  now,
		Modified: now,
		Updated:  now,
	}
	if err = st.VirtualCopies().Put(ctx, virtual.Copy{Virtual: v.Key, Path: path, Member: member, SourceSHA256: sha, Policy: policy, Item: it}); err != nil {
		return model.Item{}, fmt.Errorf("store POM copy: %w", err)
	}
	return it, nil
}

func (s *Kit) pomError(prefix string, err error) error {
	status := http.StatusNotFound
	var answer *apierror.Error
	var rejection *format.RejectionError
	switch {
	case errors.As(err, &answer):
		status = answer.Status
	case errors.As(err, &rejection):
		status = rejection.Status
	case errors.Is(err, kernel.ErrUnauthenticated):
		status = http.StatusUnauthorized
	case errors.Is(err, source.ErrDenied), errors.Is(err, kernel.ErrForbidden):
		status = http.StatusForbidden
	}
	return apierror.New(status, prefix+": "+strings.TrimSpace(err.Error()))
}

func (s *Kit) pomCopyUsage(r *http.Request, st Store, f Found, it model.Item, result source.Result) source.Result {
	used := result.Used
	p, _ := kernel.FromContext(r.Context())
	result.Used = func(ctx context.Context) error {
		if used != nil {
			if err := used(ctx); err != nil {
				return fmt.Errorf("record POM source use: %w", err)
			}
		}
		if it.ID != 0 {
			recordDownload(ctx, st, Found{Repo: f.Repo, Item: it}, p)
		}
		return nil
	}
	return result
}

func (s *Kit) serveReturnedPOM(w http.ResponseWriter, r *http.Request, st Store, v repo.Config, req ItemRequest, f Found, p kernel.Principal) {
	f, err := s.pomBeforeReturn(r, st, v, req.Path, f)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if f.remote != nil {
		s.serveRemoteRead(w, r, f.Repo, v, foundRequest(req, f), p, *f.remote)
		return
	}
	s.ServeItemFile(w, r, st, f, p)
}
