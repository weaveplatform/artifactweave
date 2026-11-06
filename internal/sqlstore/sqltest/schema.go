package sqltest

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ErrUnknownDialect reports a dialect name that Schema cannot describe.
var ErrUnknownDialect = errors.New("sqltest: unknown dialect")

// historyTable is matched by table name, not object name, so its automatic
// index, primary key and any sequence leave the description with it.
const historyTable = "schema_migrations"

// Schema returns a normalised, deterministic description of every table,
// index, constraint and sequence in db, plus the row count of every table
// that holds rows. It leaves out every catalog object of schema_migrations,
// whose shape the per-module migration streams change.
//
// dialect is a sqlcommon.Dialect name, "sqlite" or "postgres"; any other
// name returns ErrUnknownDialect. The description holds nothing that differs
// between runs or hosts, such as object ids, SQLite root pages or the
// database's default collation, so two databases built by the same schema
// compare equal as strings, line by line.
func Schema(ctx context.Context, db *sql.DB, dialect string) (string, error) {
	var describe func(context.Context, *sql.DB) (lines, tables []string, err error)
	switch dialect {
	case "sqlite":
		describe = sqliteSchema
	case "postgres":
		describe = postgresSchema
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownDialect, dialect)
	}
	lines, tables, err := describe(ctx, db)
	if err != nil {
		return "", err
	}
	for _, table := range tables {
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(table)).
			Scan(&n); err != nil {
			return "", fmt.Errorf("sqltest: count rows of %s: %w", table, err)
		}
		if n > 0 {
			lines = append(lines, "rows "+table+" "+strconv.FormatInt(n, 10))
		}
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// sqliteQuery reads the SQLite catalog except the history table, passed as
// the argument. The catalog's BINARY collation orders names byte by byte.
const sqliteQuery = "SELECT type, name, tbl_name, sql FROM sqlite_master " +
	"WHERE tbl_name <> ? ORDER BY type, name"

// sqliteSchema describes every sqlite_master row in (type, name) order as
// type, name, table and normalised CREATE statement. Automatic indexes have
// no statement, so their line ends at the table name.
func sqliteSchema(ctx context.Context, db *sql.DB) (lines, tables []string, err error) {
	err = each(ctx, db, sqliteQuery, func(rows *sql.Rows) error {
		var kind, name, table string
		var stmt sql.NullString
		if err := rows.Scan(&kind, &name, &table, &stmt); err != nil {
			return fmt.Errorf("sqltest: scan sqlite_master: %w", err)
		}
		line := kind + " " + name + " " + table
		if stmt.Valid {
			line += " " + normaliseSQL(stmt.String)
		}
		lines = append(lines, line)
		if kind == "table" {
			tables = append(tables, name)
		}
		return nil
	})
	return lines, tables, err
}

// normaliseSQL makes SQLite's stored CREATE text independent of the history
// that wrote it. ALTER TABLE ADD COLUMN splices the new column into the
// stored text as typed, and RENAME rewrites the name in double quotes, so a
// table built by one CREATE reads differently from the same table built by a
// CREATE and later ALTERs. It strips -- comments and double quotes, collapses
// whitespace runs to one space and removes spaces next to "(", ")" and ",".
// Single-quoted literals are copied unchanged.
func normaliseSQL(stmt string) string {
	var b strings.Builder
	space, literal := false, false
	for i := 0; i < len(stmt); i++ {
		c := stmt[i]
		switch {
		case literal:
			literal = c != '\''
			b.WriteByte(c)
			continue
		case c == '"':
			continue
		case c == '-' && strings.HasPrefix(stmt[i:], "--"):
			for i < len(stmt) && stmt[i] != '\n' {
				i++
			}
			space = true
			continue
		case strings.IndexByte(" \t\n\r\f\v", c) >= 0:
			space = true
			continue
		}
		if space && b.Len() > 0 && !tight(c) && !tight(b.String()[b.Len()-1]) {
			b.WriteByte(' ')
		}
		space, literal = false, c == '\''
		b.WriteByte(c)
	}
	return b.String()
}

