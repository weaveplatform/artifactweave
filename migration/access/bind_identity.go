package access

import (
	"context"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
)

var _ accessapi.Identity = identity{}

// identity reads user accounts and the auto-join groups. Errors are the
// store's, unwrapped, so errors.Is(err, kernel.ErrNotFound) and each caller's
// own wrap still apply.
type identity struct{ st Store }

func (b identity) User(ctx context.Context, name string) (accessapi.UserInfo, error) {
	u, err := b.st.Users().Get(ctx, name)
	if err != nil {
		return accessapi.UserInfo{}, err //nolint:wrapcheck // the store's errors are the contract's
	}
	return accessapi.UserInfo{
		Name: u.Name, Email: u.Email, Admin: u.Admin, Groups: u.Groups, Status: string(u.Status),
	}, nil
}

func (b identity) AutoJoinGroups(ctx context.Context) ([]string, error) {
	return b.st.Groups().AutoJoin(ctx) //nolint:wrapcheck // the store's errors are the contract's
}
