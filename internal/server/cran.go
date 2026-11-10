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
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/cran"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var (
	errCranVirtualCycle = errors.New("server: cran virtual member cycle")
	errCranMemberType   = errors.New("server: cran virtual member has incompatible package type")
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

// cranAPI serves CRAN's indexes and packages over the item kit.
type cranAPI struct{ *itemkit.Kit }

func tryCranItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	s := cranAPI{k}
	if !cran.Serves(c.PackageType) || req.IsFolder {
		return false
	}
	if strings.HasSuffix(req.Path, "/PACKAGES.rds") {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound()) // R falls back to PACKAGES.gz.
		return true
	}
	if _, _, ok := cran.IndexPath(req.Path); ok {
		s.serveCranIndex(w, r, st, c, cached, req, p, acc)
		return true
	}
	if cran.IsPackage(req.Path) {
		if c.Class == repo.Virtual || c.Class == repo.Remote {
			s.serveCranPackage(w, r, st, c, cached, req, p, acc)
			return true
		}
		return false
	}
	return false
}

func (s cranAPI) serveCranIndex(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	ctx := r.Context()
	feed, name, ok := cran.IndexPath(req.Path)
	if !ok || len(req.Props) != 0 || r.URL.RawQuery != "" ||
		cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !acc.Folder(c, feed, types.ActionRead) || !acc.File(c, req.Path, types.ActionRead) ||
		generic.CheckDownload(c, req.Path, false) != nil {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	var candidates []cranCandidate
	var err error
	var used func(context.Context) error
	if c.Class == repo.Virtual {
		candidates, err = s.cranCandidates(ctx, st, c, req.Path, p, acc, map[string]bool{})
	} else {
		var view cranIndexView
		view, err = s.cranMemberIndex(ctx, st, c, cached, req.Path, p, acc)
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
		httpkit.WriteErr(
			w,
			r,
			s.ItemError(ctx, "CRAN index", pkgroute.SourceError(err, p.Anonymous)),
		)
		return
	}
	packages := make([]cran.Package, 0, len(candidates))
	for _, candidate := range candidates {
		packages = append(packages, candidate.pkg)
	}
	generated, err := cran.EncodeIndex(packages, feed)
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "CRAN index", err))
		return
	}
	body := generated[name]
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if name == cran.PackagesGzipName {
		w.Header().Set("Content-Type", "application/gzip")
	}
	w.Header().Set("Cache-Control", "private, no-store")
	sw := &itemkit.StatusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, name, time.Time{}, bytes.NewReader(body))
	if sw.Complete(r) && used != nil {
		_ = used(ctx)
	}
}

func cranPackageReadable(
	ctx context.Context, c repo.Config, p string, acc itemkit.Access,
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
			return false, fmt.Errorf("cran: package read authority: %w", err)
		}
	}
	return true, nil
}

func (s cranAPI) cranMemberIndex(
	ctx context.Context, st store.Store, c repo.Config, cached bool, indexPath string,
	p kernel.Principal, acc itemkit.Access,
) (cranIndexView, error) {
	view := cranIndexView{repo: c}
	var body []byte
	var err error
	if c.Class == repo.Remote {
		if s.RemoteSources == nil {
			return view, fmt.Errorf("cran: remote index source: %w",
				pkgroute.SourceError(source.ErrUnavailable, p.Anonymous))
		}
		query := source.Request{
			Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
			CacheOnly: cached,
			Resource:  source.Resource{Path: indexPath, Method: http.MethodGet},
		}
		query, err = itemkit.CranSourceRequest(query)
		if err != nil {
			return view, err
		}
		result, err := s.RemoteSources.Read(ctx, query)
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
		if err := s.Await(ctx, st, cran.KindIndex, c.Key); err != nil {
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

func (s cranAPI) readCranBlob(ctx context.Context, sha string) ([]byte, error) {
	rc, _, err := s.Blobs.Open(ctx, sha)
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

func (s cranAPI) cranCandidates(
	ctx context.Context, st store.Store, c repo.Config, indexPath string,
	p kernel.Principal, acc itemkit.Access, seen map[string]bool,
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
		if !acc.Folder(member, feed, types.ActionRead) ||
			!acc.File(member, indexPath, types.ActionRead) ||
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
				!acc.File(c, entry.pkg.Path, types.ActionRead) ||
				generic.CheckDownload(c, entry.pkg.Path, false) != nil {
				continue
			}
			selected[entry.pkg.Name] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

func (s cranAPI) serveCranPackage(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	if len(req.Props) != 0 || r.URL.RawQuery != "" || cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !acc.File(c, req.Path, types.ActionRead) ||
		generic.CheckDownload(c, req.Path, false) != nil {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	feed, _, _, _ := cran.PackagePath(req.Path)
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
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "CRAN package index",
			pkgroute.SourceError(err, p.Anonymous)))
		return
	}
	for _, candidate := range candidates {
		if candidate.pkg.Path != req.Path {
			continue
		}
		if candidate.repo.Class == repo.Remote {
			s.serveRemoteCranPackage(w, r, candidate.repo, cached, req, candidate.pkg, p, acc)
			return
		}
		it, getErr := st.Items().Get(r.Context(), candidate.repo.Key, false, req.Path)
		if getErr != nil {
			httpkit.WriteErr(w, r, s.ItemError(r.Context(), "CRAN member package", getErr))
			return
		}
		if it.Folder {
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
			return
		}
		s.ServeFile(w, r, st, itemkit.Found{Repo: candidate.repo, Item: it}, p)
		return
	}
	httpkit.WriteErr(w, r, itemkit.ItemNotFound())
}

func (s cranAPI) serveRemoteCranPackage(
	w http.ResponseWriter, r *http.Request, c repo.Config, cached bool,
	req itemkit.ItemRequest, expected cran.Package, p kernel.Principal, acc itemkit.Access,
) {
	query := source.Request{
		Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
		CacheOnly: cached,
		Resource: source.Resource{
			Kind: source.ResourceKindArtifact, Path: req.Path, Method: r.Method,
			Checksums: types.Checksums{MD5: expected.MD5},
		},
	}
	query, err := itemkit.CranSourceRequest(query)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "CRAN source", err))
		return
	}
	result, err := s.RemoteSources.Read(r.Context(), query)
	if err != nil {
		httpkit.WriteErr(
			w,
			r,
			s.ItemError(r.Context(), "CRAN source", pkgroute.SourceError(err, p.Anonymous)),
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
	if result.Item == nil || expected.MD5 != "" && result.Item.Actual.MD5 != expected.MD5 {
		httpkit.WriteErr(w, r, pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous))
		return
	}
	s.ServeFile(w, r, s.Store,
		itemkit.Found{Repo: c, Item: *result.Item, Used: result.Used}, p)
}
