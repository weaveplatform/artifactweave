package sqltest_test

import (
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	platsqltest "github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// Tests across the tree call the old name until PRO-01 re-points them. A
// copy of the function, even one with the same body, would keep a sync.Once
// of its own and start a second container in a test process that also runs
// a module suite, so both names must hold the harness's function.
func TestUnitBridgeSQLTestPostgresDSN(t *testing.T) {
	if reflect.ValueOf(sqltest.PostgresDSN).Pointer() !=
		reflect.ValueOf(platsqltest.PostgresDSN).Pointer() {
		t.Error("sqltest.PostgresDSN is not the harness's PostgresDSN")
	}
}
