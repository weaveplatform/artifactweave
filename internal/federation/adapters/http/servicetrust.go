package federationhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// Pairing is the pairing service the service_trust endpoints call
// (pairing.Service).
type Pairing interface {
	Issue(ctx context.Context, use model.UseCase, base string) (string, error)
	Redeem(ctx context.Context, use model.UseCase, token, self string) error
	Pairings(ctx context.Context, use model.UseCase) ([]string, error)
	Unpair(ctx context.Context, use model.UseCase, base string, force bool) error
}

// The service_trust paths (research 40, section 5). 7.161.26's
// ServiceTrustResource serves them under /artifactory/api/v1/service_trust/
// with a use case path that may hold a slash ({usecase: .*}); Access serves
// the token-issuing operation under /access.
const (
	ServiceTrustPrefix       = "/artifactory/api/v1/service_trust/"
	AccessServiceTrustPrefix = "/access/api/v1/service_trust/"
	// The spec's path templates, which name each operation's 501 stub.
	PairingTemplate       = ServiceTrustPrefix + "pairing/{usecase}"
	PairingsTemplate      = ServiceTrustPrefix + "pairings/{usecase}"
	RevokeTemplate        = ServiceTrustPrefix + "revoke/{usecase}"
	ExchangePath          = ServiceTrustPrefix + "exchange"
	AccessPairingTemplate = AccessServiceTrustPrefix + "pairing/{useCase}"
)

// 7.161.26's message patterns and answers (ServiceTrustResource.java:63-81).
const (
	msgCreate      = "Error creating pairing token. %s"
	msgList        = "Error getting list of pairing tokens. %s"
	msgPair        = "Error while token pairing. %s"
	msgUnpair      = "Error while cancelling token pairing. %s"
	msgExchange    = "Error while token exchange. %s"
	msgUnknown     = "Unknown use case"
	msgEstablished = "Trust with service %s was established successfully"
	msgCanceled    = "Trust with service %s was canceled successfully"
	msgRevoked     = "Token was successfully revoked"
	// Not 7.x's: its exchange issues a token, and an artifactweave peer
	// holds none to exchange (decision 0021, deviation registered by
	// PEER-02).
	reasonNoExchange = "Peers of this server sign their requests and hold no token to exchange"
	// The reason a 500 gives: the cause goes to the log, not the client.
	reasonInternal = "Internal server error"
)

// ServiceTrustAPI serves the service_trust pairing endpoints of the
// Artifactory and Access APIs over one pairing service.
type ServiceTrustAPI struct {
	Pairing Pairing
	// Authorize refuses a caller who is not currently an administrator,
	// before a body is read or a use case is answered (7.x's
	// @RolesAllowed(ROLE_ADMIN)).
	Authorize func(ctx context.Context) error
	// Unimplemented answers an Artifactory operation for a use case a later
	// phase implements, as the spec's 501 stub for that method and path
	// template does today.
	Unimplemented func(method, template string) http.Handler
}

// RegisterRoutes claims the service_trust operations' patterns, ahead of
// the 501 stubs registered under the same patterns (decision 0002).
func (a ServiceTrustAPI) RegisterRoutes(r chi.Router) {
	routes := []struct {
		method, template string
		handler          http.HandlerFunc
	}{
		{http.MethodPost, PairingTemplate, a.issue(PairingTemplate)},
		{http.MethodPut, PairingTemplate, a.redeem},
		{http.MethodDelete, PairingTemplate, a.unpair},
		{http.MethodGet, PairingsTemplate, a.pairings},
		{http.MethodDelete, RevokeTemplate, a.revoke},
		{http.MethodPost, AccessPairingTemplate, a.issue(AccessPairingTemplate)},
	}
	for _, route := range routes {
		for _, pattern := range httpkit.ChiPatterns(route.template) {
			r.Method(route.method, pattern, route.handler)
		}
	}
	r.Post(ExchangePath, a.exchange)
}

// message writes 7.x's service_trust answer, {"message": ...}.
func message(w http.ResponseWriter, status int, msg string) {
	httpkit.WriteJSON(w, status, struct {
		Message string `json:"message"`
	}{msg})
}

// useCase reads the use case path after template's prefix. The path is the
// decoded one, so federated-repo%2F* and federated-repo/* name one use
// case.
func useCase(r *http.Request, template string) string {
	prefix := template[:strings.LastIndex(template, "/")+1]
	return strings.TrimPrefix(r.URL.Path, prefix)
}

// admit answers the request unless it names a use case P13 serves: a use
// case a later phase implements keeps its 501, as today, and anything else
// is refused unless the caller is an administrator, then answered 404
// "Unknown use case". It returns the use case and whether to go on.
func (a ServiceTrustAPI) admit(w http.ResponseWriter, r *http.Request, template string) (model.UseCase, bool) {
	path := useCase(r, template)
	switch model.PairingFor(path) {
	case model.PairingPlanned:
		a.notImplemented(w, r, template)
		return "", false
	case model.PairingServed:
		if a.refused(w, r) {
			return "", false
		}
		return model.UseCase(path), true
	default:
		if !a.refused(w, r) {
			message(w, http.StatusNotFound, msgUnknown)
		}
		return "", false
	}
}

