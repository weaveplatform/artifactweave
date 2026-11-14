package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/port"
)

// Factory returns a fresh, empty access store and registers the store's
// cleanup with t.
type Factory func(t *testing.T) port.Store

// Run runs the access contract on stores from newStore, one subtest per
// concern, each on a store of its own: IdentityPages, Users, UserLockout,
// Groups, Memberships, Permissions, Tokens, Keypairs, Certificates,
// TrustedKeys, SigningKeys, Projects, Roles, Environments, ProjectMembers,
// Assignments, APIRestrictions, APIRestrictionConcurrentAudit and
// PermissionPages.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	runIdentity(t, newStore)
	runPermissionPages(t, newStore)
	runProjects(t, newStore)
	runAPIRestrictions(t, newStore)
}
