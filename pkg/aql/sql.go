package aql

import (
	"fmt"
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

// Compiled is a query as SQL over the store's items and item_properties
// tables (internal/sqlstore migrations): the caller selects its item
// columns FROM items, then appends " WHERE " + Where and Tail, binding
// Args in order. Placeholders are "?"; a PostgreSQL caller rebinds them.
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

// repoExpr is the repository key as AQL reports it: a remote's cached items
// are in "<key>-cache".
const repoExpr = "(CASE WHEN items.cached THEN items.repo_key || '" + types.CacheSuffix +
	"' ELSE items.repo_key END)"

type compiler struct {
	d    Dialect
	now  time.Time
	args []any
	b    strings.Builder
}

// Compile writes q as SQL for d. Every value is bound, never interpolated.
func (q *Query) Compile(d Dialect, o Options) (Compiled, error) {
	c := &compiler{d: d, now: o.now()}
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
			c.b.WriteString(c.ordered(f))
			c.b.WriteString(dir)
			if fieldKinds[f] == kindTime {
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
	c.b.WriteString("items.id ASC")
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
		c.property(n)
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

func (c *compiler) field(f *FieldCriterion) {
	k := fieldKinds[f.Field]
	switch {
	case k == kindType && f.Text == "any":
		if f.Op == OpEq {
			c.b.WriteString("1=1")
		} else {
			c.b.WriteString("1=0")
		}
	case k == kindType:
		c.b.WriteString(columns[FieldType] + sqlOps[f.Op] + c.bind(f.Text))
	case k == kindNumber:
		c.b.WriteString(columns[f.Field] + sqlOps[f.Op] + c.bind(f.Number))
	case k == kindTime && f.Op == OpBefore:
		c.b.WriteString(columns[f.Field] + " < " + c.bind(f.Span.back(c.now).UnixMicro()))
	case k == kindTime && f.Op == OpLast:
		c.b.WriteString(columns[f.Field] + " > " + c.bind(f.Span.back(c.now).UnixMicro()))
	case k == kindTime:
		c.b.WriteString(columns[f.Field] + sqlOps[f.Op] + c.bind(f.Time.UnixMicro()))
	case f.Field == FieldRepo && (f.Op == OpEq || f.Op == OpNe):
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
		c.text(columns[f.Field], f.Op, f.Text)
	default:
		c.text(c.ordered(f.Field), f.Op, f.Text)
	}
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
func (c *compiler) ordered(f Field) string {
	expr := columns[f]
	if f == FieldRepo {
		expr = repoExpr
	}
	if c.d == PostgreSQL && (fieldKinds[f] == kindText || fieldKinds[f] == kindType) {
		return expr + ` COLLATE "C"`
	}
	return expr
}

// property writes an EXISTS over the item's properties, NOT EXISTS for $ne
// and $nmatch, so each @key criterion is independent of the others.
func (c *compiler) property(p *PropertyCriterion) {
	op, exists := p.Op, "EXISTS"
	switch p.Op {
	case OpNe:
		op, exists = OpEq, "NOT EXISTS"
	case OpNMatch:
		op, exists = OpMatch, "NOT EXISTS"
	}
	c.b.WriteString(exists + " (SELECT 1 FROM item_properties p WHERE p.item_id = items.id")
	if p.Key != "*" {
		c.b.WriteString(" AND p.key = " + c.bind(p.Key))
	}
	if p.Value != "*" || (op != OpEq && op != OpMatch) {
		value := "p.value"
		if c.d == PostgreSQL && op != OpEq {
			value += ` COLLATE "C"`
		}
		c.b.WriteString(" AND ")
		c.text(value, op, p.Value)
	}
	c.b.WriteByte(')')
}
