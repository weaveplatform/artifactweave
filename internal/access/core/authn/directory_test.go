package authn

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// fakeDirectory answers Login and Current as a test sets them.
type fakeDirectory struct {
	login      DirectoryLogin
	loginErr   error
	currentErr error
	logins     int
	// disabled answers Enabled false, as with no connection enabled;
	// enabledErr fails it.
	disabled   bool
	enabledErr error
	// beforePublish runs after Login answers, before publication: the
	// window in which a connection can change while the directory answered.
	beforePublish func()
	// published records PublishGroups' calls; onPublish runs in them, in
	// the publication's transaction, and its error is theirs.
	published []DirectoryGroups
	onPublish func(ctx context.Context, name string) error
}

func (d *fakeDirectory) PublishGroups(ctx context.Context, name string, groups DirectoryGroups) error {
	d.published = append(d.published, groups)
	if d.onPublish != nil {
		return d.onPublish(ctx, name)
	}
	return nil
}

func (d *fakeDirectory) Login(context.Context, string, string) (DirectoryLogin, error) {
	d.logins++
	if d.beforePublish != nil {
		d.beforePublish()
	}
	return d.login, d.loginErr
}

func (d *fakeDirectory) Current(_ context.Context, _ string, proof [32]byte) (DirectoryLogin, error) {
	if d.currentErr != nil || proof != d.login.Proof {
		return DirectoryLogin{}, errors.Join(d.currentErr, kernel.ErrBadCredentials)
	}
	out := d.login
	out.Email, out.Fresh = "", false
	return out, nil
}

func (d *fakeDirectory) Enabled(context.Context) (bool, error) {
	return !d.disabled, d.enabledErr
}

var errPublish = errors.New("publish failed")

type directoryWorld struct {
	db    *inmem.Store
	dir   *fakeDirectory
	a     *Authenticator
	clock *clock
}

func newDirectoryWorld(t *testing.T) directoryWorld {
	t.Helper()
	db := inmem.New()
	if err := db.Groups().Create(t.Context(), model.Group{Name: "everyone", AutoJoin: true}); err != nil {
		t.Fatal(err)
	}
	dir := &fakeDirectory{login: DirectoryLogin{
		Proof: [32]byte{1}, Email: "alice@example.org", AutoCreate: true, ProfileUpdatable: false, Fresh: true,
	}}
	a, c := newAuth(t, db, Options{
		Directory: dir, Tx: db, Guard: db.Projects().Guard, Groups: db.Groups(),
	})
	return directoryWorld{db: db, dir: dir, a: a, clock: c}
}

