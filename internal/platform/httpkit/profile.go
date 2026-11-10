package httpkit

import (
	"context"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// ProfileRule names the error response profile of the paths Match accepts.
// Rules form an ordered list: a narrower rule must come before a broader
// one that also matches its paths, as the JFrog operations beside the npm
// registry come before the registry's own prefix.
type ProfileRule struct {
	Match   func(path string) bool
	Profile apierror.Profile
}

// ProfileFor returns the profile of the first rule in rules that matches
// path, or apierror.ProfileArtifactory when none does. Unclaimed paths take
// the same default as handled ones so that clients can read their 404s.
func ProfileFor(rules []ProfileRule, path string) apierror.Profile {
	for _, rule := range rules {
		if rule.Match(path) {
			return rule.Profile
		}
	}
	return apierror.ProfileArtifactory
}

type profileRulesKey struct{}

// WithProfileRules returns ctx carrying rules for ErrorProfile. A test that
// calls a handler without the router passes its rules through it.
func WithProfileRules(ctx context.Context, rules []ProfileRule) context.Context {
	return context.WithValue(ctx, profileRulesKey{}, rules)
}

// ErrorProfiles is middleware that carries rules on each request's context,
// so that WriteErr and the middleware choose the error profile without
// knowing which ecosystem a path belongs to. It must be installed first,
// ahead of any middleware that can answer with an error.
func ErrorProfiles(rules []ProfileRule) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithProfileRules(r.Context(), rules)))
		})
	}
}

// ErrorProfile returns the error response profile of r's path under the
// rules on r's context. A request that never passed ErrorProfiles or
// WithProfileRules gets apierror.ProfileArtifactory.
func ErrorProfile(r *http.Request) apierror.Profile {
	rules, _ := r.Context().Value(profileRulesKey{}).([]ProfileRule)
	return ProfileFor(rules, r.URL.Path)
}
