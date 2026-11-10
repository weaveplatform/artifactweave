package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/weaveplatform/artifactweave/internal/descriptor"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// patchConfiguration is PATCH /api/system/configuration: the YAML descriptor
// as a patch (decision 0009). It is hand-written because the body is YAML,
// which the generated strict servers do not decode.
func (s artifactoryAPI) patchConfiguration(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fail := func(err error) { httpkit.WriteErr(w, r, err) }
	if err := httpkit.AdminOnly(ctx); err != nil {
		fail(err)
		return
	}
	st, err := s.Content()
	if err != nil {
		fail(err)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(apierror.New(http.StatusRequestEntityTooLarge, "request body is too large"))
			return
		}
		fail(httpkit.BadRequestf("Cannot read the request body"))
		return
	}
	// ${NAME} references resolve from the server's own environment.
	p, err := descriptor.Parse(body, os.LookupEnv)
	if err != nil {
		fail(httpkit.BadRequestf("%s", err.Error()))
		return
	}
	res, err := descriptor.Apply(ctx, st, descriptor.Access{
		Keys:            s.cfg.Access.Keys,
		Projects:        s.cfg.Access.Projects,
		AnonymousAccess: s.cfg.Access.AnonymousAccess,
	}, p, time.Now(), false)
	switch {
	case errors.Is(err, descriptor.ErrInvalid):
		fail(httpkit.BadRequestf("%s", err.Error()))
		return
	case err != nil:
		fail(s.InternalError(ctx, "apply configuration", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, res.Merged())
}
