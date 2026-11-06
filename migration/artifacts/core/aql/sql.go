package aql

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Dialect selects the SQL the compiler writes.
type Dialect int

// Dialects.
const (
	SQLite Dialect = iota
	PostgreSQL
)

// Compiled is a query as SQL over the store's tables (internal/sqlstore
// migrations): the caller selects its columns FROM the domain's table,
// items, builds or build_promotions, unaliased, then appends " WHERE " +
// Where and Tail, binding Args in order. Placeholders are "?"; a
// PostgreSQL caller rebinds them.
type Compiled struct {
	Where string
	Tail  string
	Args  []any
}

// columns maps item fields onto the items table. repo is absent: it is the
// reported key, see repoExpr.
var columns = map[Field]string{
	FieldPath: "items.path", FieldName: "items.name", FieldType: "items.type",
	FieldSize: "items.size", FieldDepth: "items.depth",
	FieldCreated: "items.created_at", FieldModified: "items.modified_at",
	FieldUpdated:   "items.updated_at",
	FieldCreatedBy: "items.created_by", FieldModifiedBy: "items.modified_by",
	FieldOriginalMD5: "items.original_md5", FieldActualMD5: "items.md5",
	FieldOriginalSHA1: "items.original_sha1", FieldActualSHA1: "items.sha1",
	FieldSHA256: "items.sha256",
}

// statColumns maps statistic fields onto the item_stats table, whose times
// are Unix microseconds and NULL when unset.
var statColumns = map[StatField]string{
	StatDownloaded: "last_downloaded_at", StatDownloads: "download_count",
	StatDownloadedBy: "last_downloaded_by", StatRemoteDownloads: "remote_download_count",
	StatRemoteDownloaded:   "remote_last_downloaded_at",
	StatRemoteDownloadedBy: "remote_last_downloaded_by",
	StatRemoteOrigin:       "remote_origin", StatRemotePath: "remote_path",
}

// repoExpr is the repository key as AQL reports it: a remote's cached items
// are in "<key>-cache".
const repoExpr = "(CASE WHEN items.cached THEN items.repo_key || '" + types.CacheSuffix +
	"' ELSE items.repo_key END)"

type compiler struct {
	d    Dialect
	now  time.Time
	args []any
	b    strings.Builder
	// primary is the domain of the found rows. alias maps each relation
	// path a Joined reaches to its table alias, "" to the found row's
	// table; nullable marks the aliases an outer join may leave null.
	primary  rel
	alias    map[string]string
	nullable map[string]bool
	seq      int
}

// Compile writes q as SQL for d. Every value is bound, never interpolated.
func (q *Query) Compile(d Dialect, o Options) (Compiled, error) {
	c := &compiler{
		d: d, now: o.now(), primary: q.Domain.rel(),
		alias: map[string]string{"": q.Domain.table()}, nullable: map[string]bool{},
	}
	if q.Criteria == nil {
		c.b.WriteString("1=1")
	} else if err := c.node(q.Criteria); err != nil {
		return Compiled{}, err
	}
	where, whereArgs := c.b.String(), c.args
	c.b.Reset()
	c.args = nil
	c.b.WriteString(" ORDER BY ")
	if q.Sort != nil {
		dir := " ASC"
		if q.Sort.Desc {
			dir = " DESC"
		}
		for _, f := range q.Sort.Fields {
			k, _ := c.primary.fieldKind(string(f))
			if c.primary == relItem {
				c.b.WriteString(c.ordered(f))
			} else {
				c.b.WriteString(c.orderedRel(c.alias[""], c.primary, string(f), k))
			}
			c.b.WriteString(dir)
			if k == kindTime {
				// NULL times sort as the smallest value, which SQLite does
				// natively and PostgreSQL only when told.
				if q.Sort.Desc {
					c.b.WriteString(" NULLS LAST")
				} else {
					c.b.WriteString(" NULLS FIRST")
				}
			}
			c.b.WriteString(", ")
		}
	}
	c.b.WriteString(c.alias[""] + ".id ASC")
	// The window counts the found rows alone. Properties and statistics are
	// never joined here (the store loads them for the rows this returns), so
	// an item with several properties is still one row to skip or count.
	offset, limit := q.Window(o)
	switch {
	case limit >= 0:
		c.b.WriteString(" LIMIT ? OFFSET ?")
		c.args = append(c.args, int64(limit), int64(offset))
	case offset > 0 && d == SQLite:
		// SQLite accepts OFFSET only after a LIMIT; -1 is no limit.
		c.b.WriteString(" LIMIT -1 OFFSET ?")
		c.args = append(c.args, int64(offset))
	case offset > 0:
		c.b.WriteString(" OFFSET ?")
		c.args = append(c.args, int64(offset))
	}
	return Compiled{Where: where, Tail: c.b.String(), Args: append(whereArgs, c.args...)}, nil
}

