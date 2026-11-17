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
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/alpine"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

const alpineIndexLimit = 36 << 20

var (
	errAlpineIndexTooLarge = errors.New("server: alpine index exceeds limit")
	// errAlpineNoSources is a remote member read on a server built without
	// a remote source service: this server's fault, which no walk passes
	// over, so the virtual index answers 500 as the remote's own index does.
	errAlpineNoSources = errors.New("server: alpine remote member read without a source service")
	// errAlpineNoMemberIndex is a virtual index no member serves: every
	// member missed, refused or was passed over. It is not found to the
	// merged metadata cache, which drops the virtual repository's entry
	// (virtual.Cache.Get), and the overlay answers it with an empty index.
	errAlpineNoMemberIndex = fmt.Errorf("server: no alpine member serves the index: %w",
		kernel.ErrNotFound)
	// errAlpineSign is a virtual index this server could not encode or sign
	// with its key pair: this server's fault, answered 500 whatever the key
	// store said, so an absent key pair is never taken for a member's miss.
	errAlpineSign = errors.New("server: alpine virtual index signing")
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
// packages, answering a package path the format refuses as a file the
// repository does not hold (itemkit.NotServed). Outside a virtual
// repository it answers a miss, a refusal and a matrix mismatch as the
// download does (itemkit.Missed, #1599). It reports whether it answered;
// every other request, a folder among them, it leaves to the overlays after
// it and the generic lookup.
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
			httpkit.WriteErr(w, r, itemkit.NotServed(c, cached, req, p, acc))
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
		httpkit.WriteErr(w, r, itemkit.NotServed(c, cached, req, p, acc))
		return
	}
	if c.Class == repo.Virtual {
		if !s.VirtualGate(w, r, st, c, req.Path, p, acc, "Alpine virtual members") {
			return
		}
	} else if !acc.Folder(c, req.Path, accessapi.ActionRead) ||
		!acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, itemkit.Refused(c, req, p))
		return
	}
	// 7.x calculates and stores a local index, caches a remote's and
	// serves a virtual repository's as a file at this path, so a
	// blacked-out repository or patterns that reject the index's own path
	// answer the policy's 404 and message once read permission is settled
	// (DV-057); a virtual repository's patterns answer that no repository
	// serves it. A remote's -cache alias is checked with the remote's
	// settings and named as the cache (itemkit.Rejected).
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Alpine index download", itemkit.Rejected(c, req, err)))
		return
	}
	if cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.Missed(c, cached, req))
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
			httpkit.WriteErr(w, r, itemkit.Mismatched(c, req))
			return
		}
	}
	var body []byte
	var used func(context.Context) error
	if c.Class == repo.Virtual {
		var err error
		body, err = s.virtualAlpineIndex(r, st, c, req.Path, p, acc)
		switch {
		case errors.Is(err, errAlpineSign):
			httpkit.WriteErr(w, r, s.InternalError(ctx, "Alpine virtual index", err))
			return
		case err != nil:
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Alpine virtual index", err))
			return
		}
	} else {
		view, err := s.alpineIndexRead(ctx, st, c, cached, req.Path,
			itemkit.RemoteResource(r, req.Path).Query, pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
			p, acc)
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "Alpine index",
				itemkit.ReadAnswer(c, cached, req, p, err)))
			return
		}
		if view.stored != nil {
			matches, err := itemkit.MatchesMatrix(ctx, st, *view.stored, req)
			if err != nil {
				httpkit.WriteErr(w, r, s.InternalError(ctx, "Alpine index properties", err))
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
			allowed, err := alpinePackageReadable(ctx, c, pkg.Path, acc, view.checkPermission)
			if err != nil {
				httpkit.WriteErr(w, r, s.ItemError(ctx, "Alpine index permission", err))
				return
			}
			// The index is refused as its own file is: 7.x serves it by
			// its path's permission alone, so no 7.x text names this
			// refusal, which is artifactweave's [I].
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

// alpineIndexRead reads the index at path of a local, federated or remote
// repository, and passes a remote's upstream the client's query (only
// under Propagate Query Params, which the source boundary applies) and its
// matrix parameters (source.Request.MatrixParams), as for any remote file.
// A virtual index's member read passes neither: the client did not ask
// that member for anything (memberPackages).
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
			return view, itemkit.RemoteReadError(err, p.Anonymous)
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
			return view, itemkit.ErrFolderAtPath
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

// alpineProtocol is the protocol whose kit walks an Alpine virtual
// repository's members (itemkit.Kit.Packages): the overlay reads each
// member's index itself, so the sources only name the classes the walk
// keeps, a remote member as its upstream step.
func alpineProtocol(k *itemkit.Kit) pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:  "alpine",
		Types: []repo.PackageType{repo.Alpine},
		Sources: map[repo.Class]pkgroute.Source{
			repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
			repo.Remote: pkgroute.Remote{Service: k.RemoteSources},
		},
	}
}

