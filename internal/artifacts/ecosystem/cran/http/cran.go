package cranhttp

import (
	"bytes"
	"context"
	"encoding/gob"
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
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cran"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

var (
	// errCranNoMember is a virtual index no member serves: every member
	// missed, refused or was passed over. It is not found to the merged
	// metadata cache, which drops the virtual repository's entry
	// (virtual.Cache.Get), and the overlay answers it with an empty index,
	// as it has before.
	errCranNoMember = fmt.Errorf("server: cran: no member serves the index: %w",
		kernel.ErrNotFound)
	// errCranVirtualIndex is a virtual index this server could not keep in
	// the merged metadata cache, or read back: its fault, answered 500.
	errCranVirtualIndex = errors.New("server: cran: virtual index")
)

type cranIndexView struct {
	repo     repo.Config
	packages []cran.Package
	// checkPermission is a remote index read's permission-only check of
	// each package the index lists (source.Result.CheckPermission).
	checkPermission func(context.Context, string) error
	revalidate      func(context.Context) error
	used            func(context.Context) error
	// stored is a local or federated repository's PACKAGES item, whose
	// properties a request's matrix parameters are matched against.
	stored *model.Item
}

type cranCandidate struct {
	repo repo.Config
	pkg  cran.Package
}

// CranAPI is the CRAN ecosystem's partial strict server: the archive
// migration operation, the source and binary upload routes, and the item
// overlay that serves CRAN's indexes and packages, over the item kit.
type CranAPI struct {
	*itemkit.Kit
	// ArchiveMoverEnabled is the cran.archive_mover_enabled setting, which
	// the router passes on: while it is off, MoveCranArchives answers 404,
	// as the legacy mover is opt-in.
	ArchiveMoverEnabled bool
}

// TryCranItem is the overlay (itemkit.Overlay) that serves a CRAN
// repository's PACKAGES and PACKAGES.gz, listing only the packages the
// caller has Read permission on, a package whose file the include and
// exclude patterns reject among them, and a virtual or remote repository's
// packages, only as an index the caller can see lists them, each under the
// download policy at its own path. It answers PACKAGES.rds as a file the
// repository does not hold (itemkit.NotServed), which sends R to
// PACKAGES.gz. Outside a virtual repository it answers a miss, a refusal
// and a matrix mismatch as the download does (itemkit.Missed, #1599). It
// reports whether it answered; every other request, a folder or a local
// repository's package among them, it leaves to the overlays after it and
// the generic lookup.
func TryCranItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	s := CranAPI{Kit: k}
	if !cran.Serves(c.PackageType) || req.IsFolder {
		return false
	}
	if strings.HasSuffix(req.Path, "/PACKAGES.rds") {
		// R falls back to PACKAGES.gz on any failure.
		httpkit.WriteErr(w, r, itemkit.NotServed(c, cached, req, p, acc))
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

func (s CranAPI) serveCranIndex(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	ctx := r.Context()
	feed, name, ok := cran.IndexPath(req.Path)
	if !ok || cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.NotServed(c, cached, req, p, acc))
		return
	}
	if c.Class == repo.Virtual {
		if !s.VirtualGate(w, r, st, c, req.Path, p, acc, "CRAN virtual members") {
			return
		}
	} else if !acc.Folder(c, feed, accessapi.ActionRead) || !acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, itemkit.Refused(c, req, p))
		return
	}
	// 7.x calculates and stores a local repository's PACKAGES and caches a
	// remote's at this path, so a blacked-out repository or patterns that
	// reject the file's own path answer the policy's 404 and message once
	// Read permission is settled (DV-057); a virtual repository's patterns
	// answer that no repository serves it. A remote's -cache alias is
	// checked with the remote's settings and named as the cache
	// (itemkit.Rejected).
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "CRAN index download", itemkit.Rejected(c, req, err)))
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
			httpkit.WriteErr(w, r, s.InternalError(ctx, "CRAN index properties", err))
			return
		}
		if !matches {
			httpkit.WriteErr(w, r, itemkit.Mismatched(c, req))
			return
		}
	}
	var packages []cran.Package
	var err error
	var used func(context.Context) error
	if c.Class == repo.Virtual {
		packages, err = s.virtualCranIndex(r, st, c, feed, req.Path, p, acc)
		if errors.Is(err, errCranVirtualIndex) {
			httpkit.WriteErr(w, r, s.InternalError(ctx, "CRAN virtual index", err))
			return
		}
	} else {
		packages, used, err = s.ownCranIndex(r, st, c, cached, req, p, acc)
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "CRAN index",
			pkgroute.SourceError(itemkit.ReadAnswer(c, cached, req, p, err), p.Anonymous)))
		return
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
	itemkit.ServeContent(sw, r, name, time.Time{}, bytes.NewReader(body))
	if sw.Complete(r) && used != nil {
		_ = used(ctx)
	}
}

