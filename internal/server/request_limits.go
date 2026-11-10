package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/build"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/apilimit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func healthRequest(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.EscapedPath() {
	case "/artifactory/api/system/ping",
		"/artifactory/api/v1/system/readiness",
		"/artifactory/api/v1/system/liveness",
		"/access/api/v1/system/ping",
		"/access/api/v1/system/readiness",
		"/access/api/v1/system/liveness",
		"/router/api/v1/system/health",
		"/router/api/v1/system/readiness",
		"/router/api/v1/system/liveness":
		return true
	}
	return false
}

// limitOptions are the values the request-limit middleware takes from the
// router, which builds them from the code that owns each request kind.
type limitOptions struct {
	// Health reports the probes, which pass every limit so that a busy
	// server still answers its health checks.
	Health func(*http.Request) bool
	// Recovery reports the administrative recovery API's requests, which
	// take the small reserved recovery lane (apilimit.Service.EnterRecovery)
	// instead of the shared limits, so that an administrator can still reach
	// it when those are exhausted.
	Recovery func(*http.Request) bool
	// Limits is the request-limit service (Config.RequestLimits).
	Limits *apilimit.Service
}

// limitIngress admits each request to the process's concurrency and
// authentication limits before authentication runs.
func (s artifactoryAPI) limitIngress(o limitOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if o.Health(r) {
				next.ServeHTTP(w, r)
				return
			}
			if o.Recovery(r) {
				release, decision, err := o.Limits.EnterRecovery(r.Context())
				if decision.Policy != "" {
					setLimitHeaders(w, decision)
				}
				if err != nil {
					writeLimitError(w, r, err)
					return
				}
				if !decision.Allowed {
					writeLimitDecision(w, r, decision)
					return
				}
				defer release()
				next.ServeHTTP(w, r)
				return
			}
			release, err := o.Limits.Enter(r.Context())
			if err != nil {
				writeLimitError(w, r, err)
				return
			}
			defer release()
			decision, err := o.Limits.Authentication(r.Context())
			if err != nil {
				writeLimitError(w, r, err)
				return
			}
			if !decision.Allowed {
				writeLimitDecision(w, r, decision)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// limitPrincipal admits an authenticated request to its principal's
// budget and, for an expensive one, to the expensive lane.
func (s artifactoryAPI) limitPrincipal(o limitOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if o.Health(r) || o.Recovery(r) {
				next.ServeHTTP(w, r)
				return
			}
			p, authenticated := kernel.FromContext(r.Context())
			name := "anonymous"
			if authenticated && !p.Anonymous {
				name = p.Name
			}
			scope := apilimit.Scope{Principal: name, Expensive: expensiveRequest(r)}
			if err := s.budgetResources(r, p, &scope); err != nil {
				writeLimitError(w, r, err)
				return
			}
			decision, err := o.Limits.Admit(r.Context(), scope)
			if err != nil {
				writeLimitError(w, r, err)
				return
			}
			setLimitHeaders(w, decision)
			if !decision.Allowed {
				writeLimitDecision(w, r, decision)
				return
			}
			if scope.Expensive {
				release, err := o.Limits.EnterExpensive(r.Context(), name)
				if err != nil {
					writeLimitError(w, r, err)
					return
				}
				defer release()
				ctx, cancel := context.WithTimeout(
					r.Context(),
					o.Limits.Options().ExpensiveTimeout,
				)
				defer cancel()
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func expensiveRequest(r *http.Request) bool {
	p := r.URL.Path
	if r.Method != http.MethodGet && r.Method != http.MethodHead && strings.Contains(p, "/api/") {
		return true
	}
	for _, prefix := range []string{
		"/artifactory/api/search/", "/artifactory/api/archive/", "/artifactory/api/import/",
		"/artifactory/api/export/", "/artifactory/api/artifactexport/", "/artifactory/api/vcs/",
		"/artifactory/api/build", "/artifactory/api/system/storage/", "/artifactory/api/trash/",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	switch p {
	case "/artifactory/api/repositories",
		"/artifactory/api/security/users",
		"/artifactory/api/security/groups":
		return true
	}
	return strings.HasSuffix(p, "/reindex") || strings.HasSuffix(p, "/move-archives") ||
		strings.HasPrefix(p, "/artifactory/api/storage/") && r.URL.Query().Has("list")
}

// Resource quotas are charged only for existing resources visible to this
// authenticated principal. Invented query/path labels cannot allocate counters
// or exhaust another account's repository quota.
func (s artifactoryAPI) budgetResources(
	r *http.Request,
	p kernel.Principal,
	scope *apilimit.Scope,
) error {
	d, _ := accessapi.DeciderFrom(r.Context())
	if d == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestLimits.Options().BudgetTimeout)
	defer cancel()
	project := ""
	if strings.HasPrefix(r.URL.Path, "/artifactory/api/build") {
		project = build.ProjectKey(r.URL.Query().Get("project"))
	}
	if project != "" && len(project) <= 128 {
		_, adminErr := kernel.RequireAdmin(ctx)
		memberErr := s.cfg.Access.Projects.Member(ctx, project, p.Name)
		if memberErr != nil && !errors.Is(memberErr, store.ErrNotFound) {
			return fmt.Errorf("budget project membership: %w", memberErr)
		}
		if adminErr == nil || memberErr == nil {
			if err := s.cfg.Access.Projects.Exists(ctx, project); err == nil {
				scope.Project = project
			} else if !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("budget project: %w", err)
			}
		}
	}
	key := budgetRepository(r)
	if key == "" || len(key) > 255 {
		return nil
	}
	key, _ = types.ResolveRepoKey(key)
	c, err := s.cfg.Store.Repos().Get(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("budget repository: %w", err)
	}
	if (itemkit.Access{Decider: d}).Visible(c) {
		scope.Repository = c.Key
		// A query parameter cannot redirect a repository's project accounting.
		scope.Project = c.ProjectKey
	}
	return nil
}

func budgetRepository(r *http.Request) string {
	path, ok := strings.CutPrefix(r.URL.EscapedPath(), "/artifactory/")
	if !ok {
		return ""
	}
	parts := strings.Split(path, "/")
	key := ""
	if parts[0] != "api" {
		// Artifact routes allow matrix properties after the repository name.
		// Strip those before unescaping, preserving an encoded semicolon in
		// an actual name, just as pkgroute.ParsePath does.
		key, _, _ = strings.Cut(parts[0], ";")
	} else if len(parts) >= 3 {
		switch parts[1] {
		case "storage",
			"repositories",
			"npm",
			"pypi",
			"nuget",
			"helm",
			"go",
			"terraform",
			"docker",
			"vcs",
			"cran",
			"conda",
			"huggingfaceml",
			"ansible",
			"chef",
			"puppet",
			"nix":
			key = parts[2]
		case "build", "builds":
			key = types.BuildInfoRepo(build.ProjectKey(r.URL.Query().Get("project")))
		}
	}
	if key == "" {
		return r.URL.Query().Get("repo")
	}
	decoded, err := url.PathUnescape(key)
	if err != nil {
		return ""
	}
	return decoded
}

func limitSeconds(d time.Duration) int { return max(1, int((d+time.Second-1)/time.Second)) }

func setLimitHeaders(w http.ResponseWriter, d apilimit.Decision) {
	w.Header().Set("RateLimit-Policy", fmt.Sprintf("%q;q=%d;w=60", d.Policy, d.Limit))
	w.Header().
		Set("RateLimit", fmt.Sprintf("%q;r=%d;t=%d", d.Policy, d.Remaining, limitSeconds(d.ResetAfter)))
}

func writeLimitDecision(w http.ResponseWriter, r *http.Request, d apilimit.Decision) {
	setLimitHeaders(w, d)
	e := apierror.New(http.StatusTooManyRequests, "API request limit exceeded")
	e.RetryAfterSeconds = limitSeconds(d.RetryAfter)
	apierror.Write(w, errorProfileFor(r.URL.Path), e)
}

func writeLimitError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := http.StatusServiceUnavailable, "API admission is temporarily unavailable"
	if errors.Is(err, apilimit.ErrBusy) {
		status, message = http.StatusTooManyRequests, "API concurrent work limit exceeded"
	} else {
		httpkit.NoteCause(r.Context(), err)
	}
	e := apierror.New(status, message)
	e.RetryAfterSeconds = 1
	apierror.Write(w, errorProfileFor(r.URL.Path), e)
}
