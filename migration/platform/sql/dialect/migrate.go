package dialect

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

// ErrLegacyHistory reports a database whose schema_migrations table has no
// module column: its history predates the per-module streams, so its versions
// cannot be matched to them. The migrator refuses it before writing anything,
// and there is no upgrade path (decision 0044, "Persistence"): such a database
// is recreated. It wraps ErrMigration.
var ErrLegacyHistory = fmt.Errorf(
	"%w: database history predates per-module migrations; not upgraded", ErrMigration)

// ErrUnknownModule reports a module name that none of the modules passed to
// the migrator has. The error names it. It wraps ErrMigration.
var ErrUnknownModule = fmt.Errorf("%w: unknown module", ErrMigration)

// ErrModuleRequired reports a rollback to a version other than 0 with no
// module: each stream numbers its versions on its own, so a version names a
// point in one stream only. It wraps ErrMigration.
var ErrModuleRequired = fmt.Errorf("%w: a target version other than 0 needs a module", ErrMigration)

// migrationsTable records the applied migrations of every stream.
const migrationsTable = "schema_migrations"

// The statements that record a migration in the history and remove it, with
// "?" placeholders for Rebind.
const (
	recordMigration = "INSERT INTO " + migrationsTable +
		" (module, version, name, applied_at) VALUES (?, ?, ?, ?)"
	forgetMigration = "DELETE FROM " + migrationsTable + " WHERE module = ? AND version = ?"
)

// Migration is one parsed migration file: the version and name its file name
// gives, and the statements of its up and down sections.
type Migration struct {
	Version int
	Name    string
	Up      []string
	Down    []string
}

// LoadMigrations parses every NNNN_name.sql file at the top of fsys, sorted
// by version. A statement ends with a semicolon at the end of a line, the
// last one of a section may leave it out, and comment and blank lines are
// dropped. It fails with ErrMigration for a directory or file it cannot read,
// a malformed file name, a version below 1, a version defined twice and a
// file with no up statements.
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

// Applied names one migration of one stream: the Module whose stream holds
// it and its Version.
type Applied struct {
	Module  string
	Version int
}

// String returns the module and the version as "module:version", the form
// the migrate command prints, such as "platform:1".
func (a Applied) String() string { return a.Module + ":" + strconv.Itoa(a.Version) }

// StreamVersion is one module's stream: the highest version its history
// records, Current (0 when none), and the highest version the stream holds,
// Latest, so a caller can report a pending schema.
type StreamVersion struct {
	Module          string
	Current, Latest int
}

// Migrate applies every pending migration of each module's stream, the
// streams in the order of mods and each stream by version, and returns the
// migrations applied. When only is set it applies that module's stream
// alone, and a name that no module has fails with ErrUnknownModule. Each
// migration runs in its own transaction with the row that records it. On a
// failure it returns the migrations applied before it and an error that
// wraps ErrMigration and names the module, the migration and the first line
// of a failing statement.
//
// Like Rollback and Version, it first loads every stream and then reads the
// history table's columns: a table with no module column fails with
// ErrLegacyHistory, and nothing has been written to the database. Only then
// does it create the history table when it is absent.
func Migrate(
	ctx context.Context,
	db *sql.DB,
	d Dialect,
	mods []Module,
	only string,
) ([]Applied, error) {
	if only != "" && !hasModule(mods, only) {
		return nil, fmt.Errorf("%w %q", ErrUnknownModule, only)
	}
	streams, err := state(ctx, db, d, mods)
	if err != nil {
		return nil, err
	}
	var done []Applied
	for _, s := range streams {
		if only != "" && s.module != only {
			continue
		}
		for _, m := range s.migrations {
			if s.applied[m.Version] {
				continue
			}
			err := step(ctx, db, m.Up, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, d.Rebind(recordMigration),
					s.module, m.Version, m.Name, time.Now().UTC().UnixMicro())
				return err //nolint:wrapcheck // wrapped by step
			})
			if err != nil {
				return done, fmt.Errorf("%w: %s: %w", ErrMigration, s.label(m), err)
			}
			done = append(done, Applied{Module: s.module, Version: m.Version})
		}
	}
	return done, nil
}

// Rollback reverts the applied migrations of module's stream that are newer
// than target, newest first, and returns the migrations reverted. With no
// module it reverts every stream, the streams in the reverse order of mods,
// and accepts only target 0: any other target fails with ErrModuleRequired.
// A module that none of mods has fails with ErrUnknownModule. Each migration
// is reverted in its own transaction with the removal of its row. On a
// failure it returns the migrations reverted before it and an error that
// wraps ErrMigration. It refuses a legacy history as Migrate does.
func Rollback(
	ctx context.Context,
	db *sql.DB,
	d Dialect,
	mods []Module,
	module string,
	target int,
) ([]Applied, error) {
	switch {
	case module == "" && target != 0:
		return nil, ErrModuleRequired
	case module != "" && !hasModule(mods, module):
		return nil, fmt.Errorf("%w %q", ErrUnknownModule, module)
	}
	streams, err := state(ctx, db, d, mods)
	if err != nil {
		return nil, err
	}
	var done []Applied
	for _, s := range slices.Backward(streams) {
		if module != "" && s.module != module {
			continue
		}
		for _, m := range slices.Backward(s.migrations) {
			if m.Version <= target || !s.applied[m.Version] {
				continue
			}
			err := step(ctx, db, m.Down, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, d.Rebind(forgetMigration), s.module, m.Version)
				return err //nolint:wrapcheck // wrapped by step
			})
			if err != nil {
				return done, fmt.Errorf("%w: %s down: %w", ErrMigration, s.label(m), err)
			}
			done = append(done, Applied{Module: s.module, Version: m.Version})
		}
	}
	return done, nil
}

