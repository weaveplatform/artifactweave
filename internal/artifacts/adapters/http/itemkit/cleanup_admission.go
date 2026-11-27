package itemkit

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/downloadadmission"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func (s *Kit) admitHosted(w http.ResponseWriter, r *http.Request, st Store, f Found) (func(), bool) {
	if r.Method != http.MethodGet {
		return func() {}, true
	}
	done, err := downloadadmission.Begin(r.Context(), st, f.Repo, f.Item)
	if err != nil {
		s.Log().WarnContext(r.Context(), "hosted download temporarily unavailable", "error", err)
		httpkit.WriteErr(w, r, apierror.New(http.StatusServiceUnavailable, "download temporarily unavailable"))
		return nil, false
	}
	return done, true
}