// ownCranIndex is the packages the index req names of the local, federated
// or remote repository c lists for the caller, with the read's used hook.
func (s CranAPI) ownCranIndex(
	r *http.Request, st itemkit.Store, c repo.Config, cached bool, req itemkit.ItemRequest,
	p kernel.Principal, acc itemkit.Access,
) ([]cran.Package, func(context.Context) error, error) {
	ctx := r.Context()
	view, err := s.cranIndexRead(ctx, st, c, cached, false, req.Path,
		itemkit.RemoteResource(r, req.Path).Query, pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
		p, acc)
	if err == nil && view.stored != nil {
		var matches bool
		matches, err = itemkit.MatchesMatrix(ctx, st, *view.stored, req)
		if err == nil && !matches {
			err = itemkit.Mismatched(c, req)
		}
	}
	if err == nil && view.revalidate != nil {
		err = view.revalidate(ctx)
	}
	if err != nil {
		return nil, view.used, err
	}
	var packages []cran.Package
	for _, pkg := range view.packages {
		allowed, err := cranPackageReadable(ctx, c, pkg.Path, acc, view.checkPermission)
		if err != nil {
			return nil, view.used, err
		}
		if allowed {
			packages = append(packages, pkg)
		}
	}
	return packages, view.used, nil
}

// cranPackageReadable reports whether the caller has Read permission on a
// package an index lists, in c and, through check, at a remote's source.
// It is permission alone: an index keeps a package whose file the patterns
// reject and only that file's GET is refused, as 7.x keeps such a package
// in a Debian or RPM index (RTFACT-30830, RTFACT-30656).
func cranPackageReadable(
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
			return false, fmt.Errorf("cran: package read authority: %w", err)
		}
	}
	return true, nil
}

// cranMemberIndex reads the index at indexPath of a local, federated or
// remote repository. lookup says the read is the one a package GET makes to
// find the package and pin its MD5, rather than a read of the document
// itself.
func (s CranAPI) cranMemberIndex(
	ctx context.Context, st itemkit.Store, c repo.Config, cached, lookup bool, indexPath string,
	p kernel.Principal, acc itemkit.Access,
) (cranIndexView, error) {
	return s.cranIndexRead(ctx, st, c, cached, lookup, indexPath, "", "", p, acc)
}

