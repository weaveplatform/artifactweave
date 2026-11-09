package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const debianIndexLimit = 64 << 20

var errDebianIndexTooLarge = errors.New("server: debian Packages index exceeds limit")

// tryDebianItem ensures a Packages index is never returned to a caller who
// cannot read every package it advertises. The ordinary item route cannot
// make that guarantee for a cached or virtual metadata file.
func (s artifactoryService) tryDebianItem(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p kernel.Principal, acc access,
) bool {
	if !debian.Serves(c.PackageType) || req.isFolder {
		return false
	}
	if c.Class == repo.Virtual && strings.HasPrefix(req.path, "dists/") {
		_, byHash := debian.ByHashMetadataPath(req.path)
		if debian.ReleaseMetadataPath(req.path) ||
			debian.PackagesIndexPath(req.path) || byHash {
			s.serveVirtualDebianMetadata(w, r, st, c, req, p, acc)
		} else {
			httpkit.WriteErr(w, r, itemNotFound())
		}
		return true
	}
	if c.Class == repo.Virtual && debian.ValidBinaryPath(req.path) {
		s.serveVirtualDebianPackage(w, r, st, c, req, p, acc)
		return true
	}
	_, byHash := debian.ByHashMetadataPath(req.path)
	if !debian.PackagesIndexPath(req.path) && !debian.SourcesIndexPath(req.path) && !byHash {
		return false
	}
	s.serveDebianIndex(w, r, st, c, cached, req, p, acc)
	return true
}

func (s artifactoryService) serveDebianIndex(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p kernel.Principal, acc access,
) {
	ctx := r.Context()
	if len(req.props) != 0 || r.URL.RawQuery != "" {
		httpkit.WriteErr(w, r, itemNotFound())
		return
	}
	if !acc.folder(c, req.path, types.ActionRead) ||
		!acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		httpkit.WriteErr(w, r, Denied(p))
		return
	}
	if cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemNotFound())
		return
	}
	var body []byte
	var used func(context.Context) error
	var checkRead func(context.Context, string) error
	var err error
	switch c.Class {
	case repo.Local, repo.Federated:
		var it types.Item
		it, err = st.Items().Get(ctx, c.Key, false, req.path)
		if err == nil && it.Folder {
			err = store.ErrNotFound
		}
		if err == nil {
			body, err = s.readDebianBlob(ctx, it.Actual.SHA256)
		}
	case repo.Remote:
		if s.cfg.RemoteSources == nil {
			err = source.ErrUnavailable
			break
		}
		query := source.Request{
			Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.d, Project: c.ProjectKey,
			CacheOnly: cached,
			Resource:  source.Resource{Path: req.path, Method: r.Method},
		}
		query, err = debianSourceRequest(query)
		if err != nil {
			break
		}
		var result source.Result
		result, err = s.cfg.RemoteSources.Read(ctx, query)
		if err != nil {
			err = pkgroute.SourceError(err, p.Anonymous)
			break
		}
		checkRead, used = result.CheckRead, result.Used
		switch {
		case result.Stream != nil:
			defer func() { _ = result.Stream.Body.Close() }()
			body, err = io.ReadAll(io.LimitReader(result.Stream.Body, debianIndexLimit+1))
		case result.Item != nil:
			body, err = s.readDebianBlob(ctx, result.Item.Actual.SHA256)
		default:
			err = source.ErrInvalidResult
		}
	default:
		err = store.ErrNotFound
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.itemError(ctx, "Debian Packages index", err))
		return
	}
	if len(body) > debianIndexLimit {
		httpkit.WriteErr(
			w,
			r,
			s.internalError(ctx, "Debian Packages index", errDebianIndexTooLarge),
		)
		return
	}
	paths, err := debianIndexPaths(req.path, body)
	if err != nil {
		httpkit.WriteErr(w, r, s.internalError(ctx, "Debian index", err))
		return
	}
	for _, path := range paths {
		if !acc.file(c, path, types.ActionRead) ||
			generic.CheckDownload(c, path, false) != nil {
			httpkit.WriteErr(w, r, Denied(p))
			return
		}
		if checkRead != nil {
			if err := checkRead(ctx, path); err != nil {
				if errors.Is(err, source.ErrDenied) {
					httpkit.WriteErr(w, r, Denied(p))
				} else {
					httpkit.WriteErr(w, r, s.itemError(ctx, "Debian package read authority", err))
				}
				return
			}
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	sw := &statusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, req.path, time.Time{}, bytes.NewReader(body))
	if sw.complete(r) && used != nil {
		_ = used(ctx)
	}
}

func debianIndexPaths(path string, body []byte) ([]string, error) {
	paths := []string{}
	_, byHash := debian.ByHashMetadataPath(path)
	if debian.PackagesIndexPath(path) || byHash && strings.Contains(path, "/binary-") {
		rows, err := debian.DecodePackagesIndex(body)
		for _, row := range rows {
			paths = append(paths, row.Filename)
		}
		if err != nil {
			return nil, fmt.Errorf("debian Packages index: %w", err)
		}
		return paths, nil
	}
	rows, err := debian.DecodeSourcesIndex(body)
	for _, row := range rows {
		paths = append(paths, row.Files...)
	}
	if err != nil {
		return nil, fmt.Errorf("debian Sources index: %w", err)
	}
	return paths, nil
}

func (s artifactoryService) readDebianBlob(ctx context.Context, sha string) ([]byte, error) {
	r, _, err := s.cfg.Blobs.Open(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("debian: blob open: %w", err)
	}
	defer func() { _ = r.Close() }()
	body, err := io.ReadAll(io.LimitReader(r, debianIndexLimit+1))
	if err != nil {
		return nil, fmt.Errorf("debian: blob read: %w", err)
	}
	return body, nil
}
