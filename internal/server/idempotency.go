package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/build"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/idempotency"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const (
	replayRetention = 24 * time.Hour
	replayWait      = 30 * time.Second
)

// Explicitly refuse a key on unsupported routes before any handler can mutate
// state. In particular, token responses never enter a replay cache.
func IdempotencyRoutes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if values, present := r.Header["Idempotency-Key"]; present {
			parts := strings.Split(
				strings.TrimPrefix(r.URL.EscapedPath(), buildsPath+"/append/"),
				"/",
			)
			supported := r.Method == http.MethodPost &&
				strings.HasPrefix(r.URL.EscapedPath(), buildsPath+"/append/") &&
				len(parts) == 2 && parts[0] != "" && parts[1] != ""
			if !supported || len(values) != 1 {
				apierror.Write(
					w,
					httpkit.ErrorProfile(r),
					httpkit.BadRequestf("Idempotency-Key is supported only once, on build append"),
				)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func replayKey(raw string) (string, error) {
	if len(raw) < 3 || len(raw) > 130 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", httpkit.BadRequestf(
			"Idempotency-Key must be a quoted string of 1-128 letters, digits, . _ : or -",
		)
	}
	key := raw[1 : len(raw)-1]
	for _, c := range key {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == ':' || c == '-' {
			continue
		}
		return "", httpkit.BadRequestf("Idempotency-Key contains an unsupported character")
	}
	return digestReplay([]byte(key)), nil
}

func digestReplay(value []byte) string { return fmt.Sprintf("%x", sha256.Sum256(value)) }

func appendReplayRequest(
	raw string,
	p kernel.Principal,
	ap build.Append,
) (idempotency.ReplayRequest, error) {
	var request idempotency.ReplayRequest
	key, err := replayKey(raw)
	if err != nil {
		return request, err
	}
	if p.Anonymous || p.Registry != nil || !p.Password && p.TokenID == "" {
		return request, httpkit.Denied(p)
	}
	scope, err := json.Marshal(struct {
		Credential kernel.CredentialReference `json:"credential"`
		Operation  string                     `json:"operation"`
		Project    string                     `json:"project"`
		Name       string                     `json:"name"`
		Number     string                     `json:"number"`
		Started    time.Time                  `json:"started"`
	}{p.CredentialReference(), "appendBuild", build.ProjectKey(ap.ProjectKey), ap.Name, ap.Number, store.Normalise(ap.Started)})
	if err != nil {
		return request, fmt.Errorf("append replay scope: %w", err)
	}
	now := time.Now().UTC()
	return idempotency.ReplayRequest{
		Scope: digestReplay(scope), Key: key, Fingerprint: digestReplay(ap.Modules),
		Now: now, Expires: now.Add(replayRetention),
	}, nil
}

func (s ArtifactoryAPI) appendOnce(
	ctx context.Context,
	svc *build.Service,
	p kernel.Principal,
	ap build.Append,
	key string,
) error {
	r, err := appendReplayRequest(key, p, ap)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, replayWait)
	defer cancel()
	_, err = svc.AppendOnce(ctx, ap, r, func(ctx context.Context) (build.Access, error) {
		if s.cfg.Access.Authenticator == nil {
			return nil, build.ErrForbidden
		}
		current, err := s.cfg.Access.Authenticator.Current(ctx, p)
		if err != nil {
			if errors.Is(err, kernel.ErrBadCredentials) ||
				errors.Is(err, kernel.ErrPasswordExpired) ||
				errors.Is(err, kernel.ErrAccountLocked) ||
				errors.Is(err, store.ErrNotFound) {
				return nil, build.ErrForbidden
			}
			return nil, fmt.Errorf("recheck append credential: %w", err)
		}
		// Not the request's decider: a replay evaluates the permissions of
		// the credential as it stands now, so revoked permissions cannot
		// recover a replayed append (decision 0043).
		a, err := itemkit.AccessFor(ctx, s.cfg.Access.Authorizer, current, nil)
		return buildAccess{a}, err
	})
	switch {
	case errors.Is(err, idempotency.ErrReplayMismatch):
		return apierror.New(
			http.StatusUnprocessableEntity,
			"Idempotency-Key was used with different request bytes",
		)
	case errors.Is(err, context.DeadlineExceeded):
		e := apierror.New(
			http.StatusServiceUnavailable,
			"Protected append exceeded its wait budget; retry the same key",
		)
		e.RetryAfterSeconds = 1
		return e
	default:
		if err != nil {
			return fmt.Errorf("protected append: %w", err)
		}
		return nil
	}
}
