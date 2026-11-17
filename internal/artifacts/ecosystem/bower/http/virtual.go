package bowerhttp

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	bowerfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bower"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// A virtual repository's package list, lookup and refs merge what its
// members hold through the package handlers' walk (pkgroute.Collect): nested
// virtual repositories flattened, cycles and duplicates skipped, each member
// in 7.x's effective order, and a member that is blacked out, that the
// caller may read nothing in, or whose read misses or whose upstream fails
// passed over (decision 0008, R1 to R3). Bower keeps none of this metadata
// at a documented path, so no path of it takes the members' patterns.
// Each member decides Read on its own rows: a virtual repository has no
// permission target of its own (decision 0008, R10).

// virtualListed is the names a virtual repository's package list and search
// answer: each name a member lists with a version the caller may read in
// that member.
func (h handler) virtualListed(r *http.Request, query string) ([]string, error) {
	parts, err := pkgroute.Collect(h.k, r, "", func(mr *http.Request) ([]string, error) {
		return h.listed(mr, query, false)
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit's answer or a member's fault, as it is
	}
	return bowerfmt.Names{}.Merge(r.Context(), parts) //nolint:wrapcheck // a pure merge never fails
}

// virtualVersions is the versions a virtual repository resolves name to:
// the union of the versions each member holds that the caller may read
// there, in the walk's order.
func (h handler) virtualVersions(r *http.Request, name string) ([]string, error) {
	parts, err := pkgroute.Collect(h.k, r, "", func(mr *http.Request) ([]string, error) {
		return h.versions(mr, name)
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit's answer or a member's fault, as it is
	}
	return bowerfmt.Versions{}.Merge(r.Context(), parts) //nolint:wrapcheck // a pure merge never fails
}

// serveBinary serves version's archive from r's own repository, never a
// virtual one: binary hands a virtual request to virtualBinary, which
// serves through a member request.
func (h handler) serveBinary(w http.ResponseWriter, r *http.Request, name, version, tag string) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		h.remoteBinary(w, r, name, version, tag)
		return
	}
	h.localBinary(w, r, name, version)
}

// virtualBinary serves version's archive from the first member in the walk
// that holds it and serves it: binary has checked the virtual repository's
// own patterns (archivePolicy). A member the caller may not read the
// version's manifest in, one whose own policy refuses the archive, one
// without the version and one whose read misses or whose upstream fails is
// passed over, as 7.x's walk passes over every member that does not serve
// the file (decision 0008, R3; decision 0032, R5).
func (h handler) virtualBinary(w http.ResponseWriter, r *http.Request, name, version, tag string) {
	members, err := h.k.MergeMembers(r, "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if !h.k.Can(mr, bowerfmt.ManifestPath(name, version), api.ActionRead) {
			continue
		}
		refuses, err := h.memberRefuses(mr, name, version)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if refuses {
			continue
		}
		vs, err := h.versions(mr, name)
		switch {
		case err != nil && pkgroute.PassedOver(err):
			continue
		case err != nil:
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		case !slices.Contains(vs, version):
			continue
		}
		if dependencyRewrite(pkgroute.RequestFrom(r).Repo) {
			h.rewrittenMember(w, r, mr, name, version, tag)
		} else {
			h.serveBinary(w, mr, name, version, tag)
		}
		return
	}
	h.k.Error(w, r, apierror.New(http.StatusNotFound, "Bower version not found"))
}

// memberRefuses reports whether a virtual repository's member refuses the
// archive of name's version by its own download policy, so that the next
// member serves it: a remote member's at the path its cache keeps the
// archive at, which the source boundary checks when it reads it, and a
// hosted member's at the version's manifest and every file the archive
// holds, which localBinary checks, so a member whose patterns reject one
// file is passed over rather than answering for the virtual repository.
// The virtual repository's own patterns rejecting one of those files is
// returned as the refusal to answer.
func (h handler) memberRefuses(mr *http.Request, name, version string) (bool, error) {
	member := pkgroute.RequestFrom(mr).Repo
	if member.Class == repo.Remote {
		return generic.CheckDownload(member, remoteArchivePath(name, version), false) != nil, nil
	}
	paths := []string{bowerfmt.ManifestPath(name, version)}
	for page := (kernel.Page{Limit: kernel.MaxLimit}); ; {
		batch, next, err := h.k.DB().
			Items().
			Descendants(mr.Context(), member.Key, false, name+"/"+version, page)
		if err != nil {
			return false, fmt.Errorf("bower list member files: %w", err)
		}
		for _, it := range batch {
			if !it.Folder {
				paths = append(paths, it.FullPath())
			}
		}
		if next == "" {
			break
		}
		page.After = next
	}
	for _, path := range paths {
		err := h.k.CheckDownload(mr, path)
		if pkgroute.IsRefusal(err) {
			return false, fmt.Errorf("bower virtual archive: %w", err)
		}
		if err != nil {
			return true, nil
		}
	}
	return false, nil
}
