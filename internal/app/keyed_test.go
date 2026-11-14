package app

import (
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/platform/apilimit"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
)

// Every operation whose spec declares IdempotencyKey has a replay binding
// here, and every binding names such an operation: declaring the parameter
// is what turns the replay on, so a declaration without a binding would
// answer a keyed request 500 rather than run it unprotected.
func TestUnitKeyedOperationsHaveBindings(t *testing.T) {
	k := newKeyedOperations(RouterConfig{}, newArtifactoryAPI(RouterConfig{}))
	declared := map[string]map[string]bool{}
	for _, op := range operations.Table {
		if !op.IdempotencyKey {
			continue
		}
		if op.Handwritten {
			// The replay wraps generated handlers only; a hand-written
			// keyed operation needs the route middleware decision 0043
			// describes before it can declare the parameter.
			t.Errorf(
				"%s %s declares IdempotencyKey on a hand-written route",
				op.Service,
				op.OperationID,
			)
		}
		if declared[op.Service] == nil {
			declared[op.Service] = map[string]bool{}
		}
		declared[op.Service][op.OperationID] = true
		if _, ok := k.bindings[op.Service][op.OperationID]; !ok {
			t.Errorf(
				"%s %s declares IdempotencyKey but has no replay binding",
				op.Service,
				op.OperationID,
			)
		}
	}
	for service, bindings := range k.bindings {
		for id, b := range bindings {
			if !declared[service][id] {
				t.Errorf("%s %s has a replay binding but declares no IdempotencyKey", service, id)
			}
			if b.Reload == nil {
				t.Errorf("%s %s has a binding without a reload", service, id)
			}
		}
	}
	if k.replay.Store != nil {
		t.Fatal("a router without a store has a replay store")
	}
}

var pathParameter = regexp.MustCompile(`\{[^}]+\}`)

// The key gate finds a keyed operation in the table before routing; the
// router must then route the same request to that operation's pattern, or a
// key would reach a handler that ignores it.
func TestUnitKeyedOperationsRouteAsTheTableSays(t *testing.T) {
	router, ok := NewRouter(RouterConfig{}).(chi.Routes)
	if !ok {
		t.Fatal("the router is not a chi router")
	}
	for _, op := range operations.Table {
		if !op.IdempotencyKey {
			continue
		}
		path := pathParameter.ReplaceAllString(op.Pattern, "p")
		if got := router.Find(chi.NewRouteContext(), op.Method, path); got != op.Pattern {
			t.Errorf("%s %s routes to %q, the table says %q", op.Method, path, got, op.Pattern)
		}
	}
}

// A key anywhere the specs do not accept one answers 400 in the path's
// envelope before admission: a 501 stub, a native protocol route, a
// hand-written alias and an unknown path alike, uncharged and without rate
// headers, while the keyed append is admitted and charged.
func TestAccKeyGateThroughRouter(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 15, 0, time.UTC)
	w := newItemWorld(t, false, func(c *RouterConfig) {
		o := apilimit.Default()
		o.PrincipalPerMinute = 1
		var err error
		c.RequestLimits, err = apilimit.New(budgets(c), o, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
	})
	var stub string
	for _, op := range operations.Table {
		if op.Service == "distribution" && op.Method == http.MethodPost {
			stub = pathParameter.ReplaceAllString(op.Pattern, "p")
			break
		}
	}
	const refusal = "Idempotency-Key is supported only once, on build append"
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, stub, `{"errors":[{"message":"` + refusal + `","status":400}],"message":"` + refusal + `","status_code":400}`},
		{http.MethodPost, "/v2/libs-local/blobs/uploads/", `{"errors":[{"code":"BAD_REQUEST","message":"` + refusal + `"}]}`},
		{http.MethodPost, "/access/api/v1/tokens", `{"errors":[{"code":"BAD_REQUEST","message":"` + refusal + `"}]}`},
		{http.MethodGet, "/artifactory/api/build/app", `{"errors":[{"message":"` + refusal + `","status":400}]}`},
		{http.MethodPost, "/unknown", `{"errors":[{"message":"` + refusal + `","status":400}]}`},
	} {
		rec := w.do(c.method, c.path, "admin", "{}", "Idempotency-Key", `"k"`)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != c.body+"\n" ||
			rec.Header().Get("RateLimit") != "" || rec.Header().Get("RateLimit-Policy") != "" {
			t.Errorf("%s %s: %d %s %v", c.method, c.path, rec.Code, rec.Body, rec.Header())
		}
	}
	// None of those was charged: the minute's one request is still there,
	// and the keyed append that takes it carries its rate headers.
	rec := w.do(
		http.MethodPost,
		"/artifactory/api/build/append/app/1",
		"admin",
		"[]",
		"Idempotency-Key",
		`"k"`,
	)
	if rec.Code != http.StatusNotFound || rec.Header().Get("RateLimit-Policy") == "" {
		t.Fatalf("keyed append: %d %s %v", rec.Code, rec.Body, rec.Header())
	}
	wantStatus(
		t,
		w.do(http.MethodGet, "/artifactory/api/system/version", "admin", ""),
		http.StatusTooManyRequests,
	)
}

// A service name that no row carries would leave that server's keyed
// operations unwrapped behind a gate that accepts their key.
func TestUnitStrictKeyedRefusesAnUnknownService(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an unknown service was accepted")
		}
	}()
	strictKeyed[artgen.StrictHandlerFunc, artgen.StrictMiddlewareFunc](
		keyedOperations{},
		"artifactroy",
	)
}
