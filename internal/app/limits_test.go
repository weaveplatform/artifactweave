package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/apilimit"
)

// budgets is the request budget store of the in-memory composite that
// newItemWorld builds the router over. The router's store leaves budgets out,
// because the router never reads them: the application builds the limits.
func budgets(c *RouterConfig) apilimit.Store { return c.Store.(*inmem.Store) }

func TestAccAPIRequestRates(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 15, 0, time.UTC)
	w := newItemWorld(t, false, func(c *RouterConfig) {
		o := apilimit.Default()
		o.PrincipalPerMinute, o.GlobalPerMinute = 2, 3
		var err error
		c.RequestLimits, err = apilimit.New(budgets(c), o, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
	})
	for range 2 {
		rec := w.do(http.MethodGet, "/artifactory/libs-local/absent", "admin", "")
		wantStatus(t, rec, http.StatusNotFound)
		if rec.Header().Get("RateLimit-Policy") != `"api";q=2;w=60` {
			t.Fatalf("quota policy: %v", rec.Header())
		}
	}
	rec := w.do(http.MethodGet, "/artifactory/libs-local/absent", "admin", "")
	wantStatus(t, rec, http.StatusTooManyRequests)
	if rec.Header().Get("Retry-After") != "45" ||
		rec.Header().Get("RateLimit") != `"api";r=0;t=45` {
		t.Fatalf("rate metadata: %v", rec.Header())
	}
	wantStatus(
		t,
		w.do(http.MethodGet, "/artifactory/libs-local/absent", "reader", ""),
		http.StatusNotFound,
	)
	wantStatus(
		t,
		w.do(http.MethodGet, "/artifactory/libs-local/absent", "reader", ""),
		http.StatusTooManyRequests,
	)
	wantStatus(t, w.do(http.MethodGet, "/router/api/v1/system/liveness", "", ""), http.StatusOK)
	now = now.Add(time.Minute)
	wantStatus(
		t,
		w.do(http.MethodGet, "/artifactory/libs-local/absent", "admin", ""),
		http.StatusNotFound,
	)
}

func TestAccAPIUnauthenticatedBudget(t *testing.T) {
	w := newItemWorld(t, false, func(c *RouterConfig) {
		o := apilimit.Default()
		o.AuthenticationPerMinute = 1
		var err error
		c.RequestLimits, err = apilimit.New(budgets(c), o, nil)
		if err != nil {
			t.Fatal(err)
		}
	})
	wantStatus(
		t,
		w.do(http.MethodGet, "/artifactory/api/system/version", "", ""),
		http.StatusUnauthorized,
	)
	rec := w.do(http.MethodGet, "/artifactory/api/system/version", "", "")
	wantStatus(t, rec, http.StatusTooManyRequests)
	if !strings.Contains(rec.Header().Get("RateLimit"), `"authentication";r=0;`) ||
		rec.Header().Get("Retry-After") == "" {
		t.Fatalf("unauthenticated rate metadata: %v", rec.Header())
	}
	wantStatus(t, w.do(http.MethodGet, "/router/api/v1/system/liveness", "", ""), http.StatusOK)
}

type (
	unavailableBudgetStore struct{}
	unavailableBudget      struct{}
)

func (unavailableBudgetStore) RequestBudgets() apilimit.RequestBudgetStore {
	return unavailableBudget{}
}

func (unavailableBudget) Take(
	context.Context,
	apilimit.BudgetRequest,
) (apilimit.BudgetDecision, error) {
	return apilimit.BudgetDecision{}, context.DeadlineExceeded
}

func TestAccAPIBudgetBackendFailure(t *testing.T) {
	w := newItemWorld(t, false, func(c *RouterConfig) {
		var err error
		c.RequestLimits, err = apilimit.New(
			unavailableBudgetStore{},
			apilimit.Default(),
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
	})
	rec := w.do(http.MethodGet, "/artifactory/api/system/version", "admin", "")
	wantStatus(t, rec, http.StatusServiceUnavailable)
	if rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("backend failure retry hint: %v", rec.Header())
	}
	wantStatus(t, w.do(http.MethodGet, "/router/api/v1/system/liveness", "", ""), http.StatusOK)
}

