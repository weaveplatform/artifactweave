package bower

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
		if errors.Is(err, store.ErrNotFound) {
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
		if !h.k.CanFolder(mr, "", types.ActionRead) {
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

func (h handler) virtualVersions(r *http.Request, name string) ([]string, error) {
	members, err := h.virtualMembers(r)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, mr := range members {
		vs, err := h.versions(mr, name)
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
		members, err := h.virtualMembers(r)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		for _, mr := range members {
			vs, err := h.versions(mr, name)
			if err != nil {
				h.k.Error(w, r, pkgroute.SourceError(err, false))
				return
			}
			if !slices.Contains(vs, version) {
				continue
			}
			h.serveBinary(w, mr, name, version, tag)
			return
		}
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Bower version not found"))
	default:
		h.localBinary(w, r, name, version)
	}
}
