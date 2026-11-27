package itemkit

import (
	"errors"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/downloadadmission"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func (s *Kit) admitHosted(w http.ResponseWriter, r *http.Request, st Store, f Found) (func(), bool) {
	if r.Method != http.MethodGet {
		return func() {}, true
	}
	done, err := downloadadmission.Begin(r.Context(), st, f.Repo, f.Item)
	if err != nil {
		// An ordinary concurrent deletion keeps the existing missing-item answer.
		if errors.Is(err, kernel.ErrNotFound) {
			httpkit.WriteErr(w, r, ItemNotFound())
			return nil, false
		}

		s.Log().WarnContext(r.Context(), "hosted download temporarily unavailable", "error", err)
		httpkit.WriteErr(w, r, apierror.New(http.StatusServiceUnavailable, "download temporarily unavailable"))
		return nil, false
	}
	return done, true
}
