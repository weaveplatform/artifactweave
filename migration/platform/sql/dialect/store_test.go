package dialect_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

func TestAccMustJSONPanicsOnUnencodable(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("encoding a channel did not panic")
		}
	}()
	dialect.MustJSON(make(chan int))
}
