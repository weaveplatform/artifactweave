package idempotency_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
	"github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
)

func TestUnitParseKey(t *testing.T) {
	for _, raw := range []string{`"key"`, `"8e03978e-40d5-43e8-bc93-6894a57f9324"`, `"A.b_c:1"`, `"` + strings.Repeat("k", 128) + `"`} {
		key, err := idempotency.ParseKey(raw)
		if err != nil || key != digestOf(raw[1:len(raw)-1]) {
			t.Fatalf("valid key %q: %q, %v", raw, key, err)
		}
	}
	for _, raw := range []string{"", "key", `""`, `"with space"`, `"with\tescape"`, `"é"`, `"key";extra=1`, `"` + strings.Repeat("k", 129) + `"`} {
		_, err := idempotency.ParseKey(raw)
		var typed *apierror.Error
		if !errors.As(err, &typed) || typed.Status != http.StatusBadRequest {
			t.Fatalf("invalid key %q: %v", raw, err)
		}
	}
}

// strictFunc is a generated package's StrictHandlerFunc.
type strictFunc func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error)

// thing is the request object of the keyed operation the tests wrap.
type thing struct {
	Target string
	DryRun bool
}

var table = []operations.Operation{
	{
		Service:        "svc",
		Method:         http.MethodPost,
		Pattern:        "/svc/things/{target}",
		OperationID:    "makeThing",
		Idempotency:    "key",
		IdempotencyKey: true,
	},
	{
		Service:     "svc",
		Method:      http.MethodGet,
		Pattern:     "/svc/things/{target}",
		OperationID: "getThing",
		Idempotency: "safe",
	},
	{
		Service:        "other",
		Method:         http.MethodPost,
		Pattern:        "/other/things/{target}",
		OperationID:    "makeThing",
		Idempotency:    "key",
		IdempotencyKey: true,
	},
}

// world is a keyed operation over an in-memory store: its handler counts
// effects, writes a setting and reports resource 7, and its reload answers
// with the resource and whether it replayed.
type world struct {
	t       *testing.T
	replay  idempotency.Replay
	store   *inmem.Store
	binding idempotency.Binding
	effects int
	handler func(ctx context.Context, request any) (any, error)
}

func newWorld(t *testing.T) *world {
	t.Helper()
	p, s := newReplay()
	w := &world{t: t, replay: p, store: s}
	w.binding = idempotency.Binding{
		Name: "thing",
		Resource: func(_ context.Context, request any) (any, error) {
			th := request.(thing) //nolint:forcetypeassert // the test's own request
			if th.Target == "" {
				return nil, apierror.New(http.StatusNotFound, "no target")
			}
			return th.Target, nil
		},
		Reload: func(_ context.Context, _ any, receipt idempotency.ReplayReceipt) (any, error) {
			return receipt, nil
		},
	}
	w.handler = func(ctx context.Context, _ any) (any, error) {
		w.effects++
		if err := s.Settings().Put(ctx, "thing", w.effects); err != nil {
			return nil, err //nolint:wrapcheck // the test's own store
		}
		idempotency.Report(ctx, 7)
		return "made", nil
	}
	return w
}

// call sends request to operation name of service svc with the raw key
// values and body, as user, and returns the answer and its error.
func (w *world) call(
	name string,
	request thing,
	user kernel.Principal,
	body string,
	keys ...string,
) (any, error) {
	w.t.Helper()
	mw := idempotency.Strict[strictFunc](
		w.replay,
		"svc",
		table,
		map[string]idempotency.Binding{"makeThing": w.binding},
	)
	next := mw(
		func(ctx context.Context, _ http.ResponseWriter, _ *http.Request, request any) (any, error) {
			return w.handler(ctx, request)
		},
		name,
	)
	r := httptest.NewRequestWithContext(w.t.Context(), http.MethodPost, "/svc/things/x", nil)
	for _, k := range keys {
		r.Header.Add(idempotency.Header, k)
	}
	ctx := httpkit.WithRawBody(kernel.WithPrincipal(w.t.Context(), user), []byte(body))
	return next(ctx, httptest.NewRecorder(), r, request)
}