func (w directoryWorld) user(t *testing.T, name string) (model.User, bool) {
	t.Helper()
	u, err := w.db.Users().Get(t.Context(), name)
	if errors.Is(err, kernel.ErrNotFound) {
		return model.User{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return u, true
}

func TestUnitDirectoryNeedsPublication(t *testing.T) {
	db := inmem.New()
	if _, err := NewAuthenticator(db.Users(), db.Settings(), Options{Directory: &fakeDirectory{}}); !errors.Is(
		err, errNoPublication) {
		t.Fatalf("a directory without a transaction: %v", err)
	}
}

func TestUnitDirectoryAutoCreate(t *testing.T) {
	w := newDirectoryWorld(t)
	ctx := t.Context()
	p, err := w.a.Basic(ctx, "Alice", "directory-pw")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "alice" || !p.Password || p.PasswordRevision != w.dir.login.Proof || p.Identity != "" ||
		!slices.Equal(p.Groups, []string{"everyone"}) {
		t.Fatalf("principal %+v", p)
	}
	u, ok := w.user(t, "alice")
	if !ok || u.Realm != model.RealmLDAP || u.Email != "alice@example.org" || u.ProfileUpdatable ||
		len(u.PasswordHash) != 0 || u.Status != model.UserEnabled || !u.Created.Equal(w.clock.t) ||
		!u.LastLoggedIn.Equal(w.clock.t) || !slices.Equal(u.Groups, []string{"everyone"}) {
		t.Fatalf("stored user %+v", u)
	}
	// A later fresh login takes the directory's new email; a remembered one
	// writes nothing.
	w.clock.t = w.clock.t.Add(time.Hour)
	w.dir.login.Email = "alice@new.example.org"
	if _, err := w.a.Basic(ctx, "alice", "directory-pw"); err != nil {
		t.Fatal(err)
	}
	if u, _ := w.user(t, "alice"); u.Email != "alice@new.example.org" || !u.LastLoggedIn.Equal(w.clock.t) {
		t.Fatalf("after a fresh login %+v", u)
	}
	w.dir.login.Email, w.dir.login.Fresh = "ignored@example.org", false
	w.clock.t = w.clock.t.Add(time.Hour)
	if _, err := w.a.Basic(ctx, "alice", "directory-pw"); err != nil {
		t.Fatal(err)
	}
	if u, _ := w.user(t, "alice"); u.Email != "alice@new.example.org" || u.LastLoggedIn.Equal(w.clock.t) {
		t.Fatalf("a remembered login wrote %+v", u)
	}
	// The recheck of work that outlives the request, and its download
	// reference, ask the directory again.
	fresh, err := w.a.Current(ctx, p, w.db.Tokens())
	if err != nil || fresh.PasswordRevision != p.PasswordRevision || fresh.Identity != "" {
		t.Fatalf("Current %+v, %v", fresh, err)
	}
	ref, err := p.DownloadReference()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.a.RestoreDownload(ctx, p.Name, ref, w.db.Tokens()); err != nil {
		t.Fatalf("RestoreDownload: %v", err)
	}
	w.dir.currentErr = errPublish
	if _, err := w.a.Current(ctx, p, w.db.Tokens()); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("Current of a changed connection: %v", err)
	}
	w.dir.currentErr = nil
	if err := w.db.Settings().Put(ctx, settings.SettingBasicAuthEnabled, false); err != nil {
		t.Fatal(err)
	}
	// The switch turns internal passwords off, not a directory's: the
	// directory login and its recheck go on.
	w.a.ForgetPolicies()
	if _, err := w.a.Current(ctx, p, w.db.Tokens()); err != nil {
		t.Fatalf("Current with internal password login off: %v", err)
	}
	if again, err := w.a.Basic(ctx, "alice", "directory-pw"); err != nil || again.PasswordRevision != p.PasswordRevision {
		t.Fatalf("a directory login with internal password login off: %+v %v", again, err)
	}
	if err := w.db.Settings().Delete(ctx, settings.SettingBasicAuthEnabled); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Users().Delete(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.a.Current(ctx, p, w.db.Tokens()); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("Current of a deleted user the connection stores: %v", err)
	}
}

func TestUnitDirectoryTransient(t *testing.T) {
	w := newDirectoryWorld(t)
	ctx := t.Context()
	w.dir.login.AutoCreate = false
	p, err := w.a.Basic(ctx, "bob", "directory-pw")
	if err != nil {
		t.Fatal(err)
	}
	if p.Identity != kernel.IdentityTransient || !slices.Equal(p.Groups, []string{"everyone"}) || !p.Password {
		t.Fatalf("transient principal %+v", p)
	}
	if _, ok := w.user(t, "bob"); ok {
		t.Fatal("a transient user was stored")
	}
	fresh, err := w.a.Current(ctx, p, w.db.Tokens())
	if err != nil || fresh.Identity != kernel.IdentityTransient || !slices.Equal(fresh.Groups, []string{"everyone"}) {
		t.Fatalf("Current %+v, %v", fresh, err)
	}
	// A registry token obtained with the login stays bound to its proof.
	reg := kernel.Principal{
		Name: "bob", IsTokenAuthenticated: true, Registry: &kernel.RegistryAccess{}, TokenID: "t",
		TokenExpires: w.clock.t.Add(time.Hour), TokenSubject: "jfrt@01/users/bob",
		Identity: kernel.IdentityTransient, PasswordRevision: p.PasswordRevision,
	}
	if _, err := w.a.CurrentRegistry(ctx, reg, w.db.Tokens()); err != nil {
		t.Fatalf("CurrentRegistry: %v", err)
	}
	w.dir.currentErr = errPublish
	if _, err := w.a.CurrentRegistry(ctx, reg, w.db.Tokens()); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("CurrentRegistry of a changed connection: %v", err)
	}
}

