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

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/debian"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const debianIndexLimit = 64 << 20

var errDebianIndexTooLarge = errors.New("debian Packages index exceeds limit")

// tryDebianItem ensures a Packages index is never returned to a caller who
// cannot read every package it advertises. The ordinary item route cannot
// make that guarantee for a cached or virtual metadata file.
func (s artifactoryService) tryDebianItem(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) bool {
	if !debian.Serves(c.PackageType) || req.folder {
		return false
	}
	if c.Class == repo.Virtual && strings.HasPrefix(req.path, "dists/") {
		_, byHash := debian.ByHashMetadataPath(req.path)
		if debian.ReleaseMetadataPath(req.path) ||
			debian.PackagesIndexPath(req.path) || byHash {
			s.serveVirtualDebianMetadata(w, r, st, c, req, p, acc)
		} else {
			writeErr(w, r, itemNotFound())
		}
		return true
	}
	if c.Class == repo.Virtual && debian.ValidBinaryPath(req.path) {
		s.serveVirtualDebianPackage(w, r, st, c, req, p, acc)
		return true
	}
	if !debian.PackagesIndexPath(req.path) {
		return false
	}
	s.serveDebianPackages(w, r, st, c, cached, req, p, acc)
	return true
}

func (s artifactoryService) serveDebianPackages(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) {
	ctx := r.Context()
	if len(req.props) != 0 || r.URL.RawQuery != "" {
		writeErr(w, r, itemNotFound())
		return
	}
	if !acc.folder(c, req.path, types.ActionRead) ||
		!acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		writeErr(w, r, denied(p))
		return
	}
	if cached && c.Class != repo.Remote {
		writeErr(w, r, itemNotFound())
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
			Repo: c, Subject: p.Subject(), Access: acc.d, Project: c.ProjectKey,
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
		writeErr(w, r, s.itemError(ctx, "Debian Packages index", err))
		return
	}
	if len(body) > debianIndexLimit {
		writeErr(
			w,
			r,
			s.internalError(ctx, "Debian Packages index", errDebianIndexTooLarge),
		)
		return
	}
	rows, err := debian.DecodePackagesIndex(body)
	if err != nil {
		writeErr(w, r, s.internalError(ctx, "Debian Packages index", err))
		return
	}
	for _, row := range rows {
		if !acc.file(c, row.Filename, types.ActionRead) ||
			generic.CheckDownload(c, row.Filename, false) != nil {
			writeErr(w, r, denied(p))
			return
		}
		if checkRead != nil {
			if err := checkRead(ctx, row.Filename); err != nil {
				if errors.Is(err, source.ErrDenied) {
					writeErr(w, r, denied(p))
				} else {
					writeErr(w, r, s.itemError(ctx, "Debian package read authority", err))
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
