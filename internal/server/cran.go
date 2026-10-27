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
	"github.com/weaveplatform/artifactweave/internal/format/cran"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var (
	errCranVirtualCycle = errors.New("CRAN virtual member cycle")
	errCranMemberType   = errors.New("CRAN virtual member has incompatible package type")
)

type cranIndexView struct {
	repo       repo.Config
	packages   []cran.Package
	checkRead  func(context.Context, string) error
	revalidate func(context.Context) error
	used       func(context.Context) error
}

type cranCandidate struct {
	repo repo.Config
	pkg  cran.Package
}

func (s artifactoryService) tryCranItem(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) bool {
	if !cran.Serves(c.PackageType) || req.isFolder {
		return false
	}
	if strings.HasSuffix(req.path, "/PACKAGES.rds") {
		writeErr(w, r, itemNotFound()) // R falls back to PACKAGES.gz.
		return true
	}
	if _, _, ok := cran.IndexPath(req.path); ok {
		s.serveCranIndex(w, r, st, c, cached, req, p, acc)
		return true
	}
	if cran.IsPackage(req.path) {
		if c.Class == repo.Virtual || c.Class == repo.Remote {
			s.serveCranPackage(w, r, st, c, cached, req, p, acc)
			return true
		}
		return false
	}
	return false
}

func (s artifactoryService) serveCranIndex(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) {
	ctx := r.Context()
	feed, name, ok := cran.IndexPath(req.path)
	if !ok || len(req.props) != 0 || r.URL.RawQuery != "" ||
		cached && c.Class != repo.Remote {
		writeErr(w, r, itemNotFound())
		return
	}
	if !acc.folder(c, feed, types.ActionRead) || !acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		writeErr(w, r, denied(p))
		return
	}
	var candidates []cranCandidate
	var err error
	var used func(context.Context) error
	if c.Class == repo.Virtual {
		candidates, err = s.cranCandidates(ctx, st, c, req.path, p, acc, map[string]bool{})
	} else {
		var view cranIndexView
		view, err = s.cranMemberIndex(ctx, st, c, cached, req.path, p, acc)
		if err == nil && view.revalidate != nil {
			err = view.revalidate(ctx)
		}
		if err == nil {
			for _, pkg := range view.packages {
				allowed, checkErr := cranPackageReadable(ctx, c, pkg.Path, acc, view.checkRead)
				if checkErr != nil {
					err = checkErr
					break
				}
				if allowed {
					candidates = append(candidates, cranCandidate{repo: c, pkg: pkg})
				}
			}
		}
		used = view.used
	}
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "CRAN index", pkgroute.SourceError(err, p.Anonymous)))
		return
	}
	packages := make([]cran.Package, 0, len(candidates))
	for _, candidate := range candidates {
		packages = append(packages, candidate.pkg)
	}
	generated, err := cran.EncodeIndex(packages, feed)
	if err != nil {
		writeErr(w, r, s.internalError(ctx, "CRAN index", err))
		return
	}
	body := generated[name]
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if name == cran.PackagesGzipName {
		w.Header().Set("Content-Type", "application/gzip")
	}
	w.Header().Set("Cache-Control", "private, no-store")
	sw := &statusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, name, time.Time{}, bytes.NewReader(body))
	if sw.complete(r) && used != nil {
		_ = used(ctx)
	}
}

func cranPackageReadable(
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
			return false, fmt.Errorf("cran: package read authority: %w", err)
		}
	}
	return true, nil
}

