package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/format/opkg"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var (
	errOpkgVirtualCycle = errors.New("server: opkg virtual member cycle")
	errOpkgMemberType   = errors.New("server: opkg virtual member has incompatible package type")
	errOpkgIndexLarge   = errors.New("server: opkg index exceeds limit")
)

type opkgIndexView struct {
	repo       repo.Config
	packages   []opkg.Package
	raw        []byte
	checkRead  func(context.Context, string) error
	revalidate func(context.Context) error
	used       func(context.Context) error
}

type opkgCandidate struct {
	repo repo.Config
	pkg  opkg.Package
}

func (s artifactoryService) tryOpkgItem(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) bool {
	if !opkg.Serves(c.PackageType) || req.isFolder {
		return false
	}
	if opkg.IsIndex(req.path) {
		if name := path.Base(req.path); name == opkg.PackagesName ||
			name == opkg.PackagesGzipName || c.Class == repo.Virtual {
			s.serveOpkgIndex(w, r, st, c, cached, req, p, acc)
			return true
		}
		return false
	}
	if strings.HasSuffix(req.path, ".ipk") {
		if !opkg.ValidPath(req.path) {
			writeErr(w, r, itemNotFound())
			return true
		}
		if c.Class == repo.Virtual || c.Class == repo.Remote {
			s.serveOpkgPackage(w, r, st, c, cached, req, p, acc)
			return true
		}
	}
	return false
}

func (s artifactoryService) serveOpkgIndex(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) {
	ctx := r.Context()
	feed, ok := opkg.FeedPath(req.path)
	if !ok || len(req.props) != 0 || r.URL.RawQuery != "" ||
		cached && c.Class != repo.Remote {
		writeErr(w, r, itemNotFound())
		return
	}
	if !acc.folder(c, req.path, types.ActionRead) ||
		!acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		writeErr(w, r, denied(p))
		return
	}
	var body []byte
	var used func(context.Context) error
	if c.Class == repo.Virtual {
		indexPath := opkg.IndexPath(feed, opkg.PackagesName)
		candidates, err := s.opkgCandidates(ctx, st, c, indexPath, p, acc, map[string]bool{})
		if err != nil {
			writeErr(w, r, s.itemError(ctx, "Opkg virtual feed", err))
			return
		}
		packages := make([]opkg.Package, 0, len(candidates))
		for _, candidate := range candidates {
			packages = append(packages, candidate.pkg)
		}
		files, err := opkg.EncodeIndex(packages, feed)
		if err == nil {
			err = opkg.SignFeed(ctx, st, c, files)
		}
		if err != nil {
			writeErr(w, r, s.internalError(ctx, "Opkg virtual feed", err))
			return
		}
		body = files[path.Base(req.path)]
		if body == nil {
			writeErr(w, r, itemNotFound())
			return
		}
	} else {
		view, err := s.opkgMemberIndex(ctx, st, c, cached, req.path, p, acc)
		if err != nil {
			writeErr(w, r, s.itemError(ctx, "Opkg feed", err))
			return
		}
		if view.revalidate != nil {
			if err := view.revalidate(ctx); err != nil {
				writeErr(w, r, pkgroute.SourceError(err, p.Anonymous))
				return
			}
		}
		for _, pkg := range view.packages {
			allowed, err := opkgPackageReadable(ctx, c, pkg.Path, acc, view.checkRead)
			if err != nil {
				writeErr(w, r, s.itemError(ctx, "Opkg package read authority", err))
				return
			}
			if !allowed {
				writeErr(w, r, denied(p))
				return
			}
		}
		body, used = view.raw, view.used
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	sw := &statusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, path.Base(req.path), time.Time{}, bytes.NewReader(body))
	if sw.complete(r) && used != nil {
		_ = used(ctx)
	}
}

func opkgPackageReadable(
	ctx context.Context, c repo.Config, p string, acc access,
	check func(context.Context, string) error,
) (bool, error) {
	if !acc.file(c, p, types.ActionRead) || generic.CheckDownload(c, p, false) != nil {
		return false, nil
	}
	if check != nil {
		if err := check(ctx, p); err != nil {
			if errors.Is(err, source.ErrDenied) {
				return false, nil
			}
			return false, fmt.Errorf("opkg: source read authority: %w", err)
		}
	}
	return true, nil
}