func (c *compiler) bind(v any) string {
	c.args = append(c.args, v)
	return "?"
}

func (c *compiler) node(n Node) error {
	switch n := n.(type) {
	case And:
		return c.join(n, " AND ", "1=1")
	case Or:
		return c.join(n, " OR ", "1=0")
	case *FieldCriterion:
		c.field(n)
		return nil
	case *PropertyCriterion:
		c.property(relItem, "items", n)
		return nil
	case MSP:
		c.msp(relItem, "items", n)
		return nil
	case *StatCriterion:
		c.stat(n)
		return nil
	case RepoSet:
		c.repoSet(n)
		return nil
	case *Joined:
		return c.joined(n)
	case *PathCriterion:
		c.guarded(n.Path, n.rel, func() { c.pathField(n) })
		return nil
	case *PathProperty:
		c.guarded(n.Path, n.rel, func() { c.pathProperty(n) })
		return nil
	case *PathMSP:
		c.guarded(n.Path, n.rel, func() { c.msp(n.rel, c.alias[n.Path], n.MSP) })
		return nil
	}
	return fmt.Errorf("%w: criteria node %T", ErrUnsupported, n)
}

func (c *compiler) join(children []Node, sep, empty string) error {
	if len(children) == 0 {
		c.b.WriteString(empty)
		return nil
	}
	c.b.WriteByte('(')
	for i, child := range children {
		if i > 0 {
			c.b.WriteString(sep)
		}
		if err := c.node(child); err != nil {
			return err
		}
	}
	c.b.WriteByte(')')
	return nil
}

// sqlOps are the comparison operators of the ordering Ops.
var sqlOps = map[Op]string{
	OpEq: " = ", OpNe: " <> ", OpGt: " > ", OpGte: " >= ", OpLt: " < ", OpLte: " <= ",
}

func (c *compiler) field(f *FieldCriterion) { c.fieldOf("items", f) }

// fieldOf writes a comparison of a field of the item at alias: the found
// item, or one a relation path reaches.
func (c *compiler) fieldOf(alias string, f *FieldCriterion) {
	col := func(f Field) string { return onAlias(alias, columns[f]) }
	k := fieldKinds[f.Field]
	switch {
	case k == kindType && f.Text == "any":
		if f.Op == OpEq {
			c.b.WriteString("1=1")
		} else {
			c.b.WriteString("1=0")
		}
	case k == kindType:
		c.b.WriteString(col(FieldType) + sqlOps[f.Op] + c.bind(f.Text))
	case k == kindNumber:
		c.b.WriteString(col(f.Field) + sqlOps[f.Op] + c.bind(f.Number))
	case k == kindTime:
		c.time(col(f.Field), f.Op, f.Time, f.Span)
	case f.Field == FieldRepo && (f.Op == OpEq || f.Op == OpNe) && alias == "items":
		// Spelled out on the stored key and cached flag so the natural-key
		// index serves the CLI's most common criterion.
		stored, cached := types.ResolveRepoKey(f.Text)
		if f.Op == OpNe {
			c.b.WriteString("NOT ")
		}
		c.b.WriteString("(items.repo_key = " + c.bind(stored) +
			" AND items.cached = " + c.bind(cached) + ")")
	case f.Op == OpEq || f.Op == OpNe:
		// Equality ignores collation, and the plain column keeps its index.
		c.text(c.itemExpr(alias, f.Field), f.Op, f.Text)
	default:
		c.text(c.orderedOf(alias, f.Field), f.Op, f.Text)
	}
}

// time writes a comparison of a time column, which holds Unix
// microseconds, with a criterion's time or relative span.
func (c *compiler) time(col string, op Op, t time.Time, s Span) {
	switch op {
	case OpBefore:
		c.b.WriteString(col + " < " + c.bind(s.back(c.now).UnixMicro()))
	case OpLast:
		c.b.WriteString(col + " > " + c.bind(s.back(c.now).UnixMicro()))
	default:
		c.b.WriteString(col + sqlOps[op] + c.bind(t.UnixMicro()))
	}
}

