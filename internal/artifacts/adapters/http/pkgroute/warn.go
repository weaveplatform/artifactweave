package pkgroute

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// WarnPassedOver logs a warning when err, from a read of the member mr
// names, is a fault a virtual repository's walk passes over (PassedOver):
// an upstream's 5xx, an upstream it cannot reach or that timed out, or an
// answer the member could not use. 7.x's processStandard passes over every
// member resource it did not find, so the client's answer carries no sign
// of the fault [I]; the warning is the operator's only one. A miss and a
// refusal are how a member says it does not serve the path, and an error
// that fails the walk instead is answered, so neither is logged.
func (k *Kit) WarnPassedOver(mr *http.Request, err error) {
	if !PassedOver(err) || !memberFault(err) {
		return
	}
	q := RequestFrom(mr)
	attrs := []any{
		"request_id", httpkit.RequestIDFrom(mr.Context()),
		"member", q.Repo.Key,
		"error", err,
	}
	if q.virtual != nil {
		attrs = append(attrs, "virtual", q.virtual.Key)
	}
	k.log().WarnContext(mr.Context(), "virtual repository passed over a failing member", attrs...)
}

// memberFault reports whether err, which PassedOver passes over, is a
// fault rather than a miss or a refusal: SourceError answers an upstream it
// could not reach, or whose answer failed its checks, with a 5xx.
func memberFault(err error) bool {
	var answer *apierror.Error
	var upstream *source.HTTPError
	switch {
	case errors.Is(err, source.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		return true
	case errors.As(err, &upstream):
		return upstream.Status >= http.StatusInternalServerError
	case errors.As(err, &answer):
		return answer.Status >= http.StatusInternalServerError
	}
	return false
}

func (k *Kit) log() *slog.Logger {
	if k.o.Logger == nil {
		return slog.Default()
	}
	return k.o.Logger
}
