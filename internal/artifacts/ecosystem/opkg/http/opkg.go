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
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

var (
	errOpkgIndexLarge = errors.New("server: opkg index exceeds limit")
	// errOpkgNoSources is a remote member read on a server built without a
	// remote source service: this server's fault, which no walk passes
	// over, so the virtual feed answers 500 as the remote's own feed does.
	errOpkgNoSources = errors.New("server: opkg remote member read without a source service")
	// errOpkgVirtualFeed is a virtual feed this server could not encode or
	// sign: its fault, answered 500 whatever the key store said.
	errOpkgVirtualFeed = errors.New("server: opkg virtual feed")
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
// answering an IPK path the format refuses as a file the repository does
// not hold (itemkit.NotServed). Outside a virtual repository it answers a
// miss, a refusal and a matrix mismatch as the download does
// (itemkit.Missed, #1599). It reports whether it answered; every other
// request, a folder among them, it leaves to the overlays after it and the
// generic lookup.
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
			httpkit.WriteErr(w, r, itemkit.NotServed(c, cached, req, p, acc))
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
		httpkit.WriteErr(w, r, itemkit.NotServed(c, cached, req, p, acc))
		return
	}
	if c.Class == repo.Virtual {
		if !s.VirtualGate(w, r, st, c, req.Path, p, acc, "Opkg virtual members") {
			return
		}
	} else if !acc.Folder(c, req.Path, accessapi.ActionRead) ||
		!acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, itemkit.Refused(c, req, p))
		return
	}
	// 7.x calculates and stores a local feed's Packages and caches a
	// remote's at this path, so a blacked-out repository or patterns that
	// reject the file's own path answer the policy's 404 and message once
	// read permission is settled (DV-057); a virtual repository's patterns
	// answer that no repository serves it. A remote's -cache alias is
	// checked with the remote's settings and named as the cache
	// (itemkit.Rejected).
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Opkg feed download", itemkit.Rejected(c, req, err)))
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
		files, err := s.virtualOpkgFeed(r, st, c, feed, p, acc)
		switch {
		case errors.Is(err, errOpkgVirtualFeed):
			httpkit.WriteErr(w, r, s.InternalError(ctx, "Opkg virtual feed", err))
			return
		case err != nil:
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Opkg virtual feed", err))
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
				httpkit.WriteErr(w, r, itemkit.Mismatched(c, req))
				return
			}
		}
		view, err := s.opkgIndexRead(ctx, st, c, cached, req.Path,
			itemkit.RemoteResource(r, req.Path).Query, pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
			p, acc)
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Opkg feed",
				itemkit.ReadAnswer(c, cached, req, p, err)))
			return
		}
		if view.stored != nil {
			matches, err := itemkit.MatchesMatrix(ctx, st, *view.stored, req)
			if err != nil {
				httpkit.WriteErr(w, r, s.InternalError(ctx, "Opkg feed properties", err))
				return
			}
			if !matches {
				httpkit.WriteErr(w, r, itemkit.Mismatched(c, req))
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
			// The feed is refused as its own file is; 7.x serves it by its
			// path's permission alone, so no 7.x text names this refusal [I].
			if !allowed {
				httpkit.WriteErr(w, r, itemkit.Refused(c, req, p))
				return
			}
		}
		body, used = view.raw, view.used
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	sw := &itemkit.StatusWriter{ResponseWriter: w}
	itemkit.ServeContent(sw, r, path.Base(req.Path), time.Time{}, bytes.NewReader(body))
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
				itemkit.RemoteReadError(err, p.Anonymous),
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
			return view, itemkit.ErrFolderAtPath
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

// opkgProtocol is the protocol whose kit walks an Opkg virtual
// repository's members (itemkit.Kit.Packages): the overlay reads each
// member's feed itself, so the sources only name the classes the walk
// keeps, a remote member as its upstream step.
func opkgProtocol(k *itemkit.Kit) pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:  "opkg",
		Types: []repo.PackageType{repo.Opkg},
		Sources: map[repo.Class]pkgroute.Source{
			repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
			repo.Remote: pkgroute.Remote{Service: k.RemoteSources},
		},
	}
}

