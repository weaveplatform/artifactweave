package swifthttp

import (
	"errors"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	swiftfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/swift"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// A virtual repository's release list, release metadata, Package.swift and
// source archive come from its members through the package handlers' walk
// (pkgroute.Collect and pkgroute.MergeMembers): nested virtual
// repositories flattened, cycles and duplicates skipped, each member in
// 7.x's effective order (decision 0008, R1, R2). A member that is blacked
// out, that the caller may read nothing in or may not read the release in,
// whose read misses or is refused, or whose upstream fails or answers
// what the registry cannot use is passed over (R3): no member answers the
// registry's not-found, never 403 or 502. 7.x keeps a release's metadata
// and Package.swift in documents of its own (RTFACT-29202), whose path no
// source names, so only the archive takes the virtual repository's
// patterns (R5). Each member decides Read on what it serves: a virtual
// repository has no permission target of its own (R10).

func (h handler) virtualRead(w http.ResponseWriter, r *http.Request, q request) {
	switch q.kind {
	case "releases":
		h.virtualReleases(w, r, q.packageID)
	case "archive":
		h.virtualArchive(w, r, q.packageID)
	default:
		h.virtualRelease(w, r, q)
	}
}

// virtualReleases merges the members' release lists with
// swiftfmt.Releases, each release at the virtual repository's URL.
func (h handler) virtualReleases(w http.ResponseWriter, r *http.Request, p swiftfmt.Package) {
	parts, err := pkgroute.Collect(h.k, r, "", func(mr *http.Request) ([]string, error) {
		return h.memberVersions(mr, p)
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	versions, _ := swiftfmt.Releases{}.Merge(r.Context(), parts)
	if len(versions) == 0 {
		h.k.Error(w, r, bad(http.StatusNotFound, "Swift package not found"))
		return
	}
	writeJSON(w, r, releaseList(r, p, versions))
}

// memberVersions is the part of one member in a virtual repository's
// release list: the releases of p it lists whose archive the caller may
// read there.
func (h handler) memberVersions(mr *http.Request, p swiftfmt.Package) ([]string, error) {
	if !h.k.CanFolder(mr, p.Scope+"/"+p.Name, api.ActionRead) {
		return nil, nil
	}
	if pkgroute.RequestFrom(mr).Repo.Class != repo.Remote {
		return h.localVersions(mr, p)
	}
	versions, result, err := h.remoteVersions(mr, p)
	if err != nil {
		return nil, err
	}
	// The member's list is merged rather than served, so the recheck the
	// serving would run (fence) runs here.
	if result.Revalidate != nil {
		if err := result.Revalidate(mr.Context()); err != nil {
			return nil, pkgroute.SourceError(err, false) //nolint:wrapcheck // the protocol's answer, which the walk passes over
		}
	}
	if result.Used != nil {
		_ = result.Used(mr.Context())
	}
	return versions, nil
}

// virtualRelease serves a release's metadata or Package.swift from the
// first member that has the release and lets the caller read its archive.
// Package.swift's alternate links and its redirect to the default manifest
// name the virtual repository: a member request keeps the request's URL
// (releaseURL), and the links are dropped, so a client asks the virtual
// repository for an alternate manifest as it asked for this one.
func (h handler) virtualRelease(w http.ResponseWriter, r *http.Request, q request) {
	p := q.packageID
	members, err := h.k.MergeMembers(r, "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if !h.k.Can(mr, p.ArchivePath(), api.ActionRead) {
			continue
		}
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			if served, err := h.remoteRelease(w, mr, q); served || err != nil {
				if err != nil {
					h.k.Error(w, r, err)
				}
				return
			}
			continue
		}
		f, err := h.localArchive(mr, q.kind, p.ArchivePath())
		switch {
		case errors.Is(err, kernel.ErrNotFound), err == nil && f.Item.Folder:
			continue
		case err != nil:
			h.k.Error(w, r, err)
			return
		}
		if q.kind == "release" {
			h.localReleaseInfo(w, mr, p, f.Item)
		} else {
			h.localManifest(withoutLinks{w}, mr, p, f.Item)
		}
		return
	}
	h.k.Error(w, r, bad(http.StatusNotFound, "Swift release not found"))
}

// remoteRelease serves the release metadata or Package.swift of q through
// the remote member mr, and reports whether it did: a read the walk passes
// over serves nothing and is no error.
func (h handler) remoteRelease(w http.ResponseWriter, mr *http.Request, q request) (bool, error) {
	p := q.packageID
	if q.kind == "release" {
		info, result, err := h.remoteInfo(mr, p)
		if err != nil {
			return false, passedOver(err)
		}
		h.serveRemoteInfo(w, mr, p, info, result)
		return true, nil
	}
	result, err := h.remoteManifest(mr, p)
	if err != nil {
		return false, passedOver(err)
	}
	w.Header().Set("Content-Version", "1")
	h.k.ServeResult(w, mr, result)
	return true, nil
}

// passedOver is nil for a member read the walk passes over, and err
// otherwise.
func passedOver(err error) error {
	if pkgroute.PassedOver(err) {
		return nil
	}
	return err
}

// withoutLinks drops the Link header a member's Package.swift lists its
// alternates in.
type withoutLinks struct{ http.ResponseWriter }

func (w withoutLinks) WriteHeader(status int) {
	w.Header().Del("Link")
	w.ResponseWriter.WriteHeader(status)
}

func (w withoutLinks) Write(data []byte) (int, error) {
	w.Header().Del("Link")
	return w.ResponseWriter.Write(data) //nolint:wrapcheck // the writer's own error, as it is
}

// virtualArchive serves a release's source archive from the first member
// that has it and lets the caller read it. The virtual repository's own
// patterns leave it no member to search, which 6.20's
// VirtualRepoDownloadStrategy answers "No repository found to serve the
// request", and a member whose own policy refuses the archive is passed
// over (decision 0032, R5).
func (h handler) virtualArchive(w http.ResponseWriter, r *http.Request, p swiftfmt.Package) {
	path := p.ArchivePath()
	members, err := h.k.MergeMembers(r, path)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if !h.k.Can(mr, path, api.ActionRead) {
			continue
		}
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			result, err := h.remoteArchive(mr, p)
			if pkgroute.PassedOver(err) {
				continue
			}
			if err != nil {
				h.k.Error(w, r, err)
				return
			}
			w.Header().Set("Content-Version", "1")
			w.Header().Set("Content-Type", "application/zip")
			h.k.ServeResult(w, mr, result)
			return
		}
		found, err := h.k.ResolveDownloadItem(mr, path)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if found.Item.Folder {
			continue
		}
		archiveHeaders(w, p, found.Item)
		h.k.ServeFound(w, mr, found)
		return
	}
	h.k.Error(w, r, bad(http.StatusNotFound, "Swift release not found"))
}

// virtualPublish publishes a release through a virtual repository into its
// default deployment repository, on whose path the caller needs Write, as
// JFrog's Swift Repositories page has SwiftPM publish to the URL of a
// virtual repository (decision 0008, R6). Without a local deployment
// repository the answer is 7.x's 405 with Allow: GET.
func (h handler) virtualPublish(w http.ResponseWriter, r *http.Request, p swiftfmt.Package) {
	v := pkgroute.RequestFrom(r).Repo
	target, err := virtual.DeployTarget(r.Context(), h.k.DB().Repos(), v)
	if errors.Is(err, virtual.ErrNoDeployTarget) {
		h.k.Error(w, r, itemkit.NoDeployTarget(v.Key))
		return
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	mr, err := h.k.MemberRequest(r, target)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	h.localPublish(w, mr, p)
}
