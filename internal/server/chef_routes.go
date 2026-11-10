package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// chefAPI serves Chef's reindex route over the item kit.
type chefAPI struct{ *itemkit.Kit }

func (s chefAPI) registerChefReindex(r chi.Router) {
	r.Post("/artifactory/api/chef/{repoKey}/reindex", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			apierror.Write(
				w,
				apierror.ProfileArtifactory,
				httpkit.BadRequestf("Chef reindex does not accept query parameters"),
			)
			return
		}
		_, err := s.Reindex(r.Context(), repo.Chef, chi.URLParam(r, "repoKey"), "", true)
		if err != nil {
			apierror.Write(w, apierror.ProfileArtifactory, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Chef repository reindexed.\n"))
	})
}