// cranIndexRead reads the index as cranMemberIndex does, and passes a
// remote's upstream the client's query (only under Propagate Query Params,
// which the source boundary applies) and its matrix parameters
// (source.Request.MatrixParams), as for any remote file. A virtual index's
// member and a package GET's lookup read a document the client did not ask
// for, so they pass neither (cranMemberIndex).
func (s CranAPI) cranIndexRead(
	ctx context.Context, st itemkit.Store, c repo.Config, cached, lookup bool,
	indexPath, rawQuery, matrix string, p kernel.Principal, acc itemkit.Access,
) (cranIndexView, error) {
	view := cranIndexView{repo: c}
	var body []byte
	var err error
	if c.Class == repo.Remote {
		// A missing service is this server's fault, not the upstream's, so
		// the read answers 500 as Alpine's and opkg's index reads do. The
		// answer is made here because every caller passes the error through
		// SourceError, which would turn a bare source.ErrUnavailable into
		// the upstream's 502.
		if s.RemoteSources == nil {
			return view, fmt.Errorf("cran: remote index source: %w",
				s.InternalError(ctx, "CRAN index source", source.ErrUnavailable))
		}
		query := source.Request{
			Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
			CacheOnly: cached, MatrixParams: matrix,
			Resource: source.Resource{Path: indexPath, Method: http.MethodGet, Query: rawQuery},
		}
		query, err = itemkit.CranSourceRequest(query)
		if err != nil {
			return view, err
		}
		// The kit checks Read permission at the feed folder, and without a
		// policy path the source would check the remote's patterns there too,
		// so a pattern that matches only the folder, such as src/*, would
		// refuse the index and every package. The policy is checked at the
		// path 7.x caches the index at instead, and a package GET's lookup
		// checks only the blackout, since the package's own path was checked
		// before the lookup (decision 0032, "Where the policy applies").
		query.Resource.PolicyPath = indexPath
		query.Resource.BlackoutOnly = lookup
		result, err := s.RemoteSources.Read(ctx, query)
		if err != nil {
			return view, fmt.Errorf(
				"cran: remote index: %w",
				itemkit.RemoteReadError(err, p.Anonymous),
			)
		}
		view.checkPermission, view.revalidate, view.used =
			result.CheckPermission, result.Revalidate, result.Used
		switch {
		case result.Stream != nil:
			defer func() { _ = result.Stream.Body.Close() }()
			// The stream is the server's copy, cached or spooled, so a
			// failed read of it is a storage fault, 500, not the
			// upstream's 502 (#1488).
			body, err = pkgroute.ReadServed(ctx, result.Stream.Body, 32<<20,
				pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous))
		case result.Item != nil:
			body, err = s.readCranBlob(ctx, result.Item.Actual.SHA256)
		default:
			return view, source.ErrInvalidResult
		}
		if err != nil {
			return view, fmt.Errorf("cran: remote index body: %w", err)
		}
	} else {
		if c.Class != repo.Local && c.Class != repo.Federated || cached {
			return view, kernel.ErrNotFound
		}
		if err := s.Await(ctx, st, cran.KindIndex, c.Key); err != nil {
			return view, fmt.Errorf("cran: refresh index: %w", err)
		}
		it, getErr := st.Items().Get(ctx, c.Key, false, indexPath)
		if getErr != nil {
			return view, getErr //nolint:wrapcheck // ErrNotFound is the answer.
		}
		if it.Folder {
			return view, itemkit.ErrFolderAtPath
		}
		view.stored = &it
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

func (s CranAPI) readCranBlob(ctx context.Context, sha string) ([]byte, error) {
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

// cranProtocol is the protocol whose kit walks a CRAN virtual repository's
// members (itemkit.Kit.Packages): the overlay reads each member's index
// itself, so the sources only name the classes the walk keeps, a remote
// member as its upstream step.
func cranProtocol(k *itemkit.Kit) pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:  "cran",
		Types: []repo.PackageType{repo.Cran},
		Sources: map[repo.Class]pkgroute.Source{
			repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
			repo.Remote: pkgroute.Remote{Service: k.RemoteSources},
		},
	}
}

