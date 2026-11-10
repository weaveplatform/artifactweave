package chefhttp

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// ChefAPI serves Chef's reindex route over the item kit.
type ChefAPI struct{ *itemkit.Kit }

// RegisterChefReindex mounts Chef's reindex, POST
// /artifactory/api/chef/{repoKey}/reindex, on r. It refuses any query with
// 400, runs the item kit's Reindex for Chef and answers its text once the
// reindex has run; it writes errors in the artifactory profile.
func (s ChefAPI) RegisterChefReindex(r chi.Router) {
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
