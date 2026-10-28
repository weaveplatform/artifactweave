package conda

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"

	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (h handler) members(r *http.Request) ([]*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class != repo.Virtual {
		return nil, apierror.New(http.StatusBadRequest, "not a virtual Conda repository")
	}
	members := make([]*http.Request, 0, len(c.Repositories))
	seen := map[string]bool{c.Key: true}
	for _, key := range c.Repositories {
		if seen[key] {
			return nil, apierror.New(http.StatusBadRequest, "duplicate or cyclic Conda member")
		}
		seen[key] = true
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("conda: virtual member %s: %w", key, err)
		}
		if member.PackageType != repo.Conda || member.Class != repo.Local &&
			member.Class != repo.Remote && member.Class != repo.Federated &&
			member.Class != repo.Virtual {
			return nil, apierror.New(http.StatusBadRequest, "incompatible Conda virtual member")
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("conda: member request %s: %w", key, err)
		}
		members = append(members, mr)
	}
	return members, nil
}

func (h handler) virtualIndex(r *http.Request, id condafmt.MetadataPath, seen map[string]bool) (
	condafmt.Repodata, func(context.Context) error, error,
) {
	members, err := h.members(r)
	if err != nil {
		return condafmt.Repodata{}, nil, err
	}
	combined := condafmt.EmptyRepodata(id.Subdir)
	var fences []func(context.Context) error
	for _, mr := range members {
		if !h.k.CanFolder(mr, path.Dir(id.Path), types.ActionRead) ||
			!h.k.Can(mr, id.Path, types.ActionRead) {
			continue
		}
		index, finish, err := h.memberIndex(mr, id.Path, seen)
		if shardNotFound(err) {
			continue
		}
		if err != nil {
			return condafmt.Repodata{}, nil, err
		}
		if finish != nil {
			fences = append(fences, finish)
		}
		for name, rec := range index.Packages {
			if _, exists := combined.Packages[name]; !exists {
				combined.Packages[name] = rec
			}
		}
		for name, rec := range index.PackagesConda {
			if _, exists := combined.PackagesConda[name]; !exists {
				combined.PackagesConda[name] = rec
			}
		}
	}
	filtered, err := h.filterIndex(r.Context(), r, combined, id, nil)
	if err != nil {
		return condafmt.Repodata{}, nil, err
	}
	return filtered, func(ctx context.Context) error {
		for _, fence := range fences {
			if err := fence(ctx); err != nil {
				return fmt.Errorf("conda: virtual source authority: %w", err)
			}
		}
		return nil
	}, nil
}
