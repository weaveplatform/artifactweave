// Package aql parses Artifactory Query Language and runs it (decision 0012).
// It covers the items domain in the shape the JFrog CLI generates for every
// file specification (research 09): $and and $or, field comparisons, $match
// and $nmatch globs, @property criteria, and the include, sort, offset,
// limit, transitive and distinct suffixes.
//
// A parsed Query is run two ways that must agree row for row: Match and
// Compare are the reference semantics (the in-memory store uses them), and
// Compile turns the query into SQL for SQLite or PostgreSQL. The store
// contract suite runs the same queries on every backend and expects the
// same ordered rows.
package aql

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// MaxLength is the longest query accepted, in characters: Artifactory's
// artifactory.aql.search.query.size.limit default.
const MaxLength = 6000

// maxDepth bounds $and/$or nesting, so a hostile query cannot exhaust the
// stack of the parser or of the database planner.
const maxDepth = 32

var (
	// ErrSyntax reports a query that does not parse.
	ErrSyntax = errors.New("aql: syntax error")
	// ErrUnsupported reports valid AQL that artifactweave does not serve:
	// another domain, a secondary-domain field, or an operator outside the
	// supported set.
	ErrUnsupported = errors.New("aql: unsupported")
	// ErrTooLong reports a query above MaxLength characters. Its text is
	// Artifactory's message.
	ErrTooLong = errors.New("AQL query is too long")
	// ErrRestricted reports a query a non-admin caller may not run.
	ErrRestricted = errors.New("aql: restricted")
)

// Field is an item field name as AQL spells it.
type Field string

// Item fields. Checksums are the actual ones unless named original.
const (
	FieldRepo         Field = "repo"
	FieldPath         Field = "path"
	FieldName         Field = "name"
	FieldType         Field = "type"
	FieldSize         Field = "size"
	FieldDepth        Field = "depth"
	FieldCreated      Field = "created"
	FieldCreatedBy    Field = "created_by"
	FieldModified     Field = "modified"
	FieldModifiedBy   Field = "modified_by"
	FieldUpdated      Field = "updated"
	FieldOriginalMD5  Field = "original_md5"
	FieldActualMD5    Field = "actual_md5"
	FieldOriginalSHA1 Field = "original_sha1"
	FieldActualSHA1   Field = "actual_sha1"
	FieldSHA256       Field = "sha256"
)

// kind is how a field's values are compared.
type kind int

const (
	kindText kind = iota
	kindNumber
	kindTime
	kindType
)

// fieldOrder lists the item fields in the order results render them.
var fieldOrder = []Field{
	FieldRepo, FieldPath, FieldName, FieldType, FieldSize, FieldDepth,
	FieldCreated, FieldCreatedBy, FieldModified, FieldModifiedBy, FieldUpdated,
	FieldOriginalMD5, FieldActualMD5, FieldOriginalSHA1, FieldActualSHA1, FieldSHA256,
}

// fieldKinds maps each supported item field to its kind.
var fieldKinds = map[Field]kind{
	FieldRepo: kindText, FieldPath: kindText, FieldName: kindText, FieldType: kindType,
	FieldSize: kindNumber, FieldDepth: kindNumber,
	FieldCreated: kindTime, FieldModified: kindTime, FieldUpdated: kindTime,
	FieldCreatedBy: kindText, FieldModifiedBy: kindText,
	FieldOriginalMD5: kindText, FieldActualMD5: kindText,
	FieldOriginalSHA1: kindText, FieldActualSHA1: kindText, FieldSHA256: kindText,
}

// defaultFields are what an items query returns without an include of item
// fields, as Artifactory documents for the items domain.
var defaultFields = []Field{
	FieldRepo, FieldPath, FieldName, FieldType, FieldSize,
	FieldCreated, FieldCreatedBy, FieldModified, FieldModifiedBy, FieldUpdated,
}

// Op is a comparison operator.
type Op string

// Operators. Before and Last are relative times, resolved against the
// caller's clock when the query runs.
const (
	OpEq     Op = "$eq"
	OpNe     Op = "$ne"
	OpGt     Op = "$gt"
	OpGte    Op = "$gte"
	OpLt     Op = "$lt"
	OpLte    Op = "$lte"
	OpMatch  Op = "$match"
	OpNMatch Op = "$nmatch"
	OpBefore Op = "$before"
	OpLast   Op = "$last"
)

