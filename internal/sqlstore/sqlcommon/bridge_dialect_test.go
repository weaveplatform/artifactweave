package sqlcommon_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// The bridge must alias the dialect layer's types, not declare its own
// (decision 0044, "Type-alias bridges"): the SQLite and PostgreSQL packages
// declare their dialects as sqlcommon.Dialect values, which New passes to
// dialect.New, and the bridged LoadMigrations and Q return dialect values to
// callers that use the sqlcommon names, with no conversion on either side. A
// declared Queryer would still compile everywhere, because interfaces with the
// same methods are assignable both ways. A struct is assignable only to its
// own type and a pointer only to a pointer to the identical type, so these
// lines compile only while each pair is one type.
var (
	_ dialect.Dialect       = sqlcommon.Dialect{}
	_ dialect.Migration     = sqlcommon.Migration{}
	_ dialect.Applied       = sqlcommon.Applied{}
	_ dialect.StreamVersion = sqlcommon.StreamVersion{}
	_ *dialect.Queryer      = (*sqlcommon.Queryer)(nil)
)

// If a bridged sentinel were a copy, it would still compile and print the
// same text, but errors.Is would stop matching between the dialect layer,
// which returns it, and callers that test the old name, such as the key
// rotation checks of the SQL test harness, which expect ErrNoKeyring from a
// store opened without a keyring. Only the same value matches in both
// directions, so that is what the test checks, also through a wrap. The texts
// keep their sqlcommon: prefix.
func TestUnitBridgeDialectSentinels(t *testing.T) {
	cases := []struct {
		legacy, target error
		text           string
	}{
		{sqlcommon.ErrTransaction, dialect.ErrTransaction, "sqlcommon: transaction failed"},
		{sqlcommon.ErrMigration, dialect.ErrMigration, "sqlcommon: migration"},
		{
			sqlcommon.ErrNoKeyring, dialect.ErrNoKeyring,
			"sqlcommon: value is sealed but no keyring is configured",
		},
		{
			sqlcommon.ErrLegacyHistory, dialect.ErrLegacyHistory,
			"sqlcommon: migration: database history predates per-module migrations; not upgraded",
		},
		{
			sqlcommon.ErrUnknownModule, dialect.ErrUnknownModule,
			"sqlcommon: migration: unknown module",
		},
		{
			sqlcommon.ErrModuleRequired, dialect.ErrModuleRequired,
			"sqlcommon: migration: a target version other than 0 needs a module",
		},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			if !errors.Is(c.legacy, c.target) || !errors.Is(c.target, c.legacy) {
				t.Error("the sqlcommon and dialect names hold different errors")
			}
			if !errors.Is(fmt.Errorf("op: %w", c.target), c.legacy) ||
				!errors.Is(fmt.Errorf("op: %w", c.legacy), c.target) {
				t.Error("a wrapped sentinel does not match under both names")
			}
			for _, err := range []error{c.legacy, c.target} {
				if got := err.Error(); got != c.text {
					t.Errorf("Error() = %q, want %q", got, c.text)
				}
			}
		})
	}
}

// The entity files build their statements with Upsert, InsertIgnore, Insert
// and Placeholders, and the backends, the migrate command and the SQL test
// harness call the migrator through the sqlcommon names until PRO-01
// re-points them. A copy of any of these functions, even one with the same
// body, would leave two that could drift apart unnoticed, so both names must
// hold the dialect function.
func TestUnitBridgeDialectFunctions(t *testing.T) {
	for _, c := range []struct {
		name           string
		legacy, target any
	}{
		{"Upsert", sqlcommon.Upsert, dialect.Upsert},
		{"InsertIgnore", sqlcommon.InsertIgnore, dialect.InsertIgnore},
		{"Insert", sqlcommon.Insert, dialect.Insert},
		{"Placeholders", sqlcommon.Placeholders, dialect.Placeholders},
		{"LoadMigrations", sqlcommon.LoadMigrations, dialect.LoadMigrations},
		{"Migrate", sqlcommon.Migrate, dialect.Migrate},
		{"Rollback", sqlcommon.Rollback, dialect.Rollback},
		{"Version", sqlcommon.Version, dialect.Version},
		{"MustSub", sqlcommon.MustSub, dialect.MustSub},
	} {
		if reflect.ValueOf(c.legacy).Pointer() != reflect.ValueOf(c.target).Pointer() {
			t.Errorf("sqlcommon.%s is not dialect.%[1]s", c.name)
		}
	}
}
