package server

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccUnder(t *testing.T) {
	if under("org/", "") != nil {
		t.Fatal("an empty prefix narrows nothing")
	}
	// Each prefix is a deeper path or a file in the prefix's folder.
	either, _ := under("org/acme/lib/", "lib").(aql.Or)
	if len(either) != 4 {
		t.Fatalf("under %#v", either)
	}
	at, _ := either[3].(aql.And)
	if dir, _ := at[0].(*aql.FieldCriterion); dir == nil || dir.Text != types.RootPath {
		t.Fatalf("a prefix without a slash is a name at the root: %#v", at)
	}
}
