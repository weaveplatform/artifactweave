package authn

import (
	"encoding/json"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
)

func TestUnitDownloadCredentialRestoration(t *testing.T) {
	ctx := t.Context()
	db := inmem.New()
	a, clock := newAuth(t, db, Options{})
	addUser(t, db, model.User{Name: "user", Admin: true}, "password")
	p, err := a.Basic(ctx, "user", "password")
	if err != nil {
		t.Fatal(err)
	}
	reference, err := p.DownloadReference()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	var restored kernel.DownloadCredential
	if err = json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	fresh, err := a.RestoreDownload(ctx, "user", restored, db.Tokens())
	if err != nil || !fresh.Password || fresh.PasswordRevision != p.PasswordRevision {
		t.Fatal(fresh, err)
	}
	user, _ := db.Users().Get(ctx, "user")
	user.PasswordHash = []byte("changed")
	if err = db.Users().Update(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RestoreDownload(ctx, "user", restored, db.Tokens()); err == nil {
		t.Fatal("password change ignored")
	}
	tok := model.Token{
		ID:        "download-parent",
		Subject:   "jfac@test/users/user",
		Scope:     scope(t, "applied-permissions/user"),
		ExpiresAt: clock.now().Add(time.Minute),
		Identity:  kernel.IdentityStored,
	}
	if err = db.Tokens().Create(ctx, tok); err != nil {
		t.Fatal(err)
	}
	p = tokenPrincipal(user, tok.Scope)
	p.TokenID = tok.ID
	reference, err = p.DownloadReference()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err = a.RestoreDownload(ctx, "user", reference, db.Tokens())
	if err != nil || accessapi.SubjectOf(fresh).Scope == nil || fresh.TokenID != tok.ID ||
		!fresh.TokenExpires.Equal(tok.ExpiresAt) ||
		fresh.Identity != tok.Identity {
		t.Fatal(fresh, err)
	}
	if _, err = a.RestoreDownload(ctx, "other", reference, db.Tokens()); err == nil {
		t.Fatal("subject widened")
	}
	clock.t = clock.now().Add(2 * time.Minute)
	if _, err = a.RestoreDownload(ctx, "user", reference, db.Tokens()); err == nil {
		t.Fatal("expired parent accepted")
	}
	if err = db.Tokens().Delete(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RestoreDownload(ctx, "user", reference, db.Tokens()); err == nil {
		t.Fatal("revoked parent accepted")
	}
	reference, err = kernel.Anonymous.DownloadReference()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.RestoreDownload(ctx, kernel.AnonymousUser, reference, db.Tokens()); err == nil {
		t.Fatal("anonymous off")
	}
	if err = db.Settings().Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RestoreDownload(ctx, kernel.AnonymousUser, reference, db.Tokens()); err != nil {
		t.Fatal(err)
	}
	if err = db.Settings().Put(ctx, settings.SettingAnonymousAccess, false); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RestoreDownload(ctx, kernel.AnonymousUser, reference, db.Tokens()); err == nil {
		t.Fatal("anonymous toggle ignored")
	}
}

func TestUnitDownloadCredentialRejectsUnverifiedAndMixedReferences(t *testing.T) {
	db := inmem.New()
	a, _ := newAuth(t, db, Options{})
	for _, p := range []kernel.Principal{{}, {Name: "user"}, {Name: "user", Registry: &kernel.RegistryAccess{}, TokenID: "registry"}, {Name: "user", Password: true, PasswordExpired: true}} {
		if _, err := p.DownloadReference(); err == nil {
			t.Fatal("unverified principal", p)
		}
	}
	for _, reference := range []kernel.DownloadCredential{{}, {Kind: "unknown"}, {Kind: "password"}, {Kind: "token"}, {Kind: "anonymous", TokenID: "other"}, {Kind: "token", TokenID: "t", PasswordRevision: [32]byte{1}}, {Kind: "password", TokenID: "t", PasswordRevision: [32]byte{1}}} {
		if _, err := a.RestoreDownload(t.Context(), "user", reference, db.Tokens()); err == nil {
			t.Fatal(reference)
		}
	}
	if _, err := a.RestoreDownload(
		t.Context(),
		"user",
		kernel.DownloadCredential{Kind: "anonymous"},
		db.Tokens(),
	); err == nil {
		t.Fatal("anonymous subject changed")
	}
}
