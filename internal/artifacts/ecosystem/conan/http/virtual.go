package conanhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	conanfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conan"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type virtualSource struct{}

func (virtualSource) Item(
	context.Context,
	pkgroute.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func (h handler) virtualMembers(r *http.Request) ([]*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class != repo.Virtual {
		return nil, apierror.New(400, "not a virtual repository")
	}
	seen := map[string]bool{c.Key: true}
	var out []*http.Request
	for _, key := range c.Repositories {
		if seen[key] {
			return nil, apierror.New(400, "invalid Conan virtual member")
		}
		seen[key] = true
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("conan member %s: %w", key, err)
		}
		if member.Class != repo.Local && member.Class != repo.Federated &&
			member.Class != repo.Remote {
			return nil, apierror.New(400, "unsupported Conan virtual member class")
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("resolve conan member: %w", err)
		}
		out = append(out, mr)
	}
	return out, nil
}

func (h handler) virtualDeployment(
	w http.ResponseWriter,
	r *http.Request,
	path string,
) (*http.Request, bool) {
	if !h.k.Require(w, r, path, api.ActionWrite) {
		return nil, false
	}
	c := pkgroute.RequestFrom(r).Repo
	if c.DefaultDeploymentRepo == "" || !slices.Contains(c.Repositories, c.DefaultDeploymentRepo) {
		h.error(
			w,
			r,
			apierror.New(405, "virtual Conan repository has no local deployment target"),
		)
		return nil, false
	}
	target, err := h.k.DB().Repos().Get(r.Context(), c.DefaultDeploymentRepo)
	if err != nil {
		if errors.Is(err, kernel.ErrNotFound) {
			err = apierror.New(http.StatusNotFound, "Conan deployment target not found")
		}
		h.error(w, r, err)
		return nil, false
	}
	if target.Class != repo.Local && target.Class != repo.Federated {
		h.error(w, r, apierror.New(400, "Conan deployment target must be local"))
		return nil, false
	}
	mr, err := h.k.MemberRequest(r, target)
	if err != nil || !h.k.Can(mr, path, api.ActionWrite) {
		h.error(w, r, denied(r))
		return nil, false
	}
	return mr, true
}

func (h handler) virtualUpload(w http.ResponseWriter, r *http.Request, f conanfmt.FilePath) {
	mr, ok := h.virtualDeployment(w, r, f.Path())
	if ok {
		h.upload(w, mr, f)
	}
}

func missing(err error) bool {
	if errors.Is(err, kernel.ErrNotFound) {
		return true
	}
	var e *apierror.Error
	return errors.As(err, &e) && e.Status == 404
}

func (h handler) virtualRevisions(
	r *http.Request,
	ref conanfmt.Reference,
	id string,
) ([]revision, error) {
	members, err := h.virtualMembers(r)
	if err != nil {
		return nil, err
	}
	var out []revision
	seen := map[string]bool{}
	for _, mr := range members {
		if !h.k.CanFolder(mr, revisionFolder(ref, id), api.ActionRead) {
			continue
		}
		// 7.x ignores a blacked-out member when it resolves (R5).
		if generic.CheckBlackedOut(pkgroute.RequestFrom(mr).Repo, "") != nil {
			continue
		}
		list, err := h.revisions(mr, ref, id)
		if missing(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, rev := range list {
			if !seen[rev.Revision] {
				out = append(out, rev)
				seen[rev.Revision] = true
			}
		}
	}
	if len(out) == 0 {
		return nil, apierror.New(404, "Conan revision not found")
	}
	slices.SortFunc(out, func(a, b revision) int {
		if n := b.Time.Compare(a.Time); n != 0 {
			return n
		}
		return strings.Compare(b.Revision, a.Revision)
	})
	return out, nil
}

// virtualRevisionMember returns the request for the first member that has
// f's revision. A blacked-out member is passed over, and for a download,
// a member whose own policy refuses f, remote members included, so the
// next member serves it (R5). A file list is answered by a member whose
// patterns reject one of the files, as that member's own list is (R3).
func (h handler) virtualRevisionMember(
	r *http.Request,
	f conanfmt.FilePath,
	download bool,
) (*http.Request, error) {
	members, err := h.virtualMembers(r)
	if err != nil {
		return nil, err
	}
	// With no path checked only the blackout refuses; policy is the path a
	// remote member's blackout would name.
	checked, policy := "", requestPath(r)
	if download {
		checked, policy = f.Path(), f.Path()
	}
	for _, mr := range members {
		if !h.k.Can(mr, f.Path(), api.ActionRead) {
			continue
		}
		if generic.CheckDownload(pkgroute.RequestFrom(mr).Repo, checked, false) != nil {
			continue
		}
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			manifest := f
			manifest.File = conanfmt.Manifest
			entries, e := h.remoteManifest(mr, manifest, policy)
			if missing(e) {
				continue
			}
			if e != nil {
				return nil, fmt.Errorf("read conan member file: %w", e)
			}
			archive := !f.Package && f.File == "conan_export.tgz" ||
				f.Package && f.File == "conan_package.tgz"
			if f.File == conanfmt.Manifest || entries[f.File] != "" || archive {
				return mr, nil
			}
			continue
		}
		if e := h.requireComplete(mr, f.Root()); missing(e) {
			continue
		} else if e != nil {
			return nil, e
		}
		if f.File != conanfmt.Manifest {
			lookup := h.k.DownloadItem
			if document(f.File) {
				lookup = h.k.StoredItem
			}
			_, e := lookup(mr, f.Path())
			if missing(e) {
				continue
			}
			if e != nil {
				return nil, fmt.Errorf("resolve conan member file: %w", e)
			}
		}
		return mr, nil
	}
	return nil, apierror.New(404, "Conan revision not found")
}