// tight reports the punctuation normaliseSQL writes with no space beside it.
func tight(c byte) bool { return c == '(' || c == ')' || c == ',' }

// pgKind orders the lines of one PostgreSQL table and names them.
type pgKind int

const (
	pgTable pgKind = iota
	pgColumn
	pgConstraint
	pgIndex
	pgSequence
)

func (k pgKind) String() string {
	return [...]string{"table", "column", "constraint", "index", "sequence"}[k]
}

// pgLine is one line of the PostgreSQL description. Columns sort by pos, their
// attnum, so they keep their declared order; the other kinds sort by name.
type pgLine struct {
	table string
	kind  pgKind
	pos   int64
	name  string
	text  string
}

// The PostgreSQL queries read the tables of the current schema, where the
// migrations create them, except the history table passed as $1. Results are
// sorted in Go, in byte order, so no line depends on the server's locale.
const (
	pgTablesQuery = `
SELECT c.relname::text
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p') AND c.relname <> $1`

	// A column's collation is written only when it is not its type's default,
	// so the database's own default collation never reaches the description.
	pgColumnsQuery = `
SELECT c.relname::text, a.attnum, a.attname::text,
	pg_catalog.format_type(a.atttypid, a.atttypmod),
	CASE WHEN a.attcollation <> t.typcollation THEN COALESCE(quote_ident(co.collname), '') ELSE '' END,
	a.attnotnull,
	COALESCE(pg_catalog.pg_get_expr(d.adbin, d.adrelid), ''),
	a.attidentity::text,
	a.attgenerated::text
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
JOIN pg_catalog.pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
LEFT JOIN pg_catalog.pg_collation co ON co.oid = a.attcollation
WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p') AND c.relname <> $1`

	pgConstraintsQuery = `
SELECT c.relname::text, k.conname::text, pg_catalog.pg_get_constraintdef(k.oid)
FROM pg_catalog.pg_constraint k
JOIN pg_catalog.pg_class c ON c.oid = k.conrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p') AND c.relname <> $1`

	// pg_get_indexdef, behind indexdef, qualifies the table with its schema
	// whatever the search_path; dropping the qualifier keeps the name of the
	// test database's schema out of the golden.
	pgIndexesQuery = `
SELECT tablename::text, indexname::text,
	replace(indexdef, ' ON ' || quote_ident(schemaname) || '.', ' ON ')
FROM pg_catalog.pg_indexes
WHERE schemaname = current_schema() AND tablename <> $1`

	// Serial columns own their sequence through an automatic dependency and
	// identity columns through an internal one.
	pgSequencesQuery = `
SELECT c.relname::text, s.relname::text, format(
	'AS %s START WITH %s INCREMENT BY %s MINVALUE %s MAXVALUE %s CACHE %s %s OWNED BY %s.%s',
	pg_catalog.format_type(q.seqtypid, NULL), q.seqstart, q.seqincrement, q.seqmin, q.seqmax,
	q.seqcache, CASE WHEN q.seqcycle THEN 'CYCLE' ELSE 'NO CYCLE' END, c.relname, a.attname)
FROM pg_catalog.pg_depend dep
JOIN pg_catalog.pg_class s ON s.oid = dep.objid AND s.relkind = 'S'
JOIN pg_catalog.pg_sequence q ON q.seqrelid = s.oid
JOIN pg_catalog.pg_class c ON c.oid = dep.refobjid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = dep.refobjsubid
WHERE dep.classid = 'pg_catalog.pg_class'::regclass
	AND dep.refclassid = 'pg_catalog.pg_class'::regclass
	AND dep.deptype IN ('a', 'i')
	AND n.nspname = current_schema() AND c.relkind IN ('r', 'p') AND c.relname <> $1`
)

