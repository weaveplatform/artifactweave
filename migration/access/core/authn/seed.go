package authn

import (
	"context"
	"errors"
	"fmt"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	artifactsapi "github.com/weaveplatform/artifactweave/migration/artifacts/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/pkg/store"
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

// seedMarker is the SettingSeeded value.
type seedMarker struct {
	Version int       `json:"version"`
	At      time.Time `json:"at"`
}

// Seeded reports whether the built-in principals have been created.
func Seeded(ctx context.Context, st store.Store) (bool, error) {
	var m seedMarker
	err := st.Settings().Get(ctx, settings.SettingSeeded, &m)
	if errors.Is(err, kernel.ErrNotFound) {
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
// race to seed it first. The repository belongs to the artifacts module,
// so the seed creates it through repos, which must work over st: Create
// gets the transaction's context and joins the transaction as the other
// creates do.
func Seed(
	ctx context.Context,
	st store.Store,
	repos artifactsapi.Repositories,
	adminPassword string,
	now time.Time,
) (bool, error) {
	if seeded, err := Seeded(ctx, st); err != nil || seeded {
		return false, err
	}
	hash, err := HashPassword(adminPassword)
	if err != nil {
		return false, err
	}
	return SeedHashed(ctx, st, repos, hash, now)
}

// SeedHashed is Seed with the admin password already hashed (HashPassword or
// HashPasswordWith).
func SeedHashed(
	ctx context.Context,
	st store.Store,
	repos artifactsapi.Repositories,
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
		return seed(ctx, st, repos, adminHash, now)
	})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errAlreadySeeded):
		return false, nil
	case errors.Is(err, kernel.ErrConflict):
		// Two instances seeding one database at once: the loser's creates
		// conflict. It is fine if the winner's marker is now there.
		if seeded, serr := Seeded(ctx, st); serr == nil && seeded {
			return false, nil
		}
	}
	return false, fmt.Errorf("auth: seed: %w", err)
}

var errAlreadySeeded = errors.New("auth: already seeded")

func seed(
	ctx context.Context,
	st store.Store,
	repos artifactsapi.Repositories,
	adminHash []byte,
	now time.Time,
) error {
	if err := st.Groups().Create(ctx, model.Group{
		Name: ReadersGroup, Description: "A group for read-only users",
		AutoJoin: true, Realm: "internal",
	}); err != nil {
		return fmt.Errorf("group %s: %w", ReadersGroup, err)
	}
	for _, u := range []model.User{
		{
			Name: AdminUser, PasswordHash: adminHash, Admin: true, ProfileUpdatable: true,
			Realm: "internal", Status: model.UserEnabled, Created: now, PasswordChanged: now,
		},
		// No password: anonymous never logs in, it is what a request without
		// credentials acts as when anonymous access is on.
		{Name: kernel.AnonymousUser, Realm: "internal", Status: model.UserEnabled, Created: now},
	} {
		if err := st.Users().Create(ctx, u); err != nil {
			return fmt.Errorf("user %s: %w", u.Name, err)
		}
	}
	everything := []string{"**"}
	for _, p := range []model.Permission{
		{
			Name: AnythingPermission,
			Resources: map[accessapi.ResourceType]model.Resource{accessapi.ResourceArtifact: {
				Actions: model.Grants{Groups: map[string]accessapi.Actions{ReadersGroup: accessapi.ActionRead}},
				Targets: map[string]model.Target{accessapi.TargetAny: {Include: everything, Exclude: []string{}}},
			}},
			Created: now, Updated: now,
		},
		{
			Name: AnyRemotePermission,
			Resources: map[accessapi.ResourceType]model.Resource{accessapi.ResourceArtifact: {
				// No grants, as Artifactory ships it since 7.84.3: anonymous
				// here would let anyone fetch through every remote, and fill
				// its cache, once anonymous access is on.
				Actions: model.Grants{},
				Targets: map[string]model.Target{
					accessapi.TargetAnyRemote: {Include: everything, Exclude: []string{}},
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
		Key: accessapi.BuildInfoRepository, Class: repo.Local, PackageType: repo.BuildInfo,
		Description: "Build Info repository", RepoLayoutRef: repo.LayoutSimple,
	}
	if err := repos.Create(ctx, *repo.Defaults(&buildInfo)); err != nil {
		return fmt.Errorf("repository %s: %w", accessapi.BuildInfoRepository, err)
	}
	if err := st.Settings().
		Put(ctx, settings.SettingSeeded, seedMarker{Version: 1, At: now}); err != nil {
		return fmt.Errorf("seeded marker: %w", err)
	}
	return nil
}
