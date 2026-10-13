package aql

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Hit is one item a query found, with its properties when the query
// includes them.
type Hit struct {
	Item       types.Item
	Properties types.Properties
}

// Select runs q over candidates the way the SQL compiler's output does:
// filter, sort (by ID when the query does not sort, and by ID within equal
// sort keys), then offset and limit. Properties are dropped unless the
// query includes them. It is the reference the SQL backends are tested
// against.
func Select(q *Query, o Options, candidates []Hit) []Hit {
	now := o.now()
	var out []Hit
	for _, h := range candidates {
		if q.Match(h.Item, h.Properties, now) {
			if !q.Properties() {
				h.Properties = nil
			}
			out = append(out, h)
		}
	}
	slices.SortFunc(out, func(a, b Hit) int { return q.Compare(a.Item, b.Item) })
	offset, limit := q.Window(o)
	out = out[min(offset, len(out)):]
	if limit >= 0 && limit < len(out) {
		out = out[:limit]
	}
	return out
}

// Match reports whether the item satisfies the criteria.
func (q *Query) Match(it types.Item, props types.Properties, now time.Time) bool {
	return q.Criteria == nil || match(q.Criteria, it, props, now)
}

func match(n Node, it types.Item, props types.Properties, now time.Time) bool {
	switch n := n.(type) {
	case And:
		for _, c := range n {
			if !match(c, it, props, now) {
				return false
			}
		}
		return true
	case Or:
		for _, c := range n {
			if match(c, it, props, now) {
				return true
			}
		}
		return false
	case *FieldCriterion:
		return n.match(it, now)
	case *PropertyCriterion:
		return n.match(props)
	}
	return false
}

func (c *FieldCriterion) match(it types.Item, now time.Time) bool {
	switch fieldKinds[c.Field] {
	case kindType:
		if c.Text == "any" {
			return c.Op == OpEq
		}
		return (itemType(it) == c.Text) == (c.Op == OpEq)
	case kindNumber:
		return compareOp(c.Op, cmp.Compare(number(it, c.Field), c.Number))
	case kindTime:
		// A missing time is SQL NULL: no comparison holds, $ne included.
		t := itemTime(it, c.Field)
		if t.IsZero() {
			return false
		}
		switch c.Op {
		case OpBefore:
			return t.Before(micro(c.Span.back(now)))
		case OpLast:
			return t.After(micro(c.Span.back(now)))
		}
		return compareOp(c.Op, t.Compare(micro(c.Time)))
	}
	s := text(it, c.Field)
	switch c.Op {
	case OpMatch:
		return globMatch(c.Text, s)
	case OpNMatch:
		return !globMatch(c.Text, s)
	}
	return compareOp(c.Op, strings.Compare(s, c.Text))
}

// match holds when some value of the key satisfies the positive operator;
// $ne and $nmatch hold when none does.
func (c *PropertyCriterion) match(props types.Properties) bool {
	positive, negate := c.Op, false
	switch c.Op {
	case OpNe:
		positive, negate = OpEq, true
	case OpNMatch:
		positive, negate = OpMatch, true
	}
	found := false
	for k, vs := range props {
		if c.Key != "*" && k != c.Key {
			continue
		}
		for _, v := range vs {
			if valueMatches(positive, c.Value, v) {
				found = true
			}
		}
	}
	return found != negate
}

func valueMatches(op Op, want, got string) bool {
	switch {
	case want == "*" && (op == OpEq || op == OpMatch):
		return true
	case op == OpMatch:
		return globMatch(want, got)
	}
	return compareOp(op, strings.Compare(got, want))
}

// compareOp applies an ordering operator to c, the comparison of the item's
// value with the criterion's.
func compareOp(op Op, c int) bool {
	switch op {
	case OpEq:
		return c == 0
	case OpNe:
		return c != 0
	case OpGt:
		return c > 0
	case OpGte:
		return c >= 0
	case OpLt:
		return c < 0
	case OpLte:
		return c <= 0
	}
	return false
}

// Compare orders two items by the query's sort, then by ID, as the SQL
// ORDER BY does. A missing time sorts first ascending and last descending.
func (q *Query) Compare(a, b types.Item) int {
	if q.Sort != nil {
		for _, f := range q.Sort.Fields {
			var c int
			switch fieldKinds[f] {
			case kindNumber:
				c = cmp.Compare(number(a, f), number(b, f))
			case kindTime:
				c = itemTime(a, f).Compare(itemTime(b, f))
			case kindType:
				c = strings.Compare(itemType(a), itemType(b))
			default:
				c = strings.Compare(text(a, f), text(b, f))
			}
			if q.Sort.Desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
	}
	return cmp.Compare(a.ID, b.ID)
}

func itemType(it types.Item) string {
	if it.Folder {
		return "folder"
	}
	return "file"
}

func number(it types.Item, f Field) int64 {
	if f == FieldDepth {
		return int64(it.Depth)
	}
	return it.Size
}

func itemTime(it types.Item, f Field) time.Time {
	switch f {
	case FieldCreated:
		return it.Created
	case FieldModified:
		return it.Modified
	default:
		return it.Updated
	}
}

func text(it types.Item, f Field) string {
	switch f {
	case FieldRepo:
		return it.ReportedRepoKey()
	case FieldPath:
		return it.Path
	case FieldName:
		return it.Name
	case FieldCreatedBy:
		return it.CreatedBy
	case FieldModifiedBy:
		return it.ModifiedBy
	case FieldOriginalMD5:
		return it.Original.MD5
	case FieldActualMD5:
		return it.Actual.MD5
	case FieldOriginalSHA1:
		return it.Original.SHA1
	case FieldActualSHA1:
		return it.Actual.SHA1
	default:
		return it.Actual.SHA256
	}
}

// micro truncates t to the microseconds the SQL stores keep, so a
// criterion compares the same on every backend.
func micro(t time.Time) time.Time { return time.UnixMicro(t.UnixMicro()).UTC() }
