package apilimit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestUnitSharedRatesAndScopes(t *testing.T) {
	st := inmem.New()
	o := Default()
	o.PrincipalPerMinute, o.GlobalPerMinute = 2, 10
	o.ExpensivePrincipalPerMinute, o.ExpensivePerMinute = 1, 2
	o.RepositoryPerMinute, o.ProjectPerMinute = 1, 1
	now := time.Date(2026, 10, 28, 12, 0, 15, 0, time.UTC)
	clock := func() time.Time { return now }
	a, err := New(st, o, clock)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(st, o, clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Service{a, b} {
		d, err := s.Admit(t.Context(), Scope{Principal: "alice"})
		if err != nil || !d.Allowed {
			t.Fatalf("admit: %+v, %v", d, err)
		}
	}
	d, err := b.Admit(t.Context(), Scope{Principal: "alice"})
	if err != nil || d.Allowed || d.Remaining != 0 || d.RetryAfter != 45*time.Second {
		t.Fatalf("shared quota: %+v, %v", d, err)
	}
	d, err = b.Admit(t.Context(), Scope{Principal: "bob", Project: "project", Repository: "repo", Expensive: true})
	if err != nil || !d.Allowed || d.Policy != "expensive" {
		t.Fatalf("cost quota: %+v, %v", d, err)
	}
	d, err = a.Admit(t.Context(), Scope{Principal: "charlie", Repository: "repo"})
	if err != nil || d.Allowed {
		t.Fatalf("repository quota: %+v, %v", d, err)
	}
	d, err = a.Admit(t.Context(), Scope{Principal: "charlie", Project: "project"})
	if err != nil || d.Allowed {
		t.Fatalf("project quota: %+v, %v", d, err)
	}
	now = now.Add(time.Minute)
	d, err = a.Admit(t.Context(), Scope{Principal: "alice"})
	if err != nil || !d.Allowed || d.Remaining != 1 {
		t.Fatalf("reset: %+v, %v", d, err)
	}
	if a.Options() != o {
		t.Fatal("policy copy changed")
	}
}

func TestUnitBoundedConcurrency(t *testing.T) {
	o := Default()
	o.MaxConcurrent, o.ExpensiveConcurrent, o.ExpensivePrincipalConcurrent = 2, 2, 1
	s, err := New(inmem.New(), o, nil)
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
	if _, err := s.Enter(ctx); !errors.Is(err, ErrBusy) {
		t.Fatalf("unbounded ingress: %v", err)
	}
	first()
	first()
	second()
	first, err = s.EnterExpensive(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnterExpensive(ctx, "alice"); !errors.Is(err, ErrBusy) {
		t.Fatalf("principal monopolized work: %v", err)
	}
	second, err = s.EnterExpensive(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnterExpensive(ctx, "charlie"); !errors.Is(err, ErrBusy) {
		t.Fatalf("unbounded expensive work: %v", err)
	}
	first()
	first()
	second()
	if s.active != 0 || s.expensive != 0 || len(s.principals) != 0 {
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

func (stalledBudgets) Take(ctx context.Context, _ store.BudgetRequest) (store.BudgetDecision, error) {
	<-ctx.Done()
	return store.BudgetDecision{}, ctx.Err()
}

func TestUnitBudgetFailureIsClosedAndBounded(t *testing.T) {
	o := Default()
	o.BudgetTimeout = time.Millisecond
	s, err := New(inmem.New(), o, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.store = stalledBudgets{}
	for _, call := range []func(context.Context) (Decision, error){s.Authentication, func(ctx context.Context) (Decision, error) { return s.Admit(ctx, Scope{Principal: "alice"}) }} {
		d, err := call(t.Context())
		if d.Allowed || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("failed backend granted admission: %+v, %v", d, err)
		}
	}
}

func TestUnitAPIPolicyValidation(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Options){
		func(o *Options) { o.AuthenticationPerMinute = 0 }, func(o *Options) { o.GlobalPerMinute = 1_000_000_001 },
		func(o *Options) { o.MaxConcurrent = 0 }, func(o *Options) { o.ExpensiveConcurrent = 0 },
		func(o *Options) { o.ExpensivePrincipalConcurrent = 0 }, func(o *Options) { o.MaxKeys = 0 },
		func(o *Options) { o.BudgetTimeout = 0 }, func(o *Options) { o.ExpensiveTimeout = 0 },
	} {
		o := Default()
		change(&o)
		if _, err := New(inmem.New(), o, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid policy accepted: %v", err)
		}
	}
	if _, err := New(nil, Default(), nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing store: %v", err)
	}
	o := Default()
	o.Enabled = false
	if s, err := New(nil, o, nil); s != nil || err != nil {
		t.Fatalf("disabled limits: %v, %v", s, err)
	}
}
