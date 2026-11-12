package cranhttp

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
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cran"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

var (
	errCranVirtualCycle = errors.New("server: cran virtual member cycle")
	errCranMemberType   = errors.New("server: cran virtual member has incompatible package type")
)

type cranIndexView struct {
	repo     repo.Config
	packages []cran.Package
	// checkPermission is a remote index read's permission-only check of
	// each package the index lists (source.Result.CheckPermission).
	checkPermission func(context.Context, string) error
	revalidate      func(context.Context) error
	used            func(context.Context) error
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
// download policy at its own path. It answers 404 for PACKAGES.rds,
// which sends R to PACKAGES.gz. It reports whether it answered; every other
// request, a folder or a local repository's package among them, it leaves
// to the overlays after it and the generic lookup.
func TryCranItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	s := CranAPI{Kit: k}
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

func (s CranAPI) serveCranIndex(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	ctx := r.Context()
	feed, name, ok := cran.IndexPath(req.Path)
	if !ok || len(req.Props) != 0 || r.URL.RawQuery != "" ||
		cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !acc.Folder(c, feed, accessapi.ActionRead) || !acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	// 7.x calculates and stores a local repository's PACKAGES and caches a
	// remote's at this path, so a blacked-out repository or patterns that
	// reject the file's own path answer the policy's 404 and message once
	// Read permission is settled (DV-057); a virtual repository's patterns
	// answer that no repository serves it. A remote's -cache alias answers
	// as the remote does.
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "CRAN index download", err))
		return
	}
	var candidates []cranCandidate
	var err error
	var used func(context.Context) error
	if c.Class == repo.Virtual {
		candidates, err = s.cranCandidates(ctx, st, c, req.Path, p, acc, false,
			map[string]bool{})
	} else {
		var view cranIndexView
		view, err = s.cranMemberIndex(ctx, st, c, cached, false, req.Path, p, acc)
		if err == nil && view.revalidate != nil {
			err = view.revalidate(ctx)
		}
		if err == nil {
			for _, pkg := range view.packages {
				allowed, checkErr := cranPackageReadable(ctx, c, pkg.Path, acc,
					view.checkPermission)
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
			CacheOnly: cached,
			Resource:  source.Resource{Path: indexPath, Method: http.MethodGet},
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
				pkgroute.SourceError(err, p.Anonymous),
			)
		}
		view.checkPermission, view.revalidate, view.used =
			result.CheckPermission, result.Revalidate, result.Used
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
			return view, kernel.ErrNotFound
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

// cranCandidates walks ordered virtual members. A package name belongs to
// the first member that lists a readable row for it. A member whose
// download policy refuses its index takes no part in the index, and only a
// blacked-out one in a package GET (cranMemberRefusesIndex). With download
// false, for the index, a row stays when the member's or the virtual
// repository's patterns reject its file, which the index keeps as 7.x's
// virtual indexes do (RTFACT-30656), so the name belongs to that row. With
// download true, for a package GET, such a row is left out before names are
// claimed, so the GET passes over that member to the next one that lists
// the name, as it did before the index kept the row.
func (s CranAPI) cranCandidates(
	ctx context.Context, st itemkit.Store, c repo.Config, indexPath string,
	p kernel.Principal, acc itemkit.Access, download bool, seen map[string]bool,
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
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cran: virtual member %s: %w", key, err)
		}
		if !cran.Serves(member.PackageType) {
			return nil, fmt.Errorf("%w: %s", errCranMemberType, key)
		}
		feed, _, _ := cran.IndexPath(indexPath)
		if !acc.Folder(member, feed, accessapi.ActionRead) ||
			!acc.File(member, indexPath, accessapi.ActionRead) ||
			cranMemberRefusesIndex(member, indexPath, download) {
			continue
		}
		var entries []cranCandidate
		if member.Class == repo.Virtual {
			entries, err = s.cranCandidates(ctx, st, member, indexPath, p, acc, download, seen)
		} else {
			var view cranIndexView
			view, err = s.cranMemberIndex(ctx, st, member, false, download, indexPath, p, acc)
			if err == nil && view.revalidate != nil {
				err = view.revalidate(ctx)
			}
			if err == nil {
				for _, pkg := range view.packages {
					if download && generic.CheckDownload(member, pkg.Path, false) != nil {
						continue
					}
					allowed, checkErr := cranPackageReadable(
						ctx,
						member,
						pkg.Path,
						acc,
						view.checkPermission,
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
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cran: member %s index: %w", key, err)
		}
		for _, entry := range entries {
			if selected[entry.pkg.Name] ||
				!acc.File(c, entry.pkg.Path, accessapi.ActionRead) {
				continue
			}
			if download && generic.CheckDownload(c, entry.pkg.Path, false) != nil {
				continue
			}
			selected[entry.pkg.Name] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

// cranMemberRefusesIndex reports whether a virtual repository's member
// takes no part because its download policy refuses the index at
// indexPath. The virtual index leaves out a member that refuses the
// document. A package GET checks each member at the package's own path, as
// 6.20's VirtualRepoDownloadStrategy does and as the patterns FAQ puts it
// ("artifacts that match an exclude pattern are not resolved from that
// repository"), so there the index is only the lookup that finds the file:
// a blacked-out member, which refuses every file, takes no part, and
// patterns that reject only the index leave the member's packages served.
func cranMemberRefusesIndex(member repo.Config, indexPath string, download bool) bool {
	if !download {
		return generic.CheckDownload(member, indexPath, false) != nil
	}
	return member.Class != repo.Virtual && generic.CheckBlackedOut(member, indexPath) != nil
}

func (s CranAPI) serveCranPackage(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	if len(req.Props) != 0 || r.URL.RawQuery != "" || cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
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
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "CRAN package download", err))
		return
	}
	feed, _, _, _ := cran.PackagePath(req.Path)
	indexPath := feed + "/" + cran.PackagesName
	var candidates []cranCandidate
	var err error
	if c.Class == repo.Virtual {
		candidates, err = s.cranCandidates(r.Context(), st, c, indexPath, p, acc, true,
			map[string]bool{})
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

func (s CranAPI) serveRemoteCranPackage(
	w http.ResponseWriter, r *http.Request, c repo.Config, cached bool,
	req itemkit.ItemRequest, expected cran.Package, p kernel.Principal, acc itemkit.Access,
) {
	// The route reads the remote's index first, which already answers 500
	// for a missing service, so only a direct call gets here without one;
	// it answers as the index read does rather than calling through nil.
	if s.RemoteSources == nil {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "CRAN source", source.ErrUnavailable))
		return
	}
	query := source.Request{
		Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
		CacheOnly: cached,
		Resource: source.Resource{
			Kind: source.ResourceKindArtifact, Path: req.Path, Method: r.Method,
			Checksums: kernel.Checksums{MD5: expected.MD5},
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