func (s artifactoryService) cranMemberIndex(
	ctx context.Context, st store.Store, c repo.Config, cached bool, indexPath string,
	p auth.Principal, acc access,
) (cranIndexView, error) {
	view := cranIndexView{repo: c}
	var body []byte
	var err error
	if c.Class == repo.Remote {
		if s.cfg.RemoteSources == nil {
			return view, fmt.Errorf("cran: remote index source: %w",
				pkgroute.SourceError(source.ErrUnavailable, p.Anonymous))
		}
		query := source.Request{
			Repo: c, Subject: p.Subject(), Access: acc.d, Project: c.ProjectKey,
			CacheOnly: cached,
			Resource:  source.Resource{Path: indexPath, Method: http.MethodGet},
		}
		query, err = cranSourceRequest(query)
		if err != nil {
			return view, err
		}
		result, err := s.cfg.RemoteSources.Read(ctx, query)
		if err != nil {
			return view, fmt.Errorf(
				"cran: remote index: %w",
				pkgroute.SourceError(err, p.Anonymous),
			)
		}
		view.checkRead, view.revalidate, view.used =
			result.CheckRead, result.Revalidate, result.Used
		switch {
		case result.Stream != nil:
			defer func() { _ = result.Stream.Body.Close() }()
			body, err = io.ReadAll(io.LimitReader(result.Stream.Body, 32<<20+1))
		case result.Item != nil:
			body, err = s.readCranBlob(ctx, result.Item.Actual.SHA256)
		default:
			return view, source.ErrInvalidResult
		}
		if err != nil {
			if result.Stream != nil {
				err = pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous)
			}
			return view, fmt.Errorf("cran: remote index body: %w", err)
		}
	} else {
		if c.Class != repo.Local && c.Class != repo.Federated || cached {
			return view, store.ErrNotFound
		}
		if err := s.await(ctx, st, cran.KindIndex, c.Key); err != nil {
			return view, fmt.Errorf("cran: refresh index: %w", err)
		}
		it, getErr := st.Items().Get(ctx, c.Key, false, indexPath)
		if getErr != nil {
			return view, getErr //nolint:wrapcheck // ErrNotFound is the answer.
		}
		if it.Folder {
			return view, store.ErrNotFound
		}
		body, err = s.readCranBlob(ctx, it.Actual.SHA256)
		if err != nil {
			return view, fmt.Errorf("cran: hosted index body: %w", err)
		}
	}
	if len(body) > 32<<20 {
		if c.Class == repo.Remote {
			return view, fmt.Errorf("cran: oversized remote index: %w",
				pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous))
		}
		return view, fmt.Errorf("cran: read index: %w", source.ErrInvalidResult)
	}
	view.packages, err = cran.DecodeIndex(body, indexPath)
	if err != nil {
		if c.Class == repo.Remote {
			return view, fmt.Errorf("cran: malformed remote index: %w",
				pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous))
		}
		return view, fmt.Errorf("cran: parse index: %w", err)
	}
	return view, nil
}

