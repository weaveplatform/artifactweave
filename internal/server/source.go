package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// getRemoteItem returns false for existing folder/archive browsing, which remains
// cache-only. Native adapters and virtual member fetching require their own opt-in.
func (s artifactoryService) getRemoteItem(w http.ResponseWriter, r *http.Request, c repo.Config,
	req itemRequest, p auth.Principal, acc access,
) bool {
	ctx := r.Context()
	existing, err := s.cfg.Store.Items().Get(ctx, c.Key, true, req.path)
	if err == nil && existing.Folder {
		return false
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeErr(w, r, s.itemError(ctx, "cached item", err))
		return true
	}
	if errors.Is(err, store.ErrNotFound) && strings.Contains(req.path, "!/") {
		return false
	}
	resource := remoteResource(r, req.path)
	sidecar := ""
	if artifact, alg, ok := item.Sidecar(req.path); ok && errors.Is(err, store.ErrNotFound) {
		// A checksum cannot disclose a file the caller cannot read. Cold SHA-256
		// retrieval is an explicit transient operation; other cold sidecars stay404.
		if !acc.file(c, artifact, types.ActionRead) {
			writeErr(w, r, itemNotFound())
			return true
		}
		cached, getErr := s.cfg.Store.Items().Get(ctx, c.Key, true, artifact)
		switch {
		case getErr == nil && !cached.Folder:
			sidecar = string(alg)
			resource.Path = artifact
			resource.EscapedPath = strings.TrimSuffix(resource.EscapedPath, "."+sidecar)
		case errors.Is(getErr, store.ErrNotFound) && string(alg) == "sha256" &&
			c.RetrieveSha256FromServer != nil && *c.RetrieveSha256FromServer:
			resource.ChecksumOnly = true
		default:
			writeErr(w, r, itemNotFound())
			return true
		}
	}
	result, err := s.cfg.RemoteSources.Read(ctx, source.Request{
		Repo:     c,
		Subject:  p.Subject(),
		Access:   acc.d,
		Resource: resource,
		Matrix:   req.matrix(),
		Project:  c.ProjectKey,
	})
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "read source", pkgroute.SourceError(err, p.Anonymous)))
		return true
	}
	if result.Stream != nil {
		if !req.matrix().Matches(nil) {
			_ = result.Stream.Body.Close()
			writeErr(w, r, itemNotFound())
			return true
		}
		if sidecar != "" {
			_ = result.Stream.Body.Close()
			s.serveRemoteChecksum(
				w,
				r,
				item.Algorithm(sidecar).Of(result.Stream.Checksums),
				result.Stream.Modified,
			)
			return true
		}
		if resource.ChecksumOnly && r.Method != http.MethodHead {
			s.serveColdChecksum(w, r, result.Stream)
			return true
		}
		if resource.ChecksumOnly {
			// GET normalizes a checksum plus optional filename to exactly 64 hex
			// characters. HEAD cannot inspect that body, but must describe the same
			// representation rather than the upstream checksum file's byte length.
			result.Stream.Size = 64
			result.Stream.MediaType = "text/plain"
			result.Stream.ETag = ""
			result.Stream.Checksums = types.Checksums{}
			result.Stream.ContentRange = ""
		}
		sw := &statusWriter{ResponseWriter: w}
		if err = pkgroute.ServeStream(sw, r, result.Stream); err != nil {
			noteCause(ctx, err)
			panic(http.ErrAbortHandler)
		}
		if sw.complete(r) && result.Used != nil {
			_ = result.Used(ctx)
		}
		return true
	}
	check := req
	check.path = resource.Path
	ok, matchErr := matchesMatrix(ctx, s.cfg.Store, *result.Item, check)
	switch {
	case matchErr != nil:
		writeErr(w, r, s.internalError(ctx, "item properties", matchErr))
	case !ok:
		writeErr(w, r, itemNotFound())
	case sidecar != "":
		s.serveRemoteChecksum(
			w,
			r,
			item.ServedChecksum(c, *result.Item, item.Algorithm(sidecar)),
			result.Item.Modified,
		)
	default:
		s.serveFile(w, r, s.cfg.Store, found{repo: c, item: *result.Item, used: result.Used}, p)
	}
	return true
}

func remoteResource(r *http.Request, path string) source.Resource {
	escapedKey, _, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/"), "/")
	resource := pkgroute.Resource(
		r,
		path,
		pkgroute.EscapedResourcePath(r, "/artifactory/"+escapedKey+"/"),
	)
	// Strip local controls without normalizing signed query bytes or ordering.
	segments := strings.Split(r.URL.RawQuery, "&")
	kept := segments[:0]
	for _, segment := range segments {
		rawKey, _, _ := strings.Cut(segment, "=")
		key, _ := url.QueryUnescape(rawKey)
		switch key {
		case "trace", "skipUpdateStats", "content", "download":
			continue
		}
		kept = append(kept, segment)
	}
	resource.Query = strings.Join(kept, "&")
	return resource
}

func (s artifactoryService) serveRemoteChecksum(
	w http.ResponseWriter,
	r *http.Request,
	value string,
	modified time.Time,
) {
	if value == "" {
		writeErr(w, r, itemNotFound())
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	http.ServeContent(w, r, "", modified, strings.NewReader(value))
}

// Cache alias downloads have no source lookup token. Touch only entries naming
// these exact stored bytes; an invalidated token cannot revive an old generation.
func (s artifactoryService) recordCacheUse(ctx context.Context, f found) {
	if f.used != nil {
		_ = f.used(ctx)
		return
	}
	if f.repo.Class != repo.Remote || !f.item.Cached {
		return
	}
	page := store.Page{}
	for {
		entries, next, err := s.cfg.Store.RemoteCache().
			List(ctx, f.repo.Key, f.item.FullPath(), page)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.ItemID == f.item.ID && entry.Token.Key.Path == f.item.FullPath() &&
				entry.Blob.Checksums == f.item.Actual {
				_ = s.cfg.Store.RemoteCache().Touch(ctx, entry.Token, time.Now().UTC())
			}
		}
		if next == "" {
			return
		}
		page.After = next
	}
}

// Match checksum uploads: a hex digest, optionally followed by a filename.
// The digest describes an uncached artifact; this validates syntax, not its bytes.
func (s artifactoryService) serveColdChecksum(
	w http.ResponseWriter,
	r *http.Request,
	stream *source.Stream,
) {
	defer func() { _ = stream.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(stream.Body, 4097))
	value, _, _ := strings.Cut(strings.TrimSpace(string(data)), " ")
	sums, parseErr := item.ParseChecksums(types.Checksums{SHA256: value})
	if err != nil || len(data) > 4096 || parseErr != nil || sums.SHA256 == "" {
		writeErr(
			w,
			r,
			pkgroute.SourceError(
				&source.HTTPError{Status: 502, Kind: source.IntegrityFailure},
				false,
			),
		)
		return
	}
	s.serveRemoteChecksum(w, r, sums.SHA256, stream.Modified)
}
