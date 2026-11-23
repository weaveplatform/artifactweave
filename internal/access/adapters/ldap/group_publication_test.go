package ldap_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessldap "github.com/weaveplatform/artifactweave/internal/access/adapters/ldap"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	coreldap "github.com/weaveplatform/artifactweave/internal/access/core/ldap"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// groupRaced is the directory client, which runs during once, inside the
// first group read: the window in which an administrator can change the
// settings a refresh or a login read its groups through.
type groupRaced struct {
	accessldap.Client
	once   *sync.Once
	during func()
}

func (r groupRaced) Groups(ctx context.Context, c coreldap.Connection, g api.LDAPGroupSetting) ([]coreldap.DirectoryGroup, error) {
	r.once.Do(r.during)
	return r.Client.Groups(ctx, c, g) //nolint:wrapcheck // the client's answer, unchanged
}

func (r groupRaced) UserGroups(
	ctx context.Context,
	c coreldap.Connection,
	g api.LDAPGroupSetting,
	name, dn string,
) ([]coreldap.DirectoryGroup, error) {
	r.once.Do(r.during)
	return r.Client.UserGroups(ctx, c, g, name, dn) //nolint:wrapcheck // the client's answer, unchanged
}

var errJoin = errors.New("join failure")

// joinFailing fails every membership it is asked to add, after the groups a
// refresh imports were written in the same transaction.
type joinFailing struct{ port.GroupStore }

func (joinFailing) AddMembers(context.Context, string, ...string) error { return errJoin }

// groupWorld is one SQL store with the corp connection, the teams setting
// over the seeded ou=groups, and Alice stored in the ldap realm with one
// LDAP membership the directory no longer gives her.
type groupWorld struct {
	db  *appstoretest.SQLStore
	svc coreldap.Service
}

