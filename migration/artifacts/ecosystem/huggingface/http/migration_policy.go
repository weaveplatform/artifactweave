package huggingfacehttp

import (
	"errors"
	"fmt"
	"net/http"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	format "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/huggingface"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// Grants.AccessChange compares each permission's entire target union for
// every moved pair. Keeping every grant's applicability unchanged preserves
// users, groups and anonymous grants, including exclusions; merely checking
// the administrator would not.
func (h handler) migrationPolicy(
	r *http.Request,
	id format.Identity,
	files []migrationFile,
	status migrationStatus,
	ref string,
) error {
	if err := h.migrationAccess(r, files, status, ref); err != nil {
		return err
	}
	c, err := h.k.DB().Repos().Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key)
	if err != nil {
		return fmt.Errorf("huggingface: migration repository: %w", err)
	}
	if c.PackageType != repo.HuggingFaceML || c.Class != repo.Local && c.Class != repo.Federated ||
		c.BlackedOut != nil && *c.BlackedOut {
		return apierror.New(http.StatusConflict, "repository changed or is blacked out")
	}
	pairs := make([]accessapi.PathPair, 0, len(files)+3)
	pairs = append(
		pairs,
		accessapi.PathPair{
			Source:      id.Kind + "/" + id.RepoID(),
			Destination: id.Prefix(),
			Folder:      true,
		},
		accessapi.PathPair{Source: status.Source, Destination: status.Destination, Folder: true},
	)
	for _, file := range files {
		pairs = append(
			pairs,
			accessapi.PathPair{Source: file.item.FullPath(), Destination: file.path},
		)
	}
	if ref != "" {
		pairs = append(pairs, accessapi.PathPair{Source: status.Source, Destination: ref})
	}
	patterns := repo.PatternsOf(c)
	for _, pair := range pairs {
		if patterns.Accepts(
			pair.Source,
			pair.Folder,
		) != patterns.Accepts(
			pair.Destination,
			pair.Folder,
		) {
			return apierror.New(
				http.StatusConflict,
				"repository patterns change access after migration",
			)
		}
	}
	name, err := h.o.Grants.AccessChange(
		r.Context(),
		c.Key,
		string(c.Class),
		pairs,
		maxMigrationItems,
	)
	switch {
	case errors.Is(err, accessapi.ErrScanLimit):
		return apierror.New(
			http.StatusRequestEntityTooLarge,
			"migration permission scan exceeds 1024 permissions",
		)
	case err != nil:
		return fmt.Errorf("huggingface: migration permissions: %w", err)
	case name != "":
		return apierror.New(
			http.StatusConflict,
			"permission target changes access after migration: "+name,
		)
	}
	return nil
}

func migrationAccessDenied() error {
	return apierror.New(
		http.StatusForbidden,
		"migration requires source read and destination read/deploy permissions",
	)
}

// Administrative authority does not remove an access token's artifact path
// restrictions. Check them for plans and completed status as well as writes.
func (h handler) migrationAccess(
	r *http.Request,
	files []migrationFile,
	status migrationStatus,
	ref string,
) error {
	if !h.k.CanFolder(r, status.Source, accessapi.ActionRead) ||
		!h.k.CanFolder(r, status.Destination, accessapi.ActionRead|accessapi.ActionWrite) ||
		ref != "" && !h.k.Can(r, ref, accessapi.ActionRead|accessapi.ActionWrite) {
		return migrationAccessDenied()
	}
	for _, file := range files {
		if !h.k.Can(r, file.item.FullPath(), accessapi.ActionRead) ||
			!h.k.Can(r, file.path, accessapi.ActionRead|accessapi.ActionWrite) {
			return migrationAccessDenied()
		}
	}
	return nil
}
