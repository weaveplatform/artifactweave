package debianhttp

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
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

const debianIndexLimit = 64 << 20

var errDebianIndexTooLarge = errors.New("server: debian Packages index exceeds limit")

// DebianAPI is the Debian ecosystem's partial strict server: the metadata
// and snapshot operations, the snapshot routes, and the item overlay that
// serves Debian's indexes and virtual repositories, over the item kit.
type DebianAPI struct{ *itemkit.Kit }

// TryDebianItem is the overlay (itemkit.Overlay) that serves a Debian
// repository's Packages and Sources indexes and their by-hash copies, and a
// virtual repository's files under dists/ and its binary packages: the
// metadata the virtual repository composes from its members, and every other
// file from the first member that holds it. It ensures an index is never
// returned to a caller who cannot read every package it advertises, which
// the ordinary item route cannot guarantee for a cached or virtual metadata
// file. That rule is about permission: the download policy is checked at the
// index's own path, and an index keeps a package whose file the patterns
// reject. It reports whether it answered; every other request, a folder
// among them, it leaves to the overlays after it and the generic lookup.
func TryDebianItem(
	k *itemkit.Kit, w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) bool {
	s := DebianAPI{k}
	if !debian.Serves(c.PackageType) || req.IsFolder {
		return false
	}
	if c.Class == repo.Virtual && strings.HasPrefix(req.Path, "dists/") {
		_, byHash := debian.ByHashMetadataPath(req.Path)
		if debian.ReleaseMetadataPath(req.Path) || debian.PackagesIndexPath(req.Path) || byHash {
			s.serveVirtualDebianMetadata(w, r, st, c, req, p, acc)
		} else {
			s.serveVirtualDebianFile(w, r, st, c, req, p, acc, "file")
		}
		return true
	}
	if c.Class == repo.Virtual && debian.ValidBinaryPath(req.Path) {
		s.serveVirtualDebianFile(w, r, st, c, req, p, acc, "package")
		return true
	}
	_, byHash := debian.ByHashMetadataPath(req.Path)
	if !debian.PackagesIndexPath(req.Path) && !debian.SourcesIndexPath(req.Path) && !byHash {
		return false
	}
	s.serveDebianIndex(w, r, st, c, cached, req, p, acc)
	return true
}

func (s DebianAPI) serveDebianIndex(
	w http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
	cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
) {
	ctx := r.Context()
	if !acc.Folder(c, req.Path, accessapi.ActionRead) ||
		!acc.File(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	// A stored index takes a query and matrix parameters as every other
	// file does: 7.x documents skipUpdateStats for a GET of any file
	// (RTFACT-25636, 7.21.3), so a query changes nothing, and the matrix
	// parameters are matched below. A remote index takes a query as any
	// remote file does (itemkit.DebianSourceRequest, #1349). It still
	// refuses matrix parameters once read permission is settled (DV-057):
	// 7.x appends them to the upstream URL (6.20
	// HttpRepo.downloadResource; "Appending matrix params to remote request
	// URL" in the ?trace of RTFACT-26349, 7.25.7), which the generic remote
	// read does not do for any format yet (#1371).
	if c.Class == repo.Remote && len(req.Props) != 0 {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	// 7.x stores a local index and caches a remote one at this path and
	// reads it through the download path, so a blacked-out repository, or
	// patterns that reject the index's own path, answer the policy's 404
	// and message once read permission is settled (DV-057). A remote's
	// -cache alias answers as the remote does. The source boundary checks a
	// remote index read's policy at its permission folder, the repository
	// root, so only this check sees the patterns on the index's own path.
	if err := generic.CheckDownload(c, req.Path, false); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian index download", err))
		return
	}
	if cached && c.Class != repo.Remote {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	var body []byte
	var used func(context.Context) error
	var checkPermission func(context.Context, string) error
	var err error
	switch c.Class {
	case repo.Local, repo.Federated:
		var it model.Item
		it, err = st.Items().Get(ctx, c.Key, false, req.Path)
		if err == nil && it.Folder {
			err = kernel.ErrNotFound
		}
		if err == nil {
			err = matchDebianMatrix(ctx, st, it, req)
		}
		if err == nil {
			body, err = s.readDebianBlob(ctx, it.Actual.SHA256)
		}
	case repo.Remote:
		if s.RemoteSources == nil {
			err = source.ErrUnavailable
			break
		}
		query := source.Request{
			Repo: c, Subject: accessapi.SubjectOf(p), Access: acc.Decider, Project: c.ProjectKey,
			CacheOnly: cached,
			Resource: source.Resource{
				Path: req.Path, Method: r.Method,
				// The query less the server's own controls, which the
				// source boundary passes on only under Propagate Query
				// Params, as for any remote file.
				Query: itemkit.RemoteResource(r, req.Path).Query,
			},
		}
		query, err = itemkit.DebianSourceRequest(query)
		if err != nil {
			break
		}
		var result source.Result
		result, err = s.RemoteSources.Read(ctx, query)
		if err != nil {
			err = pkgroute.SourceError(err, p.Anonymous)
			break
		}
		checkPermission, used = result.CheckPermission, result.Used
		switch {
		case result.Stream != nil:
			defer func() { _ = result.Stream.Body.Close() }()
			body, err = io.ReadAll(io.LimitReader(result.Stream.Body, debianIndexLimit+1))
		case result.Item != nil:
			body, err = s.readDebianBlob(ctx, result.Item.Actual.SHA256)
		default:
			err = source.ErrInvalidResult
		}
	default:
		err = kernel.ErrNotFound
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "Debian Packages index", err))
		return
	}
	if len(body) > debianIndexLimit {
		httpkit.WriteErr(
			w,
			r,
			s.InternalError(ctx, "Debian Packages index", errDebianIndexTooLarge),
		)
		return
	}
	paths, err := remoteDebianIndexPaths(req.Path, body, c)
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "Debian index", err))
		return
	}
	if err := s.debianIndexReadable(ctx, paths, checkPermission, p, acc, c); err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	writeDebianIndex(w, r, req.Path, body, used)
}

