package authz

import (
	"context"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
)

// TestUnitUsernameFolds: a subject named in another case, as a transient
// token's subject keeps the case it was issued with, holds the grants
// stored under the folded name, and is itself under that name (#1432).
func TestUnitUsernameFolds(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	dec, err := New(ctx, Sources{Permissions: db.Permissions(), Groups: db.Groups()},
		accessapi.Subject{Name: "BOB"})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Allows(accessapi.ResourceArtifact, libs, "x.jar", d) {
		t.Error("BOB does not hold bob's grant")
	}
	if user, _ := dec.Self(); user != "bob" {
		t.Errorf("Self = %q, want bob", user)
	}
}
