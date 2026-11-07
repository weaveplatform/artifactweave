package sqlcommon_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

// sealExempt names the purposes that are sealed outside any SQL column, each
// with its reason, so that no module registers them.
var sealExempt = map[string]string{
	"terraform-state": "Terraform state blobs, which keys rotate re-seals through " +
		"resealStates, decision 0041",
}

// TestAccSealedColumnsComplete finds every purpose the repository seals with
// and requires a module to register its column, so a secret column added in
// any module adapter cannot be left out of `artifactweave keys rotate` and
// strand its values under a retired key. It scans the repository's source
// (scanSeals) and also fails on a purpose it cannot read, on a registered
// purpose that nothing seals any more and on a stale exemption.
func TestAccSealedColumnsComplete(t *testing.T) {
	scan, err := scanSeals(repopath.Root(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.purposes) == 0 {
		t.Fatal("found no Seal calls; the scan is broken")
	}
	for _, problem := range checkSealedPurposes(scan, sqlcommon.SealedPurposes(), sealExempt) {
		t.Error(problem)
	}
}

// The modules register in the canonical module order (decision 0044,
// "Persistence"), which Reseal follows, under the names the per-module
// migration history will be keyed by, and each sealed column once.
func TestUnitModulesRegisterInTheCanonicalOrder(t *testing.T) {
	want := []string{
		"platform", "artifacts", "access", "notifications",
		"retention", "federation", "releases", "workers",
	}
	if got := sqlcommon.ModuleNames(); !slices.Equal(got, want) {
		t.Errorf("modules = %q; want %q", got, want)
	}
	seen := map[string]bool{}
	for _, p := range sqlcommon.SealedPurposes() {
		if seen[p] {
			t.Errorf("purpose %q is registered more than once", p)
		}
		seen[p] = true
	}
}

// TestAccResealSealsPlainValues covers a database written without a keyring:
// rotation seals its plain values for the first time.
func TestAccResealSealsPlainValues(t *testing.T) {
	const password = "legacy-repository-password-awaiting-first-seal"
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "aw.db")
	plain, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.Repos().Create(ctx, repo.Config{
		Key: "r", Class: repo.Remote,
		PackageType: repo.Generic, Password: password,
	}); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := plain.DB().
		QueryRowContext(ctx, "SELECT password FROM repository_secrets").
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) != password {
		t.Fatal("the legacy fixture did not store a plaintext password")
	}
	_ = plain.Close()
	keys := contentKeys(t)
	s := openContent(t, path, keys)
	if n, err := s.Reseal(ctx); err != nil || n != 1 {
		t.Fatalf("Reseal = %d, %v", n, err)
	}
	if err := s.DB().
		QueryRowContext(ctx, "SELECT password FROM repository_secrets").
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	// Ciphertext may contain any short substring by chance. Verify its envelope
	// and authenticated contents instead of searching random bytes for "pw".
	if !crypt.IsSealed(stored) {
		t.Fatal("the password has no sealed envelope")
	}
	opened, err := keys.Open("repositories.password", "r", stored)
	if err != nil || string(opened) != password {
		t.Fatalf("open sealed password: %v", err)
	}
	if c, err := s.Repos().Get(ctx, "r"); err != nil || c.Password != password {
		t.Fatalf("Get = %q, %v", c.Password, err)
	}
	// A value sealed under a key the ring does not hold stops the rotation.
	other := openContent(t, path, contentKeysOther(t))
	if _, err := other.Reseal(ctx); err == nil {
		t.Fatal("Reseal with an unrelated key succeeded")
	}
	// So does a broken table.
	execSQL(t, s, "DROP TABLE repository_secrets")
	if _, err := s.Reseal(ctx); err == nil {
		t.Fatal("Reseal over a missing table succeeded")
	}
}

// contentKeysOther is a version-1 keyring under a different master key.
func contentKeysOther(t *testing.T) *crypt.Keyring {
	t.Helper()
	k, err := crypt.New([]byte(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return k
}
