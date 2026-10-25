package swift

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (h handler) virtualMembers(r *http.Request) ([]*http.Request, error) {
	virtual := pkgroute.RequestFrom(r).Repo
	seen := map[string]bool{virtual.Key: true}
	out := []*http.Request{}
	for _, key := range virtual.Repositories {
		if seen[key] {
			return nil, bad(http.StatusBadRequest, "invalid Swift virtual member")
		}
		seen[key] = true
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("swift: virtual member: %w", err)
		}
		if member.Class != repo.Local && member.Class != repo.Federated &&
			member.Class != repo.Remote {
			return nil, bad(http.StatusBadRequest, "invalid Swift virtual member class")
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("swift: virtual member: %w", err)
		}
		out = append(out, mr)
	}
	return out, nil
}

func (h handler) virtualRead(w http.ResponseWriter, r *http.Request, q request) {
	path := q.packageID.ArchivePath()
	if q.kind == "releases" {
		if !h.k.RequireFolder(w, r, q.packageID.Scope+"/"+q.packageID.Name, types.ActionRead) {
			return
		}
	} else if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	members, err := h.virtualMembers(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if q.kind == "archive" {
		h.virtualArchive(w, r, q, members)
		return
	}
	if q.kind == "releases" {
		merged := releases{Releases: map[string]release{}}
		for _, mr := range members {
			if !h.k.CanFolder(mr, q.packageID.Scope+"/"+q.packageID.Name, types.ActionRead) {
				continue
			}
			response := h.memberMetadata(mr, q)
			if response.Code == http.StatusNotFound || response.Code == http.StatusForbidden {
				continue
			}
			if response.Code != http.StatusOK {
				h.k.Error(w, r, bad(http.StatusBadGateway, "Swift virtual member failed"))
				return
			}
			member, parseErr := parseReleases(response.Body)
			if parseErr != nil {
				h.k.Error(w, r, parseErr)
				return
			}
			for version := range member.Releases {
				p := q.packageID
				p.Version = version
				if _, exists := merged.Releases[version]; !exists &&
					h.k.Can(r, p.ArchivePath(), types.ActionRead) {
					merged.Releases[version] = release{URL: releaseURL(r, p)}
				}
			}
		}
		if len(merged.Releases) == 0 {
			h.k.Error(w, r, bad(http.StatusNotFound, "Swift package not found"))
			return
		}
		writeJSON(w, r, merged)
		return
	}
	for _, mr := range members {
		if !h.k.Can(mr, path, types.ActionRead) {
			continue
		}
		response := h.memberMetadata(mr, q)
		if response.Code == http.StatusNotFound || response.Code == http.StatusForbidden {
			continue
		}
		if response.Code != http.StatusOK && response.Code != http.StatusSeeOther {
			h.k.Error(w, r, bad(http.StatusBadGateway, "Swift virtual member failed"))
			return
		}
		if q.kind == "manifest" {
			// A member's redirect or alternate links must not bypass the virtual
			// repository's permission checks on the next client request.
			response.Header().Del("Link")
			if response.Code == http.StatusSeeOther {
				response.Header().Set("Location", releaseURL(r, q.packageID)+"/Package.swift")
			}
		}
		for key, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.Code)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, response.Body)
		}
		return
	}
	h.k.Error(w, r, bad(http.StatusNotFound, "Swift release not found"))
}

func (h handler) memberMetadata(r *http.Request, q request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Local, repo.Federated:
		h.localRead(w, r, q)
	case repo.Remote:
		h.remoteRead(w, r, q)
	}
	return w
}

func (h handler) virtualArchive(
	w http.ResponseWriter,
	r *http.Request,
	q request,
	members []*http.Request,
) {
	p := q.packageID
	path := p.ArchivePath()
	for _, mr := range members {
		if !h.k.Can(mr, path, types.ActionRead) {
			continue
		}
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			result, err := h.remoteArchive(mr, p)
			if errors.Is(err, store.ErrNotFound) {
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
		found, err := h.k.ResolveItem(mr, path)
		if errors.Is(err, store.ErrNotFound) {
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
