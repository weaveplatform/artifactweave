package apilimit

import (
	"maps"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// These names open the service's internals to its tests, which are the
// external test package apilimit_test: they build services over the
// in-memory metadata store, whose package aliases this package's request
// budget port, so an in-package test importing it would be an import cycle.
// Each one reads or replaces exactly what the tests reached before.

// Permits reads, under the service's lock, the permits held now: ordinary
// ones, expensive ones, and the expensive ones held per principal.
func (s *Service) Permits() (active, expensive int, principals map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, s.expensive, maps.Clone(s.principals)
}

// SetBudgets replaces the request budget store, so a test can make it stall.
func (s *Service) SetBudgets(b store.RequestBudgetStore) { s.store = b }
