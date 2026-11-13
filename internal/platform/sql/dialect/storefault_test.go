package dialect_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// A statement the database fails to run is the store's own failure,
// kernel.ErrStore, beside the driver's error, and reads as before; the
// store's verdicts and a statement whose context ended are not (#1470).
func TestUnitWrapMarksTheStoresOwnFailures(t *testing.T) {
	d := dialect.Dialect{Name: "fake"}
	s := dialect.New(faultDB(t, faults{exec: true}), d, nil)
	_, err := s.Exec(t.Context(), "touch", "UPDATE items SET name = name")
	if want := "fake: touch: fault"; !errors.Is(err, kernel.ErrStore) || !errors.Is(err, errFault) ||
		err.Error() != want {
		t.Errorf("Exec = %v; want %s marked kernel.ErrStore", err, want)
	}
	for _, unmarked := range []error{
		sql.ErrNoRows, context.Canceled, fmt.Errorf("query: %w", context.DeadlineExceeded),
	} {
		if err := s.Wrap("op", unmarked); errors.Is(err, kernel.ErrStore) {
			t.Errorf("Wrap(%v) = %v, marked kernel.ErrStore", unmarked, err)
		}
	}
}
