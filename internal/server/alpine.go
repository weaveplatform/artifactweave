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
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/alpine"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
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

// alpineItems serves Alpine's indexes and packages over the item kit.
type alpineItems struct{ *Kit }

func tryAlpineItem(
	k *Kit, w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req ItemRequest, p kernel.Principal, acc Access,
) bool {
	s := alpineItems{k}
	if !alpine.Serves(c.PackageType) || req.IsFolder {
		return false
	}
	if strings.HasSuffix(req.Path, "/"+alpine.IndexName) {
		s.serveAlpineIndex(w, r, st, c, cached, req, p, acc)
		return true
	}
	if strings.HasSuffix(req.Path, ".apk") {
		if _, _, _, _, ok := alpine.ParsePath(req.Path); !ok {
			httpkit.WriteErr(w, r, ItemNotFound())
			return true
		}
		if c.Class == repo.Virtual {
			s.serveVirtualAlpinePackage(w, r, st, c, req, p, acc)
			return true
		}
	}
	return false
}

func (s alpineItems) serveAlpineIndex(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req ItemRequest, p kernel.Principal, acc Access,
) {
	ctx := r.Context()
	if _, _, _, ok := alpine.ParseIndexPath(req.Path); !ok || len(req.Props) != 0 ||
		r.URL.RawQuery != "" {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	if !acc.Folder(c, req.Path, types.ActionRead) ||
		!acc.File(c, req.Path, types.ActionRead) ||
		generic.CheckDownload(c, req.Path, false) != nil {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	var body []byte
	var used func(context.Context) error
	if c.Class == repo.Virtual {
		candidates, err := s.alpineCandidates(ctx, st, c, req.Path, p, acc,
			map[string]bool{})
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Alpine virtual index", err))
			return
		}
		packages := make([]alpine.Package, 0, len(candidates))
		for _, candidate := range candidates {
			packages = append(packages, candidate.packageInfo)
		}
		keyName, key, err := alpine.SigningKey(ctx, s.Access.Keys, c)
		if err != nil {
			httpkit.WriteErr(w, r, s.InternalError(ctx, "Alpine virtual signing key", err))
			return
		}
		body, err = alpine.EncodeIndex(packages, "alpine "+c.Key, keyName, key)
		if err != nil {
			httpkit.WriteErr(w, r, s.InternalError(ctx, "Alpine virtual index", err))
			return
		}
	} else {
		view, err := s.alpineMemberIndex(ctx, st, c, cached, req.Path, p, acc)
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Alpine index", err))
			return
		}
		if view.revalidate != nil {
			if err := view.revalidate(ctx); err != nil {
				httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
				return
			}
		}
		for _, pkg := range view.packages {
			allowed, err := alpinePackageReadable(ctx, c, pkg.Path, acc, view.checkRead)
			if err != nil {
				httpkit.WriteErr(w, r, s.ItemError(ctx, "Alpine index permission", err))
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
	http.ServeContent(sw, r, alpine.IndexName, time.Time{}, bytes.NewReader(body))
	if sw.Complete(r) && used != nil {
		_ = used(ctx)
	}
}

func alpinePackageReadable(
	ctx context.Context, c repo.Config, path string, acc Access,
	check func(context.Context, string) error,
) (bool, error) {
	if !acc.File(c, path, types.ActionRead) || generic.CheckDownload(c, path, false) != nil {
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

func (s alpineItems) alpineMemberIndex(
	ctx context.Context, st store.Store, c repo.Config, cached bool, path string,
	p kernel.Principal, acc Access,
) (alpineIndexView, error) {
	view := alpineIndexView{repo: c}
	if c.Class == repo.Remote {
		if s.RemoteSources == nil {
			return view, source.ErrUnavailable
		}
		query := source.Request{
			Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
			CacheOnly: cached,
			Resource:  source.Resource{Path: path, Method: http.MethodGet},
		}
		query, err := AlpineSourceRequest(query)
		if err != nil {
			return view, err
		}
		result, err := s.RemoteSources.Read(ctx, query)
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

func (s alpineItems) readAlpineBlob(ctx context.Context, sha string) ([]byte, error) {
	rc, _, err := s.Blobs.Open(ctx, sha)
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
func (s alpineItems) alpineCandidates(
	ctx context.Context, st store.Store, c repo.Config, indexPath string,
	p kernel.Principal, acc Access, seen map[string]bool,
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
		if !acc.Folder(member, indexPath, types.ActionRead) ||
			!acc.File(member, indexPath, types.ActionRead) ||
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
				!acc.File(c, entry.packageInfo.Path, types.ActionRead) ||
				generic.CheckDownload(c, entry.packageInfo.Path, false) != nil {
				continue
			}
			selected[entry.packageInfo.Path] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

func (s alpineItems) serveVirtualAlpinePackage(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	req ItemRequest, p kernel.Principal, acc Access,
) {
	if _, _, _, _, ok := alpine.ParsePath(req.Path); !ok || len(req.Props) != 0 ||
		r.URL.RawQuery != "" {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	if !acc.File(c, req.Path, types.ActionRead) ||
		generic.CheckDownload(c, req.Path, false) != nil {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	indexPath := req.Path[:strings.LastIndexByte(req.Path, '/')+1] + alpine.IndexName
	candidates, err := s.alpineCandidates(r.Context(), st, c, indexPath, p, acc,
		map[string]bool{})
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Alpine virtual package", err))
		return
	}
	for _, candidate := range candidates {
		if candidate.packageInfo.Path != req.Path {
			continue
		}
		if candidate.repo.Class == repo.Remote {
			s.serveRemoteAlpinePackage(w, r, candidate.repo, req, p, acc)
			return
		}
		it, err := st.Items().Get(r.Context(), candidate.repo.Key, false, req.Path)
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Alpine virtual member", err))
			return
		}
		if it.Folder {
			httpkit.WriteErr(w, r, ItemNotFound())
			return
		}
		s.ServeFile(w, r, st, Found{Repo: candidate.repo, Item: it}, p)
		return
	}
	httpkit.WriteErr(w, r, ItemNotFound())
}

func (s alpineItems) serveRemoteAlpinePackage(
	w http.ResponseWriter, r *http.Request, c repo.Config, req ItemRequest,
	p kernel.Principal, acc Access,
) {
	query := source.Request{
		Repo:    c,
		Subject: accessapi.SubjectOf(p),
		Access:  acc.Decider,
		Project: c.ProjectKey,
		Resource: source.Resource{
			Kind:   source.ResourceKindArtifact,
			Path:   req.Path,
			Method: r.Method,
		},
	}
	query, err := AlpineSourceRequest(query)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Alpine source", err))
		return
	}
	result, err := s.RemoteSources.Read(r.Context(), query)
	if err != nil {
		httpkit.WriteErr(
			w,
			r,
			s.ItemError(r.Context(), "Alpine source", pkgroute.SourceError(err, p.Anonymous)),
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
	if result.Item == nil {
		httpkit.WriteErr(
			w,
			r,
			s.InternalError(r.Context(), "Alpine source", source.ErrInvalidResult),
		)
		return
	}
	s.ServeFile(w, r, s.Store,
		Found{Repo: c, Item: *result.Item, Used: result.Used}, p)
}