// Version returns every module's stream in the order of mods, with the
// highest version its history records and the highest it holds. It refuses
// a legacy history as Migrate does, and its errors wrap ErrMigration.
func Version(ctx context.Context, db *sql.DB, d Dialect, mods []Module) ([]StreamVersion, error) {
	streams, err := state(ctx, db, d, mods)
	if err != nil {
		return nil, err
	}
	out := make([]StreamVersion, 0, len(streams))
	for _, s := range streams {
		v := StreamVersion{Module: s.module}
		for a := range s.applied {
			v.Current = max(v.Current, a)
		}
		if len(s.migrations) > 0 {
			v.Latest = s.migrations[len(s.migrations)-1].Version
		}
		out = append(out, v)
	}
	return out, nil
}

// stream is one module's migrations for a dialect and the versions the
// history records for that module.
type stream struct {
	module     string
	migrations []Migration
	applied    map[int]bool
}

// label names m in an error with its module, because every stream starts
// with a 1_baseline.
func (s stream) label(m Migration) string {
	return s.module + " " + strconv.Itoa(m.Version) + "_" + m.Name
}

// hasModule reports whether one of mods is called name.
func hasModule(mods []Module, name string) bool {
	return slices.ContainsFunc(mods, func(m Module) bool { return m.Name == name })
}

// state loads the stream of every module, refuses a legacy history, creates
// the history table when it is absent and reads the applied versions. A row
// of a module that mods does not name is left alone.
func state(ctx context.Context, db *sql.DB, d Dialect, mods []Module) ([]stream, error) {
	streams := make([]stream, 0, len(mods))
	index := make(map[string]int, len(mods))
	for _, m := range mods {
		if m.Name == "" {
			return nil, fmt.Errorf("%w: a module has no name", ErrMigration)
		}
		if _, dup := index[m.Name]; dup {
			return nil, fmt.Errorf("%w: module %q registered twice", ErrMigration, m.Name)
		}
		var ms []Migration
		if m.Migrations != nil {
			var err error
			if ms, err = LoadMigrations(MustSub(m.Migrations, d.Name)); err != nil {
				return nil, err
			}
		}
		index[m.Name] = len(streams)
		streams = append(streams, stream{module: m.Name, migrations: ms, applied: map[int]bool{}})
	}
	if err := probe(ctx, db, d); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+migrationsTable+
		" (module VARCHAR(64) NOT NULL, version INTEGER NOT NULL, name VARCHAR(255) NOT NULL,"+
		" applied_at BIGINT NOT NULL, PRIMARY KEY (module, version))",
	); err != nil {
		return nil, fmt.Errorf("%w: create %s: %w", ErrMigration, migrationsTable, err)
	}
	rows, err := db.QueryContext(ctx, "SELECT module, version FROM "+migrationsTable)
	if err != nil {
		return nil, fmt.Errorf("%w: read versions: %w", ErrMigration, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var module string
		var v int
		if err := rows.Scan(&module, &v); err != nil {
			return nil, fmt.Errorf("%w: scan version: %w", ErrMigration, err)
		}
		if i, ok := index[module]; ok {
			streams[i].applied[v] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: read versions: %w", ErrMigration, err)
	}
	return streams, nil
}

// probe fails with ErrLegacyHistory when the history table exists without a
// module column. It reads the columns through the dialect's Columns query
// and writes nothing: on the old table CREATE TABLE IF NOT EXISTS does
// nothing and the version read fails on the missing column, an error that
// does not tell the operator the database has to be recreated. A dialect
// without that query fails rather than skipping the check.
func probe(ctx context.Context, db *sql.DB, d Dialect) error {
	if d.Columns == "" {
		return fmt.Errorf("%w: dialect %s has no Columns query", ErrMigration, d.Name)
	}
	rows, err := db.QueryContext(ctx, d.Rebind(d.Columns), migrationsTable)
	if err != nil {
		return fmt.Errorf("%w: read %s columns: %w", ErrMigration, migrationsTable, err)
	}
	defer func() { _ = rows.Close() }()
	found, module := false, false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("%w: scan %s column: %w", ErrMigration, migrationsTable, err)
		}
		found, module = true, module || name == "module"
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: read %s columns: %w", ErrMigration, migrationsTable, err)
	}
	if found && !module {
		return ErrLegacyHistory
	}
	return nil
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

// MustSub returns the sub-tree dir of fsys. The migrator reads a module's
// stream for a dialect through it, from the directory named like the
// dialect, and a dialect's name is fixed when its driver is built, so a name
// that is not a valid path is a programming error rather than a run-time
// one. It panics when fs.Sub refuses dir, as it does a path that is not
// valid; a directory that does not exist surfaces as the error
// LoadMigrations returns.
func MustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(fmt.Sprintf("sqlcommon: migrations directory %q: %v", dir, err))
	}
	return sub
}
