package opkghttp

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

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

var (
	errOpkgVirtualCycle = errors.New("server: opkg virtual member cycle")
	errOpkgMemberType   = errors.New("server: opkg virtual member has incompatible package type")
	errOpkgIndexLarge   = errors.New("server: opkg index exceeds limit")
)

type opkgIndexView struct {
	repo     repo.Config
	packages []opkg.Package
	raw      []byte
	// checkPermission is a remote feed read's permission-only check of
	// each package the feed lists (source.Result.CheckPermission).
	checkPermission func(context.Context, string) error
	revalidate      func(context.Context) error
	used            func(context.Context) error
	// stored is a local or federated repository's feed item, whose
	// properties a request's matrix parameters are matched against.
	stored *model.Item
}

type opkgCandidate struct {
	repo repo.Config
	pkg  opkg.Package
}

// opkgItems serves opkg's indexes and packages over the item kit.
type opkgItems struct{ *itemkit.Kit }

// TryOpkgItem is the overlay (itemkit.Overlay) that serves an Opkg
// repository's feed: its Packages and Packages.gz, every feed file of a
// virtual repository, and the IPKs of a virtual or remote repository,
// answering 404 for an IPK path the format refuses. It reports whether it
// answered; every other request, a folder among them, it leaves to the
// overlays after it and the generic lookup.
func TryOpkgItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
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
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
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
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	ctx := r.Context()
	feed, ok := opkg.FeedPath(req.Path)
	if !ok || cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !acc.Folder(c, req.Path, accessapi.ActionRead) ||
		!acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	// 7.x calculates and stores a local feed's Packages and caches a
	// remote's at this path, so a blacked-out repository or patterns that
	// reject the file's own path answer the policy's 404 and message once
	// read permission is settled (DV-057); a virtual repository's patterns
	// answer that no repository serves it. A remote's -cache alias answers
	// as the remote does.
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Opkg feed download", err))
		return
	}
	// A feed file takes a query and matrix parameters as any other file's
	// GET does (#1350): 7.x documents skipUpdateStats and trace for a GET of
	// any file (RTFACT-25636, 7.21.3), so a query changes nothing here, and
	// matrix parameters "make resolution conditional on properties"
	// (JFrog's guide, Using Properties in Deployment and Resolution). A
	// virtual repository's composed feed carries no properties [I], so only
	// a mandatory key refuses it; a stored feed is matched below, and a
	// remote's as any remote file is (#1371).
	if c.Class == repo.Virtual && !req.Matrix().Matches(nil) {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	var body []byte
	var used func(context.Context) error
	if c.Class == repo.Virtual {
		indexPath := opkg.IndexPath(feed, opkg.PackagesName)
		candidates, err := s.opkgCandidates(ctx, st, c, indexPath, p, acc, false,
			map[string]bool{})
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
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
			return
		}
	} else {
		if c.Class == repo.Remote {
			matches, err := itemkit.RemoteCopyMatches(ctx, st, c, req.Path, req)
			if err != nil {
				httpkit.WriteErr(w, r, s.InternalError(ctx, "Opkg feed properties", err))
				return
			}
			if !matches {
				httpkit.WriteErr(w, r, itemkit.ItemNotFound())
				return
			}
		}
		view, err := s.opkgIndexRead(ctx, st, c, cached, req.Path,
			itemkit.RemoteResource(r, req.Path).Query, pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
			p, acc)
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Opkg feed", err))
			return
		}
		if view.stored != nil {
			matches, err := itemkit.MatchesMatrix(ctx, st, *view.stored, req)
			if err != nil {
				httpkit.WriteErr(w, r, s.InternalError(ctx, "Opkg feed properties", err))
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
			allowed, err := opkgPackageReadable(ctx, c, pkg.Path, acc, view.checkPermission)
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
	sw := &itemkit.StatusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, path.Base(req.Path), time.Time{}, bytes.NewReader(body))
	if sw.Complete(r) && used != nil {
		_ = used(ctx)
	}
}

