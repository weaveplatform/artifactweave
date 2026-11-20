package api

import (
	"maps"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ProjectScope preserves a verified token's project ceiling in a separately
// signed server capability. It must only be restored after that capability's
// signature and expiry have been verified; it does not authenticate a caller.
type ProjectScope struct {
	Key       string            `json:"project,omitempty"`
	Lifetimes map[string]string `json:"project_incarnations,omitempty"`
}

// ProjectScopeOf copies p's verified project ceiling. Password and unsigned
// embedding principals cannot contribute token authority to a capability.
func ProjectScopeOf(p kernel.Principal) ProjectScope {
	if !p.IsTokenAuthenticated {
		return ProjectScope{}
	}
	return ProjectScope{Key: p.TokenProject, Lifetimes: maps.Clone(p.ProjectIncarnations)}
}

// WithProjectScope returns s bounded by the project scope of a verified server
// capability. Current project lifetimes and role definitions are still checked
// when the authorizer evaluates the resulting subject.
func (s Subject) WithProjectScope(scope ProjectScope) Subject {
	s.ProjectKey = scope.Key
	s.ProjectIncarnations = maps.Clone(scope.Lifetimes)
	return s
}
