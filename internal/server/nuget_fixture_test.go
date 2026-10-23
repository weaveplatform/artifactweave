package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// nugetServer is the router over a store with nuget-local and
// nuget-forced, the seeded admin, and dev, who holds nothing; both
// passwords are "pw". It returns an access token of admin's.
func nugetServer(t *testing.T, tune ...func(*Config)) (http.Handler, *inmem.Store, string) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Users().Create(ctx, types.User{Name: "dev", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	force := true
	for _, c := range []repo.Config{
		{Key: "nuget-local", Class: repo.Local, PackageType: repo.NuGet},
		{
			Key: "nuget-forced", Class: repo.Local, PackageType: repo.NuGet,
			ForceNugetAuthentication: &force,
		},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := tokens.Issue(ctx, token.Request{Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CompatVersion: "7.155.0", BaseURL: "https://nuget.example", Store: db, Blobs: blobs,
		Auth: a, Tokens: tokens,
	}
	for _, fn := range tune {
		fn(&cfg)
	}
	h := New(cfg)
	return h, db, issued.AccessToken
}