// repoSet writes membership of the item's stored location in s, grouped by
// the cached flag so each group is one IN over the natural-key index.
func (c *compiler) repoSet(s RepoSet) {
	if len(s.Refs) == 0 {
		if s.Negate {
			c.b.WriteString("1=1")
		} else {
			c.b.WriteString("1=0")
		}
		return
	}
	if s.Negate {
		c.b.WriteString("NOT ")
	}
	c.b.WriteByte('(')
	first := true
	for _, cached := range []bool{false, true} {
		var keys []string
		for _, r := range s.Refs {
			if r.Cached == cached {
				keys = append(keys, r.Key)
			}
		}
		if len(keys) == 0 {
			continue
		}
		if !first {
			c.b.WriteString(" OR ")
		}
		first = false
		c.b.WriteString("(items.cached = " + c.bind(cached) + " AND items.repo_key IN (")
		for i, k := range keys {
			if i > 0 {
				c.b.WriteString(", ")
			}
			c.b.WriteString(c.bind(k))
		}
		c.b.WriteString("))")
	}
	c.b.WriteByte(')')
}

// text writes a comparison of a text expression with a criterion value.
func (c *compiler) text(expr string, op Op, v string) {
	switch op {
	case OpMatch, OpNMatch:
		if op == OpNMatch {
			c.b.WriteString("NOT ")
		}
		if c.d == PostgreSQL {
			c.b.WriteString("(" + expr + " LIKE " + c.bind(likePattern(v)) +
				" ESCAPE '" + likeEscape + "')")
		} else {
			c.b.WriteString("(" + expr + " GLOB " + c.bind(sqliteGlob(v)) + ")")
		}
	default:
		c.b.WriteString(expr + sqlOps[op] + c.bind(v))
	}
}

// ordered is the expression a field compares and sorts by. PostgreSQL text
// is compared byte by byte (COLLATE "C"), as SQLite and Go compare it;
// otherwise the database's locale would reorder results between backends.
func (c *compiler) ordered(f Field) string { return c.orderedOf("items", f) }

func (c *compiler) orderedOf(alias string, f Field) string {
	expr := c.itemExpr(alias, f)
	if c.d == PostgreSQL && (fieldKinds[f] == kindText || fieldKinds[f] == kindType) {
		return expr + ` COLLATE "C"`
	}
	return expr
}

// itemExpr is the expression of an item field at alias; repo is the
// reported key.
func (c *compiler) itemExpr(alias string, f Field) string {
	if f == FieldRepo {
		return onAlias(alias, repoExpr)
	}
	return onAlias(alias, columns[f])
}

// onAlias moves an expression over the items table onto alias.
func onAlias(alias, expr string) string {
	if alias == "items" {
		return expr
	}
	return strings.ReplaceAll(expr, "items.", alias+".")
}

// property writes an EXISTS over the properties of the row at alias, of
// domain r, NOT EXISTS for $ne and $nmatch, so each property criterion is
// independent of the others.
func (c *compiler) property(r rel, alias string, p *PropertyCriterion) {
	op, exists := p.Op, "EXISTS"
	switch p.Op {
	case OpNe:
		op, exists = OpEq, "NOT EXISTS"
	case OpNMatch:
		op, exists = OpMatch, "NOT EXISTS"
	}
	c.b.WriteString(exists + " (SELECT 1 FROM " + propertyRows(r, alias))
	c.propertyRow(p, op)
	c.b.WriteByte(')')
}

// propertyRows selects, as p, the property rows of the row at alias.
// Builds and modules share build_properties, the build's own at
// module_position -1 (migration 0012).
func propertyRows(r rel, alias string) string {
	switch r {
	case relBuild:
		return "build_properties p WHERE p.build_id = " + alias + ".id AND p.module_position = -1"
	case relModule:
		return "build_properties p WHERE p.build_id = " + alias + ".build_id" +
			" AND p.module_position = " + alias + ".position"
	}
	return "item_properties p WHERE p.item_id = " + alias + ".id"
}

