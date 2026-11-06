package apilimit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

// Scope carries authenticated and resolved identities, never arbitrary client
// labels. Empty resource scopes mean the request is not bound to one resource.
type Scope struct {
	Principal, Project, Repository string
	Expensive                      bool
}

// Decision exposes only the caller's quota, not other principals' shared usage.
type Decision struct {
	Allowed    bool
	Policy     string
	Limit      int64
	Remaining  int64
	Reset      time.Time
	ResetAfter time.Duration
	RetryAfter time.Duration
}

// Store is what admission needs from persistence: the request budgets that
// Authentication and Admit charge. New reads RequestBudgets once and charges
// through what it returns, so services over one store share its rates, as
// replicas over one metadata database do; the metadata store satisfies it.
type Store interface {
	RequestBudgets() RequestBudgetStore
}

// Service owns one process's permits and shares rates through metadata. It
// starts no goroutines. Release must run after the handler actually returns,
// including when its context is canceled; never release early while work runs.
type Service struct {
	store             RequestBudgetStore
	options           Options
	now               func() time.Time
	mu                sync.Mutex
	active, expensive int
	principals        map[string]int
	recovery          int
	recoveryUsed      int64
	recoveryReset     time.Time
}

func New(st Store, options Options, now func() time.Time) (*Service, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if !options.Enabled {
		return nil, nil //nolint:nilnil // disabled policy deliberately has no service
	}
	if st == nil {
		return nil, fmt.Errorf("%w: enabled limits require metadata", ErrInvalid)
	}
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:      st.RequestBudgets(),
		options:    options,
		now:        now,
		principals: map[string]int{},
	}, nil
}

// Options returns a copy of the configured policy.
func (s *Service) Options() Options { return s.options }

// Enter bounds all request work in this process, including authentication.
func (s *Service) Enter(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("admit request: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active >= s.options.MaxConcurrent {
		return nil, ErrBusy
	}
	s.active++
	return sync.OnceFunc(func() { s.mu.Lock(); s.active--; s.mu.Unlock() }), nil
}

// EnterExpensive refuses excess work immediately; no caller can fill a pending
// queue, and one principal cannot occupy every expensive permit by default.
func (s *Service) EnterExpensive(ctx context.Context, principal string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("admit expensive request: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expensive >= s.options.ExpensiveConcurrent ||
		s.principals[principal] >= s.options.ExpensivePrincipalConcurrent {
		return nil, ErrBusy
	}
	s.expensive++
	s.principals[principal]++
	return sync.OnceFunc(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.expensive--
		s.principals[principal]--
		if s.principals[principal] == 0 {
			delete(s.principals, principal)
		}
	}), nil
}

func budgetKey(policy, scope string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(policy+"\x00"+scope)))
}

// Authentication charges a shared coarse budget before credential verification.
func (s *Service) Authentication(ctx context.Context) (Decision, error) {
	now := s.now().UTC()
	b := RequestBudget{
		Key:   budgetKey("authentication", ""),
		Limit: s.options.AuthenticationPerMinute,
		Reset: now.Truncate(time.Minute).Add(time.Minute),
	}
	return s.take(ctx, now, []RequestBudget{b}, 0, "authentication")
}

// Admit charges the accepted request's global, principal, cost and resolved
// resource scopes atomically. Rejections cannot consume another user's budget.
func (s *Service) Admit(ctx context.Context, scope Scope) (Decision, error) {
	now := s.now().UTC()
	reset := now.Truncate(time.Minute).Add(time.Minute)
	budgets := make([]RequestBudget, 0, 6)
	add := func(policy, scope string, limit int64) {
		budgets = append(
			budgets,
			RequestBudget{Key: budgetKey(policy, scope), Limit: limit, Reset: reset},
		)
	}
	add("global", "", s.options.GlobalPerMinute)
	add("principal", scope.Principal, s.options.PrincipalPerMinute)
	visible, policy := 1, "api"
	if scope.Expensive {
		add("expensive", "", s.options.ExpensivePerMinute)
		add("expensive-principal", scope.Principal, s.options.ExpensivePrincipalPerMinute)
		visible, policy = 3, "expensive"
	}
	if scope.Project != "" {
		add("project", scope.Project, s.options.ProjectPerMinute)
	}
	if scope.Repository != "" {
		add("repository", scope.Repository, s.options.RepositoryPerMinute)
	}
	return s.take(ctx, now, budgets, visible, policy)
}

func (s *Service) take(
	ctx context.Context,
	now time.Time,
	budgets []RequestBudget,
	visible int,
	policy string,
) (Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, s.options.BudgetTimeout)
	defer cancel()
	result, err := s.store.Take(
		ctx,
		BudgetRequest{Budgets: budgets, Now: now, MaxKeys: s.options.MaxKeys},
	)
	if err != nil {
		return Decision{}, fmt.Errorf("request budget: %w", err)
	}
	b := result.Balances[visible]
	out := Decision{
		Allowed:   result.Allowed,
		Policy:    policy,
		Limit:     budgets[visible].Limit,
		Remaining: b.Remaining,
		Reset:     b.Reset,
	}
	out.ResetAfter = b.Reset.Sub(now)
	if !out.Allowed {
		out.Remaining = 0
		for _, balance := range result.Balances {
			if balance.Remaining == 0 {
				out.RetryAfter = max(out.RetryAfter, balance.Reset.Sub(now))
			}
		}
		out.Reset = now.Add(out.RetryAfter)
		out.ResetAfter = out.RetryAfter
	}
	return out, nil
}