func TestUnitDirectoryAndInternalPasswords(t *testing.T) {
	w := newDirectoryWorld(t)
	ctx := t.Context()
	if err := w.db.Settings().Put(ctx, settings.SettingUserLockPolicy, model.UserLockPolicy{
		Enabled: true, LoginAttempts: 3,
	}); err != nil {
		t.Fatal(err)
	}
	addUser(t, w.db, model.User{Name: "admin", Admin: true, Realm: model.RealmInternal}, "local-pw")
	// The internal password never asks the directory, so a directory that
	// is down cannot delay the local administrator.
	w.dir.loginErr = errPublish
	p, err := w.a.Basic(ctx, "admin", "local-pw")
	if err != nil || !p.Admin || w.dir.logins != 0 {
		t.Fatalf("local admin %+v, %v, directory asked %d times", p, err, w.dir.logins)
	}
	// A wrong internal password while the directory is down: refused,
	// and counted nowhere.
	if _, err := w.a.Basic(ctx, "admin", "directory-pw"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("outage: %v", err)
	}
	if u, _ := w.user(t, "admin"); u.FailedLogins != 0 {
		t.Fatalf("an outage counted %d failed logins", u.FailedLogins)
	}
	// The directory's password for an internal user is accepted as that
	// user, without a failed login.
	w.dir.loginErr = nil
	p, err = w.a.Basic(ctx, "admin", "directory-pw")
	if err != nil || !p.Admin || p.PasswordRevision != w.dir.login.Proof {
		t.Fatalf("directory login of an internal user %+v, %v", p, err)
	}
	if u, _ := w.user(t, "admin"); u.Realm != model.RealmInternal || u.FailedLogins != 0 {
		t.Fatalf("the internal user changed %+v", u)
	}
	fresh, err := w.a.Current(ctx, p, w.db.Tokens())
	if err != nil || !fresh.Admin {
		t.Fatalf("Current of an internal user's directory login %+v, %v", fresh, err)
	}
	// Both refuse: one failed login.
	w.dir.loginErr = kernel.ErrBadCredentials
	if _, err := w.a.Basic(ctx, "admin", "wrong"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatal(err)
	}
	if u, _ := w.user(t, "admin"); u.FailedLogins != 1 {
		t.Fatalf("failed logins %d, want 1", u.FailedLogins)
	}
}

