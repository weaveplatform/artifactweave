package access

import (
	"context"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/ldap"
)

var _ accessapi.LDAPSettings = ldapSettings{}

// ldapSettings binds the descriptor's view of the LDAP connections to the
// same core service the Access LDAP API writes through, so both surfaces
// store one document per connection. The descriptor holds the transaction
// and the administration guard, so the calls only join ctx's.
type ldapSettings struct {
	st Store
}

func (b ldapSettings) svc() ldap.Service { return ldap.Service{Configs: b.st.Configs()} }

func (b ldapSettings) Connections(ctx context.Context) ([]accessapi.LDAPConnection, error) {
	return b.svc().List(ctx) //nolint:wrapcheck // the service's errors are the contract's
}

func (b ldapSettings) Plan(ctx context.Context, changes []accessapi.LDAPChange) ([]accessapi.LDAPResult, error) {
	return b.svc().Plan(ctx, changes) //nolint:wrapcheck // the service's errors are the contract's
}

func (b ldapSettings) Apply(ctx context.Context, changes []accessapi.LDAPChange) ([]accessapi.LDAPResult, error) {
	return b.svc().Apply(ctx, changes) //nolint:wrapcheck // the service's errors are the contract's
}
