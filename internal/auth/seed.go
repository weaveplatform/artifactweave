package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Names of the built-in principals Artifactory ships (research 05).
const (
	AdminUser          = "admin"
	ReadersGroup       = "readers"
	AnythingPermission = "Anything"
	// AnyRemotePermission lets anonymous read remote repositories and cache
	// what it reads (Artifactory's "Deploy/Cache", WRITE in the Access
	// vocabulary). It matters only once anonymous access is turned on.
	AnyRemotePermission = "Any Remote"
)

// seedMarker is the SettingSeeded value.
type seedMarker struct {
	Version int       `json:"version"`
	At      time.Time `json:"at"`
}

// Seeded reports whether the built-in principals have been created.
func Seeded(ctx context.Context, st store.Store) (bool, error) {
	var m seedMarker
	err := st.Settings().Get(ctx, types.SettingSeeded, &m)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("auth: seeded marker: %w", err)
	}
	return true, nil
}

// Seed creates, in one transaction, the admin user with adminPassword, the
// anonymous user, the readers group, the Anything and Any Remote
// permissions and the artifactory-build-info repository, then marks the
// store seeded. It reports whether this call seeded: false when the store
// already was, including when another instance sharing the database won a
// race to seed it first.
func Seed(ctx context.Context, st store.Store, adminPassword string, now time.Time) (bool, error) {
	if seeded, err := Seeded(ctx, st); err != nil || seeded {
		return false, err
	}
	hash, err := HashPassword(adminPassword)
	if err != nil {
		return false, err
	}
	return SeedHashed(ctx, st, hash, now)
}

// SeedHashed is Seed with the admin password already hashed (HashPassword or
// HashPasswordWith).
func SeedHashed(
	ctx context.Context,
	st store.Store,
	adminHash []byte,
	now time.Time,
) (bool, error) {
	if seeded, err := Seeded(ctx, st); err != nil || seeded {
		return false, err
	}
	err := st.Tx(ctx, func(ctx context.Context) error {
		// Checked again inside the transaction for a seed that committed
		// while the hash was computed.
		seeded, err := Seeded(ctx, st)
		if err != nil {
			return err
		}
		if seeded {
			return errAlreadySeeded
		}
		return seed(ctx, st, adminHash, now)
	})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errAlreadySeeded):
		return false, nil
	case errors.Is(err, store.ErrConflict):
		// Two instances seeding one database at once: the loser's creates
		// conflict. It is fine if the winner's marker is now there.
		if seeded, serr := Seeded(ctx, st); serr == nil && seeded {
			return false, nil
		}
	}
	return false, fmt.Errorf("auth: seed: %w", err)
}

var errAlreadySeeded = errors.New("already seeded")

func seed(ctx context.Context, st store.Store, adminHash []byte, now time.Time) error {
	if err := st.Groups().Create(ctx, types.Group{
		Name: ReadersGroup, Description: "A group for read-only users",
		AutoJoin: true, Realm: "internal",
	}); err != nil {
		return fmt.Errorf("group %s: %w", ReadersGroup, err)
	}
	for _, u := range []types.User{
		{
			Name: AdminUser, PasswordHash: adminHash, Admin: true, ProfileUpdatable: true,
			Realm: "internal", Status: types.UserEnabled, Created: now, PasswordChanged: now,
		},
		// No password: anonymous never logs in, it is what a request without
		// credentials acts as when anonymous access is on.
		{Name: AnonymousUser, Realm: "internal", Status: types.UserEnabled, Created: now},
	} {
		if err := st.Users().Create(ctx, u); err != nil {
			return fmt.Errorf("user %s: %w", u.Name, err)
		}
	}
	everything := []string{"**"}
	for _, p := range []types.Permission{
		{
			Name: AnythingPermission,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Groups: map[string]types.Actions{ReadersGroup: types.ActionRead}},
				Targets: map[string]types.Target{types.TargetAny: {Include: everything, Exclude: []string{}}},
			}},
			Created: now, Updated: now,
		},
		{
			Name: AnyRemotePermission,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					AnonymousUser: types.ActionRead | types.ActionWrite,
				}},
				Targets: map[string]types.Target{
					types.TargetAnyRemote: {Include: everything, Exclude: []string{}},
				},
			}},
			Created: now, Updated: now,
		},
	} {
		if err := st.Permissions().Create(ctx, p); err != nil {
			return fmt.Errorf("permission %s: %w", p.Name, err)
		}
	}
	buildInfo := repo.Config{
		Key: types.BuildInfoRepository, Class: repo.Local, PackageType: repo.BuildInfo,
		Description: "Build Info repository", RepoLayoutRef: repo.LayoutSimple,
	}
	if err := st.Repos().Create(ctx, *repo.Defaults(&buildInfo)); err != nil {
		return fmt.Errorf("repository %s: %w", types.BuildInfoRepository, err)
	}
	if err := st.Settings().
		Put(ctx, types.SettingSeeded, seedMarker{Version: 1, At: now}); err != nil {
		return fmt.Errorf("seeded marker: %w", err)
	}
	return nil
}
