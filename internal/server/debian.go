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

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
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

// DebianAPI is the Debian ecosystem's partial strict server: the metadata
// and snapshot operations, the snapshot routes, and the item overlay that
// serves Debian's indexes and virtual repositories, over the item kit.
type DebianAPI struct{ *itemkit.Kit }

// TryDebianItem ensures a Packages index is never returned to a caller who
// cannot read every package it advertises. The ordinary item route cannot
// make that guarantee for a cached or virtual metadata file.
func TryDebianItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	s := DebianAPI{k}
	if !debian.Serves(c.PackageType) || req.IsFolder {
		return false
	}
	if c.Class == repo.Virtual && strings.HasPrefix(req.Path, "dists/") {
		_, byHash := debian.ByHashMetadataPath(req.Path)
		if debian.ReleaseMetadataPath(req.Path) ||
			debian.PackagesIndexPath(req.Path) || byHash {
			s.serveVirtualDebianMetadata(w, r, st, c, req, p, acc)
		} else {
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		}
		return true
	}
	if c.Class == repo.Virtual && debian.ValidBinaryPath(req.Path) {
		s.serveVirtualDebianPackage(w, r, st, c, req, p, acc)
		return true
	}
	_, byHash := debian.ByHashMetadataPath(req.Path)
	if !debian.PackagesIndexPath(req.Path) && !debian.SourcesIndexPath(req.Path) && !byHash {
		return false
	}
	s.serveDebianIndex(w, r, st, c, cached, req, p, acc)
	return true
}

func (s DebianAPI) serveDebianIndex(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	ctx := r.Context()
	if len(req.Props) != 0 || r.URL.RawQuery != "" {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !acc.Folder(c, req.Path, types.ActionRead) ||
		!acc.File(c, req.Path, types.ActionRead) ||
		generic.CheckDownload(c, req.Path, false) != nil {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	var body []byte
	var used func(context.Context) error
	var checkRead func(context.Context, string) error
	var err error
	switch c.Class {
	case repo.Local, repo.Federated:
		var it types.Item
		it, err = st.Items().Get(ctx, c.Key, false, req.Path)
		if err == nil && it.Folder {
			err = store.ErrNotFound
		}
		if err == nil {
			body, err = s.readDebianBlob(ctx, it.Actual.SHA256)
		}
	case repo.Remote:
		if s.RemoteSources == nil {
			err = source.ErrUnavailable
			break
		}
		query := source.Request{
			Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
			CacheOnly: cached,
			Resource:  source.Resource{Path: req.Path, Method: r.Method},
		}
		query, err = itemkit.DebianSourceRequest(query)
		if err != nil {
			break
		}
		var result source.Result
		result, err = s.RemoteSources.Read(ctx, query)
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
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian Packages index", err))
		return
	}
	if len(body) > debianIndexLimit {
		httpkit.WriteErr(
			w,
			r,
			s.InternalError(ctx, "Debian Packages index", errDebianIndexTooLarge),
		)
		return
	}
	paths, err := debianIndexPaths(req.Path, body)
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "Debian index", err))
		return
	}
	for _, path := range paths {
		if !acc.File(c, path, types.ActionRead) ||
			generic.CheckDownload(c, path, false) != nil {
			httpkit.WriteErr(w, r, httpkit.Denied(p))
			return
		}
		if checkRead != nil {
			if err := checkRead(ctx, path); err != nil {
				if errors.Is(err, source.ErrDenied) {
					httpkit.WriteErr(w, r, httpkit.Denied(p))
				} else {
					httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian package read authority", err))
				}
				return
			}
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	sw := &itemkit.StatusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, req.Path, time.Time{}, bytes.NewReader(body))
	if sw.Complete(r) && used != nil {
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

func (s DebianAPI) readDebianBlob(ctx context.Context, sha string) ([]byte, error) {
	r, _, err := s.Blobs.Open(ctx, sha)
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
