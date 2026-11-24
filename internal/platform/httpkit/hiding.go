package httpkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// Hiding reports the security configuration's hideUnauthorizedResources.
type Hiding func(context.Context) (bool, error)

type hidingKey struct{}

type mapsForbiddenKey struct{}

// MapperHidden is the answer 7.x's REST exception mappers give an anonymous
// caller's forbidden while hideUnauthorizedResources is on, in place of the
// 401 challenge they give it otherwise: interceptForbiddenStatus builds a
// bare 404 (ArtifactoryRestExceptionMapperBase 84-93, 7.161.26), and
// RestResponseFilter, which Jersey runs on every response, mapped ones
// too, fills an error without an entity with the status's reason phrase.
// Handlers whose 7.x answer builds the same bare 404 hide with it too.
var MapperHidden = apierror.New(http.StatusNotFound, http.StatusText(http.StatusNotFound))

// MapsForbidden is middleware that marks the requests applies matches as
// answered by 7.x's REST exception mappers, so that Hidden hides an
// anonymous caller's kernel.ErrUnauthenticated there as the mappers do.
// Only a route whose 7.x resource refuses anonymous by raising a forbidden
// belongs: one that builds its own 401, and a package format's native
// endpoint, keep the challenge (the router's mapperRoutes).
func MapsForbidden(applies func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if applies(r) {
				r = r.WithContext(context.WithValue(r.Context(), mapsForbiddenKey{}, true))
			}
			next.ServeHTTP(w, r)
		})
	}
}

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

// Hidden is err as ctx's request answers it: while Hides reports true, the
// Hidden answer of the apierror.Hideable that err holds, or MapperHidden
// for an anonymous caller's kernel.ErrUnauthenticated on a route
// MapsForbidden marks; err otherwise. A failed read of the setting is
// returned in place of either answer, so the client gets a 500 rather than
// a guess at which one the setting asks for.
func Hidden(ctx context.Context, err error) error {
	var hidden *apierror.Error
	var answer *apierror.Hideable
	switch {
	case errors.As(err, &answer):
		hidden = answer.Hidden
	case mapperRefusal(ctx, err):
		hidden = MapperHidden
	default:
		return err
	}
	on, readErr := Hides(ctx)
	switch {
	case readErr != nil:
		return readErr
	case on:
		return hidden
	}
	return err
}

// mapperRefusal reports whether err is the 401 artifactweave gives where
// 7.x's mappers turn an anonymous caller's forbidden into their challenge.
// The caller is anonymous only while anonymous access is on, the other
// half of interceptForbiddenStatus's condition; without a caller the
// middleware has already answered.
func mapperRefusal(ctx context.Context, err error) bool {
	if marked, _ := ctx.Value(mapsForbiddenKey{}).(bool); !marked || !errors.Is(err, kernel.ErrUnauthenticated) {
		return false
	}
	p, ok := kernel.FromContext(ctx)
	return ok && p.Anonymous
}
