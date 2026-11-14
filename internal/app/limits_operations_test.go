package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/apilimit"
)

// limitedOperation is one operation of a bundled spec as the admission
// table tests call it: its method, a request target with every path
// parameter filled in, the class it declares, and the declaration its
// answers are held to.
type limitedOperation struct {
	method, target, cost string
	op                   *openapi3.Operation
}

func (l limitedOperation) String() string {
	return l.method + " " + l.target + " (" + l.op.OperationID + ", " + l.cost + ")"
}

// limitedOperations lists every operation of the bundled specs, served or
// stubbed, once for each class it can be admitted under: once as it is,
// and once more, expensive, with each query parameter its
// x-cost-expensive-query names.
func limitedOperations(t *testing.T) []limitedOperation {
	t.Helper()
	var out []limitedOperation
	for _, name := range specFiles {
		doc := loadSpec(t, name)
		server := doc.Servers[0].URL
		for path, item := range doc.Paths.Map() {
			for method, op := range item.Operations() {
				cost, _ := op.Extensions["x-cost"].(string)
				target := server + pathParameterRE.ReplaceAllString(path, "example")
				out = append(out, limitedOperation{method, target, cost, op})
				queries, _ := stringList(op.Extensions["x-cost-expensive-query"])
				for _, q := range queries {
					out = append(out, limitedOperation{method, target + "?" + q, costExpensive, op})
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b limitedOperation) int {
		return strings.Compare(a.target+" "+a.method, b.target+" "+b.method)
	})
	return out
}

// limitStores are the request budget stores the table tests run against:
// the in-memory one and SQLite's, each a store of its own beside the world
// the router serves, as the application keeps budgets in its metadata
// database.
func limitStores(t *testing.T) map[string]apilimit.Store {
	t.Helper()
	db, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "budgets.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return map[string]apilimit.Store{"inmem": inmem.New(), "sqlite": db}
}

// declaredAnswer reports how rec departs from what op declares for its
// status: the media type, the body's schema and the required headers.
func declaredAnswer(op *openapi3.Operation, rec *httptest.ResponseRecorder) []string {
	status := strconv.Itoa(rec.Code)
	resp := op.Responses.Value(status)
	if resp == nil || resp.Value == nil {
		return []string{"declares no " + status}
	}
	var problems []string
	for name, h := range resp.Value.Headers {
		if h.Value != nil && h.Value.Required && rec.Header().Get(name) == "" {
			problems = append(problems, "lacks the required header "+name)
		}
	}
	mediaType, _, err := mime.ParseMediaType(rec.Header().Get("Content-Type"))
	if err != nil {
		return append(problems, "Content-Type: "+err.Error())
	}
	media := resp.Value.Content.Get(mediaType)
	if media == nil || media.Schema == nil {
		return append(problems, status+" declares no "+mediaType+" body")
	}
	var body any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		return append(problems, "body: "+err.Error())
	}
	if err := media.Schema.Value.VisitJSON(body); err != nil {
		problems = append(problems, "body does not match the declared schema: "+err.Error())
	}
	return problems
}

// retryAfter reports whether rec carries Retry-After in whole seconds, at
// least one, as the shared RetryAfter header declares it.
func retryAfter(rec *httptest.ResponseRecorder) bool {
	n, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	return err == nil && n >= 1
}

// limitPolicies are the allowance each admitted class is charged to and
// reports in RateLimit-Policy, with the quota the table test gives it: one
// request a minute on the principal's ordinary and expensive allowances,
// and the recovery reserve's fixed sixty.
var limitPolicies = map[string]struct {
	policy string
	quota  int
}{
	costOrdinary:  {"api", 1},
	costExpensive: {"expensive", 1},
	costRecovery:  {"recovery", 60},
}

// spendsCredentials names the operations whose answer leaves the caller's
// password expired, so authentication refuses the caller's next call before
// admission sees it, as decision 0043 orders them ("Rate headers and
// Retry-After"). Another request of the same class spends their budget.
var spendsCredentials = map[string]bool{"expirePasswordForAllUsers": true}

// TestAccAPILimitEveryOperation: with a budget of one request a minute,
// every operation of the bundled specs, served or stubbed, answers its
// next call 429. The answer carries Retry-After, RateLimit and
// RateLimit-Policy, names the allowance of the operation's class, so an
// expensive operation is charged to the expensive lane and an ordinary one
// is not, and matches the 429 the operation declares, in the envelope of
// its path. The recovery operations are refused once their reserve's sixty
// are spent. It runs against the in-memory and the SQLite budget stores;
// each operation gets a world of its own, so one operation's effect cannot
// change another's answer, and a minute of its own.
func TestAccAPILimitEveryOperation(t *testing.T) {
	ops := limitedOperations(t)
	for name, store := range limitStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 11, 14, 12, 0, 15, 0, time.UTC)
			o := apilimit.Default()
			o.PrincipalPerMinute, o.ExpensivePrincipalPerMinute = 1, 1
			limits, err := apilimit.New(store, o, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			refused, admitted := map[string]int{}, map[string]int{}
			for _, l := range ops {
				want, ok := limitPolicies[l.cost]
				if !ok {
					continue
				}
				admitted[want.policy]++
				now = now.Add(time.Minute)
				w := newItemWorld(t, false, func(c *RouterConfig) { c.RequestLimits = limits })
				for range want.quota {
					method, target := l.method, l.target
					if spendsCredentials[l.op.OperationID] {
						method, target = http.MethodGet, "/artifactory/api/search/spent"
					}
					if rec := w.do(
						method,
						target,
						"admin",
						"",
					); rec.Code == http.StatusTooManyRequests {
						t.Errorf("%s: refused within its quota: %s", l, rec.Body)
					}
				}
				rec := w.do(l.method, l.target, "admin", "")
				policy := fmt.Sprintf("%q;q=%d;w=60", want.policy, want.quota)
				problems := declaredAnswer(l.op, rec)
				switch {
				case rec.Code != http.StatusTooManyRequests:
					problems = []string{"answered " + strconv.Itoa(rec.Code)}
				case !retryAfter(rec):
					problems = append(problems, "Retry-After "+rec.Header().Get("Retry-After"))
				case rec.Header().Get("RateLimit-Policy") != policy:
					problems = append(
						problems,
						"RateLimit-Policy "+rec.Header().Get("RateLimit-Policy")+", want "+policy,
					)
				case !strings.HasPrefix(rec.Header().Get("RateLimit"), fmt.Sprintf("%q;r=0;t=", want.policy)):
					problems = append(problems, "RateLimit "+rec.Header().Get("RateLimit"))
				}
				if len(problems) > 0 {
					t.Errorf(
						"%s: %s\n%v\n%s",
						l,
						strings.Join(problems, "; "),
						rec.Header(),
						rec.Body,
					)
					continue
				}
				refused[want.policy]++
			}
			if !maps.Equal(refused, admitted) || len(refused) != len(limitPolicies) {
				t.Fatalf("refused %v of %v", refused, admitted)
			}
			t.Logf("refused %v", refused)
		})
	}
}

