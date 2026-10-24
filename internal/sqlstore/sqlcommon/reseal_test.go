package sqlcommon_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// TestAccSealedColumnsComplete finds every purpose the store seals with and
// requires Reseal to cover it, so a secret column added later cannot be
// left out of `artifactweave keys rotate` and strand values under a retired
// key. It reads the package source: seal(<purpose constant>, ...) calls and
// the constants' values.
func TestAccSealedColumnsComplete(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]string{}
	var used []string
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ValueSpec:
				for i, id := range n.Names {
					if i < len(n.Values) {
						if lit, ok := n.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							consts[id.Name], _ = strconv.Unquote(lit.Value)
						}
					}
				}
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "seal" &&
					len(n.Args) > 0 {
					if id, ok := n.Args[0].(*ast.Ident); ok {
						used = append(used, id.Name)
					}
				}
			}
			return true
		})
	}
	if len(used) == 0 {
		t.Fatal("found no seal calls; the scan is broken")
	}
	covered := sqlcommon.SealedPurposes()
	for _, name := range used {
		purpose, ok := consts[name]
		if !ok {
			t.Fatalf("seal is called with %s, which is not a string constant in this package", name)
		}
		if !slices.Contains(covered, purpose) {
			t.Errorf("purpose %q is sealed but missing from sealedColumns", purpose)
		}
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

func TestAccMustJSONPanicsOnUnencodable(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("encoding a channel did not panic")
		}
	}()
	sqlcommon.MustJSON(make(chan int))
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