// virtualAlpineIndex is the virtual repository c's index at indexPath for
// the caller: its members' packages (alpine.Merger), encoded and signed
// with c's own key pair, which the docs name for a virtual index while the
// members' keys are ignored (alpine-linux-repositories.md); with none the
// index is unsigned, as the docs' --allow-untrusted note says. The members
// are the package handlers' walk (pkgroute.Collect): 7.x's order, nested
// virtual repositories flattened with a cycle or a diamond skipped, a
// member of another type passed over (decision 0008, R1, R2 and R11), and
// each member that misses, refuses or fails passed over with the rest
// listed (R3; RTFACT-30422 shows a YUM virtual merging without its failing
// remote) [I]. The merge is kept for c's virtualRetrievalCachePeriodSecs,
// which JFrog's page lists for Alpine (R8, pkgroute.Kit.Merged). An index
// no member serves drops the kept one and answers empty, as it has before
// [I].
func (s alpineItems) virtualAlpineIndex(
	r *http.Request, st itemkit.Store, c repo.Config, indexPath string,
	p kernel.Principal, acc itemkit.Access,
) ([]byte, error) {
	k, err := s.Packages(alpineProtocol(s.Kit), true)
	if err != nil {
		return nil, fmt.Errorf("alpine virtual index: %w", err)
	}
	e, err := k.Merged(k.Bind(r, c), indexPath, indexPath,
		func(r *http.Request) (virtual.Entry, error) {
			parts, err := pkgroute.Collect(k, r, indexPath, func(mr *http.Request) ([]alpine.Package, error) {
				rows, err := s.memberPackages(mr, st, indexPath, p, acc)
				k.WarnPassedOver(mr, err)
				return rows, err
			})
			if err != nil {
				return virtual.Entry{}, err //nolint:wrapcheck // the kit names the member and its fault
			}
			if len(parts) == 0 {
				return virtual.Entry{}, errAlpineNoMemberIndex
			}
			packages, _ := alpine.Merger{}.Merge(r.Context(), parts)
			body, err := s.signedAlpineIndex(r.Context(), c, packages)
			return virtual.Entry{Body: body}, err
		})
	if errors.Is(err, errAlpineNoMemberIndex) {
		return s.signedAlpineIndex(r.Context(), c, nil)
	}
	return e.Body, err //nolint:wrapcheck // the kit's and the merge's own answers
}

// signedAlpineIndex encodes packages as the virtual repository c's index,
// signed with c's key pair (alpine.SigningKey).
func (s alpineItems) signedAlpineIndex(
	ctx context.Context, c repo.Config, packages []alpine.Package,
) ([]byte, error) {
	keyName, key, err := alpine.SigningKey(ctx, s.Access.Keys, c)
	if err == nil {
		var body []byte
		if body, err = alpine.EncodeIndex(packages, "alpine "+c.Key, keyName, key); err == nil {
			return body, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", errAlpineSign, err.Error())
}

// memberPackages reads the packages the index at indexPath of one member
// of a virtual repository lists, through the member request mr, filtered by
// the caller's Read in that member alone: no permission target can name
// the virtual repository (decision 0006), so the member that serves a row
// decides it (decision 0008, R3). A member whose index the caller may not
// read is passed over as not found. A row whose file the patterns reject
// stays, as 7.x's virtual indexes keep it and refuse only its GET
// (RTFACT-30656).
func (s alpineItems) memberPackages(
	mr *http.Request, st itemkit.Store, indexPath string, p kernel.Principal, acc itemkit.Access,
) ([]alpine.Package, error) {
	ctx := mr.Context()
	q := pkgroute.RequestFrom(mr)
	m := q.Repo
	if !acc.Folder(m, indexPath, accessapi.ActionRead) ||
		!acc.File(m, indexPath, accessapi.ActionRead) {
		return nil, kernel.ErrForbidden
	}
	if m.Class == repo.Remote && s.RemoteSources == nil {
		return nil, errAlpineNoSources
	}
	view, err := s.alpineIndexRead(ctx, st, m, q.CacheOnly, indexPath, "", "", p, acc)
	if err == nil && view.revalidate != nil {
		err = view.revalidate(ctx)
	}
	if err != nil {
		return nil, err
	}
	var rows []alpine.Package
	for _, pkg := range view.packages {
		allowed, err := alpinePackageReadable(ctx, m, pkg.Path, acc, view.checkPermission)
		if err != nil {
			return nil, err
		}
		if allowed {
			rows = append(rows, pkg)
		}
	}
	return rows, nil
}

// virtualAlpineCandidates are the members of the virtual repository c that
// list the package at path, in the walk's order, each with its row: the
// package handlers' walk for the package's own path (pkgroute.Collect), so
// each member is checked at the package's path, never at the index it
// reads to find it, as 6.20's VirtualRepoDownloadStrategy and the patterns
// FAQ have it ("artifacts that match an exclude pattern are not resolved
// from that repository"): a member whose patterns reject only its index
// still serves the package.
func (s alpineItems) virtualAlpineCandidates(
	r *http.Request, st itemkit.Store, c repo.Config, path, indexPath string,
	p kernel.Principal, acc itemkit.Access,
) ([]alpineCandidate, error) {
	k, err := s.Packages(alpineProtocol(s.Kit), true)
	if err != nil {
		return nil, fmt.Errorf("alpine virtual package: %w", err)
	}
	parts, err := pkgroute.Collect(k, k.Bind(r, c), path, func(mr *http.Request) ([]alpine.Package, error) {
		rows, err := s.memberPackages(mr, st, indexPath, p, acc)
		k.WarnPassedOver(mr, err)
		return rows, err
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit names the member and its fault
	}
	var out []alpineCandidate
	for _, part := range parts {
		for _, pkg := range part.Doc {
			if pkg.Path == path {
				out = append(out, alpineCandidate{repo: part.Member, packageInfo: pkg})
			}
		}
	}
	return out, nil
}

func (s alpineItems) serveVirtualAlpinePackage(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	if _, _, _, _, ok := alpine.ParsePath(req.Path); !ok {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !s.VirtualGate(w, r, st, c, req.Path, p, acc, "Alpine virtual members") {
		return
	}
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Alpine virtual package download", err))
		return
	}
	indexPath := req.Path[:strings.LastIndexByte(req.Path, '/')+1] + alpine.IndexName
	candidates, err := s.virtualAlpineCandidates(r, st, c, req.Path, indexPath, p, acc)
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
