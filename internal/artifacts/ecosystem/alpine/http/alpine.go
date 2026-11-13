package alpinehttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/alpine"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
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
	repo     repo.Config
	packages []alpine.Package
	raw      []byte
	// checkPermission is a remote index read's permission-only check of
	// each package the index lists (source.Result.CheckPermission).
	checkPermission func(context.Context, string) error
	revalidate      func(context.Context) error
	used            func(context.Context) error
	// stored is a local or federated repository's index item, whose
	// properties a request's matrix parameters are matched against.
	stored *model.Item
}

type alpineCandidate struct {
	repo        repo.Config
	packageInfo alpine.Package
}

// alpineItems serves Alpine's indexes and packages over the item kit.
type alpineItems struct{ *itemkit.Kit }

// TryAlpineItem is the overlay (itemkit.Overlay) that serves an Alpine
// repository's APKINDEX.tar.gz files and a virtual repository's .apk
// packages, answering 404 for a package path the format refuses. It
// reports whether it answered; every other request, a folder among them,
// it leaves to the overlays after it and the generic lookup.
func TryAlpineItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
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
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
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
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	ctx := r.Context()
	if _, _, _, ok := alpine.ParseIndexPath(req.Path); !ok {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !acc.Folder(c, req.Path, accessapi.ActionRead) ||
		!acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	// 7.x calculates and stores a local index, caches a remote's and
	// serves a virtual repository's as a file at this path, so a
	// blacked-out repository or patterns that reject the index's own path
	// answer the policy's 404 and message once read permission is settled
	// (DV-057); a virtual repository's patterns answer that no repository
	// serves it. A remote's -cache alias answers as the remote does.
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Alpine index download", err))
		return
	}
	if cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	// An index takes a query and matrix parameters as any other file's GET
	// does (#1350): 7.x documents skipUpdateStats and trace for a GET of any
	// file (RTFACT-25636, 7.21.3), so a query changes nothing here, and
	// matrix parameters "make resolution conditional on properties"
	// (JFrog's guide, Using Properties in Deployment and Resolution). A
	// virtual repository's composed index carries no properties [I], so
	// only a mandatory key refuses it; a stored index is matched below, and
	// a remote's as any remote file is (#1371).
	switch c.Class {
	case repo.Virtual:
		if !req.Matrix().Matches(nil) {
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
			return
		}
	case repo.Remote:
		matches, err := itemkit.RemoteCopyMatches(ctx, st, c, req.Path, req)
		if err != nil {
			httpkit.WriteErr(w, r, s.InternalError(ctx, "Alpine index properties", err))
			return
		}
		if !matches {
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
			return
		}
	}
	var body []byte
	var used func(context.Context) error
	if c.Class == repo.Virtual {
		candidates, err := s.alpineCandidates(ctx, st, c, req.Path, p, acc, false,
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
		view, err := s.alpineIndexRead(ctx, st, c, cached, req.Path,
			itemkit.RemoteResource(r, req.Path).Query, pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
			p, acc)
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Alpine index", err))
			return
		}
		if view.stored != nil {
			matches, err := itemkit.MatchesMatrix(ctx, st, *view.stored, req)
			if err != nil {
				httpkit.WriteErr(w, r, s.InternalError(ctx, "Alpine index properties", err))
				return
			}
			if !matches {
				httpkit.WriteErr(w, r, itemkit.ItemNotFound())
				return
			}
		}
		if view.revalidate != nil {
			if err := view.revalidate(ctx); err != nil {
				httpkit.WriteErr(w, r, pkgroute.SourceError(err, p.Anonymous))
				return
			}
		}
		for _, pkg := range view.packages {
			allowed, err := alpinePackageReadable(ctx, c, pkg.Path, acc, view.checkPermission)
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
	sw := &itemkit.StatusWriter{ResponseWriter: w}
	itemkit.ServeContent(sw, r, alpine.IndexName, time.Time{}, bytes.NewReader(body))
	if sw.Complete(r) && used != nil {
		_ = used(ctx)
	}
}

// alpinePackageReadable reports whether the caller has Read permission on
// a package an index lists, in c and, through check, at a remote's source.
// It is permission alone: an index keeps a package whose file the patterns
// reject and only that file's GET is refused, as 7.x keeps such a package
// in a Debian or RPM index (RTFACT-30830, RTFACT-30656).
func alpinePackageReadable(
	ctx context.Context, c repo.Config, path string, acc itemkit.Access,
	check func(context.Context, string) error,
) (bool, error) {
	if !acc.File(c, path, accessapi.ActionRead) {
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

// alpineMemberIndex reads the index at path of a local, federated or
// remote repository as a lookup: a virtual index's member, which the client
// did not ask for, so nothing of the client's request goes to a remote's
// upstream.
func (s alpineItems) alpineMemberIndex(
	ctx context.Context, st itemkit.Store, c repo.Config, cached bool, path string,
	p kernel.Principal, acc itemkit.Access,
) (alpineIndexView, error) {
	return s.alpineIndexRead(ctx, st, c, cached, path, "", "", p, acc)
}

// alpineIndexRead reads the index at path as alpineMemberIndex does, and
// passes a remote's upstream the client's query (only under Propagate
// Query Params, which the source boundary applies) and its matrix
// parameters (source.Request.MatrixParams), as for any remote file.
func (s alpineItems) alpineIndexRead(
	ctx context.Context, st itemkit.Store, c repo.Config, cached bool,
	path, rawQuery, matrix string, p kernel.Principal, acc itemkit.Access,
) (alpineIndexView, error) {
	view := alpineIndexView{repo: c}
	if c.Class == repo.Remote {
		if s.RemoteSources == nil {
			return view, source.ErrUnavailable
		}
		query := source.Request{
			Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
			CacheOnly: cached, MatrixParams: matrix,
			Resource: source.Resource{Path: path, Method: http.MethodGet, Query: rawQuery},
		}
		query, err := itemkit.AlpineSourceRequest(query)
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
		view.checkPermission = result.CheckPermission
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
			return view, kernel.ErrNotFound
		}
		it, err := st.Items().Get(ctx, c.Key, false, path)
		if err != nil {
			return view, err //nolint:wrapcheck // ErrNotFound is the answer.
		}
		if it.Folder {
			return view, kernel.ErrNotFound
		}
		view.stored = &it
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
// the first readable member; for a package GET (download true) the later
// members' rows for it follow, so a GET whose matrix parameters the first
// member's file does not match can pass over to the next
// (serveVirtualAlpinePackage). A member whose download policy refuses its
// index takes no part in the index, and only a blacked-out one in a package
// GET (alpineMemberRefusesIndex). With download false, for the index, a row
// stays when the member's or the virtual repository's patterns reject its
// file, which the index keeps as 7.x's virtual indexes do (RTFACT-30656).
// With download true, for a package GET, such a row is left out, so the GET
// passes over that member to the next one that lists the path.
func (s alpineItems) alpineCandidates(
	ctx context.Context, st itemkit.Store, c repo.Config, indexPath string,
	p kernel.Principal, acc itemkit.Access, download bool, seen map[string]bool,
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
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("alpine virtual member %s: %w", key, err)
		}
		if member.PackageType != repo.Alpine {
			return nil, fmt.Errorf("%w: %s", errAlpineMemberType, key)
		}
		if !acc.Folder(member, indexPath, accessapi.ActionRead) ||
			!acc.File(member, indexPath, accessapi.ActionRead) ||
			alpineMemberRefusesIndex(member, indexPath, download) {
			continue
		}
		var entries []alpineCandidate
		if member.Class == repo.Virtual {
			entries, err = s.alpineCandidates(ctx, st, member, indexPath, p, acc, download, seen)
		} else {
			var view alpineIndexView
			view, err = s.alpineMemberIndex(ctx, st, member, false, indexPath, p, acc)
			if err == nil && view.revalidate != nil {
				err = view.revalidate(ctx)
			}
			if err == nil {
				for _, pkg := range view.packages {
					if download && generic.CheckDownload(member, pkg.Path, false) != nil {
						continue
					}
					allowed, checkErr := alpinePackageReadable(
						ctx, member, pkg.Path, acc, view.checkPermission)
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
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("alpine member %s index: %w", key, err)
		}
		for _, entry := range entries {
			if !download && selected[entry.packageInfo.Path] ||
				!acc.File(c, entry.packageInfo.Path, accessapi.ActionRead) {
				continue
			}
			if download && generic.CheckDownload(c, entry.packageInfo.Path, false) != nil {
				continue
			}
			selected[entry.packageInfo.Path] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

// alpineMemberRefusesIndex reports whether a virtual repository's member
// takes no part because its download policy refuses the index at
// indexPath. The virtual index leaves out a member that refuses the
// document. A package GET checks each member at the package's own path, as
// 6.20's VirtualRepoDownloadStrategy does and as the patterns FAQ puts it
// ("artifacts that match an exclude pattern are not resolved from that
// repository"), so there the index is only the lookup that finds the file:
// a blacked-out member, which refuses every file, takes no part, and
// patterns that reject only the index leave the member's packages served.
func alpineMemberRefusesIndex(member repo.Config, indexPath string, download bool) bool {
	if !download {
		return generic.CheckDownload(member, indexPath, false) != nil
	}
	return member.Class != repo.Virtual && generic.CheckBlackedOut(member, indexPath) != nil
}

func (s alpineItems) serveVirtualAlpinePackage(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	if _, _, _, _, ok := alpine.ParsePath(req.Path); !ok {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Alpine virtual package download", err))
		return
	}
	indexPath := req.Path[:strings.LastIndexByte(req.Path, '/')+1] + alpine.IndexName
	candidates, err := s.alpineCandidates(r.Context(), st, c, indexPath, p, acc, true,
		map[string]bool{})
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Alpine virtual package", err))
		return
	}
	// A member's file that the matrix parameters do not match does not
	// serve the GET, and the next member that lists the package is asked,
	// as 7.x's virtual download passes over a member that does not find the
	// file (ArtifactoryVirtualDownloadStrategy.processStandard, 7.161.26),
	// a property mismatch among the reasons (DbStoringRepoMixin.getInfo)
	// [I]. A remote member's copy is matched only when its cache holds one.
	for _, candidate := range candidates {
		if candidate.packageInfo.Path != req.Path {
			continue
		}
		var matches bool
		if candidate.repo.Class == repo.Remote {
			matches, err = itemkit.RemoteCopyMatches(r.Context(), st, candidate.repo, req.Path, req)
		} else {
			var it model.Item
			it, err = st.Items().Get(r.Context(), candidate.repo.Key, false, req.Path)
			if err != nil {
				httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Alpine virtual member", err))
				return
			}
			if it.Folder {
				httpkit.WriteErr(w, r, itemkit.ItemNotFound())
				return
			}
			matches, err = itemkit.MatchesMatrix(r.Context(), st, it, req)
			if err == nil && matches {
				s.ServeItemFile(w, r, st, itemkit.Found{Repo: candidate.repo, Item: it}, p)
				return
			}
		}
		if err != nil {
			httpkit.WriteErr(w, r,
				s.InternalError(r.Context(), "Alpine virtual member properties", err))
			return
		}
		if !matches {
			continue
		}
		s.serveRemoteAlpinePackage(w, r, candidate.repo, req, p, acc)
		return
	}
	httpkit.WriteErr(w, r, itemkit.ItemNotFound())
}

func (s alpineItems) serveRemoteAlpinePackage(
	w http.ResponseWriter, r *http.Request, c repo.Config, req itemkit.ItemRequest,
	p kernel.Principal, acc itemkit.Access,
) {
	// The missing service is this server's fault, not the upstream's, so it
	// answers 500 as the index read does rather than SourceError's 502.
	if s.RemoteSources == nil {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "Alpine source", source.ErrUnavailable))
		return
	}
	// The upstream is asked with the client's query, under Propagate Query
	// Params, and its matrix parameters, as for any remote file (#1371).
	query := source.Request{
		Repo:         c,
		Subject:      accessapi.SubjectOf(p),
		Access:       acc.Decider,
		Project:      c.ProjectKey,
		MatrixParams: pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
		Resource: source.Resource{
			Kind:   source.ResourceKindArtifact,
			Path:   req.Path,
			Method: r.Method,
			Query:  itemkit.RemoteResource(r, req.Path).Query,
		},
	}
	query, err := itemkit.AlpineSourceRequest(query)
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
	s.ServeItemFile(w, r, s.Store,
		itemkit.Found{Repo: c, Item: *result.Item, Used: result.Used}, p)
}
