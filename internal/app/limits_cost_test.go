package app

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	accesshttp "github.com/weaveplatform/artifactweave/internal/access/adapters/http"
	configurationhttp "github.com/weaveplatform/artifactweave/internal/configuration/adapters/http"
	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
)

// costRequest is a request with method and the escaped path and query of
// target, as a client sends it.
func costRequest(t *testing.T, method, target string) *http.Request {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Request{Method: method, URL: u}
}

// TestUnitAdmissionCost: a request a row addresses takes the row's class,
// expensive with a query its x-cost-expensive-query names, and a static
// segment beats a parameter as in the router; a request no row addresses,
// a HEAD of a GET-only row or a row whose class is still a placeholder
// takes the path rules' class.
func TestUnitAdmissionCost(t *testing.T) {
	x, err := operations.NewIndex([]operations.Operation{
		{
			Method: http.MethodGet, Pattern: "/artifactory/api/storage/{repoKey}/{itemPath}",
			Cost: costOrdinary, ExpensiveQuery: []string{"list"},
		},
		{Method: http.MethodPost, Pattern: "/artifactory/api/search/aql", Cost: costExpensive},
		{Method: http.MethodGet, Pattern: "/artifactory/api/search/draft", Cost: "TODO(author)"},
		{Method: http.MethodGet, Pattern: "/artifactory/api/system/ping", Cost: costExempt},
		{
			Method:  http.MethodGet,
			Pattern: "/artifactory/api/restrictions/{user}",
			Cost:    costRecovery,
		},
		{Method: http.MethodGet, Pattern: "/artifactory/api/things/fixed", Cost: costOrdinary},
		{Method: http.MethodGet, Pattern: "/artifactory/api/things/{name}", Cost: costExpensive},
		// A path rule would make it expensive; the spec says ordinary.
		{Method: http.MethodGet, Pattern: "/artifactory/api/build/info/{name}", Cost: costOrdinary},
	})
	if err != nil {
		t.Fatal(err)
	}
	o := limitOptions{
		Operations: x,
		Health:     func(r *http.Request) bool { return r.URL.Path == "/router/probe" },
		Recovery:   func(r *http.Request) bool { return r.URL.Path == "/recover" },
	}
	for _, c := range []struct{ method, target, want string }{
		{http.MethodGet, "/artifactory/api/storage/libs/a.jar", costOrdinary},
		{http.MethodGet, "/artifactory/api/storage/libs/a.jar?list", costExpensive},
		{http.MethodGet, "/artifactory/api/storage/libs/a.jar?deep=1", costOrdinary},
		// A deeper path is no row's: the path rules decide, as before.
		{http.MethodGet, "/artifactory/api/storage/libs/a/b.jar?list", costExpensive},
		{http.MethodGet, "/artifactory/api/storage/libs/a/b.jar", costOrdinary},
		{http.MethodPost, "/artifactory/api/search/aql", costExpensive},
		{http.MethodGet, "/artifactory/api/search/aql", costExpensive},
		{http.MethodGet, "/artifactory/api/search/draft", costExpensive},
		{http.MethodGet, "/artifactory/api/system/ping", costExempt},
		{http.MethodHead, "/artifactory/api/system/ping", costOrdinary},
		{http.MethodGet, "/artifactory/api/restrictions/alice", costRecovery},
		{http.MethodGet, "/artifactory/api/things/fixed", costOrdinary},
		{http.MethodGet, "/artifactory/api/things/other", costExpensive},
		{http.MethodGet, "/artifactory/api/build/info/app", costOrdinary},
		{http.MethodGet, "/artifactory/api/build/other/app", costExpensive},
		{http.MethodPut, "/artifactory/api/unknown", costExpensive},
		{http.MethodGet, "/artifactory/api/unknown", costOrdinary},
		{http.MethodGet, "/router/probe", costExempt},
		{http.MethodGet, "/recover", costRecovery},
	} {
		if got := o.cost(costRequest(t, c.method, c.target)); got != c.want {
			t.Errorf("%s %s: %s, want %s", c.method, c.target, got, c.want)
		}
	}
}

// TestUnitCostOperationsMatchItemRoutes: admission's index takes a
// hand-written item operation's pattern as the router serves it, so an
// unknown API path is no item operation, while a repository key that only
// begins with "api" still is one.
func TestUnitCostOperationsMatchItemRoutes(t *testing.T) {
	x := costOperations()
	for _, c := range []struct{ method, path, want string }{
		{http.MethodPut, "/artifactory/api/foo", ""},
		{http.MethodDelete, "/artifactory/api/foo", ""},
		{http.MethodGet, "/artifactory/api/foo", ""},
		{http.MethodPut, "/artifactory/libs/foo", "deployArtifact"},
		{http.MethodPut, "/artifactory/apis/foo", "deployArtifact"},
		{http.MethodGet, "/artifactory/libs/a.zip!/b", "archiveEntryDownload"},
		{http.MethodGet, "/artifactory/api/repositories", "getRepositoriesByTypeAndProject"},
	} {
		op, _ := x.Find(c.method, c.path)
		if op.OperationID != c.want {
			t.Errorf("%s %s: %q, want %q", c.method, c.path, op.OperationID, c.want)
		}
	}
}