// Node is a criteria tree node: And, Or, *FieldCriterion or
// *PropertyCriterion.
type Node interface{ node() }

// And holds when every child holds; an empty And always holds.
type And []Node

// Or holds when any child holds; an empty Or never holds.
type Or []Node

// FieldCriterion compares an item field. Exactly one of Text, Number and
// Time is meaningful, by the field's kind; Before and Last carry their
// span in Span instead.
type FieldCriterion struct {
	Field  Field
	Op     Op
	Text   string
	Number int64
	Time   time.Time
	Span   Span
}

// PropertyCriterion matches items by property. Key "*" is any key; Value
// "*" with $eq (or $match) is any value. A negative operator ($ne,
// $nmatch) holds when the item has no property of Key whose value matches
// the positive form, so items without the key match it too.
type PropertyCriterion struct {
	Key   string
	Op    Op
	Value string
}

func (And) node()                {}
func (Or) node()                 {}
func (*FieldCriterion) node()    {}
func (*PropertyCriterion) node() {}

// Span is a relative time for $before and $last: N units back from now.
type Span struct {
	N    int
	Unit string
}

// back returns now moved back by the span.
func (s Span) back(now time.Time) time.Time {
	switch s.Unit {
	case "mo":
		return now.AddDate(0, -s.N, 0)
	case "y":
		return now.AddDate(-s.N, 0, 0)
	}
	return now.Add(-time.Duration(s.N) * spanUnits[s.Unit])
}

// spanUnits are the fixed-length units of $before and $last; months and
// years are calendar units handled in back.
var spanUnits = map[string]time.Duration{
	"ms": time.Millisecond, "s": time.Second, "minutes": time.Minute,
	"d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
}

// Sort orders results by Fields, all ascending or all descending.
type Sort struct {
	Desc   bool
	Fields []Field
}

// Query is a parsed items query.
type Query struct {
	// Criteria is the find criteria; it already carries the implicit
	// type=file that applies when the query names no type.
	Criteria Node
	// Include is the include list as given, in order.
	Include []string
	// Sort is nil when the query does not sort.
	Sort *Sort
	// Offset and Limit are the suffixes; HasLimit tells .limit(0) from no
	// limit.
	Offset   int
	Limit    int
	HasLimit bool
	// Transitive and Distinct are accepted for compatibility and have no
	// effect: items rows are always distinct, and artifactweave has no
	// virtual-repository search to make transitive.
	Transitive bool
	Distinct   bool
}

// Options are the caller's settings for running a query.
type Options struct {
	// Now resolves $before and $last; zero means time.Now.
	Now time.Time
	// Limit caps a query that has no limit of its own; zero means no cap.
	Limit int
}

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

// Window returns the offset and the row limit to apply; a negative limit
// means none.
func (q *Query) Window(o Options) (offset, limit int) {
	switch {
	case q.HasLimit:
		return q.Offset, q.Limit
	case o.Limit > 0:
		return q.Offset, o.Limit
	default:
		return q.Offset, -1
	}
}

// Fields returns the item fields each result row carries, in render order:
// the included item fields, or the default fields when the include names
// none (an include of only "property" still returns the defaults).
func (q *Query) Fields() []Field {
	var out []Field
	for _, f := range fieldOrder {
		if slices.Contains(q.Include, string(f)) || slices.Contains(q.Include, "*") {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return slices.Clone(defaultFields)
	}
	return out
}

// Properties reports whether result rows carry the item's properties.
func (q *Query) Properties() bool {
	return slices.Contains(q.Include, "property") || slices.Contains(q.Include, "property.*") ||
		slices.Contains(q.Include, "property.key") || slices.Contains(q.Include, "property.value")
}

// CheckRestricted applies decision 0012's rule for non-admin callers: they
// may query only items and every result must carry repo, path and name, so
// each row can be checked against the caller's permissions.
func (q *Query) CheckRestricted() error {
	fields := q.Fields()
	for _, f := range []Field{FieldRepo, FieldPath, FieldName} {
		if !slices.Contains(fields, f) {
			return fmt.Errorf(
				"%w: non-admin queries must include repo, path and name",
				ErrRestricted,
			)
		}
	}
	return nil
}
