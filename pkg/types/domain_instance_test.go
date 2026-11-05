package types_test

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitValidateInstanceID(t *testing.T) {
	if err := types.ValidateInstanceID("jfrt@0f8fad5b-d9cb-469f-a165-70867728950e"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"jfrt@x", "jfac@0f8fad5b-d9cb-469f-a165-70867728950e", ""} {
		if err := types.ValidateInstanceID(bad); !errors.Is(err, types.ErrInvalidInstanceID) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