func newGroupWorld(t *testing.T, d directory, engine, file string) groupWorld {
	t.Helper()
	ctx := t.Context()
	var db *appstoretest.SQLStore
	var err error
	if engine == "sqlite" {
		db, err = appstoretest.OpenSQLite(ctx, file, appstoretest.SQLiteOptions{})
	} else {
		db, err = appstoretest.OpenPostgres(ctx, sqltest.ScratchDSN(t, sqltest.PostgresDSN(t)), appstoretest.PostgresOptions{})
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	svc := coreldap.Service{Configs: db.Configs(), Tx: db, Guard: db.Projects().Guard}
	url, filter, base := d.ep.URL(d.ep.BaseDN), "(uid={0})", "ou=people|ou=contractors"
	if _, err := svc.Create(ctx, api.LDAPChange{
		Key: "corp", URL: &url, SearchFilter: &filter, SearchBase: &base,
		ManagerDN: &d.ep.ReaderDN, ManagerPassword: &d.ep.ReaderPassword,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateGroupSetting(ctx, teams()); err != nil {
		t.Fatal(err)
	}
	if err := db.Groups().Create(ctx, model.Group{Name: "old", Realm: model.RealmLDAP}); err != nil {
		t.Fatal(err)
	}
	if err := db.Groups().Create(ctx, model.Group{Name: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Users().Create(ctx, model.User{Name: "alice", Realm: model.RealmLDAP, Status: model.UserEnabled}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"old", "local"} {
		if err := db.Groups().AddMembers(ctx, g, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	return groupWorld{db: db, svc: svc}
}

func teams() api.LDAPGroupChange {
	enabled, base, member, filter := "corp", "ou=groups", "uniqueMember", "(objectClass=groupOfUniqueNames)"
	return api.LDAPGroupChange{
		Name: "teams", EnabledLDAP: &enabled, GroupBaseDN: &base, GroupMemberAttribute: &member, Filter: &filter,
	}
}

func (w groupWorld) sync(dir coreldap.Directory, groups port.GroupStore) coreldap.Sync {
	return coreldap.Sync{
		Settings: w.svc, Directory: dir, Users: w.db.Users(), Groups: groups,
		Tx: func(ctx context.Context, fn func(context.Context) error) error {
			return w.db.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // fn's errors, unchanged
				if err := w.db.Projects().Guard(ctx); err != nil {
					return err //nolint:wrapcheck // the guard's error, unchanged
				}
				return fn(ctx)
			})
		},
	}
}

func (w groupWorld) aliceGroups(t *testing.T) []string {
	t.Helper()
	u, err := w.db.Users().Get(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	out := slices.Clone(u.Groups)
	slices.Sort(out)
	return out
}

// TestAccLDAPGroupPublication proves on SQLite and PostgreSQL, against the
// OpenLDAP container, that a refresh and a login publish the groups and
// memberships they read only as a whole and only while the settings they
// read them through still hold: a setting deleted or changed while the
// directory answered, or a store that fails part-way, leaves every group and
// membership as it was; a refresh racing nothing publishes all of them, and
// a membership given locally stays. Nothing stored or answered carries the
// manager password.
func TestAccLDAPGroupPublication(t *testing.T) {
	d := newDirectory(t)
	accesstest.SeedDirectoryGroups(t, d.ep)
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				during func(t *testing.T, w groupWorld)
				fail   bool
				want   func(error) bool
			}{
				{"setting deleted", func(t *testing.T, w groupWorld) {
					if err := w.svc.DeleteGroupSetting(t.Context(), "teams"); err != nil {
						t.Error(err)
					}
				}, false, func(err error) bool { return errors.Is(err, coreldap.ErrGroupSettingNotFound) }},
				{"setting changed", func(t *testing.T, w groupWorld) {
					ch := teams()
					sub := true
					ch.SubTree = &sub
					if _, err := w.svc.ReplaceGroupSetting(t.Context(), ch); err != nil {
						t.Error(err)
					}
				}, false, func(err error) bool { return errors.Is(err, coreldap.ErrChanged) }},
				{
					"store failure", func(*testing.T, groupWorld) {}, true,
					func(err error) bool { return errors.Is(err, errJoin) },
				},
				{"unchanged", func(*testing.T, groupWorld) {}, false, func(err error) bool { return err == nil }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx := t.Context()
					file := filepath.Join(t.TempDir(), "ldap.db")
					w := newGroupWorld(t, d, engine, file)
					dir := groupRaced{Client: d.client, once: &sync.Once{}, during: func() { tc.during(t, w) }}
					var groups port.GroupStore = w.db.Groups()
					if tc.fail {
						groups = joinFailing{GroupStore: groups}
					}
					sum, err := w.sync(dir, groups).Refresh(ctx, "teams", coreldap.OpUpdateAndImport, "")
					if !tc.want(err) {
						t.Fatalf("refresh %+v, %v", sum, err)
					}
					if err != nil && bytes.Contains([]byte(err.Error()), []byte(d.ep.ReaderPassword)) {
						t.Fatal("an error carried the manager password")
					}
					_, imported := w.db.Groups().Get(ctx, "developers")
					got := w.aliceGroups(t)
					if tc.want(nil) {
						if imported != nil || !slices.Equal(got, []string{"cycle-a", "developers", "local"}) {
							t.Fatalf("published %v (%v)", got, imported)
						}
					} else if imported == nil || !slices.Equal(got, []string{"local", "old"}) {
						t.Fatalf("a refused refresh published %v (developers imported: %v)", got, imported == nil)
					}
					if engine == "sqlite" {
						data, err := os.ReadFile(file)
						if err != nil {
							t.Fatal(err)
						}
						if bytes.Contains(data, []byte(d.ep.ReaderPassword)) {
							t.Fatal("the store holds the manager password in clear")
						}
					}
				})
			}
			t.Run("login", func(t *testing.T) {
				ctx := t.Context()
				w := newGroupWorld(t, d, engine, filepath.Join(t.TempDir(), "ldap.db"))
				// Import the groups, then let a login read Alice's groups while
				// the setting changes: the login stands, its groups do not.
				if _, err := w.sync(d.client, w.db.Groups()).Refresh(ctx, "teams", coreldap.OpImport, ""); err != nil {
					t.Fatal(err)
				}
				for _, race := range []bool{true, false} {
					dir := groupRaced{Client: d.client, once: &sync.Once{}, during: func() {
						if race {
							if _, err := w.svc.ReplaceGroupSetting(ctx, teams()); err != nil {
								t.Error(err)
							}
						}
					}}
					a, err := authn.NewAuthenticator(w.db.Users(), w.db.Settings(), authn.Options{
						Groups: w.db.Groups(), Tx: w.db, Guard: w.db.Projects().Guard,
						Directory: coreldap.NewLogins(w.svc, dir, coreldap.LoginOptions{
							Users: w.db.Users(), Groups: w.db.Groups(),
						}),
					})
					if err != nil {
						t.Fatal(err)
					}
					p, err := a.Basic(ctx, "alice", accesstest.Alice.Password)
					if err != nil {
						t.Fatal(err)
					}
					want := []string{"cycle-a", "developers", "local"}
					if race {
						want = []string{"local", "old"}
					}
					if got := w.aliceGroups(t); !slices.Equal(got, want) {
						t.Fatalf("race %v: stored %v, want %v", race, got, want)
					}
					if pg := slices.Sorted(slices.Values(p.Groups)); !slices.Equal(pg, want) {
						t.Fatalf("race %v: the principal acts through %v, want %v", race, pg, want)
					}
				}
			})
		})
	}
}