func TestUnitDirectoryRefusals(t *testing.T) {
	ctx := t.Context()
	t.Run("no directory enabled", func(t *testing.T) {
		w := newDirectoryWorld(t)
		w.dir.loginErr = ErrNoDirectory
		if _, err := w.a.Basic(ctx, "nobody", "pw"); !errors.Is(err, kernel.ErrBadCredentials) {
			t.Fatal(err)
		}
	})
	t.Run("directory refused a user without an internal password", func(t *testing.T) {
		w := newDirectoryWorld(t)
		addUser(t, w.db, model.User{Name: "carol", Realm: model.RealmLDAP}, "")
		if err := w.db.Settings().Put(ctx, settings.SettingUserLockPolicy, model.UserLockPolicy{
			Enabled: true, LoginAttempts: 3,
		}); err != nil {
			t.Fatal(err)
		}
		w.dir.loginErr = kernel.ErrBadCredentials
		if _, err := w.a.Basic(ctx, "carol", "pw"); !errors.Is(err, kernel.ErrBadCredentials) {
			t.Fatal(err)
		}
		if u, _ := w.user(t, "carol"); u.FailedLogins != 1 {
			t.Fatalf("failed logins %d", u.FailedLogins)
		}
	})
	t.Run("unavailable for an unknown name", func(t *testing.T) {
		w := newDirectoryWorld(t)
		w.dir.loginErr = errPublish
		if _, err := w.a.Basic(ctx, "nobody", "pw"); !errors.Is(err, kernel.ErrBadCredentials) {
			t.Fatal(err)
		}
	})
	for _, status := range []model.UserStatus{model.UserDisabled, model.UserLocked} {
		t.Run(string(status)+" local user", func(t *testing.T) {
			w := newDirectoryWorld(t)
			addUser(t, w.db, model.User{Name: "alice", Realm: model.RealmLDAP, Status: status}, "")
			if _, err := w.a.Basic(ctx, "alice", "pw"); err == nil || w.dir.logins != 0 {
				t.Fatalf("a %s user: %v, directory asked %d times", status, err, w.dir.logins)
			}
		})
	}
	for _, status := range []model.UserStatus{model.UserDisabled, model.UserLocked} {
		t.Run(string(status)+" while the directory answered", func(t *testing.T) {
			w := newDirectoryWorld(t)
			addUser(t, w.db, model.User{Name: "alice", Realm: model.RealmLDAP}, "")
			w.dir.beforePublish = func() {
				u, _ := w.user(t, "alice")
				u.Status = status
				if err := w.db.Users().Update(ctx, u); err != nil {
					t.Error(err)
				}
			}
			if _, err := w.a.Basic(ctx, "alice", "pw"); err == nil {
				t.Fatalf("a user %s during the login was accepted", status)
			}
		})
	}
	t.Run("connection changed while the directory answered", func(t *testing.T) {
		w := newDirectoryWorld(t)
		w.dir.beforePublish = func() { w.dir.currentErr = errPublish }
		if _, err := w.a.Basic(ctx, "alice", "pw"); !errors.Is(err, kernel.ErrBadCredentials) {
			t.Fatal(err)
		}
		if _, ok := w.user(t, "alice"); ok {
			t.Fatal("a stale login created its user")
		}
	})
	t.Run("publication fails", func(t *testing.T) {
		w := newDirectoryWorld(t)
		w.a.guard = func(context.Context) error { return errPublish }
		if _, err := w.a.Basic(ctx, "alice", "pw"); !errors.Is(err, errPublish) {
			t.Fatal(err)
		}
		if _, ok := w.user(t, "alice"); ok {
			t.Fatal("a failed publication created its user")
		}
	})
	t.Run("user already taken", func(t *testing.T) {
		w := newDirectoryWorld(t)
		w.dir.beforePublish = func() { addUser(t, w.db, model.User{Name: "alice", Realm: model.RealmLDAP}, "") }
		w.dir.login.Fresh = false
		if _, err := w.a.Basic(ctx, "alice", "pw"); err != nil {
			t.Fatalf("a user created meanwhile: %v", err)
		}
	})
}

// publishFailures fails the named user store write, or the second Get (the
// one publication makes), as a database that went away would.
type publishFailures struct {
	port.UserStore
	op   string
	gets int
}