// postgresSchema describes each table of the current schema in name order:
// its columns in attnum order, then its constraints, indexes and owned
// sequences, each by name.
func postgresSchema(ctx context.Context, db *sql.DB) (lines, tables []string, err error) {
	var all []pgLine
	for _, step := range []struct {
		query string
		scan  func(*sql.Rows) (pgLine, error)
	}{
		{pgTablesQuery, scanPGTable},
		{pgColumnsQuery, scanPGColumn},
		{pgConstraintsQuery, scanPGDefinition(pgConstraint)},
		{pgIndexesQuery, scanPGDefinition(pgIndex)},
		{pgSequencesQuery, scanPGDefinition(pgSequence)},
	} {
		err := each(ctx, db, step.query, func(rows *sql.Rows) error {
			l, err := step.scan(rows)
			all = append(all, l)
			return err
		})
		if err != nil {
			return nil, nil, err
		}
	}
	slices.SortFunc(all, func(a, b pgLine) int {
		return cmp.Or(
			strings.Compare(a.table, b.table),
			cmp.Compare(a.kind, b.kind),
			cmp.Compare(a.pos, b.pos),
			strings.Compare(a.name, b.name),
		)
	})
	lines = make([]string, 0, len(all))
	for _, l := range all {
		if l.kind == pgTable {
			tables = append(tables, l.table)
			lines = append(lines, "table "+l.table)
			continue
		}
		lines = append(lines, l.kind.String()+" "+l.table+"."+l.name+" "+l.text)
	}
	return lines, tables, nil
}

func scanPGTable(rows *sql.Rows) (pgLine, error) {
	l := pgLine{kind: pgTable}
	if err := rows.Scan(&l.table); err != nil {
		return l, fmt.Errorf("sqltest: scan table: %w", err)
	}
	return l, nil
}

// scanPGColumn writes a column as DDL: type, collation, NOT NULL, then its
// default, generation expression or identity.
func scanPGColumn(rows *sql.Rows) (pgLine, error) {
	l := pgLine{kind: pgColumn}
	var typ, collation, expr, identity, generated string
	var notNull bool
	if err := rows.Scan(&l.table, &l.pos, &l.name,
		&typ, &collation, &notNull, &expr, &identity, &generated); err != nil {
		return l, fmt.Errorf("sqltest: scan column: %w", err)
	}
	spec := []string{typ}
	if collation != "" {
		spec = append(spec, "COLLATE "+collation)
	}
	if notNull {
		spec = append(spec, "NOT NULL")
	}
	// pg_attrdef holds a generated column's expression where a default would
	// be. attgenerated is "s" (stored) on PostgreSQL 17; 18 adds "v" (virtual).
	switch generated {
	case "":
		if expr != "" {
			spec = append(spec, "DEFAULT "+expr)
		}
	case "s":
		spec = append(spec, "GENERATED ALWAYS AS ("+expr+") STORED")
	default:
		spec = append(spec, "GENERATED ALWAYS AS ("+expr+") VIRTUAL")
	}
	switch identity {
	case "a":
		spec = append(spec, "GENERATED ALWAYS AS IDENTITY")
	case "d":
		spec = append(spec, "GENERATED BY DEFAULT AS IDENTITY")
	}
	l.text = strings.Join(spec, " ")
	return l, nil
}

// scanPGDefinition reads rows of table, name and definition for one kind.
func scanPGDefinition(kind pgKind) func(*sql.Rows) (pgLine, error) {
	return func(rows *sql.Rows) (pgLine, error) {
		l := pgLine{kind: kind}
		if err := rows.Scan(&l.table, &l.name, &l.text); err != nil {
			return l, fmt.Errorf("sqltest: scan %s: %w", kind, err)
		}
		return l, nil
	}
}

// each runs query with the history table as its only argument and calls scan
// for every row.
func each(ctx context.Context, db *sql.DB, query string, scan func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, query, historyTable)
	if err != nil {
		return fmt.Errorf("sqltest: describe schema: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqltest: describe schema: %w", err)
	}
	return nil
}

// quoteIdent quotes a catalog name for both dialects, which share the SQL
// standard's double-quoted identifiers.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
