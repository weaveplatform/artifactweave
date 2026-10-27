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
	"github.com/weaveplatform/artifactweave/internal/format/alpine"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const alpineIndexLimit = 36 << 20

var (
	errAlpineIndexTooLarge = errors.New("server: alpine index exceeds limit")
	errAlpineVirtualCycle  = errors.New("server: alpine virtual member cycle")
	errAlpineMemberType    = errors.New(
		"server: alpine virtual member has incompatible package type",
	)
)

type alpineIndexView struct {
	repo       repo.Config
	packages   []alpine.Package
	raw        []byte
	checkRead  func(context.Context, string) error
	revalidate func(context.Context) error
	used       func(context.Context) error
}

type alpineCandidate struct {
	repo        repo.Config
	packageInfo alpine.Package
}

func (s artifactoryService) tryAlpineItem(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) bool {
	if !alpine.Serves(c.PackageType) || req.isFolder {
		return false
	}
	if strings.HasSuffix(req.path, "/"+alpine.IndexName) {
		s.serveAlpineIndex(w, r, st, c, cached, req, p, acc)
		return true
	}
	if strings.HasSuffix(req.path, ".apk") {
		if _, _, _, _, ok := alpine.ParsePath(req.path); !ok {
			writeErr(w, r, itemNotFound())
			return true
		}
		if c.Class == repo.Virtual {
			s.serveVirtualAlpinePackage(w, r, st, c, req, p, acc)
			return true
		}
	}
	return false
}

