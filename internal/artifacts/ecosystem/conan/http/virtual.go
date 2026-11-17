package conanhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
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

func (h handler) virtualMembers(r *http.Request, path string) ([]*http.Request, error) {
	members, err := h.k.MergeMembers(r, path)
	if err != nil {
		return nil, fmt.Errorf("conan virtual members: %w", err)
	}
	return members, nil
}

func (h handler) virtualDeployment(
	w http.ResponseWriter,
	r *http.Request,
	path string,
) (*http.Request, bool) {
	c := pkgroute.RequestFrom(r).Repo
	target, err := virtual.DeployTarget(r.Context(), h.k.DB().Repos(), c)
	if errors.Is(err, virtual.ErrNoDeployTarget) {
		w.Header().Set("Allow", "GET")
		h.error(
			w,
			r,
			apierror.New(http.StatusMethodNotAllowed, fmt.Sprintf("No local repository was configured as local deployment repository for the (%s) virtual repository.", c.Key)),
		)
		return nil, false
	}
	if err != nil {
		h.error(w, r, err)
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
	entry, err := h.k.Merged(r, revisionFolder(ref, id), "conan-revisions:"+revisionFolder(ref, id), func(r *http.Request) (virtual.Entry, error) {
		list, err := h.buildVirtualRevisions(r, ref, id)
		if err != nil {
			return virtual.Entry{}, err
		}
		b, err := json.Marshal(list)
		if err != nil {
			return virtual.Entry{}, fmt.Errorf("encode conan revisions: %w", err)
		}
		return virtual.Entry{Body: b}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("conan virtual revisions: %w", err)
	}
	var list []revision
	if err = json.Unmarshal(entry.Body, &list); err != nil {
		return nil, fmt.Errorf("cached conan revisions: %w", err)
	}
	return list, nil
}

func (h handler) buildVirtualRevisions(r *http.Request, ref conanfmt.Reference, id string) ([]revision, error) {
	members, err := h.virtualMembers(r, revisionFolder(ref, id))
	if err != nil {
		return nil, fmt.Errorf("conan virtual revisions: %w", err)
	}
	var parts []virtual.Part[[]conanfmt.Revision]
	for _, mr := range members {
		if !h.k.CanFolder(mr, revisionFolder(ref, id), api.ActionRead) {
			continue
		}
		// 7.x ignores a blacked-out member when it resolves (R5).
		if generic.CheckBlackedOut(pkgroute.RequestFrom(mr).Repo, "") != nil {
			continue
		}
		list, err := h.revisions(mr, ref, id)
		if pkgroute.PassedOver(err) {
			h.k.WarnPassedOver(mr, err)
			continue
		}
		if err != nil {
			return nil, err
		}
		parts = append(parts, virtual.Part[[]conanfmt.Revision]{Member: pkgroute.RequestFrom(mr).Repo, Doc: list})
	}
	out, err := (conanfmt.Merger{}).Merge(r.Context(), parts)
	if err != nil {
		return nil, fmt.Errorf("conan virtual revisions: %w", err)
	}
	if len(out) == 0 {
		return nil, apierror.New(404, "Conan revision not found")
	}

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
	memberPath := ""
	if download {
		memberPath = f.Path()
	}
	members, err := h.virtualMembers(r, memberPath)
	if err != nil {
		return nil, fmt.Errorf("conan virtual members: %w", err)
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
			if pkgroute.PassedOver(e) {
				h.k.WarnPassedOver(mr, e)
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
		if e := h.requireComplete(mr, f.Root()); missing(e) || memberDenied(e) {
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
			if pkgroute.PassedOver(e) {
				h.k.WarnPassedOver(mr, e)
				continue
			}
			if e != nil {
				return nil, fmt.Errorf("resolve conan member file: %w", e)
			}
		}
		return mr, nil
	}
	id := ""
	if f.Package {
		id = f.ID
	}
	h.k.EvictMerged(r, "conan-revisions:"+revisionFolder(f.Ref, id))
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
	h.cachedVirtual(w, r, "", "conan-search:"+r.URL.RawQuery, h.buildVirtualSearch)
}

func (h handler) buildVirtualSearch(w http.ResponseWriter, r *http.Request) {
	members, err := h.virtualMembers(r, "")
	if err != nil {
		h.error(w, r, err)
		return
	}
	var parts []virtual.Part[[]string]
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
			err := apierror.New(rec.Code, "Conan member search failed")
			if pkgroute.PassedOver(err) {
				h.k.WarnPassedOver(mr, err)
				continue
			}
			h.error(w, r, err)
			return
		}
		var value struct {
			Results []string `json:"results"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &value) != nil {
			h.error(w, r, source.ErrInvalidResult)
			return
		}
		var filtered []string
		for _, name := range value.Results {
			nv, uc, ok := strings.Cut(name, "@")
			n, v, okNV := strings.Cut(nv, "/")
			u, c, okUC := strings.Cut(uc, "/")
			folder := strings.Join([]string{u, n, v, c}, "/")
			if ok && okNV && okUC && repo.PatternsOf(pkgroute.RequestFrom(r).Repo).AcceptsRequest(folder, true) {
				filtered = append(filtered, name)
			}
		}
		parts = append(parts, virtual.Part[[]string]{Member: pkgroute.RequestFrom(mr).Repo, Doc: filtered})
	}
	results, _ := (conanfmt.SearchMerger{}).Merge(r.Context(), parts)
	if results == nil {
		results = []string{}
	}
	writeJSON(w, map[string]any{"results": results})
}

func (h handler) buildVirtualPackageSearch(
	w http.ResponseWriter,
	r *http.Request,
	ref conanfmt.Reference,
) {
	members, err := h.virtualMembers(r, ref.Root()+"/package")
	if err != nil {
		h.error(w, r, err)
		return
	}
	var parts []virtual.Part[map[string]json.RawMessage]
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
			err := apierror.New(rec.Code, "Conan member package search failed")
			if pkgroute.PassedOver(err) {
				h.k.WarnPassedOver(mr, err)
				continue
			}
			h.error(w, r, err)
			return
		}
		var value map[string]json.RawMessage
		if json.Unmarshal(rec.Body.Bytes(), &value) != nil {
			h.error(w, r, source.ErrInvalidResult)
			return
		}
		parts = append(parts, virtual.Part[map[string]json.RawMessage]{Member: pkgroute.RequestFrom(mr).Repo, Doc: value})
	}
	result, _ := (conanfmt.PackageMerger{}).Merge(r.Context(), parts)
	writeJSON(w, result)
}

func (h handler) virtualPackageSearch(w http.ResponseWriter, r *http.Request, ref conanfmt.Reference) {
	h.cachedVirtual(
		w,
		r,
		ref.Root()+"/package",
		"conan-package-search:"+ref.Root()+"?"+r.URL.RawQuery,
		func(w http.ResponseWriter, r *http.Request) { h.buildVirtualPackageSearch(w, r, ref) },
	)
}

func (h handler) cachedVirtual(w http.ResponseWriter, r *http.Request, path, document string, build http.HandlerFunc) {
	entry, err := h.k.Merged(r, path, document, func(r *http.Request) (virtual.Entry, error) {
		rec := httptest.NewRecorder()
		build(rec, r)
		if rec.Code != http.StatusOK {
			return virtual.Entry{}, apierror.New(rec.Code, "Conan virtual metadata unavailable")
		}
		return virtual.Entry{Body: rec.Body.Bytes()}, nil
	})
	if err != nil {
		h.error(w, r, err)
		return
	}
	writeJSON(w, json.RawMessage(entry.Body))
}

// A local member may expose a file but not the manifest required to verify
// its revision. That member is unreadable through a virtual (R3); an upstream
// authentication failure keeps its NoChallenge response and remains an error.
func memberDenied(err error) bool {
	var answer *apierror.Error
	return errors.As(err, &answer) && !answer.NoChallenge && (answer.Status == http.StatusForbidden || answer.Status == http.StatusUnauthorized)
}