// opkgPackageReadable reports whether the caller has Read permission on a
// package a feed lists, in c and, through check, at a remote's source. It
// is permission alone: a feed keeps a package whose file the patterns
// reject and only that file's GET is refused, as 7.x keeps such a package
// in a Debian or RPM index (RTFACT-30830, RTFACT-30656).
func opkgPackageReadable(
	ctx context.Context, c repo.Config, p string, acc itemkit.Access,
	check func(context.Context, string) error,
) (bool, error) {
	if !acc.File(c, p, accessapi.ActionRead) {
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

// opkgMemberIndex reads the feed at indexPath of a local, federated or
// remote repository as a lookup: a virtual feed's member or the feed that
// pins an IPK's SHA-256, which the client did not ask for, so nothing of
// the client's request goes to a remote's upstream.
func (s opkgItems) opkgMemberIndex(
	ctx context.Context, st itemkit.Store, c repo.Config, cached bool, indexPath string,
	p kernel.Principal, acc itemkit.Access,
) (opkgIndexView, error) {
	return s.opkgIndexRead(ctx, st, c, cached, indexPath, "", "", p, acc)
}

// opkgIndexRead reads the feed at indexPath as opkgMemberIndex does, and
// passes a remote's upstream the client's query (only under Propagate
// Query Params, which the source boundary applies) and its matrix
// parameters (source.Request.MatrixParams), as for any remote file.
func (s opkgItems) opkgIndexRead(
	ctx context.Context, st itemkit.Store, c repo.Config, cached bool,
	indexPath, rawQuery, matrix string, p kernel.Principal, acc itemkit.Access,
) (opkgIndexView, error) {
	view := opkgIndexView{repo: c}
	if c.Class == repo.Remote {
		if s.RemoteSources == nil {
			return view, source.ErrUnavailable
		}
		query := source.Request{
			Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
			CacheOnly: cached, MatrixParams: matrix,
			Resource: source.Resource{Path: indexPath, Method: http.MethodGet, Query: rawQuery},
		}
		query, err := itemkit.OpkgSourceRequest(query)
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
		view.checkPermission, view.revalidate, view.used =
			result.CheckPermission, result.Revalidate, result.Used
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
			return view, kernel.ErrNotFound
		}
		it, err := st.Items().Get(ctx, c.Key, false, indexPath)
		if err != nil {
			return view, err //nolint:wrapcheck
		}
		if it.Folder {
			return view, kernel.ErrNotFound
		}
		view.stored = &it
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

// opkgCandidates walks ordered virtual members. A duplicate path belongs to
// the first readable member; for an IPK GET (download true) the later
// members' rows for it follow, so a GET whose matrix parameters the first
// member's file does not match can pass over to the next (serveOpkgPackage).
// A member whose download policy refuses its
// feed takes no part in the feed, and only a blacked-out one in an IPK GET
// (opkgMemberRefusesFeed). With download false, for the feed, a row stays
// when the member's or the virtual repository's patterns reject its file,
// which the feed keeps as 7.x's virtual indexes do (RTFACT-30656). With
// download true, for an IPK GET, such a row is left out, so the GET passes
// over that member to the next one that lists the path.
func (s opkgItems) opkgCandidates(
	ctx context.Context, st itemkit.Store, c repo.Config, indexPath string,
	p kernel.Principal, acc itemkit.Access, download bool, seen map[string]bool,
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
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("opkg: virtual member %s: %w", key, err)
		}
		if member.PackageType != repo.Opkg {
			return nil, fmt.Errorf("%w: %s", errOpkgMemberType, key)
		}
		if !acc.Folder(member, indexPath, accessapi.ActionRead) ||
			!acc.File(member, indexPath, accessapi.ActionRead) ||
			opkgMemberRefusesFeed(member, indexPath, download) {
			continue
		}
		var entries []opkgCandidate
		if member.Class == repo.Virtual {
			entries, err = s.opkgCandidates(ctx, st, member, indexPath, p, acc, download, seen)
		} else {
			var view opkgIndexView
			view, err = s.opkgMemberIndex(ctx, st, member, false, indexPath, p, acc)
			if err == nil && view.revalidate != nil {
				err = view.revalidate(ctx)
			}
			if err == nil {
				for _, pkg := range view.packages {
					if download && generic.CheckDownload(member, pkg.Path, false) != nil {
						continue
					}
					allowed, checkErr := opkgPackageReadable(
						ctx, member, pkg.Path, acc, view.checkPermission)
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
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("opkg: member %s index: %w", key, err)
		}
		for _, entry := range entries {
			if !download && selected[entry.pkg.Path] ||
				!acc.File(c, entry.pkg.Path, accessapi.ActionRead) {
				continue
			}
			if download && generic.CheckDownload(c, entry.pkg.Path, false) != nil {
				continue
			}
			selected[entry.pkg.Path] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

// opkgMemberRefusesFeed reports whether a virtual repository's member takes
// no part because its download policy refuses the feed at indexPath. The
// virtual feed leaves out a member that refuses the document. An IPK GET
// checks each member at the IPK's own path, as 6.20's
// VirtualRepoDownloadStrategy does and as the patterns FAQ puts it
// ("artifacts that match an exclude pattern are not resolved from that
// repository"), so there the feed is only the lookup that finds the file: a
// blacked-out member, which refuses every file, takes no part, and patterns
// that reject only the feed leave the member's IPKs served.
func opkgMemberRefusesFeed(member repo.Config, indexPath string, download bool) bool {
	if !download {
		return generic.CheckDownload(member, indexPath, false) != nil
	}
	return member.Class != repo.Virtual && generic.CheckBlackedOut(member, indexPath) != nil
}

func (s opkgItems) serveOpkgPackage(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	if cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	// The IPK's own path decides, as a download does: a blacked-out remote
	// or patterns that reject the file answer the policy's 404 and message,
	// and a virtual repository's patterns answer that no repository serves
	// it. The feed read below, which pins the download's SHA-256, checks a
	// remote's blackout alone (itemkit.OpkgSourceRequest reads it at the
	// repository root), so patterns that reject only the feed's path leave
	// the IPK served, as a download checks only its own path.
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Opkg package download", err))
		return
	}
	// A remote's cached copy of the IPK is matched against the matrix
	// parameters before the feed that pins it is read, so a copy that does
	// not match asks the upstream nothing (itemkit.RemoteCopyMatches).
	if c.Class == repo.Remote {
		matches, err := itemkit.RemoteCopyMatches(r.Context(), st, c, req.Path, req)
		if err != nil {
			httpkit.WriteErr(w, r, s.InternalError(r.Context(), "Opkg package properties", err))
			return
		}
		if !matches {
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
			return
		}
	}
	indexPath := opkg.IndexPath(opkg.Feed(req.Path), opkg.PackagesName)
	var candidates []opkgCandidate
	var err error
	if c.Class == repo.Virtual {
		candidates, err = s.opkgCandidates(r.Context(), st, c, indexPath, p, acc, true,
			map[string]bool{})
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
	// A member's file that the matrix parameters do not match does not
	// serve the GET, and the next member that lists the IPK is asked, as
	// 7.x's virtual download passes over a member that does not find the
	// file (ArtifactoryVirtualDownloadStrategy.processStandard, 7.161.26),
	// a property mismatch among the reasons (DbStoringRepoMixin.getInfo)
	// [I]. A remote member's copy is matched only when its cache holds one.
	for _, candidate := range candidates {
		if candidate.pkg.Path != req.Path {
			continue
		}
		if candidate.repo.Class == repo.Remote {
			if c.Class == repo.Virtual {
				matches, err := itemkit.RemoteCopyMatches(
					r.Context(), st, candidate.repo, req.Path, req)
				if err != nil {
					httpkit.WriteErr(w, r,
						s.InternalError(r.Context(), "Opkg member package properties", err))
					return
				}
				if !matches {
					continue
				}
			}
			s.serveRemoteOpkgPackage(w, r, candidate.repo, cached, req, candidate.pkg, p, acc)
			return
		}
		it, err := st.Items().Get(r.Context(), candidate.repo.Key, false, req.Path)
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Opkg member package", err))
			return
		}
		if it.Folder || it.Actual.SHA256 != candidate.pkg.SHA256 {
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
			return
		}
		matches, err := itemkit.MatchesMatrix(r.Context(), st, it, req)
		if err != nil {
			httpkit.WriteErr(w, r,
				s.InternalError(r.Context(), "Opkg member package properties", err))
			return
		}
		if !matches {
			continue
		}
		s.ServeFile(w, r, st, itemkit.Found{Repo: candidate.repo, Item: it}, p)
		return
	}
	httpkit.WriteErr(w, r, itemkit.ItemNotFound())
}

func (s opkgItems) serveRemoteOpkgPackage(
	w http.ResponseWriter, r *http.Request, c repo.Config, cached bool,
	req itemkit.ItemRequest, expected opkg.Package, p kernel.Principal, acc itemkit.Access,
) {
	// The missing service is this server's fault, not the upstream's, so it
	// answers 500 as the index read does rather than SourceError's 502.
	if s.RemoteSources == nil {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "Opkg source", source.ErrUnavailable))
		return
	}
	// The upstream is asked with the client's query, under Propagate Query
	// Params, and its matrix parameters, as for any remote file (#1371).
	query := source.Request{
		Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
		CacheOnly: cached, MatrixParams: pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
		Resource: source.Resource{
			Kind: source.ResourceKindArtifact, Path: req.Path, Method: r.Method,
			Checksums: kernel.Checksums{SHA256: expected.SHA256},
			Query:     itemkit.RemoteResource(r, req.Path).Query,
		},
	}
	query, err := itemkit.OpkgSourceRequest(query)
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
		itemkit.Found{Repo: c, Item: *result.Item, Used: result.Used}, p)
}
