package app

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/crypt"
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const tfState = `{"version":4,"serial":1,"lineage":"l"}`

// stateService is the Terraform state service over cfg's hot blob store.
func stateService(
	t *testing.T,
	cfg config.Config,
	s store.Store,
	k *crypt.Keyring,
) *terraformstate.Service {
	t.Helper()
	hot, _ := tiers(cfg, s)
	opened, err := hot.open(context.Background(), s, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return &terraformstate.Service{
		Items: &item.Service{Store: s, Blobs: opened.store, Formats: formatSettings(cfg, k).For},
		Keys:  k,
	}
}

// writeState writes tfState, sealed under k, to workspace app of a new
// Terraform backend repository, and returns the repository.
func writeState(t *testing.T, cfg config.Config, s store.Store, k *crypt.Keyring) repo.Config {
	t.Helper()
	ctx := context.Background()
	c := *repo.Defaults(&repo.Config{Key: "tf", Class: repo.Local, PackageType: repo.TerraformBackend})
	if err := s.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	svc := stateService(t, cfg, s, k)
	if _, err := svc.CreateWorkspace(ctx, c, "app", "", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WriteState(ctx, c, "app", terraformstate.StateWrite{
		Body: strings.NewReader(tfState), MD5: tfbe.MD5([]byte(tfState)), Serial: 1, By: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// readState reads workspace app's current state in c under k.
func readState(
	t *testing.T,
	cfg config.Config,
	s store.Store,
	k *crypt.Keyring,
	c repo.Config,
) string {
	t.Helper()
	ctx := context.Background()
	svc := stateService(t, cfg, s, k)
	cur, err := svc.CurrentState(ctx, c, "app")
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := svc.ReadState(ctx, c, "app", cur.Ms)
	if err != nil {
		t.Fatal(err)
	}
	return string(plain)
}

// A configuration without a hot blob store holds no states to reseal.
func TestResealStatesWithoutBlobs(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.MasterKey = keyOne
	k, err := Keyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(ctx, cfg, sqlcommon.Options{Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	cfg.Blob.Backend = ""
	if rep, err := resealStates(ctx, cfg, s, k); err != nil || rep.Resealed != 0 {
		t.Fatalf("resealStates without blobs = %+v, %v", rep, err)
	}
}
