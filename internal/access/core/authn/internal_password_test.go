package authn

import (
	"context"
	"errors"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// TestUnitInternalPasswordSwitchSparesDirectories: with internal password
// login off, an internal password verifies nothing, while an enabled
// directory still accepts its own users' passwords, a local user's included;
// with no directory enabled every password gets the switch's own refusal,
// the answer it gave before LDAP existed.
func TestUnitInternalPasswordSwitchSparesDirectories(t *testing.T) {
	w := newDirectoryWorld(t)
	ctx := t.Context()
	addUser(t, w.db, model.User{Name: "admin", Admin: true}, "pw")
	local, err := w.a.Basic(ctx, "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Settings().Put(ctx, settings.SettingBasicAuthEnabled, false); err != nil {
		t.Fatal(err)
	}
	w.a.ForgetPolicies()
	// The internal password is not checked: the directory refuses it.
	w.dir.loginErr = kernel.ErrBadCredentials
	if _, err := w.a.Basic(ctx, "admin", "pw"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("an internal password with the switch off: %v", err)
	}
	// Work that outlives the request ends with the switch.
	if _, err := w.a.Current(ctx, local, w.db.Tokens()); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("Current of an internal password login: %v", err)
	}
	w.dir.loginErr = nil
	if p, err := w.a.Basic(ctx, "alice", "directory-pw"); err != nil || p.Name != "alice" {
		t.Fatalf("a directory login: %+v %v", p, err)
	}
	// A local user the directory also knows logs in through it.
	if p, err := w.a.Basic(ctx, "admin", "directory-pw"); err != nil || p.PasswordRevision != w.dir.login.Proof {
		t.Fatalf("a local user's directory login: %+v %v", p, err)
	}
	// Without any directory the switch's own refusal answers too.
	bare, _ := newAuth(t, w.db, Options{})
	if _, err := bare.Basic(ctx, "admin", "pw"); !errors.Is(err, ErrBasicAuthDisabled) {
		t.Errorf("no directory: %v", err)
	}
	// A switch that cannot be read fails the recheck with the store's error.
	broken, err := NewAuthenticator(w.db.Users(), failingSettings{w.db.Settings()}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.Current(ctx, local, w.db.Tokens()); err == nil || errors.Is(err, kernel.ErrBadCredentials) {
		t.Errorf("Current with an unreadable switch: %v", err)
	}
	for name, dir := range map[string]*fakeDirectory{
		"none enabled":    {disabled: true},
		"unreadable list": {enabledErr: errPublish},
	} {
		a, _ := newAuth(t, w.db, Options{Directory: dir, Tx: w.db, Guard: w.db.Projects().Guard})
		if _, err := a.Basic(ctx, "admin", "pw"); !errors.Is(err, ErrBasicAuthDisabled) {
			t.Errorf("%s: %v", name, err)
		}
		if dir.logins != 0 {
			t.Errorf("%s: the directory was asked", name)
		}
	}
}

// TestUnitPasswordEncryptionPolicy: required refuses a clear-text password
// outside a UI request before the user is read, unsupported refuses the
// encrypted password, and supported, the default, takes both.
func TestUnitPasswordEncryptionPolicy(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	a, _ := newAuth(t, db, Options{})
	addUser(t, db, model.User{Name: "dev"}, "pw")
	enc := model.Token{
		ID: "enc", Subject: "jfac@x/users/dev", Scope: kernel.UserScope(), Purpose: model.TokenPurposeEncryptedPassword,
	}
	a.tokens = fakeVerifier{"enc": enc}
	if err := db.Tokens().Create(ctx, enc); err != nil {
		t.Fatal(err)
	}
	if got, err := PasswordEncryption(ctx, db.Settings()); err != nil || got != accessapi.EncryptionSupported {
		t.Fatal("the default", got, err)
	}
	set := func(p accessapi.PasswordEncryption) {
		t.Helper()
		if err := db.Settings().Put(ctx, settings.SettingPasswordEncryption, p); err != nil {
			t.Fatal(err)
		}
		a.ForgetPolicies()
	}
	set(accessapi.EncryptionRequired)
	if _, err := a.Basic(ctx, "dev", "pw"); !errors.Is(err, ErrClearTextPassword) {
		t.Fatalf("a clear-text password while required: %v", err)
	}
	if _, err := a.Basic(ctx, "dev", "wrong"); !errors.Is(err, ErrClearTextPassword) {
		t.Fatalf("a wrong clear-text password while required: %v", err)
	}
	if u, _ := db.Users().Get(ctx, "dev"); u.FailedLogins != 0 {
		t.Fatal("a refused clear-text password counted as a failed login")
	}
	if _, err := a.Basic(WithUIRequest(ctx), "dev", "pw"); err != nil {
		t.Fatalf("a UI request's clear-text password while required: %v", err)
	}
	p, err := a.Token(ctx, "enc", "dev")
	if err != nil {
		t.Fatalf("the encrypted password while required: %v", err)
	}
	set(accessapi.EncryptionUnsupported)
	if _, err := a.Token(ctx, "enc", "dev"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("the encrypted password while unsupported: %v", err)
	}
	if _, err := a.Current(ctx, p, db.Tokens()); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("Current of an encrypted password while unsupported: %v", err)
	}
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatalf("a clear-text password while unsupported: %v", err)
	}
	set(accessapi.EncryptionSupported)
	if _, err := a.Current(ctx, p, db.Tokens()); err != nil {
		t.Fatalf("Current of an encrypted password while supported: %v", err)
	}
	// The encrypted password stands for the internal password, which the
	// internal password switch turns off.
	if err := db.Settings().Put(ctx, settings.SettingBasicAuthEnabled, false); err != nil {
		t.Fatal(err)
	}
	a.ForgetPolicies()
	if _, err := a.Token(ctx, "enc", "dev"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("the encrypted password with internal passwords off: %v", err)
	}
	if _, err := a.Current(ctx, p, db.Tokens()); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("Current of an encrypted password with internal passwords off: %v", err)
	}

	broken, err := NewAuthenticator(db.Users(), failingSettings{db.Settings()}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.encryption(ctx); err == nil {
		t.Fatal("a policy that cannot be read")
	}
	if _, err := PasswordEncryption(ctx, failingSettings{db.Settings()}); err == nil {
		t.Fatal("a policy that cannot be read")
	}
}