// virtualOpkgFeed is the virtual repository c's feed for the caller, every
// feed file by its name: its members' packages (opkg.Merger), encoded and
// signed with c's own GPG key pair when it names one (opkg.SignFeed). The
// members are the package handlers' walk (pkgroute.Collect) at the feed's
// Packages: 7.x's order, nested virtual repositories flattened with a
// cycle or a diamond skipped, a member of another type passed over
// (decision 0008, R1, R2 and R11), and each member that misses, refuses or
// fails passed over with the rest listed (R3; RTFACT-30422 shows a YUM
// virtual merging without its failing remote) [I]. Opkg's virtual
// repositories carry no virtualRetrievalCachePeriodSecs, so the feed is
// built on every request (Packages without the cache), and a feed no
// member serves is the empty one.
//
// c's own include and exclude patterns are not asked again at Packages:
// the overlay has checked them at the file the client asked for, as for
// any download.
func (s opkgItems) virtualOpkgFeed(
	r *http.Request, st itemkit.Store, c repo.Config, feed string,
	p kernel.Principal, acc itemkit.Access,
) (map[string][]byte, error) {
	k, err := s.Packages(opkgProtocol(s.Kit), false)
	if err != nil {
		return nil, fmt.Errorf("opkg: virtual feed: %w", err)
	}
	open := c
	open.IncludesPattern, open.ExcludesPattern = nil, nil
	indexPath := opkg.IndexPath(feed, opkg.PackagesName)
	var files map[string][]byte
	// The kit keeps nothing, so Merged builds here, on this request, and
	// files is this request's own.
	_, err = k.Merged(k.Bind(r, open), indexPath, indexPath,
		func(r *http.Request) (virtual.Entry, error) {
			parts, err := pkgroute.Collect(k, r, indexPath, func(mr *http.Request) ([]opkg.Package, error) {
				rows, err := s.memberOpkgPackages(mr, st, indexPath, "", p, acc)
				k.WarnPassedOver(mr, err)
				return rows, err
			})
			if err != nil {
				return virtual.Entry{}, err //nolint:wrapcheck // the kit names the member and its fault
			}
			packages, _ := opkg.Merger{}.Merge(r.Context(), parts)
			files, err = opkg.EncodeIndex(packages, feed)
			if err == nil {
				err = opkg.SignFeed(r.Context(), s.Access.Keys, c, files)
			}
			if err != nil {
				return virtual.Entry{}, fmt.Errorf("%w: %s", errOpkgVirtualFeed, err.Error())
			}
			return virtual.Entry{}, nil
		})
	return files, err //nolint:wrapcheck // the kit's and the merge's own answers
}

