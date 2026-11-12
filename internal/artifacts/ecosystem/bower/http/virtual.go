package bowerhttp

import (
	"errors"
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

func (h handler) virtualMembers(r *http.Request) ([]*http.Request, error) {
	virtual := pkgroute.RequestFrom(r).Repo
	seen := map[string]bool{virtual.Key: true}
	var out []*http.Request
	for _, key := range virtual.Repositories {
		if seen[key] {
			return nil, apierror.New(http.StatusBadRequest, "invalid Bower virtual member")
		}
		seen[key] = true
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("bower virtual member: %w", err)
		}
		if member.Class != repo.Local && member.Class != repo.Federated &&
			member.Class != repo.Remote {
			return nil, apierror.New(http.StatusBadRequest, "unsupported Bower virtual member")
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("bower virtual member request: %w", err)
		}
		out = append(out, mr)
	}
	return out, nil
}

func (h handler) virtualPackageNames(r *http.Request, query string) ([]string, error) {
	members, err := h.virtualMembers(r)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, mr := range members {
		if !h.k.CanFolder(mr, "", api.ActionRead) {
			continue
		}
		names, err := h.packageNames(mr, query)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func (h handler) virtualVersions(r *http.Request, name string, resolving bool) ([]string, error) {
	members, err := h.virtualMembers(r)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, mr := range members {
		// 7.x ignores a blacked-out member when it resolves (R5). A hosted
		// member's versions come from its package index, which the
		// blackout does not touch, so it is left out here.
		if resolving && generic.CheckBlackedOut(pkgroute.RequestFrom(mr).Repo, "") != nil {
			continue
		}
		vs, err := h.readableVersions(mr, name, resolving)
		if err != nil {
			return nil, err
		}
		for _, version := range vs {
			if !seen[version] {
				seen[version] = true
				out = append(out, version)
			}
		}
	}
	return out, nil
}

func (h handler) serveBinary(w http.ResponseWriter, r *http.Request, name, version, tag string) {
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Remote:
		h.remoteBinary(w, r, name, version, tag)
	case repo.Virtual:
		// binary has checked the virtual repository's own patterns
		// (archivePolicy). A member whose own policy refuses the archive is
		// passed over, as one without the version is (decision 0032, R5).
		members, err := h.virtualMembers(r)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		for _, mr := range members {
			refuses, err := h.memberRefuses(mr, name, version)
			if err != nil {
				h.k.Error(w, r, err)
				return
			}
			if refuses {
				continue
			}
			vs, err := h.versions(mr, name)
			if err != nil {
				h.k.Error(w, r, pkgroute.SourceError(err, false))
				return
			}
			if !slices.Contains(vs, version) {
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
	default:
		h.localBinary(w, r, name, version)
	}
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
