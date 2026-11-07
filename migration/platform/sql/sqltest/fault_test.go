package sqltest_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// TestUnitFaultStatements pins the statements, which are the ones the SQL
// stores' fault tests have always run.
func TestUnitFaultStatements(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{
			sqltest.FailTrigger("INSERT", "things"),
			"CREATE TRIGGER ops_fail_insert_things BEFORE INSERT ON things BEGIN SELECT RAISE(ABORT, 'injected'); END",
		},
		{
			sqltest.FailTrigger("DELETE", "kept"),
			"CREATE TRIGGER ops_fail_delete_kept BEFORE DELETE ON kept BEGIN SELECT RAISE(ABORT, 'injected'); END",
		},
		{sqltest.DropTable("things"), "DROP TABLE things"},
	} {
		if c.got != c.want {
			t.Errorf("statement %q; want %q", c.got, c.want)
		}
	}
}

// TestUnitErrKeepsTheError checks that Err and Err3 return the call's error
// as it is, nil included.
func TestUnitErrKeepsTheError(t *testing.T) {
	errCall := errors.New("call failed")
	if got := sqltest.Err(1, errCall); got != errCall {
		t.Errorf("Err = %v; want the call's error", got)
	}
	if got := sqltest.Err3([]string{"a"}, "next", errCall); got != errCall {
		t.Errorf("Err3 = %v; want the call's error", got)
	}
	if sqltest.Err("v", nil) != nil || sqltest.Err3(0, "", nil) != nil {
		t.Error("a call without an error gave one")
	}
}

// TestAccFaultHelpers runs the fault statements on SQLite: the trigger
// fails the event it names with "injected" and leaves the others alone, a
// dropped table fails the statements that touch it, and Fails passes when
// every call failed.
func TestAccFaultHelpers(t *testing.T) {
	ctx := t.Context()
	s := sqltest.OpenSQLite(t, things)
	insert(t, s, "things", "a")
	insert(t, s, "things", "b")
	sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "things"))
	_, insertErr := s.Exec(ctx, "insert", "INSERT INTO things (name) VALUES (?)", "c")
	if insertErr == nil || !strings.Contains(insertErr.Error(), "injected") {
		t.Fatalf("insert past the trigger = %v; want the injected failure", insertErr)
	}
	if _, err := s.Exec(ctx, "delete", "DELETE FROM things WHERE name = ?", "a"); err != nil {
		t.Fatalf("the INSERT trigger failed a DELETE: %v", err)
	}
	sqltest.Exec(t, s.DB(), sqltest.DropTable("things"))
	list := func() ([]string, string, error) {
		rows, err := s.Q(ctx).QueryContext(ctx, "SELECT name FROM things")
		if err != nil {
			return nil, "", err
		}
		_ = rows.Close()
		return nil, "", nil
	}
	exists := "SELECT 1 FROM things WHERE name = ?"
	sqltest.Fails(t, map[string]error{
		"insert past the trigger": insertErr,
		"exists after the drop":   sqltest.Err(s.Exists(ctx, "exists", exists, "b")),
		"list after the drop":     sqltest.Err3(list()),
	})
}
