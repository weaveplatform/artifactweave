package access_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// The LDAP binding plans, applies and lists through access/core/ldap in the
// caller's transaction, through API and the store-only constructor alike.
func TestUnitLDAPBinding(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	url, pattern := "ldap://ldap.example.com", "uid={0}"
	create := []accessapi.LDAPChange{{Key: "corp", URL: &url, UserDNPattern: &pattern}}
	for name, b := range map[string]accessapi.LDAPSettings{"API": w.api.LDAP, "LDAPSettingsOf": access.LDAPSettingsOf(w.st)} {
		planned, err := b.Plan(ctx, create)
		if err != nil || len(planned) != 1 || planned[0].Outcome != accessapi.LDAPCreated {
			t.Fatalf("%s Plan = %v, %v", name, planned, err)
		}
		err = w.st.Tx(ctx, func(ctx context.Context) error {
			if _, err := b.Apply(ctx, create); err != nil {
				return err
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatal(err)
		}
		if all, err := b.Connections(ctx); err != nil || len(all) != 0 {
			t.Fatalf("%s: a rolled-back apply left %v, %v", name, all, err)
		}
	}
	if _, err := w.api.LDAP.Apply(ctx, create); err != nil {
		t.Fatal(err)
	}
	all, err := access.LDAPSettingsOf(w.st).Connections(ctx)
	if err != nil || len(all) != 1 || all[0].Key != "corp" || all[0].URL != url {
		t.Fatalf("Connections = %v, %v", all, err)
	}
	bad := "http://x"
	if _, err := w.api.LDAP.Plan(ctx, []accessapi.LDAPChange{{Key: "corp", URL: &bad}}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal(err)
	}
}