var alice = kernel.Principal{Name: "alice", Password: true}

func status(t *testing.T, err error) int {
	t.Helper()
	var typed *apierror.Error
	if !errors.As(err, &typed) {
		t.Fatalf("untyped answer: %v", err)
	}
	return typed.Status
}

func TestUnitStrictReplaysOnce(t *testing.T) {
	w := newWorld(t)
	out, err := w.call("MakeThing", thing{Target: "x"}, alice, `{"a":1}`, `"k"`)
	if err != nil || out != "made" || w.effects != 1 {
		t.Fatalf("first: %v, %v, %d effects", out, err, w.effects)
	}
	out, err = w.call("MakeThing", thing{Target: "x"}, alice, `{"a":1}`, `"k"`)
	receipt, ok := out.(idempotency.ReplayReceipt)
	if err != nil || !ok || !receipt.Replayed || receipt.ResourceID != 7 || w.effects != 1 {
		t.Fatalf("replay: %v, %v, %d effects", out, err, w.effects)
	}
	// Whitespace is intent: other bytes under the same key are refused.
	if _, err := w.call(
		"MakeThing",
		thing{Target: "x"},
		alice,
		`{"a": 1}`,
		`"k"`,
	); status(
		t,
		err,
	) != http.StatusUnprocessableEntity {
		t.Fatalf("changed body: %v", err)
	}
	// Another resource, credential or key is another intent.
	for _, c := range []struct {
		target string
		user   kernel.Principal
		key    string
	}{
		{"y", alice, `"k"`},
		{"x", kernel.Principal{Name: "bob", TokenID: "t1"}, `"k"`},
		{"x", alice, `"other"`},
	} {
		if _, err := w.call(
			"MakeThing",
			thing{Target: c.target},
			c.user,
			`{"a":1}`,
			c.key,
		); err != nil {
			t.Fatal(err)
		}
	}
	if w.effects != 4 {
		t.Fatalf("new intents ran %d effects, want 4", w.effects)
	}
}

func TestUnitStrictPassesThrough(t *testing.T) {
	w := newWorld(t)
	// An unkeyed request, an operation that declares no key, and one of
	// another spec run as they would unwrapped.
	for _, c := range []struct {
		name string
		keys []string
	}{{"MakeThing", nil}, {"GetThing", []string{`"k"`}}, {"Unknown", []string{`"k"`}}} {
		out, err := w.call(c.name, thing{}, kernel.Anonymous, "", c.keys...)
		if err != nil || out != "made" {
			t.Fatalf("%s: %v, %v", c.name, out, err)
		}
	}
	if w.effects != 3 {
		t.Fatalf("pass-through ran %d effects", w.effects)
	}
	// Report outside the replay is harmless.
	idempotency.Report(t.Context(), 1)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	if idempotency.Keyed(r) {
		t.Fatal("a request without a key reported keyed")
	}
	r.Header.Set(idempotency.Header, "")
	if !idempotency.Keyed(r) {
		t.Fatal("a request with an empty key reported unkeyed")
	}
}

func TestUnitStrictRefusals(t *testing.T) {
	registry := kernel.Principal{Name: "reg", Registry: &kernel.RegistryAccess{}}
	for _, c := range []struct {
		name    string
		request thing
		user    kernel.Principal
		key     string
		noUser  bool
		want    int
	}{
		{name: "resource check first", request: thing{}, user: alice, key: "unquoted", want: http.StatusNotFound},
		{name: "malformed key", request: thing{Target: "x"}, user: alice, key: "unquoted", want: http.StatusBadRequest},
		{name: "no caller", request: thing{Target: "x"}, key: `"k"`, noUser: true, want: http.StatusUnauthorized},
		{name: "anonymous", request: thing{Target: "x"}, user: kernel.Anonymous, key: `"k"`, want: http.StatusUnauthorized},
		{name: "registry token", request: thing{Target: "x"}, user: registry, key: `"k"`, want: http.StatusForbidden},
		{name: "no stable credential", request: thing{Target: "x"}, user: kernel.Principal{Name: "x"}, key: `"k"`, want: http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			var err error
			if c.noUser {
				mw := idempotency.Strict[strictFunc](
					w.replay,
					"svc",
					table,
					map[string]idempotency.Binding{"makeThing": w.binding},
				)
				r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
				r.Header.Set(idempotency.Header, c.key)
				_, err = mw(
					func(context.Context, http.ResponseWriter, *http.Request, any) (any, error) {
						return nil, nil
					},
					"MakeThing",
				)(
					t.Context(),
					httptest.NewRecorder(),
					r,
					c.request,
				)
			} else {
				_, err = w.call("MakeThing", c.request, c.user, "", c.key)
			}
			if status(t, err) != c.want || w.effects != 0 {
				t.Fatalf("answered %v with %d effects, want %d", err, w.effects, c.want)
			}
		})
	}
}

