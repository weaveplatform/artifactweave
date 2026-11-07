package postgres_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	pgdrv "github.com/weaveplatform/artifactweave/migration/platform/sql/postgres"
)

// If the bridged sentinel were a copy, it would print the same text, but
// errors.Is would stop matching between Open, which returns the driver's
// value, and callers that test the old name, such as TestAccOpenErrors. Only
// the same value matches in both directions, so that is what the test
// checks, also through a wrap and on the error Open returns.
func TestUnitBridgePostgresSentinel(t *testing.T) {
	legacy, target := postgres.ErrDSNRequired, pgdrv.ErrDSNRequired
	if !errors.Is(legacy, target) || !errors.Is(target, legacy) {
		t.Error("postgres.ErrDSNRequired and the driver's ErrDSNRequired are different errors")
	}
	if !errors.Is(fmt.Errorf("op: %w", target), legacy) ||
		!errors.Is(fmt.Errorf("op: %w", legacy), target) {
		t.Error("a wrapped ErrDSNRequired does not match under both names")
	}
	_, err := postgres.Open(t.Context(), "", postgres.Options{})
	if !errors.Is(err, legacy) || !errors.Is(err, target) {
		t.Errorf("Open: %v does not match the sentinel under both names", err)
	}
	for _, err := range []error{legacy, target} {
		if got := err.Error(); got != "postgres: dsn is required" {
			t.Errorf("ErrDSNRequired.Error() = %q, want %q", got, "postgres: dsn is required")
		}
	}
}

// No caller uses the old name since the driver's tests moved, but the
// bridge keeps it until PRO-02. A copy of the function, even one with the same
// body, could drift from the driver's unnoticed, so both names must hold the
// driver's function, the one the driver's Dialect carries.
func TestUnitBridgePostgresUniqueViolation(t *testing.T) {
	if reflect.ValueOf(postgres.IsUniqueViolation).Pointer() !=
		reflect.ValueOf(pgdrv.IsUniqueViolation).Pointer() {
		t.Error("postgres.IsUniqueViolation is not the driver's IsUniqueViolation")
	}
}

// Dialect is a struct value, so the bridge is a copy that the package takes
// when it initialises, after the driver's. The migrator and the SQL test
// harness get the bridged value in the PostgreSQL suites where Open passes the
// driver's, so every field must agree: by value, and the functions by
// identity. The loop also covers a field the dialect layer gains later.
func TestUnitBridgePostgresDialect(t *testing.T) {
	legacy, target := reflect.ValueOf(postgres.Dialect), reflect.ValueOf(pgdrv.Dialect)
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
