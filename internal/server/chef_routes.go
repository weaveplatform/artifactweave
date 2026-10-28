package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func (s artifactoryService) registerChefReindex(r chi.Router) {
	r.Post("/artifactory/api/chef/{repoKey}/reindex", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			apierror.Write(
				w,
				apierror.ProfileArtifactory,
				badRequestf("Chef reindex does not accept query parameters"),
			)
			return
		}
		_, err := s.reindex(r.Context(), repo.Chef, chi.URLParam(r, "repoKey"), "", true)
		if err != nil {
			apierror.Write(w, apierror.ProfileArtifactory, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Chef repository reindexed.\n"))
	})
}
