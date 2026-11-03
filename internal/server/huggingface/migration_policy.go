package huggingface

import (
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/authz"
	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type migrationPathPair struct {
	source      string
	destination string
	folder      bool
}

// Compare each permission's entire target union. Keeping every grant's
// applicability unchanged preserves users, groups and anonymous grants,
// including exclusions; merely checking the administrator would not.
func (h handler) migrationPolicy(
	r *http.Request,
	id format.Identity,
	files []migrationFile,
	status migrationStatus,
	ref string,
) error {
	c, err := h.k.DB().Repos().Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key)
	if err != nil {
		return fmt.Errorf("huggingface: migration repository: %w", err)
	}
	if c.PackageType != repo.HuggingFaceML || c.Class != repo.Local && c.Class != repo.Federated ||
		c.BlackedOut != nil && *c.BlackedOut {
		return apierror.New(http.StatusConflict, "repository changed or is blacked out")
	}
	pairs := make([]migrationPathPair, 0, len(files)+3)
	pairs = append(pairs,
		migrationPathPair{id.Kind + "/" + id.RepoID(), id.Prefix(), true},
		migrationPathPair{status.Source, status.Destination, true})
	for _, file := range files {
		pairs = append(pairs, migrationPathPair{file.item.FullPath(), file.path, false})
	}
	if ref != "" {
		pairs = append(pairs, migrationPathPair{status.Source, ref, false})
	}
	patterns := authz.PatternsOf(c)
	for _, pair := range pairs {
		if patterns.Accepts(
			pair.source,
			pair.folder,
		) != patterns.Accepts(
			pair.destination,
			pair.folder,
		) {
			return apierror.New(
				http.StatusConflict,
				"repository patterns change access after migration",
			)
		}
	}
	page := store.Page{Limit: maxMigrationItems}
	seen := 0
	for {
		permissions, next, err := h.k.DB().Permissions().List(r.Context(), page)
		if err != nil {
			return fmt.Errorf("huggingface: migration permissions: %w", err)
		}
		for _, permission := range permissions {
			seen++
			if seen > maxMigrationItems {
				return apierror.New(
					http.StatusRequestEntityTooLarge,
					"migration permission scan exceeds 1024 permissions",
				)
			}
			resource := permission.Resources[types.ResourceArtifact]
			var targets []types.Target
			for key, rule := range resource.Targets {
				if types.MatchesRepository(key, c.Key, string(c.Class)) {
					targets = append(targets, rule)
				}
			}
			for _, pair := range pairs {
				if migrationAllows(
					targets,
					pair.source,
					pair.folder,
				) != migrationAllows(
					targets,
					pair.destination,
					pair.folder,
				) {
					return apierror.New(
						http.StatusConflict,
						"permission target changes access after migration: "+permission.Name,
					)
				}
			}
		}
		if next == "" {
			return nil
		}
		page.After = next
	}
}

func migrationAllows(targets []types.Target, path string, folder bool) bool {
	for _, target := range targets {
		excluded := false
		for _, pattern := range target.Exclude {
			if authz.CompilePattern(pattern).Match(path) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		if len(target.Include) == 0 {
			return true
		}
		for _, pattern := range target.Include {
			compiled := authz.CompilePattern(pattern)
			if compiled.Match(path) || folder && compiled.MatchBelow(path) {
				return true
			}
		}
	}
	return false
}
