package types_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The bridge must alias the blob row, not declare its own (decision 0044,
// "Type-alias bridges"): the SQL and in-memory blob indexes return types.Blob
// values from the Get that the mirror's repair worker calls through its
// BlobIndex port as blob.Blob, and their users move to the blob names without
// conversions. A struct is assignable only to its own type, and a value of a
// named string type only to that type, so these lines compile only while each
// pair is one type.
var (
	_ blob.Blob      = types.Blob{}
	_ blob.BlobState = types.BlobState("")
)

// A constant has no identity beyond its type and value, so the bridged states
// are compared on both: a copy whose value drifted would give rows a state
// that code using the blob names does not know, and an untyped copy, though
// it still compiles at every use, is a plain string, not a BlobState,
// wherever it is boxed.
func TestUnitBridgeBlobStates(t *testing.T) {
	for _, c := range []struct{ legacy, target any }{
		{types.BlobLive, blob.BlobLive},
		{types.BlobDeleting, blob.BlobDeleting},
		{types.BlobDeleted, blob.BlobDeleted},
	} {
		if c.legacy != c.target {
			t.Errorf("types holds %#[1]v (%[1]T), blob holds %#[2]v (%[2]T)",
				c.legacy, c.target)
		}
	}
}