// virtualCranIndex is the packages the virtual repository c's index of
// feed lists for the caller, its members' (cran.Merger), which
// serveCranIndex encodes as PACKAGES and PACKAGES.gz alike.
// The members are the package handlers' walk (pkgroute.Collect) at
// indexPath, the file the client asked for: 7.x's order, nested virtual
// repositories flattened with a cycle or a diamond skipped, a member of
// another type passed over (decision 0008, R1, R2 and R11), and each
// member that misses, refuses or fails passed over with the rest listed
// (R3); a failure is logged (Kit.WarnPassedOver) [I]. The set is kept for
// c's virtualRetrievalCachePeriodSecs, which 7.x carries for CRAN (R8,
// pkgroute.Kit.Merged; RTFACT-18674), so its files keep their bytes. An
// index no member serves drops the kept set and lists nothing, as it has
// before.
//
// c's own include and exclude patterns are not asked again: the overlay
// has checked them at the file the client asked for.
func (s CranAPI) virtualCranIndex(
	r *http.Request, st itemkit.Store, c repo.Config, feed, indexPath string,
	p kernel.Principal, acc itemkit.Access,
) ([]cran.Package, error) {
	k, err := s.Packages(cranProtocol(s.Kit), true)
	if err != nil {
		return nil, fmt.Errorf("cran: virtual index: %w", err)
	}
	open := c
	open.IncludesPattern, open.ExcludesPattern = nil, nil
	e, err := k.Merged(k.Bind(r, open), indexPath, feed+"/",
		func(r *http.Request) (virtual.Entry, error) {
			parts, err := pkgroute.Collect(k, r, indexPath,
				func(mr *http.Request) ([]cran.Package, error) {
					rows, err := s.memberCranPackages(mr, st, indexPath, "", p, acc)
					k.WarnPassedOver(mr, err)
					return rows, err
				})
			if err != nil {
				return virtual.Entry{}, err //nolint:wrapcheck // the kit names the member and its fault
			}
			if len(parts) == 0 {
				return virtual.Entry{}, errCranNoMember
			}
			packages, _ := cran.Merger{}.Merge(r.Context(), parts)
			var body bytes.Buffer
			if err := gob.NewEncoder(&body).Encode(packages); err != nil {
				return virtual.Entry{}, fmt.Errorf("%w: %s", errCranVirtualIndex, err.Error())
			}
			return virtual.Entry{Body: body.Bytes()}, nil
		})
	if errors.Is(err, errCranNoMember) {
		return nil, nil
	}
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit's and the merge's own answers
	}
	var packages []cran.Package
	if err := gob.NewDecoder(bytes.NewReader(e.Body)).Decode(&packages); err != nil {
		return nil, fmt.Errorf("%w: %s", errCranVirtualIndex, err.Error())
	}
	return packages, nil
}

// virtualCranCandidates are the members of the virtual repository c whose
// index at indexPath lists the package at path, in the walk's order, each
// with its row, so a GET whose matrix parameters the first member's file
// does not match passes over to the next (serveCranPackage). The walk is
// the package handlers' at the package's own path (pkgroute.Collect), so
// each member's policy is checked at that path, never at the index read to
// find it, as 6.20's VirtualRepoDownloadStrategy and the patterns FAQ have
// it ("artifacts that match an exclude pattern are not resolved from that
// repository"): a member whose patterns reject only its index still serves
// the package, and one that rejects the package takes no part. A file the
// first member does not list is looked for in the next, as 7.x's virtual
// download finds a file in the first member that holds it (decision 0008,
// R3), whatever version an earlier member lists of the package.
func (s CranAPI) virtualCranCandidates(
	r *http.Request, st itemkit.Store, c repo.Config, indexPath, path string,
	p kernel.Principal, acc itemkit.Access,
) ([]cranCandidate, error) {
	k, err := s.Packages(cranProtocol(s.Kit), false)
	if err != nil {
		return nil, fmt.Errorf("cran: virtual package: %w", err)
	}
	open := c
	open.IncludesPattern, open.ExcludesPattern = nil, nil
	parts, err := pkgroute.Collect(k, k.Bind(r, open), path,
		func(mr *http.Request) ([]cran.Package, error) {
			rows, err := s.memberCranPackages(mr, st, indexPath, path, p, acc)
			k.WarnPassedOver(mr, err)
			return rows, err
		})
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit names the member and its fault
	}
	var out []cranCandidate
	for _, part := range parts {
		for _, pkg := range part.Doc {
			out = append(out, cranCandidate{repo: part.Member, pkg: pkg})
		}
	}
	return out, nil
}

