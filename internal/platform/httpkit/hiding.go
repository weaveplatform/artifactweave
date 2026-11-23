package httpkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// Hiding reports the security configuration's hideUnauthorizedResources.
type Hiding func(context.Context) (bool, error)

type hidingKey struct{}

// HideUnauthorized is middleware that lets each request read hides, which
// the access module binds to the stored setting. It reads nothing itself:
// Hidden and Hides call hides only for an answer that a refusal decides, so
// the setting costs a successful download nothing, and a change applies to
// the next refusal on every instance without a cache to expire.
func HideUnauthorized(hides Hiding) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), hidingKey{}, hides)))
		})
	}
}

// Hides reports whether ctx's request hides unauthorized resources. Without
// HideUnauthorized ahead of the handler, as in a router built without the
// access module, it reports false, 7.x's default.
func Hides(ctx context.Context) (bool, error) {
	hides, _ := ctx.Value(hidingKey{}).(Hiding)
	if hides == nil {
		return false, nil
	}
	on, err := hides(ctx)
	if err != nil {
		return false, fmt.Errorf("read security.hideUnauthorizedResources: %w", err)
	}
	return on, nil
}

// Hidden is err as ctx's request answers it: the Hidden answer of the
// apierror.Hideable that err holds while Hides reports true, and err
// otherwise. A failed read of the setting is returned in place of either
// answer, so the client gets a 500 rather than a guess at which one the
// setting asks for.
func Hidden(ctx context.Context, err error) error {
	var answer *apierror.Hideable
	if !errors.As(err, &answer) {
		return err
	}
	on, readErr := Hides(ctx)
	switch {
	case readErr != nil:
		return readErr
	case on:
		return answer.Hidden
	}
	return err
}
