// Package apilimit combines shared request budgets with bounded local work.
package apilimit

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalid = errors.New("api limit: invalid policy")
	ErrBusy    = errors.New("api limit: concurrent work capacity exhausted")
)

// Options is immutable after constructing a Service. Rates are shared across
// replicas; concurrency limits apply to one process and have no waiting queue.
type Options struct {
	Enabled                      bool          `yaml:"enabled"`
	AuthenticationPerMinute      int64         `yaml:"authentication_per_minute"`
	GlobalPerMinute              int64         `yaml:"global_per_minute"`
	PrincipalPerMinute           int64         `yaml:"principal_per_minute"`
	ProjectPerMinute             int64         `yaml:"project_per_minute"`
	RepositoryPerMinute          int64         `yaml:"repository_per_minute"`
	ExpensivePerMinute           int64         `yaml:"expensive_per_minute"`
	ExpensivePrincipalPerMinute  int64         `yaml:"expensive_principal_per_minute"`
	MaxConcurrent                int           `yaml:"max_concurrent"`
	ExpensiveConcurrent          int           `yaml:"expensive_concurrent"`
	ExpensivePrincipalConcurrent int           `yaml:"expensive_principal_concurrent"`
	MaxKeys                      int           `yaml:"max_keys"`
	BudgetTimeout                time.Duration `yaml:"budget_timeout"`
	ExpensiveTimeout             time.Duration `yaml:"expensive_timeout"`
}

// Default returns the initial operational policy, including optional resource
// scopes once the HTTP boundary has resolved their existence.
func Default() Options {
	return Options{
		Enabled: true, AuthenticationPerMinute: 120000, GlobalPerMinute: 60000,
		PrincipalPerMinute: 6000, ProjectPerMinute: 12000, RepositoryPerMinute: 12000,
		ExpensivePerMinute: 600, ExpensivePrincipalPerMinute: 60,
		MaxConcurrent: 128, ExpensiveConcurrent: 8, ExpensivePrincipalConcurrent: 2,
		MaxKeys: 16384, BudgetTimeout: 500 * time.Millisecond, ExpensiveTimeout: 2 * time.Minute,
	}
}

// Validate rejects unsafe bounds even while disabled, so enabling a saved
// configuration does not expose a latent unbounded policy.
func (o Options) Validate() error {
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"authentication_per_minute", o.AuthenticationPerMinute},
		{"global_per_minute", o.GlobalPerMinute},
		{"principal_per_minute", o.PrincipalPerMinute},
		{"project_per_minute", o.ProjectPerMinute},
		{"repository_per_minute", o.RepositoryPerMinute},
		{"expensive_per_minute", o.ExpensivePerMinute},
		{"expensive_principal_per_minute", o.ExpensivePrincipalPerMinute},
	} {
		if field.value < 1 || field.value > 1_000_000_000 {
			return fmt.Errorf("%w: %s must be 1..1000000000", ErrInvalid, field.name)
		}
	}
	if o.MaxConcurrent < 1 || o.MaxConcurrent > 4096 || o.ExpensiveConcurrent < 1 ||
		o.ExpensiveConcurrent > o.MaxConcurrent || o.ExpensivePrincipalConcurrent < 1 ||
		o.ExpensivePrincipalConcurrent > o.ExpensiveConcurrent {
		return fmt.Errorf(
			"%w: concurrency must satisfy 1 <= principal <= expensive <= total <= 4096",
			ErrInvalid,
		)
	}
	if o.MaxKeys < 16 || o.MaxKeys > 65536 || o.BudgetTimeout < time.Millisecond ||
		o.BudgetTimeout > 5*time.Second ||
		o.ExpensiveTimeout < time.Second ||
		o.ExpensiveTimeout > time.Hour {
		return fmt.Errorf(
			"%w: counter capacity or timeout is outside its bounded range",
			ErrInvalid,
		)
	}
	return nil
}