func (s artifactoryService) readCranBlob(ctx context.Context, sha string) ([]byte, error) {
	rc, _, err := s.cfg.Blobs.Open(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("cran: open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, 32<<20+1))
	if err != nil {
		return nil, fmt.Errorf("cran: read blob: %w", err)
	}
	return body, nil
}

func (s artifactoryService) cranCandidates(
	ctx context.Context, st store.Store, c repo.Config, indexPath string,
	p auth.Principal, acc access, seen map[string]bool,
) ([]cranCandidate, error) {
	if seen[c.Key] || len(seen) >= 8 {
		return nil, errCranVirtualCycle
	}
	seen[c.Key] = true
	defer delete(seen, c.Key)
	var out []cranCandidate
	selected := map[string]bool{}
	for _, key := range c.Repositories {
		member, err := st.Repos().Get(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cran: virtual member %s: %w", key, err)
		}
		if !cran.Serves(member.PackageType) {
			return nil, fmt.Errorf("%w: %s", errCranMemberType, key)
		}
		feed, _, _ := cran.IndexPath(indexPath)
		if !acc.folder(member, feed, types.ActionRead) ||
			!acc.file(member, indexPath, types.ActionRead) ||
			generic.CheckDownload(member, indexPath, false) != nil {
			continue
		}
		var entries []cranCandidate
		if member.Class == repo.Virtual {
			entries, err = s.cranCandidates(ctx, st, member, indexPath, p, acc, seen)
		} else {
			var view cranIndexView
			view, err = s.cranMemberIndex(ctx, st, member, false, indexPath, p, acc)
			if err == nil && view.revalidate != nil {
				err = view.revalidate(ctx)
			}
			if err == nil {
				for _, pkg := range view.packages {
					allowed, checkErr := cranPackageReadable(
						ctx,
						member,
						pkg.Path,
						acc,
						view.checkRead,
					)
					if checkErr != nil {
						err = checkErr
						break
					}
					if allowed {
						entries = append(entries, cranCandidate{repo: member, pkg: pkg})
					}
				}
			}
		}
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cran: member %s index: %w", key, err)
		}
		for _, entry := range entries {
			if selected[entry.pkg.Name] ||
				!acc.file(c, entry.pkg.Path, types.ActionRead) ||
				generic.CheckDownload(c, entry.pkg.Path, false) != nil {
				continue
			}
			selected[entry.pkg.Name] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

func (s artifactoryService) serveCranPackage(
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
	feed, _, _, _ := cran.PackagePath(req.path)
	indexPath := feed + "/" + cran.PackagesName
	var candidates []cranCandidate
	var err error
	if c.Class == repo.Virtual {
		candidates, err = s.cranCandidates(r.Context(), st, c, indexPath, p, acc, map[string]bool{})
	} else {
		var view cranIndexView
		view, err = s.cranMemberIndex(r.Context(), st, c, cached, indexPath, p, acc)
		if err == nil && view.revalidate != nil {
			err = view.revalidate(r.Context())
		}
		if err == nil {
			for _, pkg := range view.packages {
				candidates = append(candidates, cranCandidate{repo: c, pkg: pkg})
			}
		}
	}
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "CRAN package index",
			pkgroute.SourceError(err, p.Anonymous)))
		return
	}
	for _, candidate := range candidates {
		if candidate.pkg.Path != req.path {
			continue
		}
		if candidate.repo.Class == repo.Remote {
			s.serveRemoteCranPackage(w, r, candidate.repo, cached, req, candidate.pkg, p, acc)
			return
		}
		it, getErr := st.Items().Get(r.Context(), candidate.repo.Key, false, req.path)
		if getErr != nil {
			writeErr(w, r, s.itemError(r.Context(), "CRAN member package", getErr))
			return
		}
		if it.Folder {
			writeErr(w, r, itemNotFound())
			return
		}
		s.serveFile(w, r, st, found{repo: candidate.repo, item: it}, p)
		return
	}
	writeErr(w, r, itemNotFound())
}

func (s artifactoryService) serveRemoteCranPackage(
	w http.ResponseWriter, r *http.Request, c repo.Config, cached bool,
	req itemRequest, expected cran.Package, p auth.Principal, acc access,
) {
	query := source.Request{
		Repo: c, Subject: p.Subject(), Access: acc.d, Project: c.ProjectKey,
		CacheOnly: cached,
		Resource: source.Resource{
			Kind: source.ResourceKindArtifact, Path: req.path, Method: r.Method,
			Checksums: types.Checksums{MD5: expected.MD5},
		},
	}
	query, err := cranSourceRequest(query)
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "CRAN source", err))
		return
	}
	result, err := s.cfg.RemoteSources.Read(r.Context(), query)
	if err != nil {
		writeErr(
			w,
			r,
			s.itemError(r.Context(), "CRAN source", pkgroute.SourceError(err, p.Anonymous)),
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
	if result.Item == nil || expected.MD5 != "" && result.Item.Actual.MD5 != expected.MD5 {
		writeErr(w, r, pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous))
		return
	}
	s.serveFile(w, r, s.cfg.Store,
		found{repo: c, item: *result.Item, used: result.Used}, p)
}