// msp writes one EXISTS whose property row satisfies every criterion. A
// negative operator is a condition on that row, and "*" there (any key or
// value) leaves no row to satisfy it.
func (c *compiler) msp(r rel, alias string, m MSP) {
	c.b.WriteString("EXISTS (SELECT 1 FROM " + propertyRows(r, alias))
	for _, p := range m {
		if p.Op != OpNe && p.Op != OpNMatch {
			c.propertyRow(p, p.Op)
			continue
		}
		col := c.propertyColumn(p)
		if p.Value == "*" {
			c.b.WriteString(" AND 1=0")
			continue
		}
		if c.d == PostgreSQL && p.Op == OpNMatch {
			col += ` COLLATE "C"`
		}
		c.b.WriteString(" AND ")
		c.text(col, p.Op, p.Value)
	}
	c.b.WriteByte(')')
}

// propertyRow writes the conditions one property row p meets for the
// criterion, with op in place of its own operator.
func (c *compiler) propertyRow(p *PropertyCriterion, op Op) {
	col := c.propertyColumn(p)
	if p.Value == "*" && (op == OpEq || op == OpMatch) {
		return
	}
	if c.d == PostgreSQL && col == "p.value" {
		c.valuePrefix(op, p.Value)
	}
	if c.d == PostgreSQL && op != OpEq {
		col += ` COLLATE "C"`
	}
	c.b.WriteString(" AND ")
	c.text(col, op, p.Value)
}

// valuePrefixLen is the length, in characters, of the property value prefix
// that PostgreSQL indexes (its migration 0013), because a whole value can be
// longer than a B-tree entry holds.
const valuePrefixLen = 255

// valuePrefix writes a condition on the indexed prefix of p.value that every
// row meeting op against v also meets, so PostgreSQL can find the rows
// through the index and then compare whole values. Under COLLATE "C",
// cutting two values to the same length can make them equal but never
// reverses their order, so a range holds for the prefixes with equality
// allowed.
func (c *compiler) valuePrefix(op Op, v string) {
	n := strconv.Itoa(valuePrefixLen)
	prefix := " AND left(p.value, " + n + ")"
	switch op {
	case OpEq:
		c.b.WriteString(prefix + " = left(" + c.bind(v) + ", " + n + ")")
	case OpGt, OpGte:
		c.b.WriteString(prefix + " >= left(" + c.bind(v) + ", " + n + ")")
	case OpLt, OpLte:
		c.b.WriteString(prefix + " <= left(" + c.bind(v) + ", " + n + ")")
	case OpMatch:
		// The literal characters before the first wildcard start every
		// match; LIKE with a literal prefix scans the index as a range.
		lit, _, _ := strings.Cut(v, "*")
		lit, _, _ = strings.Cut(lit, "?")
		if lit == "" {
			return
		}
		if r := []rune(lit); len(r) > valuePrefixLen {
			lit = string(r[:valuePrefixLen])
		}
		c.b.WriteString(prefix + " LIKE " + c.bind(likePattern(lit)+"%") +
			" ESCAPE '" + likeEscape + "'")
	}
}

// propertyColumn writes the key condition of an "@key" criterion and returns
// the column the criterion's value compares with.
func (c *compiler) propertyColumn(p *PropertyCriterion) string {
	switch p.Part {
	case PartKey:
		return "p.key"
	case PartValue:
		return "p.value"
	}
	if p.Key != "*" {
		c.b.WriteString(" AND p.key = " + c.bind(p.Key))
	}
	return "p.value"
}

// stat writes a comparison of a statistic. The scalar subquery is NULL for an
// item without statistics, so a comparison fails there as it does on a NULL
// time column, and null criteria are IS NULL tests.
func (c *compiler) stat(s *StatCriterion) {
	expr := "(SELECT s." + statColumns[s.Field] + " FROM item_stats s WHERE s.item_id = items.id)"
	k := statKinds[s.Field]
	switch {
	case s.Null && s.Op == OpEq:
		c.b.WriteString(expr + " IS NULL")
	case s.Null:
		c.b.WriteString(expr + " IS NOT NULL")
	case k == kindNumber:
		c.b.WriteString(expr + sqlOps[s.Op] + c.bind(s.Number))
	case k == kindTime && s.Op == OpBefore:
		c.b.WriteString(expr + " < " + c.bind(s.Span.back(c.now).UnixMicro()))
	case k == kindTime && s.Op == OpLast:
		c.b.WriteString(expr + " > " + c.bind(s.Span.back(c.now).UnixMicro()))
	case k == kindTime:
		c.b.WriteString(expr + sqlOps[s.Op] + c.bind(s.Time.UnixMicro()))
	default:
		if c.d == PostgreSQL && s.Op != OpEq && s.Op != OpNe {
			expr += ` COLLATE "C"`
		}
		c.text(expr, s.Op, s.Text)
	}
}
