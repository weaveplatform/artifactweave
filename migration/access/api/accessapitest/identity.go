package accessapitest

import (
	"context"
	"fmt"
	"slices"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

var _ accessapi.Identity = (*Identity)(nil)

// Identity is an Identity over a map of accounts and a list of auto-join
// groups, which it only reads. A name missing from Users answers an error
// matching kernel.ErrNotFound; Err, when set, answers every call instead, as
// a store that cannot be read does. The groups it returns are copies, so a
// caller that changes them changes nothing here. It may be used from several
// goroutines while no one changes its fields.
type Identity struct {
	// Users maps a user name to the account.
	Users map[string]accessapi.UserInfo
	// AutoJoin is the names of the groups new users join, in order.
	AutoJoin []string
	// Err, when set, is every call's error.
	Err error
}

// User returns the account called name, Err, or an error matching
// kernel.ErrNotFound when Users has no such name.
func (i *Identity) User(_ context.Context, name string) (accessapi.UserInfo, error) {
	if i.Err != nil {
		return accessapi.UserInfo{}, i.Err
	}
	u, ok := i.Users[name]
	if !ok {
		return accessapi.UserInfo{}, fmt.Errorf(
			"accessapitest: user %q: %w",
			name,
			kernel.ErrNotFound,
		)
	}
	u.Groups = slices.Clone(u.Groups)
	return u, nil
}

// AutoJoinGroups returns a copy of AutoJoin, or Err.
func (i *Identity) AutoJoinGroups(context.Context) ([]string, error) {
	if i.Err != nil {
		return nil, i.Err
	}
	return slices.Clone(i.AutoJoin), nil
}