func TestAccAPIConcurrencyCancellation(t *testing.T) {
	o := apilimit.Default()
	o.MaxConcurrent, o.ExpensiveConcurrent, o.ExpensivePrincipalConcurrent = 4, 2, 1
	st := inmem.New()
	limits, err := apilimit.New(st, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := requestLimiter{cfg: RouterConfig{Store: st, RequestLimits: limits}}
	router := chi.NewRouter()
	router.Use(s.limitIngress(newLimitOptions(limits)), s.limitPrincipal(newLimitOptions(limits)))
	entered := make(chan string, 4)
	releaseBob := make(chan struct{})
	router.Post("/artifactory/api/search/aql", func(w http.ResponseWriter, r *http.Request) {
		p, _ := kernel.FromContext(r.Context())
		entered <- p.Name
		select {
		case <-r.Context().Done():
		case <-releaseBob:
		}
		w.WriteHeader(http.StatusNoContent)
	})
	call := func(ctx context.Context, name string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(
			kernel.WithPrincipal(ctx, kernel.Principal{Name: name}),
			http.MethodPost,
			"/artifactory/api/search/aql",
			nil,
		)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	aliceCtx, cancelAlice := context.WithCancel(t.Context())
	defer cancelAlice()
	aliceDone, bobDone := make(
		chan *httptest.ResponseRecorder,
		1,
	), make(
		chan *httptest.ResponseRecorder,
		1,
	)
	go func() { aliceDone <- call(aliceCtx, "alice") }()
	<-entered
	wantStatus(t, call(t.Context(), "alice"), http.StatusTooManyRequests)
	go func() { bobDone <- call(t.Context(), "bob") }()
	<-entered
	wantStatus(t, call(t.Context(), "charlie"), http.StatusTooManyRequests)
	cancelAlice()
	wantStatus(t, <-aliceDone, http.StatusNoContent)
	close(releaseBob)
	wantStatus(t, <-bobDone, http.StatusNoContent)
	wantStatus(t, call(t.Context(), "charlie"), http.StatusNoContent)
}

// TestAccAPILimitEnvelopes: the limiter's 429 and 503 answer in the envelope
// of the path they refuse, with their Retry-After. Under /v2, by either
// addressing method, the 429 carries the distribution specification's
// TOOMANYREQUESTS, which Docker's client reads, and the 503 its status's
// name, since the specification names no code for a server error (#1248).
// Pub, Swift and Agent Packages paths answer in their protocol's envelope,
// as the protocol's own handlers do (#1249).
func TestAccAPILimitEnvelopes(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 15, 0, time.UTC)
	limited := newItemWorld(t, false, func(c *RouterConfig) {
		o := apilimit.Default()
		o.PrincipalPerMinute = 1
		var err error
		c.RequestLimits, err = apilimit.New(budgets(c), o, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
	})
	unavailable := newItemWorld(t, false, func(c *RouterConfig) {
		var err error
		c.RequestLimits, err = apilimit.New(unavailableBudgetStore{}, apilimit.Default(), nil)
		if err != nil {
			t.Fatal(err)
		}
	})
	// The minute's one request.
	_ = limited.do(http.MethodGet, "/artifactory/api/system/version", "admin", "")
	const (
		oci     = "application/json"
		pub     = "application/vnd.pub.v2+json"
		problem = "application/problem+json; charset=utf-8"
	)
	for _, c := range []struct {
		path, contentType, tooMany, unavailable string
	}{
		{
			"/v2/", oci,
			`{"errors":[{"code":"TOOMANYREQUESTS","message":"API request limit exceeded"}]}`,
			`{"errors":[{"code":"SERVICE_UNAVAILABLE","message":"API admission is temporarily unavailable"}]}`,
		},
		{
			"/artifactory/api/docker/libs-local/v2/", oci,
			`{"errors":[{"code":"TOOMANYREQUESTS","message":"API request limit exceeded"}]}`,
			`{"errors":[{"code":"SERVICE_UNAVAILABLE","message":"API admission is temporarily unavailable"}]}`,
		},
		{
			"/artifactory/api/pub/libs-local/api/packages/widget", pub,
			`{"error":{"code":"TOO_MANY_REQUESTS","message":"API request limit exceeded"}}`,
			`{"error":{"code":"SERVICE_UNAVAILABLE","message":"API admission is temporarily unavailable"}}`,
		},
		{
			"/artifactory/api/swift/libs-local/acme/widget", problem,
			`{"detail":"API request limit exceeded","status":429,"title":"Too Many Requests"}`,
			`{"detail":"API admission is temporarily unavailable","status":503,"title":"Service Unavailable"}`,
		},
		{
			"/artifactory/api/agentpackages/libs-local/v1/search", problem,
			`{"detail":"API request limit exceeded","status":429,"title":"Too Many Requests"}`,
			`{"detail":"API admission is temporarily unavailable","status":503,"title":"Service Unavailable"}`,
		},
	} {
		for _, a := range []struct {
			rec        *httptest.ResponseRecorder
			status     int
			body       string
			retryAfter string
		}{
			{limited.do(http.MethodGet, c.path, "admin", ""), http.StatusTooManyRequests, c.tooMany, "45"},
			{unavailable.do(http.MethodGet, c.path, "admin", ""), http.StatusServiceUnavailable, c.unavailable, "1"},
		} {
			if a.rec.Code != a.status || a.rec.Body.String() != a.body+"\n" ||
				a.rec.Header().Get("Content-Type") != c.contentType ||
				a.rec.Header().Get("Retry-After") != a.retryAfter {
				t.Errorf("%s: %d %s %v, want %d %s %q",
					c.path, a.rec.Code, a.rec.Body, a.rec.Header(), a.status, a.body, c.contentType)
			}
		}
	}
}

// TestAccAPITelemetryIsOrdinary (#1597): the JFrog Terraform provider posts
// a usage report for every resource it creates, reads, updates or deletes,
// and the JFrog CLI posts one and its jfconnect metrics after each command.
// 7.161.26's UsageResource adds a report to an in-memory tally that the
// call home job sends later, and no 7.x source limits it, so admission
// charges the reports and the metrics as ordinary requests (decision 0043,
// "Classes moved since"). A thousand of them within a minute, more than the
// default expensive allowance of 600, answer 200 with the ordinary policy,
// and leave that allowance whole for the repository create after them.
func TestAccAPITelemetryIsOrdinary(t *testing.T) {
	w := newItemWorld(t, false, fixedClockLimits(t))
	const report = `{"productId":"terraform-provider-artifactory/12.11.14",` +
		`"features":[{"featureId":"Partner/ACC-007450"}]}`
	sinks := []string{"/artifactory/api/system/usage", "/jfconnect/api/v1/backoffice/metrics/log"}
	for i := range 1000 {
		path := sinks[i%2]
		rec := w.do(http.MethodPost, path, "admin", report)
		policy := rec.Header().Get("RateLimit-Policy")
		if rec.Code != http.StatusOK || policy != `"api";q=6000;w=60` {
			t.Fatalf("report %d to %s: %d, RateLimit-Policy %q", i+1, path, rec.Code, policy)
		}
	}
	rec := w.do(http.MethodPut, "/artifactory/api/repositories/after-reports", "admin",
		`{"rclass":"local","packageType":"generic"}`, "Content-Type", "application/json")
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("RateLimit-Policy") != `"expensive";q=600;w=60` ||
		!strings.HasPrefix(rec.Header().Get("RateLimit"), `"expensive";r=599;`) {
		t.Fatalf("repository create after the reports: %v", rec.Header())
	}
}

// TestAccAPIHeadIsAdmittedAsItsGet (#1598): a HEAD that the router answers
// through a generated GET is admitted as that GET, so it answers the GET's
// rate headers too. The Artifactory readiness probe is exempt, where the
// path rules would charge a HEAD of it as an ordinary request; the system
// version is ordinary.
func TestAccAPIHeadIsAdmittedAsItsGet(t *testing.T) {
	w := newItemWorld(t, false, fixedClockLimits(t))
	for _, c := range []struct{ path, policy string }{
		{"/artifactory/api/v1/system/readiness", ""},
		{"/artifactory/api/system/version", `"api";q=6000;w=60`},
	} {
		get := w.do(http.MethodGet, c.path, "admin", "")
		head := w.do(http.MethodHead, c.path, "admin", "")
		if head.Code != get.Code || get.Code != http.StatusOK ||
			head.Header().Get("RateLimit-Policy") != c.policy ||
			get.Header().Get("RateLimit-Policy") != c.policy {
			t.Errorf("%s: GET %d %v, HEAD %d %v",
				c.path, get.Code, get.Header(), head.Code, head.Header())
		}
	}
}

// fixedClockLimits gives a server the default request limits on a clock
// that stands still one second into a minute, so that no window turns over
// while a test spends a budget.
func fixedClockLimits(t *testing.T) func(*RouterConfig) {
	t.Helper()
	now := time.Date(2026, 11, 16, 12, 0, 1, 0, time.UTC)
	clock := func() time.Time { return now }
	return func(c *RouterConfig) {
		var err error
		c.RequestLimits, err = apilimit.New(budgets(c), apilimit.Default(), clock)
		if err != nil {
			t.Fatal(err)
		}
	}
}
