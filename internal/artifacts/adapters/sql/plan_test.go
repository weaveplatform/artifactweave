package sql_test

import (
	"strings"
	"testing"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
)

// explain returns the engine's plan for q with args: SQLite's EXPLAIN
// QUERY PLAN, and on PostgreSQL EXPLAIN ANALYZE, which runs q, so the plan
// shows the rows each step read. The Docker, npm and NuGet, and Puppet
// scale tests read their plans through it.
func explain(t *testing.T, s *artsql.Store, q string, args ...any) string {
	t.Helper()
	prefix := "EXPLAIN QUERY PLAN "
	if s.Dialect().Dollar {
		prefix = "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) "
	}
	rows, err := s.DB().QueryContext(t.Context(), s.Dialect().Rebind(prefix+q), args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if s.Dialect().Dollar {
			err = rows.Scan(&line)
		} else {
			var id, parent, unused int
			err = rows.Scan(&id, &parent, &unused, &line)
		}
		if err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}
