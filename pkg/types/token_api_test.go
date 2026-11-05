package types_test

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitIsReferenceToken(t *testing.T) {
	ref := "cmVmd" + strings.Repeat("A", 59)
	if !types.IsReferenceToken(ref) || types.IsReferenceToken(ref+"A") ||
		types.IsReferenceToken("eyJhbGciOi") {
		t.Error("IsReferenceToken")
	}
}