// memberCranPackages reads the packages one member of a virtual repository
// lists in its index at indexPath, through the member request mr, filtered
// by the caller's Read in that member alone: no permission target can name
// the virtual repository (decision 0006), so the member that serves a row
// decides it (decision 0008, R3). A member whose index the caller may not
// read is passed over as not found. For the virtual index (pkg "") a row
// whose file the patterns reject stays, as 7.x's virtual indexes keep such
// a package and refuse only its GET (RTFACT-30656); for a package GET (pkg
// its path) only that package's row is kept, and the index is only the
// lookup that finds it, so only a remote member's blackout refuses the
// read (cranMemberIndex).
func (s CranAPI) memberCranPackages(
	mr *http.Request, st itemkit.Store, indexPath, pkg string, p kernel.Principal, acc itemkit.Access,
) ([]cran.Package, error) {
	ctx := mr.Context()
	q := pkgroute.RequestFrom(mr)
	m := q.Repo
	feed, _, _ := cran.IndexPath(indexPath)
	if !acc.Folder(m, feed, accessapi.ActionRead) || !acc.File(m, indexPath, accessapi.ActionRead) {
		return nil, kernel.ErrForbidden
	}
	lookup := pkg != ""
	view, err := s.cranMemberIndex(ctx, st, m, q.CacheOnly, lookup, indexPath, p, acc)
	if err == nil && view.revalidate != nil {
		err = view.revalidate(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("cran: member %s index: %w", m.Key, err)
	}
	var rows []cran.Package
	for _, row := range view.packages {
		if lookup && row.Path != pkg {
			continue
		}
		allowed, err := cranPackageReadable(ctx, m, row.Path, acc, view.checkPermission)
		if err != nil {
			return nil, err
		}
		if allowed {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (s CranAPI) serveCranPackage(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	if cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.Missed(c, cached, req))
		return
	}
	if c.Class == repo.Virtual {
		if !s.VirtualGate(w, r, st, c, req.Path, p, acc, "CRAN virtual members") {
			return
		}
	} else if !acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, itemkit.Refused(c, req, p))
		return
	}
	// The package's own path decides, as a download does: a blacked-out
	// remote or patterns that reject the file answer the policy's 404 and
	// message, and a virtual repository's patterns answer that no repository
	// serves it. The index read below, which pins a remote download's MD5,
	// is a lookup that checks only the remote's blackout (cranMemberIndex),
	// so patterns that reject the PACKAGES file or its folder leave the
	// package served, as a download checks only its own path.
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r,
			s.ItemError(r.Context(), "CRAN package download", itemkit.Rejected(c, req, err)))
		return
	}
	// A remote's cached copy of the package is matched against the matrix
	// parameters before the index that pins it is read, so a copy that does
	// not match asks the upstream nothing (itemkit.RemoteCopyMatches).
	if c.Class == repo.Remote {
		matches, err := itemkit.RemoteCopyMatches(r.Context(), st, c, req.Path, req)
		if err != nil {
			httpkit.WriteErr(w, r, s.InternalError(r.Context(), "CRAN package properties", err))
			return
		}
		if !matches {
			httpkit.WriteErr(w, r, itemkit.Mismatched(c, req))
			return
		}
	}
	feed, _, _, _ := cran.PackagePath(req.Path)
	indexPath := feed + "/" + cran.PackagesName
	var candidates []cranCandidate
	var err error
	if c.Class == repo.Virtual {
		candidates, err = s.virtualCranCandidates(r, st, c, indexPath, req.Path, p, acc)
	} else {
		var view cranIndexView
		view, err = s.cranMemberIndex(r.Context(), st, c, cached, true, indexPath, p, acc)
		if err == nil && view.revalidate != nil {
			err = view.revalidate(r.Context())
		}
		if err == nil {
			for _, pkg := range view.packages {
				candidates = append(candidates, cranCandidate{repo: c, pkg: pkg})
			}
		}
	}
	// A remote's index that cannot be read leaves the package unserved,
	// and so answers as the package's own miss or refusal would [I].
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "CRAN package index",
			pkgroute.SourceError(itemkit.ReadAnswer(c, cached, req, p, err), p.Anonymous)))
		return
	}
	// A member's file that the matrix parameters do not match does not
	// serve the GET, and the next member that lists the package is asked,
	// as 7.x's virtual download passes over a member that does not find the
	// file (ArtifactoryVirtualDownloadStrategy.processStandard, 7.161.26),
	// a property mismatch among the reasons (DbStoringRepoMixin.getInfo)
	// [I]. A remote member's copy is matched only when its cache holds one.
	for _, candidate := range candidates {
		if candidate.pkg.Path != req.Path {
			continue
		}
		if candidate.repo.Class == repo.Remote {
			if c.Class == repo.Virtual {
				matches, matchErr := itemkit.RemoteCopyMatches(
					r.Context(), st, candidate.repo, req.Path, req)
				if matchErr != nil {
					httpkit.WriteErr(w, r,
						s.InternalError(r.Context(), "CRAN member package properties", matchErr))
					return
				}
				if !matches {
					continue
				}
			}
			s.serveRemoteCranPackage(w, r, c, candidate.repo, cached, req, candidate.pkg, p, acc)
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
		matches, matchErr := itemkit.MatchesMatrix(r.Context(), st, it, req)
		if matchErr != nil {
			httpkit.WriteErr(w, r,
				s.InternalError(r.Context(), "CRAN member package properties", matchErr))
			return
		}
		if !matches {
			continue
		}
		s.ServeItemFile(w, r, st, itemkit.Found{Repo: candidate.repo, Item: it}, p)
		return
	}
	// A remote's index that does not list the package leaves it unserved [I].
	httpkit.WriteErr(w, r, itemkit.Missed(c, cached, req))
}

