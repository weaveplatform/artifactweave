package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	configapi "github.com/weaveplatform/artifactweave/migration/configuration/api"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// PatchConfiguration is PATCH /api/system/configuration: the YAML descriptor
// as a patch (decision 0009). It is hand-written because the body is YAML,
// which the generated strict servers do not decode.
func (s SystemAPI) PatchConfiguration(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fail := func(err error) { httpkit.WriteErr(w, r, err) }
	if err := httpkit.AdminOnly(ctx); err != nil {
		fail(err)
		return
	}
	// Without a store there is no configuration to apply, and the router
	// binds Configuration only over one.
	if _, err := s.content(); err != nil {
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
	conf := s.deps.Configuration.Descriptor
	p, err := conf.Parse(body, os.LookupEnv)
	if err != nil {
		fail(httpkit.BadRequestf("%s", err.Error()))
		return
	}
	res, err := conf.Apply(ctx, p, time.Now(), false)
	switch {
	case errors.Is(err, configapi.ErrInvalid):
		fail(httpkit.BadRequestf("%s", err.Error()))
		return
	case err != nil:
		fail(s.internalError(ctx, "apply configuration", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, res.Merged())
}
