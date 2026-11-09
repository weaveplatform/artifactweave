package port

import "github.com/weaveplatform/artifactweave/pkg/types"

// IdentityFilter narrows a name-ordered identity listing before pagination.
// NameContains is a literal, Unicode-lowercased substring, not a SQL pattern.
// Resource selects exact permission targets with nonzero grants; it does not
// evaluate artifact include/exclude patterns or expand wildcard targets.
type IdentityFilter struct {
	NameContains string
	Descending   bool
	Resource     *IdentityResource
}

// IdentityResource selects a permission resource and its exact target name.
type IdentityResource struct {
	Type types.ResourceType
	Name string
}

// UserFilter adds account status and effective administrator selection.
type UserFilter struct {
	IdentityFilter
	Status     types.UserStatus
	OnlyAdmins bool
}

// UserSummary is the public listing projection. It deliberately excludes
// password hashes, credentials and membership lists.
type UserSummary struct {
	Name   string
	Realm  string
	Status types.UserStatus
}