// serveRemoteCranPackage serves req's package from c, a remote repository:
// asked itself, or a member of asked, the virtual repository the request
// named, whose answers the source's failure takes.
func (s CranAPI) serveRemoteCranPackage(
	w http.ResponseWriter, r *http.Request, asked, c repo.Config, cached bool,
	req itemkit.ItemRequest, expected cran.Package, p kernel.Principal, acc itemkit.Access,
) {
	// The route reads the remote's index first, which already answers 500
	// for a missing service, so only a direct call gets here without one;
	// it answers as the index read does rather than calling through nil.
	if s.RemoteSources == nil {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "CRAN source", source.ErrUnavailable))
		return
	}
	// The upstream is asked with the client's query, under Propagate Query
	// Params, and its matrix parameters, as for any remote file (#1371).
	query := source.Request{
		Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
		CacheOnly: cached, MatrixParams: pkgroute.UpstreamMatrix(r.URL.EscapedPath()),
		Resource: source.Resource{
			Kind: source.ResourceKindArtifact, Path: req.Path, Method: r.Method,
			Checksums: kernel.Checksums{MD5: expected.MD5},
			Query:     itemkit.RemoteResource(r, req.Path).Query,
		},
	}
	query, err := itemkit.CranSourceRequest(query)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "CRAN source", err))
		return
	}
	result, err := s.RemoteSources.Read(r.Context(), query)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "CRAN source",
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
	if result.Item == nil || expected.MD5 != "" && result.Item.Actual.MD5 != expected.MD5 {
		httpkit.WriteErr(w, r, pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous))
		return
	}
	s.ServeItemFile(w, r, s.Store,
		itemkit.Found{Repo: c, Item: *result.Item, Used: result.Used}, p)
}