func (s artifactoryService) serveAlpineIndex(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) {
	ctx := r.Context()
	if _, _, _, ok := alpine.ParseIndexPath(req.path); !ok || len(req.props) != 0 ||
		r.URL.RawQuery != "" {
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
	if c.Class == repo.Virtual {
		candidates, err := s.alpineCandidates(ctx, st, c, req.path, p, acc,
			map[string]bool{})
		if err != nil {
			writeErr(w, r, s.itemError(ctx, "Alpine virtual index", err))
			return
		}
		packages := make([]alpine.Package, 0, len(candidates))
		for _, candidate := range candidates {
			packages = append(packages, candidate.packageInfo)
		}
		keyName, key, err := alpine.SigningKey(ctx, st, c)
		if err != nil {
			writeErr(w, r, s.internalError(ctx, "Alpine virtual signing key", err))
			return
		}
		body, err = alpine.EncodeIndex(packages, "alpine "+c.Key, keyName, key)
		if err != nil {
			writeErr(w, r, s.internalError(ctx, "Alpine virtual index", err))
			return
		}
	} else {
		view, err := s.alpineMemberIndex(ctx, st, c, cached, req.path, p, acc)
		if err != nil {
			writeErr(w, r, s.itemError(ctx, "Alpine index", err))
			return
		}
		if view.revalidate != nil {
			if err := view.revalidate(ctx); err != nil {
				writeErr(w, r, pkgroute.SourceError(err, p.Anonymous))
				return
			}
		}
		for _, pkg := range view.packages {
			allowed, err := alpinePackageReadable(ctx, c, pkg.Path, acc, view.checkRead)
			if err != nil {
				writeErr(w, r, s.itemError(ctx, "Alpine index permission", err))
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
	http.ServeContent(sw, r, alpine.IndexName, time.Time{}, bytes.NewReader(body))
	if sw.complete(r) && used != nil {
		_ = used(ctx)
	}
}

func alpinePackageReadable(
	ctx context.Context, c repo.Config, path string, acc access,
	check func(context.Context, string) error,
) (bool, error) {
	if !acc.file(c, path, types.ActionRead) || generic.CheckDownload(c, path, false) != nil {
		return false, nil
	}
	if check != nil {
		if err := check(ctx, path); err != nil {
			if errors.Is(err, source.ErrDenied) {
				return false, nil
			}
			return false, fmt.Errorf("alpine package read authority: %w", err)
		}
	}
	return true, nil
}

func (s artifactoryService) alpineMemberIndex(
	ctx context.Context, st store.Store, c repo.Config, cached bool, path string,
	p auth.Principal, acc access,
) (alpineIndexView, error) {
	view := alpineIndexView{repo: c}
	if c.Class == repo.Remote {
		if s.cfg.RemoteSources == nil {
			return view, source.ErrUnavailable
		}
		query := source.Request{
			Repo: c, Subject: p.Subject(), Access: acc.d, Project: c.ProjectKey,
			CacheOnly: cached,
			Resource:  source.Resource{Path: path, Method: http.MethodGet},
		}
		query, err := alpineSourceRequest(query)
		if err != nil {
			return view, err
		}
		result, err := s.cfg.RemoteSources.Read(ctx, query)
		if err != nil {
			//nolint:wrapcheck // Preserve the classified source status.
			return view, pkgroute.SourceError(
				err,
				p.Anonymous,
			)
		}
		view.checkRead = result.CheckRead
		view.revalidate = result.Revalidate
		view.used = result.Used
		switch {
		case result.Stream != nil:
			defer func() { _ = result.Stream.Body.Close() }()
			view.raw, err = io.ReadAll(io.LimitReader(result.Stream.Body, alpineIndexLimit+1))
		case result.Item != nil:
			view.raw, err = s.readAlpineBlob(ctx, result.Item.Actual.SHA256)
		default:
			return view, source.ErrInvalidResult
		}
		if err != nil {
			return view, fmt.Errorf("alpine remote index: %w", err)
		}
	} else {
		if c.Class != repo.Local && c.Class != repo.Federated || cached {
			return view, store.ErrNotFound
		}
		it, err := st.Items().Get(ctx, c.Key, false, path)
		if err != nil {
			return view, err //nolint:wrapcheck // ErrNotFound is the answer.
		}
		if it.Folder {
			return view, store.ErrNotFound
		}
		view.raw, err = s.readAlpineBlob(ctx, it.Actual.SHA256)
		if err != nil {
			return view, err
		}
	}
	if len(view.raw) == 0 || len(view.raw) > alpineIndexLimit {
		return view, errAlpineIndexTooLarge
	}
	var err error
	view.packages, err = alpine.DecodeIndex(ctx, view.raw, path)
	if err != nil {
		return view, fmt.Errorf("alpine index parse: %w", err)
	}
	return view, nil
}

func (s artifactoryService) readAlpineBlob(ctx context.Context, sha string) ([]byte, error) {
	rc, _, err := s.cfg.Blobs.Open(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("alpine blob open: %w", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, alpineIndexLimit+1))
	if err != nil {
		return nil, fmt.Errorf("alpine blob read: %w", err)
	}
	return body, nil
}

// alpineCandidates walks ordered virtual members. A duplicate path belongs to
// the first readable member, which is the same member package GET must use.
func (s artifactoryService) alpineCandidates(
	ctx context.Context, st store.Store, c repo.Config, indexPath string,
	p auth.Principal, acc access, seen map[string]bool,
) ([]alpineCandidate, error) {
	if seen[c.Key] || len(seen) >= 8 {
		return nil, errAlpineVirtualCycle
	}
	seen[c.Key] = true
	defer delete(seen, c.Key)
	var out []alpineCandidate
	selected := map[string]bool{}
	for _, key := range c.Repositories {
		member, err := st.Repos().Get(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("alpine virtual member %s: %w", key, err)
		}
		if member.PackageType != repo.Alpine {
			return nil, fmt.Errorf("%w: %s", errAlpineMemberType, key)
		}
		if !acc.folder(member, indexPath, types.ActionRead) ||
			!acc.file(member, indexPath, types.ActionRead) ||
			generic.CheckDownload(member, indexPath, false) != nil {
			continue
		}
		var entries []alpineCandidate
		if member.Class == repo.Virtual {
			entries, err = s.alpineCandidates(ctx, st, member, indexPath, p, acc, seen)
		} else {
			var view alpineIndexView
			view, err = s.alpineMemberIndex(ctx, st, member, false, indexPath, p, acc)
			if err == nil && view.revalidate != nil {
				err = view.revalidate(ctx)
			}
			if err == nil {
				for _, pkg := range view.packages {
					allowed, checkErr := alpinePackageReadable(
						ctx, member, pkg.Path, acc, view.checkRead)
					if checkErr != nil {
						err = checkErr
						break
					}
					if allowed {
						entries = append(entries, alpineCandidate{repo: member, packageInfo: pkg})
					}
				}
			}
		}
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("alpine member %s index: %w", key, err)
		}
		for _, entry := range entries {
			if selected[entry.packageInfo.Path] ||
				!acc.file(c, entry.packageInfo.Path, types.ActionRead) ||
				generic.CheckDownload(c, entry.packageInfo.Path, false) != nil {
				continue
			}
			selected[entry.packageInfo.Path] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

func (s artifactoryService) serveVirtualAlpinePackage(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	req itemRequest, p auth.Principal, acc access,
) {
	if _, _, _, _, ok := alpine.ParsePath(req.path); !ok || len(req.props) != 0 ||
		r.URL.RawQuery != "" {
		writeErr(w, r, itemNotFound())
		return
	}
	if !acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		writeErr(w, r, denied(p))
		return
	}
	indexPath := req.path[:strings.LastIndexByte(req.path, '/')+1] + alpine.IndexName
	candidates, err := s.alpineCandidates(r.Context(), st, c, indexPath, p, acc,
		map[string]bool{})
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "Alpine virtual package", err))
		return
	}
	for _, candidate := range candidates {
		if candidate.packageInfo.Path != req.path {
			continue
		}
		if candidate.repo.Class == repo.Remote {
			s.serveRemoteAlpinePackage(w, r, candidate.repo, req, p, acc)
			return
		}
		it, err := st.Items().Get(r.Context(), candidate.repo.Key, false, req.path)
		if err != nil {
			writeErr(w, r, s.itemError(r.Context(), "Alpine virtual member", err))
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

func (s artifactoryService) serveRemoteAlpinePackage(
	w http.ResponseWriter, r *http.Request, c repo.Config, req itemRequest,
	p auth.Principal, acc access,
) {
	query := source.Request{
		Repo:    c,
		Subject: p.Subject(),
		Access:  acc.d,
		Project: c.ProjectKey,
		Resource: source.Resource{
			Kind:   source.ResourceKindArtifact,
			Path:   req.path,
			Method: r.Method,
		},
	}
	query, err := alpineSourceRequest(query)
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "Alpine source", err))
		return
	}
	result, err := s.cfg.RemoteSources.Read(r.Context(), query)
	if err != nil {
		writeErr(
			w,
			r,
			s.itemError(r.Context(), "Alpine source", pkgroute.SourceError(err, p.Anonymous)),
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
	if result.Item == nil {
		writeErr(w, r, s.internalError(r.Context(), "Alpine source", source.ErrInvalidResult))
		return
	}
	s.serveFile(w, r, s.cfg.Store,
		found{repo: c, item: *result.Item, used: result.Used}, p)
}