func (h handler) virtualFileList(w http.ResponseWriter, r *http.Request, root string) {
	f, ok := conanfmt.ParseStored(root + "/" + conanfmt.Manifest)
	if !ok {
		h.bad(w, r, "invalid Conan revision root")
		return
	}
	mr, err := h.virtualRevisionMember(r, f, false)
	if err != nil {
		h.error(w, r, err)
		return
	}
	h.fileList(w, mr, root)
}

func (h handler) virtualFile(w http.ResponseWriter, r *http.Request, f conanfmt.FilePath) {
	// The virtual repository's own patterns leave it no member to search,
	// which says so rather than "Conan revision not found" (R5).
	if err := h.k.CheckDownload(r, f.Path()); err != nil {
		h.error(w, r, err)
		return
	}
	mr, err := h.virtualRevisionMember(r, f, true)
	if err != nil {
		h.error(w, r, err)
		return
	}
	h.file(w, mr, f)
}

func (h handler) virtualSearch(w http.ResponseWriter, r *http.Request) {
	members, err := h.virtualMembers(r)
	if err != nil {
		h.error(w, r, err)
		return
	}
	set := map[string]bool{}
	for _, mr := range members {
		if !h.k.CanFolder(mr, "", api.ActionRead) {
			continue
		}
		// A blacked-out remote member, whose own search the blackout
		// refuses, is passed over rather than failing the whole search, as
		// 7.x ignores it (R5, #216). A local member's search keeps its
		// answer, as a native search does under the blackout (R2b).
		if c := pkgroute.RequestFrom(mr).Repo; c.Class == repo.Remote &&
			generic.CheckBlackedOut(c, "") != nil {
			continue
		}
		rec := httptest.NewRecorder()
		h.search(rec, mr)
		if rec.Code != 200 {
			h.error(w, r, apierror.New(rec.Code, "Conan member search failed"))
			return
		}
		var value struct {
			Results []string `json:"results"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &value) != nil {
			h.error(w, r, source.ErrInvalidResult)
			return
		}
		for _, name := range value.Results {
			set[name] = true
		}
	}
	results := make([]string, 0, len(set))
	for name := range set {
		results = append(results, name)
	}
	slices.Sort(results)
	writeJSON(w, map[string]any{"results": results})
}

func (h handler) virtualPackageSearch(
	w http.ResponseWriter,
	r *http.Request,
	ref conanfmt.Reference,
) {
	members, err := h.virtualMembers(r)
	if err != nil {
		h.error(w, r, err)
		return
	}
	result := map[string]json.RawMessage{}
	for _, mr := range members {
		if !h.k.CanFolder(mr, ref.Root()+"/package", api.ActionRead) {
			continue
		}
		rec := httptest.NewRecorder()
		h.packageSearch(rec, mr, ref)
		if rec.Code == 404 {
			continue
		}
		if rec.Code != 200 {
			h.error(w, r, apierror.New(rec.Code, "Conan member package search failed"))
			return
		}
		var value map[string]json.RawMessage
		if json.Unmarshal(rec.Body.Bytes(), &value) != nil {
			h.error(w, r, source.ErrInvalidResult)
			return
		}
		for id, raw := range value {
			if _, ok := result[id]; !ok {
				result[id] = raw
			}
		}
	}
	writeJSON(w, result)
}
