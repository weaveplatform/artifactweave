package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/format/cran"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// MoveCranArchives migrates the legacy package/version source archive layout.
// All item moves, lifecycle hooks and queued work commit together. JFrog
// documents the endpoint and switch, but not its response document; moved is
// artifactweave's count of source archives relocated by this request.
func (s artifactoryService) MoveCranArchives(
	ctx context.Context,
	req artgen.MoveCranArchivesRequestObject,
) (artgen.MoveCranArchivesResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	if !s.cfg.CranArchiveMoverEnabled {
		return nil, apierror.New(http.StatusNotFound, "CRAN archive migration is disabled")
	}
	st, c, by, err := s.operationRepo(ctx, repo.Cran, reindexOp{
		isAdminOnly: true,
		classes:     []repo.Class{repo.Local, repo.Federated},
		refused:     "Repository '%s' must be a local or federated CRAN repository",
	}, req.RepoKey)
	if err != nil {
		return nil, err
	}
	moved := 0
	err = st.Tx(ctx, func(ctx context.Context) error {
		var err error
		moved, err = s.moveCranArchives(ctx, st, c, by)
		return err
	})
	if errors.Is(err, store.ErrConflict) {
		return nil, apierror.New(
			http.StatusConflict,
			"CRAN archive migration conflicts with existing or changed content; no archives were moved",
		)
	}
	if err != nil {
		return nil, s.itemError(ctx, "move CRAN archives", err)
	}
	return artgen.MoveCranArchives200JSONResponse{"moved": moved}, nil
}

func (s artifactoryService) moveCranArchives(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	by string,
) (int, error) {
	svc := s.items(st)
	page := store.Page{Limit: store.MaxLimit}
	moved := 0
	for {
		entries, next, err := st.Items().Descendants(ctx, c.Key, false, "src/contrib/Archive", page)
		if err != nil {
			return 0, fmt.Errorf("cran: list archives: %w", err)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return 0, fmt.Errorf("cran: archive migration canceled: %w", err)
			}
			if entry.Folder {
				continue
			}
			target, legacy := cran.LegacyArchiveTarget(entry.FullPath())
			if !legacy {
				continue
			}
			n, err := moveCranArchive(ctx, svc, c, entry, target, by)
			if err != nil {
				return 0, err
			}
			moved += n
		}
		if next == "" {
			return moved, nil
		}
		page.After = next
	}
}

func moveCranArchive(
	ctx context.Context,
	svc *item.Service,
	c repo.Config,
	entry types.Item,
	target, by string,
) (int, error) {
	// GetByID holds the SQL row lock until the enclosing transaction ends.
	// A competing migration may have moved it while this request waited.
	current, err := svc.Store.Items().GetByID(ctx, entry.ID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("cran: lock archive: %w", err)
	}
	if current.FullPath() != entry.FullPath() || current.Folder {
		return 0, fmt.Errorf("cran: archive changed during migration: %w", store.ErrConflict)
	}
	if _, err := svc.Store.Items().Get(ctx, c.Key, false, target); err == nil {
		return 0, store.ErrConflict
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, fmt.Errorf("cran: archive destination: %w", err)
	}
	result, err := svc.Transfer(ctx, item.Transfer{
		From: item.Target{Repo: c, Path: current.FullPath()},
		To:   item.Target{Repo: c, Path: target},
		Move: true, FailFast: true, IfAbsent: true, By: by,
	})
	if err != nil {
		return 0, fmt.Errorf("cran: move archive: %w", err)
	}
	if result.Failed != 0 || result.Files != 1 {
		return 0, badRequestf("CRAN archive cannot be migrated; no archives were moved")
	}
	return 1, nil
}