// TestAccAPILimitEveryOperationUnavailable: while the budget store fails,
// every operation of the bundled specs that admission charges to it
// answers 503 with Retry-After: 1 and no rate headers, since no allowance
// was evaluated, matching the 503 it declares, in the envelope of its path.
// The recovery operations, whose reserve this process keeps without the
// store, are still served.
func TestAccAPILimitEveryOperationUnavailable(t *testing.T) {
	w := newItemWorld(t, false, func(c *RouterConfig) {
		var err error
		c.RequestLimits, err = apilimit.New(unavailableBudgetStore{}, apilimit.Default(), nil)
		if err != nil {
			t.Fatal(err)
		}
	})
	unavailable := 0
	for _, l := range limitedOperations(t) {
		if l.cost == costExempt {
			continue
		}
		rec := w.do(l.method, l.target, "admin", "")
		if l.cost == costRecovery {
			if rec.Code == http.StatusServiceUnavailable {
				t.Errorf("%s: recovery refused while the budget store fails: %s", l, rec.Body)
			}
			continue
		}
		problems := declaredAnswer(l.op, rec)
		switch {
		case rec.Code != http.StatusServiceUnavailable:
			problems = []string{"answered " + strconv.Itoa(rec.Code)}
		case rec.Header().Get("Retry-After") != "1":
			problems = append(problems, "Retry-After "+rec.Header().Get("Retry-After"))
		case rec.Header().Get("RateLimit") != "" || rec.Header().Get("RateLimit-Policy") != "":
			problems = append(problems, "reports an allowance")
		}
		if len(problems) > 0 {
			t.Errorf("%s: %s\n%v\n%s", l, strings.Join(problems, "; "), rec.Header(), rec.Body)
			continue
		}
		unavailable++
	}
	if unavailable == 0 {
		t.Fatal("no operation answered 503")
	}
	t.Logf("%d answers 503", unavailable)
}

// TestAccAPILimitExemptProbes: the exempt probes of the bundled specs and
// the router's own, which no spec declares, are never refused: not once
// every allowance is spent, and not while the budget store fails. They
// report no allowance, since none is evaluated for them.
func TestAccAPILimitExemptProbes(t *testing.T) {
	probes := []string{
		"/router/api/v1/system/health",
		"/router/api/v1/system/readiness",
		"/router/api/v1/system/liveness",
	}
	for _, l := range limitedOperations(t) {
		if l.cost == costExempt {
			probes = append(probes, l.target)
		}
	}
	o := apilimit.Default()
	o.AuthenticationPerMinute, o.GlobalPerMinute, o.PrincipalPerMinute = 1, 1, 1
	o.ExpensivePerMinute, o.ExpensivePrincipalPerMinute = 1, 1
	o.MaxConcurrent, o.ExpensiveConcurrent, o.ExpensivePrincipalConcurrent = 1, 1, 1
	worlds := map[string]*itemWorld{}
	for name, store := range limitStores(t) {
		worlds[name] = newItemWorld(t, false, func(c *RouterConfig) {
			var err error
			c.RequestLimits, err = apilimit.New(store, o, nil)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	worlds["unavailable"] = newItemWorld(t, false, func(c *RouterConfig) {
		var err error
		c.RequestLimits, err = apilimit.New(unavailableBudgetStore{}, apilimit.Default(), nil)
		if err != nil {
			t.Fatal(err)
		}
	})
	for name, w := range worlds {
		// Spend every allowance first, the shared authentication one too.
		_ = w.do(http.MethodGet, "/artifactory/api/search/spent", "admin", "")
		for _, probe := range probes {
			for range 3 {
				rec := w.do(http.MethodGet, probe, "", "")
				if rec.Code != http.StatusOK || rec.Header().Get("RateLimit-Policy") != "" {
					t.Errorf("%s: GET %s: %d %v %s", name, probe, rec.Code, rec.Header(), rec.Body)
				}
			}
		}
	}
	if len(probes) != 9 {
		t.Fatalf("probes %v", probes)
	}
}
