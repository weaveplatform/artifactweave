package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	// AnyRemotePermission covers every remote repository and grants no one
	// anything. Artifactory has shipped it without anonymous since 7.84.3, so
	// a request without credentials reaches remote content only through a
	// permission an administrator writes for anonymous.
	AnyRemotePermission = "Any Remote"
)

// seedVersion is the seed this code writes. Version 1 also granted anonymous
// read and deploy/cache (WRITE) through Any Remote, which UpgradeSeed takes
// away from a store seeded at version 1.
const seedVersion = 2

// seedMarker is the SettingSeeded value.
type seedMarker struct {
	Version int       `json:"version"`
	At      time.Time `json:"at"`
}

// readMarker returns the seeded marker, and whether there is one.
func readMarker(ctx context.Context, st store.Store) (seedMarker, bool, error) {
	var m seedMarker
	err := st.Settings().Get(ctx, types.SettingSeeded, &m)
	if errors.Is(err, store.ErrNotFound) {
		return m, false, nil
	}
	if err != nil {
		return m, false, fmt.Errorf("auth: seeded marker: %w", err)
	}
	return m, true, nil
}

// Seeded reports whether the built-in principals have been created.
func Seeded(ctx context.Context, st store.Store) (bool, error) {
	_, seeded, err := readMarker(ctx, st)
	return seeded, err
}

// UpgradeSeed brings a store that an earlier version seeded up to the
// current seed, and reports whether it removed anonymous from Any Remote. A
// store not yet seeded, or seeded by this version, is left alone.
//
// From version 1 it removes the anonymous grant only while Any Remote is
// exactly as version 1 seeded it. An administrator who edited the permission
// chose its grants, and one who deleted it does not get it back. Anything
// never granted anonymous, so it is not touched: an anonymous grant there
// was an administrator's.
func UpgradeSeed(ctx context.Context, st store.Store, now time.Time) (bool, error) {
	m, seeded, err := readMarker(ctx, st)
	if err != nil || !seeded || m.Version >= seedVersion {
		return false, err
	}
	var removed bool
	err = st.Tx(ctx, func(ctx context.Context) error {
		// Read again inside the transaction: another instance sharing the
		// database may have upgraded it since.
		m, _, err := readMarker(ctx, st)
		if err != nil || m.Version >= seedVersion {
			return err
		}
		p, err := st.Permissions().Get(ctx, AnyRemotePermission)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return fmt.Errorf("permission %s: %w", AnyRemotePermission, err)
		case seededV1(p):
			delete(p.Resources[types.ResourceArtifact].Actions.Users, AnonymousUser)
			p.Updated = now
			if err := st.Permissions().Update(ctx, p); err != nil {
				return fmt.Errorf("permission %s: %w", AnyRemotePermission, err)
			}
			removed = true
		}
		m.Version = seedVersion
		if err := st.Settings().Put(ctx, types.SettingSeeded, m); err != nil {
			return fmt.Errorf("seeded marker: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("auth: upgrade seed: %w", err)
	}
	return removed, nil
}

// seededV1 reports whether p is Any Remote as seed version 1 wrote it:
// anonymous holding read and deploy/cache on every path of every remote
// repository, and nothing else.
func seededV1(p types.Permission) bool {
	r, ok := p.Resources[types.ResourceArtifact]
	if !ok || p.Description != "" || len(p.Resources) != 1 || len(r.Targets) != 1 ||
		len(r.Actions.Groups) != 0 || len(r.Actions.Users) != 1 ||
		r.Actions.Users[AnonymousUser] != types.ActionRead|types.ActionWrite {
		return false
	}
	t, ok := r.Targets[types.TargetAnyRemote]
	return ok && slices.Equal(t.Include, []string{"**"}) && len(t.Exclude) == 0
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
				// No grants, as Artifactory ships it since 7.84.3: anonymous
				// here would let anyone fetch through every remote, and fill
				// its cache, once anonymous access is on.
				Actions: types.Grants{},
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
		Put(ctx, types.SettingSeeded, seedMarker{Version: seedVersion, At: now}); err != nil {
		return fmt.Errorf("seeded marker: %w", err)
	}
	return nil
}
