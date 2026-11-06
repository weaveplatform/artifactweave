package apilimit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/apilimit"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestUnitSharedRatesAndScopes(t *testing.T) {
	st := inmem.New()
	o := apilimit.Default()
	o.PrincipalPerMinute, o.GlobalPerMinute = 2, 10
	o.ExpensivePrincipalPerMinute, o.ExpensivePerMinute = 1, 2
	o.RepositoryPerMinute, o.ProjectPerMinute = 1, 1
	now := time.Date(2026, 10, 28, 12, 0, 15, 0, time.UTC)
	clock := func() time.Time { return now }
	a, err := apilimit.New(st, o, clock)
	if err != nil {
		t.Fatal(err)
	}
	b, err := apilimit.New(st, o, clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*apilimit.Service{a, b} {
		d, err := s.Admit(t.Context(), apilimit.Scope{Principal: "alice"})
		if err != nil || !d.Allowed {
			t.Fatalf("admit: %+v, %v", d, err)
		}
	}
	d, err := b.Admit(t.Context(), apilimit.Scope{Principal: "alice"})
	if err != nil || d.Allowed || d.Remaining != 0 || d.RetryAfter != 45*time.Second {
		t.Fatalf("shared quota: %+v, %v", d, err)
	}
	d, err = b.Admit(
		t.Context(),
		apilimit.Scope{Principal: "bob", Project: "project", Repository: "repo", Expensive: true},
	)
	if err != nil || !d.Allowed || d.Policy != "expensive" {
		t.Fatalf("cost quota: %+v, %v", d, err)
	}
	d, err = a.Admit(t.Context(), apilimit.Scope{Principal: "charlie", Repository: "repo"})
	if err != nil || d.Allowed {
		t.Fatalf("repository quota: %+v, %v", d, err)
	}
	d, err = a.Admit(t.Context(), apilimit.Scope{Principal: "charlie", Project: "project"})
	if err != nil || d.Allowed {
		t.Fatalf("project quota: %+v, %v", d, err)
	}
	now = now.Add(time.Minute)
	d, err = a.Admit(t.Context(), apilimit.Scope{Principal: "alice"})
	if err != nil || !d.Allowed || d.Remaining != 1 {
		t.Fatalf("reset: %+v, %v", d, err)
	}
	if a.Options() != o {
		t.Fatal("policy copy changed")
	}
}

func TestUnitBoundedConcurrency(t *testing.T) {
	o := apilimit.Default()
	o.MaxConcurrent, o.ExpensiveConcurrent, o.ExpensivePrincipalConcurrent = 2, 2, 1
	s, err := apilimit.New(inmem.New(), o, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	first, err := s.Enter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Enter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enter(ctx); !errors.Is(err, apilimit.ErrBusy) {
		t.Fatalf("unbounded ingress: %v", err)
	}
	first()
	first()
	second()
	first, err = s.EnterExpensive(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnterExpensive(ctx, "alice"); !errors.Is(err, apilimit.ErrBusy) {
		t.Fatalf("principal monopolized work: %v", err)
	}
	second, err = s.EnterExpensive(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnterExpensive(ctx, "charlie"); !errors.Is(err, apilimit.ErrBusy) {
		t.Fatalf("unbounded expensive work: %v", err)
	}
	first()
	first()
	second()
	active, expensive, principals := s.Permits()
	if active != 0 || expensive != 0 || len(principals) != 0 {
		t.Fatal("released work retained permits or principal state")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Enter(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ingress: %v", err)
	}
	if _, err := s.EnterExpensive(canceled, "alice"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled work: %v", err)
	}
}

type stalledBudgets struct{}

func (stalledBudgets) Take(
	ctx context.Context,
	_ store.BudgetRequest,
) (store.BudgetDecision, error) {
	<-ctx.Done()
	return store.BudgetDecision{}, ctx.Err()
}

func TestUnitBudgetFailureIsClosedAndBounded(t *testing.T) {
	o := apilimit.Default()
	o.BudgetTimeout = time.Millisecond
	s, err := apilimit.New(inmem.New(), o, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetBudgets(stalledBudgets{})
	for _, call := range []func(context.Context) (apilimit.Decision, error){s.Authentication, func(ctx context.Context) (apilimit.Decision, error) {
		return s.Admit(ctx, apilimit.Scope{Principal: "alice"})
	}} {
		d, err := call(t.Context())
		if d.Allowed || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("failed backend granted admission: %+v, %v", d, err)
		}
	}
}

func TestUnitAPIPolicyValidation(t *testing.T) {
	if err := apilimit.Default().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*apilimit.Options){
		func(o *apilimit.Options) { o.AuthenticationPerMinute = 0 }, func(o *apilimit.Options) { o.GlobalPerMinute = 1_000_000_001 },
		func(o *apilimit.Options) { o.MaxConcurrent = 0 }, func(o *apilimit.Options) { o.ExpensiveConcurrent = 0 },
		func(o *apilimit.Options) { o.ExpensivePrincipalConcurrent = 0 }, func(o *apilimit.Options) { o.MaxKeys = 0 },
		func(o *apilimit.Options) { o.BudgetTimeout = 0 }, func(o *apilimit.Options) { o.ExpensiveTimeout = 0 },
	} {
		o := apilimit.Default()
		change(&o)
		if _, err := apilimit.New(inmem.New(), o, nil); !errors.Is(err, apilimit.ErrInvalid) {
			t.Fatalf("invalid policy accepted: %v", err)
		}
	}
	if _, err := apilimit.New(nil, apilimit.Default(), nil); !errors.Is(err, apilimit.ErrInvalid) {
		t.Fatalf("missing store: %v", err)
	}
	o := apilimit.Default()
	o.Enabled = false
	if s, err := apilimit.New(nil, o, nil); s != nil || err != nil {
		t.Fatalf("disabled limits: %v, %v", s, err)
	}
}