func TestUnitStrictUnbound(t *testing.T) {
	w := newWorld(t)
	w.binding.Reload = nil
	if _, err := w.call(
		"MakeThing",
		thing{Target: "x"},
		alice,
		"",
		`"k"`,
	); status(t, err) != http.StatusInternalServerError ||
		w.effects != 0 {
		t.Fatalf("a binding without a reload: %v, %d effects", err, w.effects)
	}
	mw := idempotency.Strict[strictFunc](w.replay, "svc", table, nil)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	r.Header.Set(idempotency.Header, `"k"`)
	_, err := mw(func(context.Context, http.ResponseWriter, *http.Request, any) (any, error) {
		t.Error("an unbound keyed operation ran")
		return nil, nil
	}, "MakeThing")(kernel.WithPrincipal(t.Context(), alice), httptest.NewRecorder(), r, thing{Target: "x"})
	if status(t, err) != http.StatusInternalServerError {
		t.Fatalf("unbound: %v", err)
	}
}

func TestUnitStrictWithoutStore(t *testing.T) {
	w := newWorld(t)
	w.replay = idempotency.Replay{}
	if _, err := w.call(
		"MakeThing",
		thing{Target: "x"},
		alice,
		"",
		`"k"`,
	); status(
		t,
		err,
	) != http.StatusNotImplemented {
		t.Fatalf("no store: %v", err)
	}
}

func TestUnitStrictFingerprintParameters(t *testing.T) {
	w := newWorld(t)
	w.binding.Fingerprint = func(_ context.Context, request any) (any, error) {
		return request.(thing).DryRun, nil //nolint:forcetypeassert // the test's own request
	}
	if _, err := w.call("MakeThing", thing{Target: "x"}, alice, "{}", `"k"`); err != nil {
		t.Fatal(err)
	}
	// A parameter that changes the effect is intent, as the body is.
	if _, err := w.call(
		"MakeThing",
		thing{Target: "x", DryRun: true},
		alice,
		"{}",
		`"k"`,
	); status(
		t,
		err,
	) != http.StatusUnprocessableEntity {
		t.Fatalf("changed effect parameter: %v", err)
	}
	w.binding.Fingerprint = func(context.Context, any) (any, error) { return nil, errEffect }
	if _, err := w.call(
		"MakeThing",
		thing{Target: "x"},
		alice,
		"{}",
		`"k"`,
	); status(
		t,
		err,
	) != http.StatusInternalServerError {
		t.Fatalf("fingerprint failure: %v", err)
	}
	w.binding.Fingerprint = func(context.Context, any) (any, error) { return func() {}, nil }
	if _, err := w.call(
		"MakeThing",
		thing{Target: "x"},
		alice,
		"{}",
		`"k"`,
	); status(
		t,
		err,
	) != http.StatusInternalServerError {
		t.Fatalf("unencodable fingerprint: %v", err)
	}
	w.binding.Resource = func(context.Context, any) (any, error) { return func() {}, nil }
	if _, err := w.call(
		"MakeThing",
		thing{Target: "x"},
		alice,
		"{}",
		`"k"`,
	); status(
		t,
		err,
	) != http.StatusInternalServerError {
		t.Fatalf("unencodable scope: %v", err)
	}
	w.binding.Resource, w.binding.Fingerprint = nil, nil
	if _, err := w.call("MakeThing", thing{}, alice, "{}", `"nil-resource"`); err != nil {
		t.Fatalf("no resource: %v", err)
	}
}