func (a ServiceTrustAPI) notImplemented(w http.ResponseWriter, r *http.Request, template string) {
	if strings.HasPrefix(template, AccessServiceTrustPrefix) || a.Unimplemented == nil {
		// The generated Access stub's answer, which this route replaces.
		httpkit.WriteErr(w, r, apierror.ErrNotImplemented)
		return
	}
	a.Unimplemented(r.Method, template).ServeHTTP(w, r)
}

// refused writes the refusal of a caller who is not an administrator, in
// the route's own error envelope, and reports whether it did.
func (a ServiceTrustAPI) refused(w http.ResponseWriter, r *http.Request) bool {
	err := kernel.ErrStore
	if a.Authorize != nil {
		err = a.Authorize(r.Context())
	}
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return true
	}
	return false
}

// fail answers err after pattern: a pairing refusal is 7.x's 400, an
// authority refusal the route's own 401 or 403, and anything else 500.
func fail(w http.ResponseWriter, r *http.Request, pattern string, err error) {
	var refusal *pairing.Refusal
	switch {
	case errors.As(err, &refusal):
		message(w, http.StatusBadRequest, strings.Replace(pattern, "%s", refusal.Reason, 1))
	case authorityError(err):
		httpkit.WriteErr(w, r, err)
	default:
		httpkit.NoteCause(r.Context(), err)
		message(w, http.StatusInternalServerError, strings.Replace(pattern, "%s", reasonInternal, 1))
	}
}

func authorityError(err error) bool {
	return errors.Is(err, kernel.ErrUnauthenticated) || errors.Is(err, kernel.ErrForbidden) ||
		errors.Is(err, kernel.ErrBadCredentials)
}

// issue serves POST pairing/{usecase} on the Artifactory and Access paths.
func (a ServiceTrustAPI) issue(template string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		use, ok := a.admit(w, r, template)
		if !ok {
			return
		}
		token, err := a.Pairing.Issue(r.Context(), use, baseurl.From(r.Context()))
		if err != nil {
			fail(w, r, msgCreate, err)
			return
		}
		httpkit.WriteJSON(w, http.StatusOK, struct {
			PairingToken string `json:"pairing_token"`
		}{token})
	}
}

// redeem serves PUT pairing/{usecase}.
func (a ServiceTrustAPI) redeem(w http.ResponseWriter, r *http.Request) {
	use, ok := a.admit(w, r, PairingTemplate)
	if !ok {
		return
	}
	var body struct {
		PairingToken string `json:"pairing_token"`
	}
	// An unreadable body carries no token, which Redeem refuses as
	// malformed.
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := a.Pairing.Redeem(r.Context(), use, body.PairingToken, baseurl.From(r.Context())); err != nil {
		fail(w, r, msgPair, err)
		return
	}
	message(w, http.StatusOK, strings.Replace(msgEstablished, "%s", use.Name(), 1))
}

// unpair serves DELETE pairing/{usecase}.
func (a ServiceTrustAPI) unpair(w http.ResponseWriter, r *http.Request) {
	use, ok := a.admit(w, r, PairingTemplate)
	if !ok {
		return
	}
	var body struct {
		BaseURL string `json:"base_url"`
	}
	// An unreadable body names no base URL, which Unpair refuses as 7.x's
	// assertBaseUrlProvided does.
	_ = json.NewDecoder(r.Body).Decode(&body)
	// 7.x's @QueryParam boolean: anything but "true" is false.
	force, _ := strconv.ParseBool(r.URL.Query().Get("force"))
	if err := a.Pairing.Unpair(r.Context(), use, body.BaseURL, force); err != nil {
		fail(w, r, msgUnpair, err)
		return
	}
	message(w, http.StatusOK, strings.Replace(msgCanceled, "%s", use.Name(), 1))
}

// pairings serves GET pairings/{usecase}: [{"base_url"}], one per peer.
func (a ServiceTrustAPI) pairings(w http.ResponseWriter, r *http.Request) {
	use, ok := a.admit(w, r, PairingsTemplate)
	if !ok {
		return
	}
	urls, err := a.Pairing.Pairings(r.Context(), use)
	if err != nil {
		fail(w, r, msgList, err)
		return
	}
	type baseURL struct {
		BaseURL string `json:"base_url"`
	}
	out := make([]baseURL, 0, len(urls))
	for _, u := range urls {
		out = append(out, baseURL{u})
	}
	httpkit.WriteJSON(w, http.StatusOK, out)
}

// revoke serves DELETE revoke/{usecase}. In 7.x it revokes the caller's own
// token when its scope names the use case; no artifactweave caller holds
// such a token, since peers sign their requests, so it changes nothing and
// answers as 7.161.26 does for such a caller (decision 0021).
func (a ServiceTrustAPI) revoke(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admit(w, r, RevokeTemplate); !ok {
		return
	}
	message(w, http.StatusOK, msgRevoked)
}

// exchange serves POST exchange, which 7.x leaves open to any caller: its
// bearer token is what it exchanges. artifactweave peers redeem through
// the peer API instead, so it is always refused (a registered deviation).
func (a ServiceTrustAPI) exchange(w http.ResponseWriter, _ *http.Request) {
	message(w, http.StatusBadRequest, strings.Replace(msgExchange, "%s", reasonNoExchange, 1))
}
