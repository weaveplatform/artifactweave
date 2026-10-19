package aql

import (
	"cmp"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Hit is one item a query found, with its properties and statistics when
// the query includes them. Stats is nil for an item that has no statistics
// record. VirtualRepos is filled by the caller, which knows the repository
// configurations, when the query includes virtual_repos.
type Hit struct {
	Item         types.Item
	Properties   types.Properties
	Stats        *types.ItemStats
	VirtualRepos []string
}

// Select runs q over candidates the way the SQL compiler's output does:
// filter, sort (by ID when the query does not sort, and by ID within equal
// sort keys), then offset and limit. Properties and statistics are dropped
// unless the query includes them. It is the reference the SQL backends are
// tested against; candidates must carry their properties and statistics.
// Relation paths reach no builds here; SelectItems joins them.
func Select(q *Query, o Options, candidates []Hit) []Hit {
	return SelectItems(q, o, &Corpus{Items: candidates})
}

// Match reports whether the hit's item, with its properties and statistics,
// satisfies the criteria. Relation paths reach no rows here.
func (q *Query) Match(h Hit, now time.Time) bool {
	if q.Criteria == nil {
		return true
	}
	e := env{c: &Corpus{}, now: now, self: &row{rel: relItem, hit: &h}}
	return e.eval(q.Criteria)
}

func match(n Node, h Hit, now time.Time) bool {
	switch n := n.(type) {
	case And:
		for _, c := range n {
			if !match(c, h, now) {
				return false
			}
		}
		return true
	case Or:
		for _, c := range n {
			if match(c, h, now) {
				return true
			}
		}
		return false
	case *FieldCriterion:
		return n.match(h.Item, now)
	case *PropertyCriterion:
		return n.match(h.Properties)
	case MSP:
		return n.match(h.Properties)
	case *StatCriterion:
		return n.match(h.Stats, now)
	case RepoSet:
		return n.match(h.Item)
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

// match holds when some property satisfies the positive operator; $ne and
// $nmatch hold when none does.
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
		for _, v := range vs {
			if c.row(positive, k, v) {
				found = true
			}
		}
	}
	return found != negate
}

// row reports whether the property k=v satisfies the criterion with op in
// place of its own operator.
func (c *PropertyCriterion) row(op Op, k, v string) bool {
	switch c.Part {
	case PartKey:
		return valueMatches(op, c.Value, k)
	case PartValue:
		return valueMatches(op, c.Value, v)
	}
	return (c.Key == "*" || k == c.Key) && valueMatches(op, c.Value, v)
}

// match holds when one property satisfies every criterion. A negative
// operator tests that property's own key or value, so "*" there excludes
// everything.
func (m MSP) match(props types.Properties) bool {
	for k, vs := range props {
		for _, v := range vs {
			if m.row(k, v) {
				return true
			}
		}
	}
	return false
}

func (m MSP) row(k, v string) bool {
	for _, c := range m {
		var ok bool
		switch c.Op {
		case OpNe, OpNMatch:
			positive := OpEq
			if c.Op == OpNMatch {
				positive = OpMatch
			}
			switch c.Part {
			case PartKey:
				ok = !valueMatches(positive, c.Value, k)
			case PartValue:
				ok = !valueMatches(positive, c.Value, v)
			default:
				ok = (c.Key == "*" || k == c.Key) && !valueMatches(positive, c.Value, v)
			}
		default:
			ok = c.row(c.Op, k, v)
		}
		if !ok {
			return false
		}
	}
	return true
}

// match compares the statistic. With no statistics record every field is
// NULL; an existing record's empty time is NULL too, while its texts and
// counts are values ("" and 0), as the SQL columns hold them.
func (c *StatCriterion) match(st *types.ItemStats, now time.Time) bool {
	null := st == nil
	if !null && statKinds[c.Field] == kindTime {
		null = statTime(st, c.Field).IsZero()
	}
	if c.Null {
		return null == (c.Op == OpEq)
	}
	if null {
		return false
	}
	switch statKinds[c.Field] {
	case kindNumber:
		return compareOp(c.Op, cmp.Compare(statNumber(st, c.Field), c.Number))
	case kindTime:
		t := statTime(st, c.Field)
		switch c.Op {
		case OpBefore:
			return t.Before(micro(c.Span.back(now)))
		case OpLast:
			return t.After(micro(c.Span.back(now)))
		}
		return compareOp(c.Op, t.Compare(micro(c.Time)))
	}
	s := statText(st, c.Field)
	switch c.Op {
	case OpMatch:
		return globMatch(c.Text, s)
	case OpNMatch:
		return !globMatch(c.Text, s)
	}
	return compareOp(c.Op, strings.Compare(s, c.Text))
}

func statNumber(st *types.ItemStats, f StatField) int64 {
	if f == StatRemoteDownloads {
		return st.RemoteDownloadCount
	}
	return st.DownloadCount
}

// statTime is a statistic time truncated to the microseconds the SQL stores
// keep.
func statTime(st *types.ItemStats, f StatField) time.Time {
	t := st.LastDownloaded
	if f == StatRemoteDownloaded {
		t = st.RemoteLastDownloaded
	}
	if t.IsZero() {
		return t
	}
	return micro(t)
}

func statText(st *types.ItemStats, f StatField) string {
	switch f {
	case StatDownloadedBy:
		return st.LastDownloadedBy
	case StatRemoteDownloadedBy:
		return st.RemoteLastDownloadedBy
	case StatRemoteOrigin:
		return st.RemoteOrigin
	default:
		return st.RemotePath
	}
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
// IDs tie only between roots without a record.
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
	// Roots without a record (RootHit) all have ID 0, before every stored
	// item; their keys order them among themselves.
	return cmp.Or(
		cmp.Compare(a.ID, b.ID), cmp.Compare(a.RepoKey, b.RepoKey), compareBool(a.Cached, b.Cached),
	)
}

// compareBool orders false before true.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
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