// debianIndexReadable returns nil when the caller may read each of paths,
// the files an index lists, in every one of repos and, for an index a
// remote source returned, by that source's permission-only check
// (checkPermission, nil for any other). Otherwise it returns the error to
// answer: 403, 401 to an anonymous caller, or the check's own fault. Each
// file is checked for read permission alone. 7.x keeps a package whose file
// the patterns reject in its Debian metadata and refuses only that file's
// GET (RTFACT-30830), so the policy refusing a listed file neither refuses
// nor filters the index.
func (s DebianAPI) debianIndexReadable(
	ctx context.Context, paths []string, checkPermission func(context.Context, string) error,
	p kernel.Principal, acc itemkit.Access, repos ...repo.Config,
) error {
	for _, path := range paths {
		for _, c := range repos {
			if !acc.File(c, path, accessapi.ActionRead) {
				return httpkit.Denied(p)
			}
		}
		if checkPermission == nil {
			continue
		}
		if err := checkPermission(ctx, path); errors.Is(err, source.ErrDenied) {
			return httpkit.Denied(p)
		} else if err != nil {
			return s.ItemError(ctx, "Debian package read authority", err)
		}
	}
	return nil
}

// writeDebianIndex serves body, the index at path. It was checked against
// this caller's permissions, so no cache may keep it for another. used, when
// it is not nil, records the delivery once the whole index was sent.
func writeDebianIndex(
	w http.ResponseWriter, r *http.Request, path string, body []byte,
	used func(context.Context) error,
) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	sw := &itemkit.StatusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, path, time.Time{}, bytes.NewReader(body))
	if sw.Complete(r) && used != nil {
		_ = used(r.Context())
	}
}

// remoteDebianIndexPaths is debianIndexPaths for the index at path that
// repository c holds. A remote repository caches its upstream's index as
// fetched, one that does not decode included (#1356): 7.x does not check an
// upstream Packages index before caching it (RTFACT-27303, an open request
// that it should). Such an index names no package the server can read, so
// it lists none here, and a caller who may read the index's own path is
// served it, as 7.x serves Debian metadata by path permission [I]. Apt
// then refuses it, and any package GET is still checked on its own path. A
// local or federated repository's index is the server's own, so one that
// does not decode stays a fault.
func remoteDebianIndexPaths(path string, body []byte, c repo.Config) ([]string, error) {
	paths, err := debianIndexPaths(path, body)
	if err != nil && c.Class == repo.Remote {
		return nil, nil
	}
	return paths, err
}

func debianIndexPaths(path string, body []byte) ([]string, error) {
	paths := []string{}
	_, byHash := debian.ByHashMetadataPath(path)
	if debian.PackagesIndexPath(path) || byHash && strings.Contains(path, "/binary-") {
		rows, err := debian.DecodePackagesIndex(body)
		for _, row := range rows {
			paths = append(paths, row.Filename)
		}
		if err != nil {
			return nil, fmt.Errorf("debian Packages index: %w", err)
		}
		return paths, nil
	}
	rows, err := debian.DecodeSourcesIndex(body)
	for _, row := range rows {
		paths = append(paths, row.Files...)
	}
	if err != nil {
		return nil, fmt.Errorf("debian Sources index: %w", err)
	}
	return paths, nil
}

// matchDebianMatrix returns kernel.ErrNotFound unless the stored file it
// matches the request's matrix parameters, which "make resolution
// conditional on properties" on a GET of any file (research 02): a key the
// file carries must have a value asked for, and a mandatory key ("key+=")
// must be carried (JFrog's guide, Using Properties in Deployment and
// Resolution). 7.x stores a local Debian index as a file and serves it
// through its download path, which matches a file's properties so (6.20
// DbStoringRepoMixin.getInfo), and no source makes Debian metadata an
// exception.
func matchDebianMatrix(
	ctx context.Context, st itemkit.Store, it model.Item, req itemkit.ItemRequest,
) error {
	if len(req.Props) == 0 {
		return nil
	}
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("debian: properties of %s: %w", it.FullPath(), err)
	}
	if !debianMatrix(req).Matches(props) {
		return kernel.ErrNotFound
	}
	return nil
}

// debianMatrix is the request's matrix parameters as a download matches them.
func debianMatrix(req itemkit.ItemRequest) model.Matrix {
	return model.Matrix{Props: req.Props, Mandatory: req.Mandatory}
}

func (s DebianAPI) readDebianBlob(ctx context.Context, sha string) ([]byte, error) {
	r, _, err := s.Blobs.Open(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("debian: blob open: %w", err)
	}
	defer func() { _ = r.Close() }()
	body, err := io.ReadAll(io.LimitReader(r, debianIndexLimit+1))
	if err != nil {
		return nil, fmt.Errorf("debian: blob read: %w", err)
	}
	return body, nil
}
