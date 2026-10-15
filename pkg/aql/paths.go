package aql

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// PathCriterion compares a field of the row a relation path reaches from
// the row a query finds (research 22 §1.1): in an items query, Path
// "artifact.module.build" with Field "name" is
// "artifact.module.build.name". In a builds or build.promotions query, Path
// "" compares the found row's own fields. Exactly one of Text, Number and
// Time is meaningful, by the field's kind; Before and Last carry Span.
type PathCriterion struct {
	Path   string
	Field  string
	Op     Op
	Text   string
	Number int64
	Time   time.Time
	Span   Span
	rel    rel
	kind   kind
}

// PathProperty is a property criterion on the item, module or build Path
// reaches; Path "" in a builds query is the build's own properties.
type PathProperty struct {
	Path      string
	Criterion *PropertyCriterion
	rel       rel
}

// PathMSP is $msp on the properties of the row Path reaches.
type PathMSP struct {
	Path string
	MSP  MSP
	rel  rel
}

// Joined holds the criteria of a query's root that name relation paths. A
// row matches when one combination of the rows its paths reach satisfies
// them all at once, as when Artifactory's SQL joins each related table once
// per query: {"artifact.module.build.name":"a","artifact.module.build.number":"1"}
// needs one build named a with number 1, not a build named a and another
// numbered 1. A path that reaches no row takes the null row an outer join
// would, on which no criterion holds, so an alternative in an $or can
// still match. Parse gathers every root conjunct that names a path into one
// Joined; criteria on the found row alone stay outside it.
type Joined struct {
	Criteria And
}

func (*PathCriterion) node() {}
func (*PathProperty) node()  {}
func (*PathMSP) node()       {}
func (*Joined) node()        {}

// Joins reports whether q's criteria name relation paths, so running it
// needs the rows of the domains they reach.
func (q *Query) Joins() bool { return reaches(q.Criteria) }

// reaches reports whether n names a relation path.
func reaches(n Node) bool {
	switch n := n.(type) {
	case And:
		return slices.ContainsFunc(n, reaches)
	case Or:
		return slices.ContainsFunc(n, reaches)
	case *Joined:
		return true
	case *PathCriterion:
		return n.Path != ""
	case *PathProperty:
		return n.Path != ""
	case *PathMSP:
		return n.Path != ""
	}
	return false
}

// joinRoot gathers the root's conjuncts that name relation paths into one
// Joined, after the others.
func joinRoot(root And) And {
	var out, joined And
	for _, n := range root {
		if reaches(n) {
			joined = append(joined, n)
		} else {
			out = append(out, n)
		}
	}
	if joined == nil {
		return root
	}
	return append(out, &Joined{Criteria: joined})
}

// relCriterion reads a criterion on a field or the properties of the row a
// relation path reaches from the domain from. A key that steps nowhere is
// the found row of a builds or build.promotions query.
func relCriterion(m member, from rel) (Node, error) {
	path, to, rest, stepped := splitPath(from, m.key)
	if !stepped && from == relItem {
		return nil, fmt.Errorf(
			"%w: field %q (a domain items do not relate to)", ErrUnsupported, m.key,
		)
	}
	if isPropertyKey(rest) {
		if !to.hasProperties() {
			return nil, syntaxErr(m.pos, "unknown field %q", m.key)
		}
		n, err := property(member{key: rest, pos: m.pos, val: m.val})
		if err != nil {
			return nil, err
		}
		return onPath(n, path, to), nil
	}
	if to == relItem && (strings.HasPrefix(rest, "stat.") || rest == FieldVirtualRepos) {
		return nil, fmt.Errorf("%w: %q on a related item", ErrUnsupported, m.key)
	}
	k, ok := to.fieldKind(rest)
	if !ok {
		return nil, syntaxErr(m.pos, "unknown field %q", m.key)
	}
	return comparisons(m, func(op Op, v value) (Node, error) {
		// The item-field parser checks the operator and reads the value by
		// kind; its field name only appears in errors.
		n, err := field(Field(m.key), k, op, v)
		if err != nil {
			return nil, err
		}
		fc := n.(*FieldCriterion) //nolint:forcetypeassert // field returns one
		return &PathCriterion{
			Path: path, Field: rest, Op: op, Text: fc.Text, Number: fc.Number, Time: fc.Time,
			Span: fc.Span, rel: to, kind: k,
		}, nil
	})
}

func isPropertyKey(key string) bool {
	return strings.HasPrefix(key, "@") || key == "property.key" || key == "property.value"
}

// onPath moves the property criteria property built, one or the And of
// several operators on one key, onto the row path reaches.
func onPath(n Node, path string, to rel) Node {
	if a, ok := n.(And); ok {
		out := make(And, len(a))
		for i, c := range a {
			out[i] = onPath(c, path, to)
		}
		return out
	}
	pc := n.(*PropertyCriterion) //nolint:forcetypeassert // property builds only these
	return &PathProperty{Path: path, Criterion: pc, rel: to}
}
