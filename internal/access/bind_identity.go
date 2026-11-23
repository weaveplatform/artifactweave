package access

import (
	"context"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var _ accessapi.Identity = identity{}

// identity reads user accounts and the auto-join groups. Errors are the
// store's, unwrapped, so errors.Is(err, kernel.ErrNotFound) and each caller's
// own wrap still apply. A user is found by name in any case and named as
// stored (model.FoldUsername).
type identity struct{ st Store }

func (b identity) User(ctx context.Context, name string) (accessapi.UserInfo, error) {
	u, err := b.st.Users().Get(ctx, model.FoldUsername(name))
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

// Administrators pages through every account once, after reading the groups
// that confer admin privileges, so an account made an administrator only by
// a group is not missed. It is meant for rare notifications, not requests.
func (b identity) Administrators(ctx context.Context) ([]accessapi.UserInfo, error) {
	var admins []model.Group
	groups := kernel.NewPages(b.st.Groups().List, kernel.MaxLimit)
	for {
		rows, more, err := groups.Next(ctx)
		if err != nil {
			return nil, fmt.Errorf("read administrator groups: %w", err)
		}
		if !more {
			break
		}
		for _, g := range rows {
			if g.AdminPrivileges {
				admins = append(admins, g)
			}
		}
	}
	var out []accessapi.UserInfo
	users := kernel.NewPages(b.st.Users().List, kernel.MaxLimit)
	for {
		rows, more, err := users.Next(ctx)
		if err != nil {
			return nil, fmt.Errorf("read administrators: %w", err)
		}
		if !more {
			break
		}
		for _, u := range rows {
			if model.EffectiveAdmin(u, admins) {
				out = append(out, accessapi.UserInfo{
					Name: u.Name, Email: u.Email, Admin: true, Groups: u.Groups, Status: string(u.Status),
				})
			}
		}
	}
	return out, nil
}
