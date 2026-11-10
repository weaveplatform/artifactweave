package dialect_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/platform/tx"
)

// The dialect store is the SQL implementation of tx.Runner: the SQL stores
// take Tx and AfterCommit from it to satisfy their ports, which embed
// Runner. A test file holds the check because the dialect package imports
// nothing of the module but the kernel and the keyring.
var _ tx.Runner = (*dialect.Store)(nil)

func TestAccMustJSONPanicsOnUnencodable(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("encoding a channel did not panic")
		}
	}()
	dialect.MustJSON(make(chan int))
}

// The store names the dialect and the operation in what it passes up from
// the driver, and keeps the driver's error matchable. SQLite cannot be made
// to fail RowsAffected or Close, so the fault driver does.
func TestUnitStoreReportsDriverFaults(t *testing.T) {
	d := dialect.Dialect{Name: "fake"}
	s := dialect.New(faultDB(t, faults{rowsAffected: true}), d, nil)
	_, err := s.Exec(context.Background(), "touch", "UPDATE items SET name = name")
	if want := "fake: touch: fault"; !errors.Is(err, errFault) || err.Error() != want {
		t.Errorf("Exec = %v; want %s", err, want)
	}
	err = dialect.New(faultDB(t, faults{close: true}), d, nil).Close()
	if want := "fake: close: fault"; !errors.Is(err, errFault) || err.Error() != want {
		t.Errorf("Close = %v; want %s", err, want)
	}
}

// A secret the keyring refuses to seal, such as one with no purpose to bind
// it to its column (decision 0017), fails the write instead of being stored
// in the clear.
func TestUnitSealReportsTheKeyringsRefusal(t *testing.T) {
	keys, err := crypt.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s := dialect.New(nil, sqliteDialect, keys) // sealing uses no pool
	sealed, err := s.Seal("", "row", "secret")
	if !errors.Is(err, crypt.ErrBadPurpose) || sealed != nil {
		t.Errorf("Seal = %q, %v; want nothing and %v", sealed, err, crypt.ErrBadPurpose)
	}
}
