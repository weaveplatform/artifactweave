package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const (
	apiRestrictionPath      = "/artifactory/api/artifactweave/api-restrictions/"
	apiRestrictionAuditPath = "/artifactory/api/artifactweave/api-restriction-audit"
)

func apiRecoveryRequest(r *http.Request) bool {
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

func apiRecoveryContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiRecoveryRequest(r) {
			// Bound recovery too, including password verification and database waits.
			ctx, cancel := context.WithTimeout(auth.WithAPIRecovery(r.Context()), 5*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// This also covers embedders with a custom Authenticator. The application
// supplies the policy to Authenticator as well, for native self-authentication
// and subsequent Current credential checks.
func (s artifactoryService) checkAPIRestriction(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.FromContext(r.Context())
		if ok && !p.Anonymous && !healthRequest(r) && !apiRecoveryRequest(r) {
			ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
			err := auth.CheckAPIRestriction(ctx, s.cfg.Store.APIRestrictions(), p.Name, time.Now())
			cancel()
			if err != nil {
				if errors.Is(err, auth.ErrAPIRestricted) {
					writeErr(w, r, err)
				} else {
					writeLimitError(w, r, err)
				}
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s artifactoryService) restrictionCaller(
	ctx context.Context,
	user string,
) (auth.Principal, error) {
	p, err := auth.RequireAdmin(ctx)
	if err != nil {
		return p, fmt.Errorf("API restriction administrator: %w", err)
	}
	if s.cfg.Store == nil {
		return p, apierror.New(
			http.StatusServiceUnavailable,
			"API restriction storage is unavailable",
		)
	}
	if user == "anonymous" || user == "" {
		return p, badRequestf("A managed user name is required")
	}
	if _, err := s.cfg.Store.Users().Get(ctx, user); err != nil {
		return p, restrictionStoreError(err)
	}
	return p, nil
}

func restrictionStoreError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
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

func restrictionDocument(r store.APIRestriction) artgen.APIRestriction {
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

func (s artifactoryService) GetAPIRestriction(
	ctx context.Context,
	req artgen.GetAPIRestrictionRequestObject,
) (artgen.GetAPIRestrictionResponseObject, error) {
	if _, err := s.restrictionCaller(ctx, req.UserName); err != nil {
		return nil, err
	}
	r, err := s.cfg.Store.APIRestrictions().Get(ctx, req.UserName)
	if errors.Is(err, store.ErrNotFound) {
		r, err = store.APIRestriction{User: req.UserName}, nil
	}
	if err != nil {
		return nil, restrictionStoreError(err)
	}
	return artgen.GetAPIRestriction200JSONResponse(restrictionDocument(r)), nil
}

func (s artifactoryService) SetAPIRestriction(
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
		return nil, badRequestf(
			"Supply a reason of 1 to 1024 bytes and an expiry within the next seven days",
		)
	}
	r, err := s.cfg.Store.APIRestrictions().Put(ctx, store.APIRestriction{
		User: req.UserName, Actor: p.Name,
		Reason: strings.TrimSpace(req.Body.Reason), Until: req.Body.Until, Changed: now,
	})
	if err != nil {
		return nil, restrictionStoreError(err)
	}
	return artgen.SetAPIRestriction200JSONResponse(restrictionDocument(r)), nil
}

func (s artifactoryService) ClearAPIRestriction(
	ctx context.Context,
	req artgen.ClearAPIRestrictionRequestObject,
) (artgen.ClearAPIRestrictionResponseObject, error) {
	p, err := s.restrictionCaller(ctx, req.UserName)
	if err != nil {
		return nil, err
	}
	_, err = s.cfg.Store.APIRestrictions().Put(ctx, store.APIRestriction{
		User: req.UserName, Actor: p.Name,
		Reason: "Restriction cleared", Changed: time.Now().UTC(),
	})
	if err != nil {
		return nil, restrictionStoreError(err)
	}
	return artgen.ClearAPIRestriction204Response{}, nil
}

func (s artifactoryService) GetAPIRestrictionAudit(
	ctx context.Context,
	req artgen.GetAPIRestrictionAuditRequestObject,
) (artgen.GetAPIRestrictionAuditResponseObject, error) {
	p, err := auth.RequireAdmin(ctx)
	if err != nil {
		return nil, fmt.Errorf("API restriction audit administrator: %w", err)
	}
	if s.cfg.Store == nil {
		return nil, apierror.New(
			http.StatusServiceUnavailable,
			"API restriction storage is unavailable",
		)
	}
	user := deref(req.Params.User)
	limit := 100
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if limit < 1 || limit > 1000 {
		return nil, badRequestf("limit must be between 1 and 1000")
	}
	page := store.Page{Limit: limit}
	if raw := deref(req.Params.Cursor); raw != "" {
		parts, err := (store.Page{After: raw}).Key(4)
		if err != nil || len(parts) != 4 || parts[0] != "v1" || parts[1] != user ||
			parts[2] != p.Name {
			return nil, badRequestf("Invalid audit cursor for this filter and administrator")
		}
		page.After = parts[3]
	}
	rows, next, err := s.cfg.Store.APIRestrictions().History(ctx, user, page)
	if errors.Is(err, store.ErrInvalidCursor) {
		return nil, badRequestf("Invalid audit cursor")
	}
	if err != nil {
		return nil, restrictionStoreError(err)
	}
	out := artgen.APIRestrictionAudit{Changes: make([]artgen.APIRestriction, 0, len(rows))}
	for _, r := range rows {
		out.Changes = append(out.Changes, restrictionDocument(r))
	}
	if next != "" {
		c := store.Cursor("v1", user, p.Name, next)
		out.Cursor = &c
	}
	return artgen.GetAPIRestrictionAudit200JSONResponse(out), nil
}
