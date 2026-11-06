package types

import (
	"maps"
	"slices"
	"time"
)

// Grants map principal names to the actions they hold.
type Grants struct {
	Users  map[string]Actions `json:"users,omitempty"`
	Groups map[string]Actions `json:"groups,omitempty"`
}

// Target scopes a resource to paths inside one repository or special target.
type Target struct {
	Include []string `json:"include_patterns"`
	Exclude []string `json:"exclude_patterns"`
}

// Resource is one section of a permission: who may do what, where.
type Resource struct {
	Actions Grants            `json:"actions"`
	Targets map[string]Target `json:"targets"`
}

// Permission is the single internal representation of every permission wire
// form (decision 0006). Resources are stored as one JSON document in the
// Access v2 shape; the grants are also flattened into a table the
// authorizer queries by principal (research 15 section 4).
type Permission struct {
	Name        string
	Description string
	Resources   map[ResourceType]Resource
	Created     time.Time
	Updated     time.Time
}

// ResourceTypes returns the permission's sections in a stable order.
func (p Permission) ResourceTypes() []ResourceType {
	return slices.Sorted(maps.Keys(p.Resources))
}

// MatchesRepository reports whether target covers a repository of the given
// key and class (local, remote, virtual, federated, distribution).
func MatchesRepository(target, key, class string) bool {
	switch target {
	case TargetAny:
		return true
	case TargetAnyLocal:
		return class == "local" || class == "federated"
	case TargetAnyRemote:
		return class == "remote"
	case TargetAnyDistribution:
		return class == "distribution"
	default:
		return target == key
	}
}