func (s artifactoryService) opkgMemberIndex(
	ctx context.Context, st store.Store, c repo.Config, cached bool, indexPath string,
	p auth.Principal, acc access,
) (opkgIndexView, error) {
	view := opkgIndexView{repo: c}
	if c.Class == repo.Remote {
		if s.cfg.RemoteSources == nil {
			return view, source.ErrUnavailable
		}
		query := source.Request{
			Repo: c, Subject: p.Subject(), Access: acc.d, Project: c.ProjectKey,
			CacheOnly: cached,
			Resource:  source.Resource{Path: indexPath, Method: http.MethodGet},
		}
		query, err := opkgSourceRequest(query)
		if err != nil {
			return view, err
		}
		result, err := s.cfg.RemoteSources.Read(ctx, query)
		if err != nil {
			return view, fmt.Errorf(
				"opkg: remote index: %w",
				pkgroute.SourceError(err, p.Anonymous),
			)
		}
		view.checkRead, view.revalidate, view.used =
			result.CheckRead, result.Revalidate, result.Used
		switch {
		case result.Stream != nil:
			defer func() { _ = result.Stream.Body.Close() }()
			view.raw, err = io.ReadAll(io.LimitReader(result.Stream.Body, 36<<20+1))
		case result.Item != nil:
			view.raw, err = s.readOpkgBlob(ctx, result.Item.Actual.SHA256)
		default:
			return view, source.ErrInvalidResult
		}
		if err != nil {
			return view, fmt.Errorf("opkg: read remote index: %w", err)
		}
	} else {
		if c.Class != repo.Local && c.Class != repo.Federated || cached {
			return view, store.ErrNotFound
		}
		it, err := st.Items().Get(ctx, c.Key, false, indexPath)
		if err != nil {
			return view, err //nolint:wrapcheck
		}
		if it.Folder {
			return view, store.ErrNotFound
		}
		view.raw, err = s.readOpkgBlob(ctx, it.Actual.SHA256)
		if err != nil {
			return view, err
		}
	}
	if len(view.raw) == 0 || len(view.raw) > 36<<20 {
		return view, errOpkgIndexLarge
	}
	var err error
	view.packages, err = opkg.DecodeIndex(view.raw, indexPath)
	if err != nil {
		return view, fmt.Errorf("opkg: parse feed: %w", err)
	}
	return view, nil
}

