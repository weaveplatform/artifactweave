package accesshttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const (
	apiRestrictionPath      = "/artifactory/api/artifactweave/api-restrictions/"
	apiRestrictionAuditPath = "/artifactory/api/artifactweave/api-restriction-audit"
)

// APIRecoveryRequest reports the administrator's recovery requests: reading,
// setting and lifting a user's API restriction and reading the audit. They
// pass the restriction check (CheckAPIRestriction), and the request limiter
// serves them from a reserve of their own, so that a restriction can always
// be lifted.
func APIRecoveryRequest(r *http.Request) bool {
	// Match chi's escaped route path. Decoding static segments first could
	// grant recovery privileges to the ordinary artifact catch-all route.
	if r.URL.EscapedPath() == apiRestrictionAuditPath {
		return r.Method == http.MethodGet
	}
	user, ok := strings.CutPrefix(r.URL.EscapedPath(), apiRestrictionPath)
	if !ok || user == "" || strings.Contains(user, "/") {
		return false
	}
	return r.Method == http.MethodGet || r.Method == http.MethodPut || r.Method == http.MethodDelete
}

// APIRecoveryContext marks a recovery request (APIRecoveryRequest) so that
// the authenticator verifies its credentials even while the caller's API
// restriction is in force, and bounds the request to five seconds. The flag
// grants no authority: the handlers still require an administrator.
func APIRecoveryContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if APIRecoveryRequest(r) {
			// Bound recovery too, including password verification and database waits.
			ctx, cancel := context.WithTimeout(authn.WithAPIRecovery(r.Context()), 5*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// CheckAPIRestriction returns the middleware that refuses an authenticated
// user's request while that user's API restriction is in force. It runs
// after authentication, which names the user, and needs a store. The router
// supplies health, the probes that every caller may make, and limitErr, which
// answers a restriction read that failed or timed out as the request limiter
// answers; recovery requests (APIRecoveryRequest) always pass.
//
// This also covers embedders with a custom Authenticator. The application
// supplies the policy to Authenticator as well, for native self-authentication
// and subsequent Current credential checks.
func (s *Server) CheckAPIRestriction(
	health func(*http.Request) bool,
	limitErr func(http.ResponseWriter, *http.Request, error),
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := kernel.FromContext(r.Context())
			if ok && !p.Anonymous && !health(r) && !APIRecoveryRequest(r) {
				ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
				err := authn.CheckAPIRestriction(
					ctx,
					s.Security.deps.Store.APIRestrictions(),
					p.Name,
					time.Now(),
				)
				cancel()
				if err != nil {
					if errors.Is(err, kernel.ErrAPIRestricted) {
						httpkit.WriteErr(w, r, err)
					} else {
						limitErr(w, r, err)
					}
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s SecurityAPI) restrictionCaller(
	ctx context.Context,
	user string,
) (kernel.Principal, error) {
	p, err := kernel.RequireAdmin(ctx)
	if err != nil {
		return p, fmt.Errorf("API restriction administrator: %w", err)
	}
	if s.deps.Store == nil {
		return p, apierror.New(
			http.StatusServiceUnavailable,
			"API restriction storage is unavailable",
		)
	}
	if user == "anonymous" || user == "" {
		return p, httpkit.BadRequestf("A managed user name is required")
	}
	if _, err := s.deps.Store.Users().Get(ctx, user); err != nil {
		return p, restrictionStoreError(err)
	}
	return p, nil
}

func restrictionStoreError(err error) error {
	if errors.Is(err, kernel.ErrNotFound) {
		return apierror.New(http.StatusNotFound, "The managed user does not exist")
	}
	if err == nil {
		return nil
	}
	e := apierror.New(
		http.StatusServiceUnavailable,
		"API restriction storage is temporarily unavailable",
	)
	e.RetryAfterSeconds = 1
	return e
}

func restrictionDocument(r port.APIRestriction) artgen.APIRestriction {
	out := artgen.APIRestriction{
		User:     r.User,
		Actor:    r.Actor,
		Reason:   r.Reason,
		Sequence: r.Sequence,
		Active:   r.Until.After(time.Now()),
	}
	if !r.Until.IsZero() {
		out.Until = &r.Until
	}
	if !r.Changed.IsZero() {
		out.Changed = &r.Changed
	}
	return out
}

// GetAPIRestriction returns a user's temporary API restriction to an
// administrator, an inactive one when none was ever set (GET
// /artifactory/api/artifactweave/api-restrictions/{userName}).
func (s SecurityAPI) GetAPIRestriction(
	ctx context.Context,
	req artgen.GetAPIRestrictionRequestObject,
) (artgen.GetAPIRestrictionResponseObject, error) {
	if _, err := s.restrictionCaller(ctx, req.UserName); err != nil {
		return nil, err
	}
	r, err := s.deps.Store.APIRestrictions().Get(ctx, req.UserName)
	if errors.Is(err, kernel.ErrNotFound) {
		r, err = port.APIRestriction{User: req.UserName}, nil
	}
	if err != nil {
		return nil, restrictionStoreError(err)
	}
	return artgen.GetAPIRestriction200JSONResponse(restrictionDocument(r)), nil
}

// SetAPIRestriction restricts a user's API access, for a reason and until a
// time at most seven days ahead, for an administrator (PUT
// /artifactory/api/artifactweave/api-restrictions/{userName}).
func (s SecurityAPI) SetAPIRestriction(
	ctx context.Context,
	req artgen.SetAPIRestrictionRequestObject,
) (artgen.SetAPIRestrictionResponseObject, error) {
	p, err := s.restrictionCaller(ctx, req.UserName)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if req.Body == nil || !req.Body.Until.After(now) ||
		req.Body.Until.After(now.Add(7*24*time.Hour)) ||
		strings.TrimSpace(req.Body.Reason) == "" ||
		len(strings.TrimSpace(req.Body.Reason)) > 1024 {
		return nil, httpkit.BadRequestf(
			"Supply a reason of 1 to 1024 bytes and an expiry within the next seven days",
		)
	}
	r, err := s.deps.Store.APIRestrictions().Put(ctx, port.APIRestriction{
		User: req.UserName, Actor: p.Name,
		Reason: strings.TrimSpace(req.Body.Reason), Until: req.Body.Until, Changed: now,
	})
	if err != nil {
		return nil, restrictionStoreError(err)
	}
	return artgen.SetAPIRestriction200JSONResponse(restrictionDocument(r)), nil
}

// ClearAPIRestriction lifts a user's temporary API restriction for an
// administrator, recording who lifted it (DELETE
// /artifactory/api/artifactweave/api-restrictions/{userName}).
func (s SecurityAPI) ClearAPIRestriction(
	ctx context.Context,
	req artgen.ClearAPIRestrictionRequestObject,
) (artgen.ClearAPIRestrictionResponseObject, error) {
	p, err := s.restrictionCaller(ctx, req.UserName)
	if err != nil {
		return nil, err
	}
	_, err = s.deps.Store.APIRestrictions().Put(ctx, port.APIRestriction{
		User: req.UserName, Actor: p.Name,
		Reason: "Restriction cleared", Changed: time.Now().UTC(),
	})
	if err != nil {
		return nil, restrictionStoreError(err)
	}
	return artgen.ClearAPIRestriction204Response{}, nil
}

// GetAPIRestrictionAudit lists the changes to temporary API restrictions,
// newest first and a page at a time, for an administrator (GET
// /artifactory/api/artifactweave/api-restriction-audit).
func (s SecurityAPI) GetAPIRestrictionAudit(
	ctx context.Context,
	req artgen.GetAPIRestrictionAuditRequestObject,
) (artgen.GetAPIRestrictionAuditResponseObject, error) {
	p, err := kernel.RequireAdmin(ctx)
	if err != nil {
		return nil, fmt.Errorf("API restriction audit administrator: %w", err)
	}
	if s.deps.Store == nil {
		return nil, apierror.New(
			http.StatusServiceUnavailable,
			"API restriction storage is unavailable",
		)
	}
	user := httpkit.Deref(req.Params.User)
	limit := 100
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if limit < 1 || limit > 1000 {
		return nil, httpkit.BadRequestf("limit must be between 1 and 1000")
	}
	page := kernel.Page{Limit: limit}
	if raw := httpkit.Deref(req.Params.Cursor); raw != "" {
		parts, err := (kernel.Page{After: raw}).Key(4)
		if err != nil || len(parts) != 4 || parts[0] != "v1" || parts[1] != user ||
			parts[2] != p.Name {
			return nil, httpkit.BadRequestf(
				"Invalid audit cursor for this filter and administrator",
			)
		}
		page.After = parts[3]
	}
	rows, next, err := s.deps.Store.APIRestrictions().History(ctx, user, page)
	if errors.Is(err, kernel.ErrInvalidCursor) {
		return nil, httpkit.BadRequestf("Invalid audit cursor")
	}
	if err != nil {
		return nil, restrictionStoreError(err)
	}
	out := artgen.APIRestrictionAudit{Changes: make([]artgen.APIRestriction, 0, len(rows))}
	for _, r := range rows {
		out.Changes = append(out.Changes, restrictionDocument(r))
	}
	if next != "" {
		c := kernel.Cursor("v1", user, p.Name, next)
		out.Cursor = &c
	}
	return artgen.GetAPIRestrictionAudit200JSONResponse(out), nil
}