func TestUnitStrictFailuresRollBack(t *testing.T) {
	w := newWorld(t)
	var value int
	unset := func() {
		t.Helper()
		if err := w.store.Settings().
			Get(t.Context(), "thing", &value); !errors.Is(
			err,
			kernel.ErrNotFound,
		) {
			t.Fatalf("rolled-back effect survived: %v, %v", value, err)
		}
	}
	effect := w.handler
	// The handler's own error answers as the handler gives it.
	w.handler = func(ctx context.Context, request any) (any, error) {
		if _, err := effect(ctx, request); err != nil {
			return nil, err
		}
		return nil, apierror.New(http.StatusConflict, "taken")
	}
	if _, err := w.call(
		"MakeThing",
		thing{Target: "x"},
		alice,
		"",
		`"k"`,
	); status(
		t,
		err,
	) != http.StatusConflict {
		t.Fatalf("handler error: %v", err)
	}
	unset()
	// An untyped error is a 500 by default, and the binding's Error maps it
	// when it has one.
	w.handler = func(ctx context.Context, request any) (any, error) {
		if _, err := effect(ctx, request); err != nil {
			return nil, err
		}
		return nil, errEffect
	}
	if _, err := w.call(
		"MakeThing",
		thing{Target: "x"},
		alice,
		"",
		`"k"`,
	); status(
		t,
		err,
	) != http.StatusInternalServerError {
		t.Fatalf("untyped handler error: %v", err)
	}
	w.binding.Error = func(_ context.Context, _ any, err error) error {
		if errors.Is(err, errEffect) {
			return apierror.New(http.StatusTeapot, "mapped")
		}
		return err
	}
	if _, err := w.call(
		"MakeThing",
		thing{Target: "x"},
		alice,
		"",
		`"k"`,
	); status(
		t,
		err,
	) != http.StatusTeapot {
		t.Fatalf("mapped handler error: %v", err)
	}
	unset()
	// A handler that answers without reporting a resource made no effect
	// the receipt could name: its work rolls back and its answer stands.
	w.handler = func(ctx context.Context, _ any) (any, error) {
		if err := w.store.Settings().Put(ctx, "thing", 1); err != nil {
			return nil, err //nolint:wrapcheck // the test's own store
		}
		return "typed refusal", nil
	}
	if out, err := w.call(
		"MakeThing",
		thing{Target: "x"},
		alice,
		"",
		`"k"`,
	); err != nil ||
		out != "typed refusal" {
		t.Fatalf("unreported answer: %v, %v", out, err)
	}
	unset()
	// A reload that refuses rolls the effect back with it.
	w.handler = effect
	w.binding.Reload = func(context.Context, any, idempotency.ReplayReceipt) (any, error) {
		return nil, errEffect
	}
	if _, err := w.call(
		"MakeThing",
		thing{Target: "x"},
		alice,
		"",
		`"k"`,
	); status(
		t,
		err,
	) != http.StatusTeapot {
		t.Fatalf("reload error: %v", err)
	}
	unset()
}

func TestUnitStrictBudget(t *testing.T) {
	w := newWorld(t)
	w.replay.Budget = 10 * time.Millisecond
	w.handler = func(ctx context.Context, _ any) (any, error) {
		<-ctx.Done()
		return nil, apierror.New(http.StatusInternalServerError, "internal error")
	}
	_, err := w.call("MakeThing", thing{Target: "x"}, alice, "", `"k"`)
	var typed *apierror.Error
	if !errors.As(err, &typed) || typed.Status != http.StatusServiceUnavailable ||
		typed.RetryAfterSeconds != 1 ||
		typed.Message != "Protected thing exceeded its wait budget; retry the same key" {
		t.Fatalf("spent budget: %+v", err)
	}
	w.binding.Name = ""
	_, err = w.call("MakeThing", thing{Target: "x"}, alice, "", `"k"`)
	if !errors.As(err, &typed) ||
		typed.Message != "Protected request exceeded its wait budget; retry the same key" {
		t.Fatalf("unnamed effect: %+v", err)
	}
}
