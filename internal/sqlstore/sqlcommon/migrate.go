package sqlcommon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrMigration reports a malformed or failing migration.
var ErrMigration = errors.New("sqlcommon: migration")

// migrationsTable records the applied schema versions.
const migrationsTable = "schema_migrations"

// Migration is one parsed migration file.
type Migration struct {
	Version int
	Name    string
	Up      []string
	Down    []string
}

// LoadMigrations parses every NNNN_name.sql file in fsys, sorted by version.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("%w: read dir: %w", ErrMigration, err)
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m, err := parseMigration(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[m.Version]; dup {
			return nil, fmt.Errorf("%w: version %d defined by %s and %s",
				ErrMigration, m.Version, prev, e.Name())
		}
		seen[m.Version] = e.Name()
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b Migration) int { return a.Version - b.Version })
	return out, nil
}

// parseMigration reads one file's version, name and statements.
func parseMigration(fsys fs.FS, name string) (Migration, error) {
	verStr, title, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
	if !ok {
		return Migration{}, fmt.Errorf("%w: %s: want NNNN_name.sql", ErrMigration, name)
	}
	version, err := strconv.Atoi(verStr)
	if err != nil || version <= 0 {
		return Migration{}, fmt.Errorf("%w: %s: bad version %q", ErrMigration, name, verStr)
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return Migration{}, fmt.Errorf("%w: %s: %w", ErrMigration, name, err)
	}
	m := Migration{Version: version, Name: title}
	section := ""
	var current []string
	flush := func() {
		stmts := splitStatements(strings.Join(current, "\n"))
		switch section {
		case "up":
			m.Up = append(m.Up, stmts...)
		case "down":
			m.Down = append(m.Down, stmts...)
		}
		current = current[:0]
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case "-- +up":
			flush()
			section = "up"
		case "-- +down":
			flush()
			section = "down"
		default:
			if section != "" {
				current = append(current, line)
			}
		}
	}
	flush()
	if len(m.Up) == 0 {
		return Migration{}, fmt.Errorf("%w: %s: no statements under -- +up", ErrMigration, name)
	}
	return m, nil
}

// splitStatements splits on semicolons at line ends and drops comment-only
// lines, so each statement runs on its own.
func splitStatements(text string) []string {
	var out []string
	var sb strings.Builder
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
		if strings.HasSuffix(trimmed, ";") {
			out = append(out, strings.TrimSuffix(strings.TrimSpace(sb.String()), ";"))
			sb.Reset()
		}
	}
	if rest := strings.TrimSpace(sb.String()); rest != "" {
		out = append(out, rest)
	}
	return out
}

// Migrate applies every pending migration in order, each in its own
// transaction, and returns the versions applied.
func Migrate(ctx context.Context, db *sql.DB, d Dialect) ([]int, error) {
	ms, applied, err := state(ctx, db, d)
	if err != nil {
		return nil, err
	}
	var done []int
	for _, m := range ms {
		if applied[m.Version] {
			continue
		}
		err := step(ctx, db, m.Up, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, d.Rebind(
				"INSERT INTO "+migrationsTable+" (version, name, applied_at) VALUES (?, ?, ?)"),
				m.Version, m.Name, time.Now().UTC().UnixMicro())
			return err //nolint:wrapcheck // wrapped by step
		})
		if err != nil {
			return done, fmt.Errorf("%w: %d_%s: %w", ErrMigration, m.Version, m.Name, err)
		}
		done = append(done, m.Version)
	}
	return done, nil
}

// Rollback reverts applied migrations newer than target (0 reverts all),
// newest first, each in its own transaction, and returns the versions
// reverted.
func Rollback(ctx context.Context, db *sql.DB, d Dialect, target int) ([]int, error) {
	ms, applied, err := state(ctx, db, d)
	if err != nil {
		return nil, err
	}
	var done []int
	for _, m := range slices.Backward(ms) {
		if m.Version <= target || !applied[m.Version] {
			continue
		}
		err := step(ctx, db, m.Down, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, d.Rebind(
				"DELETE FROM "+migrationsTable+" WHERE version = ?"), m.Version)
			return err //nolint:wrapcheck // wrapped by step
		})
		if err != nil {
			return done, fmt.Errorf("%w: %d_%s down: %w", ErrMigration, m.Version, m.Name, err)
		}
		done = append(done, m.Version)
	}
	return done, nil
}

// Version returns the highest applied version (0 when none) and the highest
// version the dialect knows, so callers can report a pending schema.
func Version(ctx context.Context, db *sql.DB, d Dialect) (current, latest int, err error) {
	ms, applied, err := state(ctx, db, d)
	if err != nil {
		return 0, 0, err
	}
	for v := range applied {
		current = max(current, v)
	}
	if len(ms) > 0 {
		latest = ms[len(ms)-1].Version
	}
	return current, latest, nil
}

// state loads the migrations and the applied versions, creating the
// version table when absent.
func state(ctx context.Context, db *sql.DB, d Dialect) ([]Migration, map[int]bool, error) {
	ms, err := LoadMigrations(d.Migrations)
	if err != nil {
		return nil, nil, err
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+migrationsTable+
		" (version INTEGER NOT NULL PRIMARY KEY, name VARCHAR(255) NOT NULL, applied_at BIGINT NOT NULL)",
	); err != nil {
		return nil, nil, fmt.Errorf("%w: create %s: %w", ErrMigration, migrationsTable, err)
	}
	rows, err := db.QueryContext(ctx, "SELECT version FROM "+migrationsTable)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read versions: %w", ErrMigration, err)
	}
	defer func() { _ = rows.Close() }()
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, nil, fmt.Errorf("%w: scan version: %w", ErrMigration, err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: read versions: %w", ErrMigration, err)
	}
	return ms, applied, nil
}

// step runs stmts and then record in one transaction.
func step(ctx context.Context, db *sql.DB, stmts []string, record func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("%q: %w", firstLine(s), err)
		}
	}
	if err := record(tx); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// firstLine names a failing statement in an error without the whole DDL.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// MustSub returns the sub-tree dir of fsys and panics when it is missing.
// Backends use it to expose their embedded migrations as a Dialect value.
func MustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(fmt.Sprintf("sqlcommon: migrations directory %q: %v", dir, err))
	}
	return sub
}
