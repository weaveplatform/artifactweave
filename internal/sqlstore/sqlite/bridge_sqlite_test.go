package sqlite_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	sqlitedrv "github.com/weaveplatform/artifactweave/migration/platform/sql/sqlite"
)

// If the bridged sentinel were a copy, it would print the same text, but
// errors.Is would stop matching between Open, which returns the driver's
// value, and callers that test the old name, such as TestAccOpenErrors. Only
// the same value matches in both directions, so that is what the test
// checks, also through a wrap and on the error Open returns.
func TestUnitBridgeSQLiteSentinel(t *testing.T) {
	legacy, target := sqlite.ErrPathRequired, sqlitedrv.ErrPathRequired
	if !errors.Is(legacy, target) || !errors.Is(target, legacy) {
		t.Error("sqlite.ErrPathRequired and the driver's ErrPathRequired are different errors")
	}
	if !errors.Is(fmt.Errorf("op: %w", target), legacy) ||
		!errors.Is(fmt.Errorf("op: %w", legacy), target) {
		t.Error("a wrapped ErrPathRequired does not match under both names")
	}
	_, err := sqlite.Open(t.Context(), "", sqlite.Options{})
	if !errors.Is(err, legacy) || !errors.Is(err, target) {
		t.Errorf("Open: %v does not match the sentinel under both names", err)
	}
	for _, err := range []error{legacy, target} {
		if got := err.Error(); got != "sqlite: path is required" {
			t.Errorf("ErrPathRequired.Error() = %q, want %q", got, "sqlite: path is required")
		}
	}
}

// No caller uses the old name since the driver's tests moved, but the
// bridge keeps it until PRO-02. A copy of the function, even one with the same
// body, could drift from the driver's unnoticed, so both names must hold the
// driver's function, the one the driver's Dialect carries.
func TestUnitBridgeSQLiteUniqueViolation(t *testing.T) {
	if reflect.ValueOf(sqlite.IsUniqueViolation).Pointer() !=
		reflect.ValueOf(sqlitedrv.IsUniqueViolation).Pointer() {
		t.Error("sqlite.IsUniqueViolation is not the driver's IsUniqueViolation")
	}
}

// Dialect is a struct value, so the bridge is a copy that the package takes
// when it initialises, after the driver's. The migrator, the SQL test harness
// and sqlcommon's tests get the bridged value where Open passes the driver's,
// so every field must agree: by value, and the functions by identity. The
// loop also covers a field the dialect layer gains later.
func TestUnitBridgeSQLiteDialect(t *testing.T) {
	legacy, target := reflect.ValueOf(sqlite.Dialect), reflect.ValueOf(sqlitedrv.Dialect)
	for i := range target.NumField() {
		name, l, r := target.Type().Field(i).Name, legacy.Field(i), target.Field(i)
		var same bool
		if r.Kind() == reflect.Func {
			same = l.Pointer() == r.Pointer()
		} else {
			same = reflect.DeepEqual(l.Interface(), r.Interface())
		}
		if !same {
			t.Errorf("Dialect.%s differs between the bridge and the driver", name)
		}
	}
}

// A constant has no identity beyond its type and value, so the bridged path
// is compared on both. Callers open a private in-memory database with it, and
// OpenDB limits the pool to one connection only for the driver's value.
func TestUnitBridgeSQLiteMemory(t *testing.T) {
	var legacy, target any = sqlite.Memory, sqlitedrv.Memory
	if legacy != target {
		t.Errorf("sqlite holds %#[1]v (%[1]T), the driver holds %#[2]v (%[2]T)", legacy, target)
	}
}

// DSN wraps the driver's function, because the two Options types differ, so
// it is compared by its result: a wrapper that dropped the busy timeout would
// still pass with the default, so the timeout is set too.
func TestUnitBridgeSQLiteDSN(t *testing.T) {
	for _, d := range []time.Duration{0, 2500 * time.Millisecond} {
		legacy := sqlite.DSN("aw.db", sqlite.Options{BusyTimeout: d})
		target := sqlitedrv.DSN("aw.db", sqlitedrv.Options{BusyTimeout: d})
		if legacy != target {
			t.Errorf("busy timeout %v: sqlite.DSN = %q, the driver's = %q", d, legacy, target)
		}
	}
}