func (f *publishFailures) Get(ctx context.Context, name string) (model.User, error) {
	f.gets++
	if f.op == "get" && f.gets > 1 {
		return model.User{}, errPublish
	}
	return f.UserStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func (f *publishFailures) Create(ctx context.Context, u model.User) error {
	if f.op == "create" {
		return errPublish
	}
	return f.UserStore.Create(ctx, u) //nolint:wrapcheck // test double
}

func (f *publishFailures) Update(ctx context.Context, u model.User) error {
	if f.op == "update" {
		return errPublish
	}
	return f.UserStore.Update(ctx, u) //nolint:wrapcheck // test double
}

func (f *publishFailures) RecordLogin(ctx context.Context, name string, at time.Time) error {
	if f.op == "login" {
		return errPublish
	}
	return f.UserStore.RecordLogin(ctx, name, at) //nolint:wrapcheck // test double
}

// failingGroups fails AutoJoin.
type failingGroups struct{ port.GroupStore }

func (failingGroups) AutoJoin(context.Context) ([]string, error) { return nil, errPublish }

func TestUnitDirectoryPublicationFailures(t *testing.T) {
	ctx := t.Context()
	for _, op := range []string{"get", "create", "update", "login", "groups"} {
		t.Run(op, func(t *testing.T) {
			db := inmem.New()
			users := &publishFailures{UserStore: db.Users(), op: op}
			dir := &fakeDirectory{login: DirectoryLogin{
				Proof: [32]byte{2}, Email: "new@example.org", AutoCreate: true, Fresh: true,
			}}
			o := Options{Directory: dir, Tx: db, Guard: db.Projects().Guard, Groups: db.Groups()}
			if op == "groups" {
				o.Groups = failingGroups{db.Groups()}
			}
			name := "alice"
			if op == "update" || op == "login" {
				addUser(t, db, model.User{Name: "existing", Realm: model.RealmLDAP, Email: "old@example.org"}, "")
				name = "existing"
			}
			a, err := NewAuthenticator(users, db.Settings(), o)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Basic(ctx, name, "pw"); !errors.Is(err, errPublish) {
				t.Fatalf("Basic = %v, want the store's failure", err)
			}
			u, err := db.Users().Get(ctx, name)
			switch {
			case name == "alice" && !errors.Is(err, kernel.ErrNotFound):
				t.Fatalf("a failed publication stored %+v", u)
			case name == "existing" && u.Email != "old@example.org":
				t.Fatalf("a failed publication changed %+v", u)
			}
		})
	}
	t.Run("current store failure", func(t *testing.T) {
		db := inmem.New()
		a, err := NewAuthenticator(failingUsers{db.Users()}, db.Settings(), Options{
			Directory: &fakeDirectory{}, Tx: db, Guard: db.Projects().Guard,
		})
		if err != nil {
			t.Fatal(err)
		}
		p := kernel.Principal{Name: "alice", Password: true}
		if _, err := a.Current(ctx, p, db.Tokens()); err == nil || errors.Is(err, kernel.ErrBadCredentials) {
			t.Fatalf("Current with a failing store: %v", err)
		}
	})
	t.Run("current transient groups failure", func(t *testing.T) {
		db := inmem.New()
		dir := &fakeDirectory{login: DirectoryLogin{Proof: [32]byte{3}}}
		a, err := NewAuthenticator(db.Users(), db.Settings(), Options{
			Directory: dir, Tx: db, Guard: db.Projects().Guard, Groups: failingGroups{db.Groups()},
		})
		if err != nil {
			t.Fatal(err)
		}
		p := kernel.Principal{Name: "bob", Password: true, PasswordRevision: [32]byte{3}}
		if _, err := a.Current(ctx, p, db.Tokens()); !errors.Is(err, errPublish) {
			t.Fatalf("Current of a transient user without groups: %v", err)
		}
	})
}

// A fresh login publishes the groups it read inside its publication, and
// the principal it answers already acts through them; a remembered login,
// a transient user and a failed publication publish nothing.
func TestUnitDirectoryPublishesGroups(t *testing.T) {
	w := newDirectoryWorld(t)
	ctx := t.Context()
	if err := w.db.Groups().Create(ctx, model.Group{Name: "devs", Realm: model.RealmLDAP}); err != nil {
		t.Fatal(err)
	}
	w.dir.login.Groups = &DirectoryGroups{Names: []string{"devs"}, Revision: [32]byte{7}}
	w.dir.onPublish = func(ctx context.Context, name string) error {
		return w.db.Groups().AddMembers(ctx, "devs", name)
	}
	p, err := w.a.Basic(ctx, "alice", "directory-pw")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Groups, []string{"devs", "everyone"}) || len(w.dir.published) != 1 ||
		w.dir.published[0].Revision != [32]byte{7} {
		t.Fatalf("principal %+v, published %+v", p, w.dir.published)
	}
	// A failed publication refuses the login and stores nothing of it.
	w.dir.onPublish = func(context.Context, string) error { return errPublish }
	if _, err := w.a.Basic(ctx, "carol", "directory-pw"); !errors.Is(err, errPublish) {
		t.Fatalf("failed publication: %v", err)
	}
	if _, ok := w.user(t, "carol"); ok {
		t.Fatal("a failed publication stored its user")
	}
	// Remembered logins and transient users publish nothing.
	w.dir.onPublish = nil
	w.dir.login.Groups = nil
	if _, err := w.a.Basic(ctx, "alice", "directory-pw"); err != nil {
		t.Fatal(err)
	}
	w.dir.login.Groups, w.dir.login.AutoCreate = &DirectoryGroups{Names: []string{"devs"}}, false
	if _, err := w.a.Basic(ctx, "dave", "directory-pw"); err != nil {
		t.Fatal(err)
	}
	if len(w.dir.published) != 2 {
		t.Fatalf("published %d times, want 2 (alice, then carol's failure)", len(w.dir.published))
	}
}
