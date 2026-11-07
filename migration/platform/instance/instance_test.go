package instance_test

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/instance"
)

func TestUnitValidateInstanceID(t *testing.T) {
	if err := instance.ValidateInstanceID("jfrt@0f8fad5b-d9cb-469f-a165-70867728950e"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"jfrt@x", "jfac@0f8fad5b-d9cb-469f-a165-70867728950e", ""} {
		if err := instance.ValidateInstanceID(bad); !errors.Is(err, instance.ErrInvalidInstanceID) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
