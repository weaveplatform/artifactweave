package ldap_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessldap "github.com/weaveplatform/artifactweave/internal/access/adapters/ldap"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	coreldap "github.com/weaveplatform/artifactweave/internal/access/core/ldap"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// raced is the directory client, which runs after once the directory has
// accepted a password and before the login is published: the window in
// which an administrator can change the connection that accepted it.
type raced struct {
	accessldap.Client
	after func()
}

func (r raced) Authenticate(
	ctx context.Context,
	c coreldap.Connection,
	name, password string,
) (coreldap.Entry, error) {
	e, err := r.Client.Authenticate(ctx, c, name, password)
	r.after()
	return e, err //nolint:wrapcheck // the client's answer, unchanged
}

func openSQLStore(t *testing.T, engine string) *appstoretest.SQLStore {
	t.Helper()
	var db *appstoretest.SQLStore
	var err error
	if engine == "sqlite" {
		db, err = appstoretest.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "ldap.db"), appstoretest.SQLiteOptions{})
	} else {
		db, err = appstoretest.OpenPostgres(t.Context(), sqltest.ScratchDSN(t, sqltest.PostgresDSN(t)),
			appstoretest.PostgresOptions{})
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

// TestAccLDAPLoginPublication proves on SQLite and PostgreSQL, against the
// OpenLDAP container, that a directory login publishes its user only while
// the connection that accepted it is enabled and unchanged: one disabled,
// changed or deleted while the directory answered stores no user and
// returns no principal, and a login racing nothing stores its user.
func TestAccLDAPLoginPublication(t *testing.T) {
	d := newDirectory(t)
	ep := d.ep
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := openSQLStore(t, engine)
			ctx := t.Context()
			if err := db.Groups().Create(ctx, model.Group{Name: "directory-users", AutoJoin: true}); err != nil {
				t.Fatal(err)
			}
			svc := coreldap.Service{Configs: db.Configs(), Tx: db, Guard: db.Projects().Guard}
			url, filter, base := ep.URL(ep.BaseDN), "(uid={0})", "ou=people|ou=contractors"
			off := false
			for _, tc := range []struct {
				name   string
				change func()
				user   accesstest.DirectoryUser
				want   error
			}{
				{"disabled", func() {
					if _, err := svc.Apply(ctx, []api.LDAPChange{{Key: "corp", Enabled: &off}}); err != nil {
						t.Error(err)
					}
				}, accesstest.Alice, kernel.ErrBadCredentials},
				{"changed", func() {
					if _, err := svc.Replace(ctx, api.LDAPChange{
						Key: "corp", URL: &url, SearchFilter: &filter, SearchBase: &base, ManagerDN: &ep.ReaderDN,
					}); err != nil {
						t.Error(err)
					}
				}, accesstest.Bob, kernel.ErrBadCredentials},
				{"deleted", func() {
					if err := svc.Delete(ctx, "corp"); err != nil {
						t.Error(err)
					}
				}, accesstest.Dave, kernel.ErrBadCredentials},
				{"unchanged", func() {}, accesstest.Erin, nil},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if err := svc.Delete(ctx, "corp"); err != nil && !errors.Is(err, coreldap.ErrNotFound) {
						t.Fatal(err)
					}
					if _, err := svc.Create(ctx, api.LDAPChange{
						Key: "corp", URL: &url, SearchFilter: &filter, SearchBase: &base,
						ManagerDN: &ep.ReaderDN, ManagerPassword: &ep.ReaderPassword,
					}); err != nil {
						t.Fatal(err)
					}
					dir := raced{Client: d.client, after: tc.change}
					a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{
						Groups:    db.Groups(),
						Directory: coreldap.NewLogins(svc, dir, coreldap.LoginOptions{}),
						Tx:        db, Guard: db.Projects().Guard,
					})
					if err != nil {
						t.Fatal(err)
					}
					p, err := a.Basic(ctx, tc.user.UID, tc.user.Password)
					if !errors.Is(err, tc.want) || (err != nil) != (p.Name == "") {
						t.Fatalf("Basic = %+v, %v; want %v", p, err, tc.want)
					}
					u, err := db.Users().Get(ctx, strings.ToLower(tc.user.UID))
					switch {
					case tc.want != nil && !errors.Is(err, kernel.ErrNotFound):
						t.Fatalf("a stale login stored %+v (%v)", u, err)
					case tc.want == nil && (err != nil || u.Realm != model.RealmLDAP || u.Email != tc.user.Email):
						t.Fatalf("the login stored %+v (%v)", u, err)
					}
				})
			}
		})
	}
}