// virtualOpkgCandidates are the members of the virtual repository c whose
// feed at indexPath lists the IPK at path, in the walk's order, each with
// its row, so a GET whose matrix parameters the first member's file does
// not match passes over to the next (serveOpkgPackage). The walk is the
// package handlers' at the IPK's own path (pkgroute.Collect), so each
// member's policy is checked at that path, never at the feed read to find
// it, as 6.20's VirtualRepoDownloadStrategy and the patterns FAQ have it
// ("artifacts that match an exclude pattern are not resolved from that
// repository"): a member whose patterns reject only its feed still serves
// the IPK, and one that rejects the IPK takes no part.
func (s opkgItems) virtualOpkgCandidates(
	r *http.Request, st itemkit.Store, c repo.Config, indexPath, path string,
	p kernel.Principal, acc itemkit.Access,
) ([]opkgCandidate, error) {
	k, err := s.Packages(opkgProtocol(s.Kit), false)
	if err != nil {
		return nil, fmt.Errorf("opkg: virtual package: %w", err)
	}
	parts, err := pkgroute.Collect(k, k.Bind(r, c), path, func(mr *http.Request) ([]opkg.Package, error) {
		rows, err := s.memberOpkgPackages(mr, st, indexPath, path, p, acc)
		k.WarnPassedOver(mr, err)
		return rows, err
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit names the member and its fault
	}
	var out []opkgCandidate
	for _, part := range parts {
		for _, pkg := range part.Doc {
			out = append(out, opkgCandidate{repo: part.Member, pkg: pkg})
		}
	}
	return out, nil
}

// memberOpkgPackages reads the packages one member of a virtual repository
// lists in its feed at indexPath, through the member request mr, filtered
// by the caller's Read in that member alone: no permission target can name
// the virtual repository (decision 0006), so the member that serves a row
// decides it (decision 0008, R3). A member whose feed the caller may not
// read is passed over as not found. For the virtual feed (ipk "") a row
// whose file the patterns reject stays, as 7.x's virtual indexes keep it
// and refuse only its GET (RTFACT-30656); for an IPK GET only that IPK's
// row is kept.
func (s opkgItems) memberOpkgPackages(
	mr *http.Request, st itemkit.Store, indexPath, ipk string, p kernel.Principal,
	acc itemkit.Access,
) ([]opkg.Package, error) {
	ctx := mr.Context()
	m := pkgroute.RequestFrom(mr).Repo
	if !acc.Folder(m, indexPath, accessapi.ActionRead) ||
		!acc.File(m, indexPath, accessapi.ActionRead) {
		return nil, kernel.ErrForbidden
	}
	if m.Class == repo.Remote && s.RemoteSources == nil {
		return nil, errOpkgNoSources
	}
	view, err := s.opkgMemberIndex(ctx, st, m, false, indexPath, p, acc)
	if err == nil && view.revalidate != nil {
		err = view.revalidate(ctx)
	}
	if err != nil {
		return nil, err
	}
	var rows []opkg.Package
	for _, pkg := range view.packages {
		if ipk != "" && pkg.Path != ipk {
			continue
		}
		allowed, err := opkgPackageReadable(ctx, m, pkg.Path, acc, view.checkPermission)
		if err != nil {
			return nil, err
		}
		if allowed {
			rows = append(rows, pkg)
		}
	}
	return rows, nil
}

func (s opkgItems) serveOpkgPackage(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	if cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.Missed(c, cached, req))
		return
	}
	if c.Class == repo.Virtual {
		if !s.VirtualGate(w, r, st, c, req.Path, p, acc, "Opkg virtual members") {
			return
		}
	} else if !acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, itemkit.Refused(c, req, p))
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
		httpkit.WriteErr(w, r,
			s.ItemError(r.Context(), "Opkg package download", itemkit.Rejected(c, req, err)))
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
			httpkit.WriteErr(w, r, itemkit.Mismatched(c, req))
			return
		}
	}
	indexPath := opkg.IndexPath(opkg.Feed(req.Path), opkg.PackagesName)
	var candidates []opkgCandidate
	var err error
	if c.Class == repo.Virtual {
		candidates, err = s.virtualOpkgCandidates(r, st, c, indexPath, req.Path, p, acc)
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
	// A remote's feed that cannot be read leaves the IPK unserved, and so
	// answers as the IPK's own miss or refusal would [I].
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Opkg package index",
			itemkit.ReadAnswer(c, cached, req, p, err)))
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
			s.serveRemoteOpkgPackage(w, r, c, candidate.repo, cached, req, candidate.pkg, p, acc)
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
		s.ServeItemFile(w, r, st, itemkit.Found{Repo: candidate.repo, Item: it}, p)
		return
	}
	// A remote's feed that does not list the IPK leaves it unserved [I].
	httpkit.WriteErr(w, r, itemkit.Missed(c, cached, req))
}

// serveRemoteOpkgPackage serves req's IPK from c, a remote repository:
// asked itself, or a member of asked, the virtual repository the request
// named, whose answers the source's failure takes.
func (s opkgItems) serveRemoteOpkgPackage(
	w http.ResponseWriter, r *http.Request, asked, c repo.Config, cached bool,
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
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Opkg source",
			pkgroute.SourceError(itemkit.ReadAnswer(asked, cached, req, p, err), p.Anonymous)))
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
	s.ServeItemFile(w, r, s.Store,
		itemkit.Found{Repo: c, Item: *result.Item, Used: result.Used}, p)
}
