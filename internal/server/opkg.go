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

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/opkg"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
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

// opkgItems serves opkg's indexes and packages over the item kit.
type opkgItems struct{ *Kit }

func tryOpkgItem(
	k *Kit, w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req ItemRequest, p kernel.Principal, acc Access,
) bool {
	s := opkgItems{k}
	if !opkg.Serves(c.PackageType) || req.IsFolder {
		return false
	}
	if opkg.IsIndex(req.Path) {
		if name := path.Base(req.Path); name == opkg.PackagesName ||
			name == opkg.PackagesGzipName || c.Class == repo.Virtual {
			s.serveOpkgIndex(w, r, st, c, cached, req, p, acc)
			return true
		}
		return false
	}
	if strings.HasSuffix(req.Path, ".ipk") {
		if !opkg.ValidPath(req.Path) {
			httpkit.WriteErr(w, r, ItemNotFound())
			return true
		}
		if c.Class == repo.Virtual || c.Class == repo.Remote {
			s.serveOpkgPackage(w, r, st, c, cached, req, p, acc)
			return true
		}
	}
	return false
}

func (s opkgItems) serveOpkgIndex(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req ItemRequest, p kernel.Principal, acc Access,
) {
	ctx := r.Context()
	feed, ok := opkg.FeedPath(req.Path)
	if !ok || len(req.Props) != 0 || r.URL.RawQuery != "" ||
		cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	if !acc.Folder(c, req.Path, types.ActionRead) ||
		!acc.File(c, req.Path, types.ActionRead) ||
		generic.CheckDownload(c, req.Path, false) != nil {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	var body []byte
	var used func(context.Context) error
	if c.Class == repo.Virtual {
		indexPath := opkg.IndexPath(feed, opkg.PackagesName)
		candidates, err := s.opkgCandidates(ctx, st, c, indexPath, p, acc, map[string]bool{})
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Opkg virtual feed", err))
			return
		}
		packages := make([]opkg.Package, 0, len(candidates))
		for _, candidate := range candidates {
			packages = append(packages, candidate.pkg)
		}
		files, err := opkg.EncodeIndex(packages, feed)
		if err == nil {
			err = opkg.SignFeed(ctx, s.Access.Keys, c, files)
		}
		if err != nil {
			httpkit.WriteErr(w, r, s.InternalError(ctx, "Opkg virtual feed", err))
			return
		}
		body = files[path.Base(req.Path)]
		if body == nil {
			httpkit.WriteErr(w, r, ItemNotFound())
			return
		}
	} else {
		view, err := s.opkgMemberIndex(ctx, st, c, cached, req.Path, p, acc)
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Opkg feed", err))
			return
		}
		if view.revalidate != nil {
			if err := view.revalidate(ctx); err != nil {
				httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
				return
			}
		}
		for _, pkg := range view.packages {
			allowed, err := opkgPackageReadable(ctx, c, pkg.Path, acc, view.checkRead)
			if err != nil {
				httpkit.WriteErr(w, r, s.ItemError(ctx, "Opkg package read authority", err))
				return
			}
			if !allowed {
				httpkit.WriteErr(w, r, httpkit.Denied(p))
				return
			}
		}
		body, used = view.raw, view.used
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	sw := &StatusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, path.Base(req.Path), time.Time{}, bytes.NewReader(body))
	if sw.Complete(r) && used != nil {
		_ = used(ctx)
	}
}

func opkgPackageReadable(
	ctx context.Context, c repo.Config, p string, acc Access,
	check func(context.Context, string) error,
) (bool, error) {
	if !acc.File(c, p, types.ActionRead) || generic.CheckDownload(c, p, false) != nil {
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

func (s opkgItems) opkgMemberIndex(
	ctx context.Context, st store.Store, c repo.Config, cached bool, indexPath string,
	p kernel.Principal, acc Access,
) (opkgIndexView, error) {
	view := opkgIndexView{repo: c}
	if c.Class == repo.Remote {
		if s.RemoteSources == nil {
			return view, source.ErrUnavailable
		}
		query := source.Request{
			Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
			CacheOnly: cached,
			Resource:  source.Resource{Path: indexPath, Method: http.MethodGet},
		}
		query, err := OpkgSourceRequest(query)
		if err != nil {
			return view, err
		}
		result, err := s.RemoteSources.Read(ctx, query)
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

func (s opkgItems) readOpkgBlob(ctx context.Context, sha string) ([]byte, error) {
	rc, _, err := s.Blobs.Open(ctx, sha)
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

func (s opkgItems) opkgCandidates(
	ctx context.Context, st store.Store, c repo.Config, indexPath string,
	p kernel.Principal, acc Access, seen map[string]bool,
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
		if !acc.Folder(member, indexPath, types.ActionRead) ||
			!acc.File(member, indexPath, types.ActionRead) ||
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
				!acc.File(c, entry.pkg.Path, types.ActionRead) ||
				generic.CheckDownload(c, entry.pkg.Path, false) != nil {
				continue
			}
			selected[entry.pkg.Path] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

func (s opkgItems) serveOpkgPackage(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req ItemRequest, p kernel.Principal, acc Access,
) {
	if len(req.Props) != 0 || r.URL.RawQuery != "" || cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	if !acc.File(c, req.Path, types.ActionRead) ||
		generic.CheckDownload(c, req.Path, false) != nil {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	indexPath := opkg.IndexPath(opkg.Feed(req.Path), opkg.PackagesName)
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
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Opkg package index", err))
		return
	}
	for _, candidate := range candidates {
		if candidate.pkg.Path != req.Path {
			continue
		}
		if candidate.repo.Class == repo.Remote {
			s.serveRemoteOpkgPackage(w, r, candidate.repo, cached, req, candidate.pkg, p, acc)
			return
		}
		it, err := st.Items().Get(r.Context(), candidate.repo.Key, false, req.Path)
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Opkg member package", err))
			return
		}
		if it.Folder || it.Actual.SHA256 != candidate.pkg.SHA256 {
			httpkit.WriteErr(w, r, ItemNotFound())
			return
		}
		s.ServeFile(w, r, st, Found{Repo: candidate.repo, Item: it}, p)
		return
	}
	httpkit.WriteErr(w, r, ItemNotFound())
}

func (s opkgItems) serveRemoteOpkgPackage(
	w http.ResponseWriter, r *http.Request, c repo.Config, cached bool,
	req ItemRequest, expected opkg.Package, p kernel.Principal, acc Access,
) {
	query := source.Request{
		Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
		CacheOnly: cached,
		Resource: source.Resource{
			Kind: source.ResourceKindArtifact, Path: req.Path, Method: r.Method,
			Checksums: types.Checksums{SHA256: expected.SHA256},
		},
	}
	query, err := OpkgSourceRequest(query)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Opkg source", err))
		return
	}
	result, err := s.RemoteSources.Read(r.Context(), query)
	if err != nil {
		httpkit.WriteErr(
			w,
			r,
			s.ItemError(r.Context(), "Opkg source", pkgroute.SourceError(err, p.Anonymous)),
		)
		return
	}
	if result.Stream != nil {
		s.ServeRemoteStream(w, r, result, c, req, query.Resource, "", p.Anonymous)
		return
	}
	if !s.RemoteDeliveryAllowed(w, r, result, p.Anonymous) {
		return
	}
	if result.Item == nil || result.Item.Actual.SHA256 != expected.SHA256 ||
		result.Item.Size != expected.Size {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "Opkg source", source.ErrInvalidResult))
		return
	}
	s.ServeFile(w, r, s.Store,
		Found{Repo: c, Item: *result.Item, Used: result.Used}, p)
}