func (s artifactoryService) readOpkgBlob(ctx context.Context, sha string) ([]byte, error) {
	rc, _, err := s.cfg.Blobs.Open(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("opkg: open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, 36<<20+1))
	if err != nil {
		return nil, fmt.Errorf("opkg: read blob: %w", err)
	}
	return body, nil
}

func (s artifactoryService) opkgCandidates(
	ctx context.Context, st store.Store, c repo.Config, indexPath string,
	p auth.Principal, acc access, seen map[string]bool,
) ([]opkgCandidate, error) {
	if seen[c.Key] || len(seen) >= 8 {
		return nil, errOpkgVirtualCycle
	}
	seen[c.Key] = true
	defer delete(seen, c.Key)
	var out []opkgCandidate
	selected := map[string]bool{}
	for _, key := range c.Repositories {
		member, err := st.Repos().Get(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("opkg: virtual member %s: %w", key, err)
		}
		if member.PackageType != repo.Opkg {
			return nil, fmt.Errorf("%w: %s", errOpkgMemberType, key)
		}
		if !acc.folder(member, indexPath, types.ActionRead) ||
			!acc.file(member, indexPath, types.ActionRead) ||
			generic.CheckDownload(member, indexPath, false) != nil {
			continue
		}
		var entries []opkgCandidate
		if member.Class == repo.Virtual {
			entries, err = s.opkgCandidates(ctx, st, member, indexPath, p, acc, seen)
		} else {
			var view opkgIndexView
			view, err = s.opkgMemberIndex(ctx, st, member, false, indexPath, p, acc)
			if err == nil && view.revalidate != nil {
				err = view.revalidate(ctx)
			}
			if err == nil {
				for _, pkg := range view.packages {
					allowed, checkErr := opkgPackageReadable(
						ctx, member, pkg.Path, acc, view.checkRead)
					if checkErr != nil {
						err = checkErr
						break
					}
					if allowed {
						entries = append(entries, opkgCandidate{repo: member, pkg: pkg})
					}
				}
			}
		}
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("opkg: member %s index: %w", key, err)
		}
		for _, entry := range entries {
			if selected[entry.pkg.Path] ||
				!acc.file(c, entry.pkg.Path, types.ActionRead) ||
				generic.CheckDownload(c, entry.pkg.Path, false) != nil {
				continue
			}
			selected[entry.pkg.Path] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

func (s artifactoryService) serveOpkgPackage(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) {
	if len(req.props) != 0 || r.URL.RawQuery != "" || cached && c.Class != repo.Remote {
		writeErr(w, r, itemNotFound())
		return
	}
	if !acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		writeErr(w, r, denied(p))
		return
	}
	indexPath := opkg.IndexPath(opkg.Feed(req.path), opkg.PackagesName)
	var candidates []opkgCandidate
	var err error
	if c.Class == repo.Virtual {
		candidates, err = s.opkgCandidates(r.Context(), st, c, indexPath, p, acc, map[string]bool{})
	} else {
		var view opkgIndexView
		view, err = s.opkgMemberIndex(r.Context(), st, c, cached, indexPath, p, acc)
		if err == nil && view.revalidate != nil {
			err = view.revalidate(r.Context())
		}
		if err == nil {
			for _, pkg := range view.packages {
				candidates = append(candidates, opkgCandidate{repo: c, pkg: pkg})
			}
		}
	}
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "Opkg package index", err))
		return
	}
	for _, candidate := range candidates {
		if candidate.pkg.Path != req.path {
			continue
		}
		if candidate.repo.Class == repo.Remote {
			s.serveRemoteOpkgPackage(w, r, candidate.repo, cached, req, candidate.pkg, p, acc)
			return
		}
		it, err := st.Items().Get(r.Context(), candidate.repo.Key, false, req.path)
		if err != nil {
			writeErr(w, r, s.itemError(r.Context(), "Opkg member package", err))
			return
		}
		if it.Folder || it.Actual.SHA256 != candidate.pkg.SHA256 {
			writeErr(w, r, itemNotFound())
			return
		}
		s.serveFile(w, r, st, found{repo: candidate.repo, item: it}, p)
		return
	}
	writeErr(w, r, itemNotFound())
}

func (s artifactoryService) serveRemoteOpkgPackage(
	w http.ResponseWriter, r *http.Request, c repo.Config, cached bool,
	req itemRequest, expected opkg.Package, p auth.Principal, acc access,
) {
	query := source.Request{
		Repo: c, Subject: p.Subject(), Access: acc.d, Project: c.ProjectKey,
		CacheOnly: cached,
		Resource: source.Resource{
			Kind: source.ResourceKindArtifact, Path: req.path, Method: r.Method,
			Checksums: types.Checksums{SHA256: expected.SHA256},
		},
	}
	query, err := opkgSourceRequest(query)
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "Opkg source", err))
		return
	}
	result, err := s.cfg.RemoteSources.Read(r.Context(), query)
	if err != nil {
		writeErr(
			w,
			r,
			s.itemError(r.Context(), "Opkg source", pkgroute.SourceError(err, p.Anonymous)),
		)
		return
	}
	if result.Stream != nil {
		s.serveRemoteStream(w, r, result, c, req, query.Resource, "", p.Anonymous)
		return
	}
	if !s.remoteDeliveryAllowed(w, r, result, p.Anonymous) {
		return
	}
	if result.Item == nil || result.Item.Actual.SHA256 != expected.SHA256 ||
		result.Item.Size != expected.Size {
		writeErr(w, r, s.internalError(r.Context(), "Opkg source", source.ErrInvalidResult))
		return
	}
	s.serveFile(w, r, s.cfg.Store,
		found{repo: c, item: *result.Item, used: result.Used}, p)
}
