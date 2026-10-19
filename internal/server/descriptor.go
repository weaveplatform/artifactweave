package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/weaveplatform/artifactweave/internal/descriptor"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

// patchConfiguration is PATCH /api/system/configuration: the YAML descriptor
// as a patch (decision 0009). It is hand-written because the body is YAML,
// which the generated strict servers do not decode.
func (s artifactoryService) patchConfiguration(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fail := func(err error) { writeErr(w, r, err) }
	if err := adminOnly(ctx); err != nil {
		fail(err)
		return
	}
	st, err := s.content()
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
		fail(badRequest("Cannot read the request body"))
		return
	}
	// ${NAME} references resolve from the server's own environment.
	p, err := descriptor.Parse(body, os.LookupEnv)
	if err != nil {
		fail(badRequest("%s", err.Error()))
		return
	}
	res, err := descriptor.Apply(ctx, st, p, time.Now(), false)
	switch {
	case errors.Is(err, descriptor.ErrInvalid):
		fail(badRequest("%s", err.Error()))
		return
	case err != nil:
		fail(s.internalError(ctx, "apply configuration", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, res.Merged())
}