// servedRoutes walks router and keys each endpoint by "METHOD pattern", as
// chi.Mux.Find names the pattern it serves a request under.
func servedRoutes(t *testing.T, router chi.Routes) map[string]http.Handler {
	t.Helper()
	routes := map[string]http.Handler{}
	err := chi.Walk(
		router,
		func(method, route string, h http.Handler, _ ...func(http.Handler) http.Handler) error {
			routes[method+" "+route] = h
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

// sameHandler reports whether two endpoints run the same code: the same
// function or method value, or the same handler value.
func sameHandler(a, b http.Handler) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Type() != vb.Type() {
		return false
	}
	switch va.Kind() {
	case reflect.Func, reflect.Pointer, reflect.Map, reflect.Chan, reflect.UnsafePointer:
		return va.Pointer() == vb.Pointer()
	}
	return va.Type().Comparable() && va.Interface() == vb.Interface()
}

// TestAccAdmissionCostAgreesWithServedRoutes holds admission to the
// bundled specs over every operation, served or stubbed, as decision 0043
// "Cost classes and how admission finds them" asks:
//   - a request to the operation's example path is admitted under its
//     x-cost, and expensive with each x-cost-expensive-query parameter;
//   - the probes and recovery requests the router's own code recognises
//     (configurationhttp.HealthRequest, accesshttp.APIRecoveryRequest),
//     which the restriction check and the recovery context still read,
//     are exactly the operations declared exempt and recovery;
//   - where the router serves the operation under a pattern of its own, a
//     wildcard for a trailing path parameter, a deeper path the same
//     handler serves is admitted under the same class, though no spec
//     pattern matches it and the path rules decide it.
//
// The router's own probes, which no spec declares, are exempt too.
func TestAccAdmissionCostAgreesWithServedRoutes(t *testing.T) {
	router := NewRouter(RouterConfig{}).(*chi.Mux)
	routes := servedRoutes(t, router)
	o := newLimitOptions(nil)
	served := func(method, path string) (string, http.Handler) {
		pattern := router.Find(chi.NewRouteContext(), method, path)
		return pattern, routes[method+" "+pattern]
	}
	checked, deeper := 0, 0
	check := func(op operations.Operation, method, target string) {
		t.Helper()
		if got := o.cost(costRequest(t, method, target)); got != op.Cost {
			t.Errorf("%s %s (%s): admitted as %s, declared %s",
				method, target, op.OperationID, got, op.Cost)
		}
		for _, q := range op.ExpensiveQuery {
			if got := o.cost(costRequest(t, method, target+"?"+q)); got != costExpensive {
				t.Errorf("%s %s?%s (%s): admitted as %s, declared expensive",
					method, target, q, op.OperationID, got)
			}
		}
	}
	for _, op := range operations.Table {
		path := pathParameterRE.ReplaceAllString(op.Pattern, "example")
		r := costRequest(t, op.Method, path)
		check(op, op.Method, path)
		checked++
		if exempt := op.Cost == costExempt; exempt != configurationhttp.HealthRequest(r) {
			t.Errorf("%s %s (%s): declared %s, but HealthRequest is %v",
				op.Method, path, op.OperationID, op.Cost, !exempt)
		}
		if recovery := op.Cost == costRecovery; recovery != accesshttp.APIRecoveryRequest(r) {
			t.Errorf("%s %s (%s): declared %s, but APIRecoveryRequest is %v",
				op.Method, path, op.OperationID, op.Cost, !recovery)
		}
		segments := strings.Split(op.Pattern, "/")
		last := segments[len(segments)-1]
		if !strings.HasPrefix(last, "{") || !strings.HasSuffix(last, "}") ||
			strings.Count(last, "{") != 1 {
			continue
		}
		pattern, handler := served(op.Method, path)
		if handler == nil {
			t.Errorf("%s %s (%s): the router serves no route for it (%q)",
				op.Method, path, op.OperationID, pattern)
			continue
		}
		deep := path + "/deeper/example"
		_, deepHandler := served(op.Method, deep)
		if deepHandler == nil || !sameHandler(handler, deepHandler) {
			continue
		}
		check(op, op.Method, deep)
		deeper++
	}
	if checked != len(operations.Table) || deeper == 0 {
		t.Fatalf("checked %d operations and %d deeper paths", checked, deeper)
	}
	t.Logf("checked %d operations and %d deeper paths served under another pattern",
		checked, deeper)
	for _, probe := range []string{
		"/router/api/v1/system/health",
		"/router/api/v1/system/readiness",
		"/router/api/v1/system/liveness",
	} {
		if got := o.cost(costRequest(t, http.MethodGet, probe)); got != costExempt {
			t.Errorf("GET %s: admitted as %s, want exempt", probe, got)
		}
	}
}
