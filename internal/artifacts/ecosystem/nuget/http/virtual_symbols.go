package nugethttp

import (
	"context"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	nugetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (h handler) virtualSymbolRoute(w http.ResponseWriter, r *http.Request, root string) bool {
	q := pkgroute.RequestFrom(r)
	if r.Method == http.MethodPut && strings.Trim(q.Rest, "/") == "symbols" {
		if mr := h.virtualDeployment(w, r); mr != nil {
			h.symbols(w, mr)
		}
		return true
	}
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.Count(strings.Trim(q.Rest, "/"), "/") == 2 && root != "flatcontainer" && root != "registration" &&
		root != "registration-semver2" {
		h.virtualSymbol(w, r)
		return true
	}
	return false
}

func (h handler) virtualSymbol(w http.ResponseWriter, r *http.Request) {
	parts, err := segments(r, "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if len(parts) != 3 {
		h.k.Error(w, r, notFound(r))
		return
	}
	path, ok := nugetfmt.SymbolPath(parts[0], parts[1], parts[2])
	if !ok {
		h.k.Error(w, r, notFound(r))
		return
	}
	// Establish a readable real path before revealing any download policy.
	members, err := h.k.MergeMembers(r, "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	readable := false
	for _, mr := range members {
		readable = readable || h.k.Can(mr, path, api.ActionRead)
	}
	if !readable {
		h.k.Error(w, r, notFound(r))
		return
	}
	members, err = h.k.DownloadMembers(r, path)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if !h.k.Can(mr, path, api.ActionRead) {
			continue
		}
		if pkgroute.RequestFrom(mr).Repo.Class != repo.Remote {
			if _, err := h.k.ResolveDownloadItem(mr, path); err != nil {
				if nugetPassedOver(err) {
					continue
				}
				h.k.Error(w, r, err)
				return
			}
			h.k.ServeItem(w, mr, path)
			return
		}
		s := &remoteSession{h: h, r: mr, virtual: true}
		result, err := s.symbolResult(path)
		if err == nil && result.Revalidate != nil {
			err = result.Revalidate(r.Context())
		}
		if err != nil {
			if result.Stream != nil {
				_ = result.Stream.Body.Close()
			}
			if nugetPassedOver(err) {
				continue
			}
			h.k.Error(w, r, err)
			return
		}
		result.Revalidate = symbolFence(result.Revalidate)
		h.k.ServeResult(w, mr, result)
		return
	}
	h.k.Error(w, r, notFound(r))
}

func symbolFence(fence func(context.Context) error) func(context.Context) error {
	if fence == nil {
		return nil
	}
	return func(ctx context.Context) error {
		err := fence(ctx)
		if nugetPassedOver(err) {
			return kernel.ErrNotFound
		}
		return err
	}
}
