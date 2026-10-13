package aql

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// criteria builds the tree for an object: an implicit $and of its members.
func criteria(v value, depth int) (Node, error) {
	if depth > maxDepth {
		return nil, syntaxErr(v.pos, "nesting deeper than %d levels", maxDepth)
	}
	out := make(And, 0, len(v.members))
	for _, m := range v.members {
		n, err := criterion(m, depth)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if len(out) == 1 {
		return out[0], nil
	}
	return out, nil
}

func criterion(m member, depth int) (Node, error) {
	switch {
	case m.key == "$and" || m.key == "$or":
		if m.val.kind != vArray {
			return nil, syntaxErr(m.val.pos, "%s takes an array of objects", m.key)
		}
		if depth+1 > maxDepth {
			return nil, syntaxErr(m.val.pos, "nesting deeper than %d levels", maxDepth)
		}
		// The operator joins every member of every object in its array:
		// {"$or":[{"path":{"$nmatch":"a"},"name":{"$nmatch":"b"}}]} is
		// path-or-name, not path-and-name. The JFrog CLI builds each
		// --exclusions pattern that way, so reading the members as an
		// implicit $and would exclude everything.
		children := make([]Node, 0, len(m.val.items))
		for _, item := range m.val.items {
			if item.kind != vObject {
				return nil, syntaxErr(item.pos, "%s takes an array of objects", m.key)
			}
			for _, im := range item.members {
				n, err := criterion(im, depth+1)
				if err != nil {
					return nil, err
				}
				children = append(children, n)
			}
		}
		if m.key == "$and" {
			return And(children), nil
		}
		return Or(children), nil
	case m.key == "$msp" || m.key == "$rf":
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, m.key)
	case strings.HasPrefix(m.key, "$"):
		return nil, syntaxErr(m.pos, "unknown operator %q", m.key)
	case strings.HasPrefix(m.key, "@"):
		return property(m)
	}
	f := Field(m.key)
	k, ok := fieldKinds[f]
	if !ok {
		if strings.Contains(m.key, ".") {
			return nil, fmt.Errorf("%w: field %q (secondary domains other than @property)",
				ErrUnsupported, m.key)
		}
		return nil, syntaxErr(m.pos, "unknown field %q", m.key)
	}
	return comparisons(m, func(op Op, v value) (Node, error) { return field(f, k, op, v) })
}

// comparisons applies build to the member's value: a scalar is $eq, and an
// object of operators is the $and of each.
func comparisons(m member, build func(Op, value) (Node, error)) (Node, error) {
	if m.val.kind != vObject {
		return build(OpEq, m.val)
	}
	if len(m.val.members) == 0 {
		return nil, syntaxErr(m.val.pos, "%q has no operator", m.key)
	}
	out := make(And, 0, len(m.val.members))
	for _, op := range m.val.members {
		n, err := build(Op(op.key), op.val)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if len(out) == 1 {
		return out[0], nil
	}
	return out, nil
}

// opsByKind lists the operators each kind of field takes.
var opsByKind = map[kind][]Op{
	kindText:   {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpMatch, OpNMatch},
	kindNumber: {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte},
	kindTime:   {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpBefore, OpLast},
	kindType:   {OpEq, OpNe},
}

func knownOp(op Op) bool {
	switch op {
	case OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpMatch, OpNMatch, OpBefore, OpLast:
		return true
	}
	return false
}

func field(f Field, k kind, op Op, v value) (Node, error) {
	if !knownOp(op) {
		return nil, syntaxErr(v.pos, "unknown operator %q", op)
	}
	allowed := false
	for _, o := range opsByKind[k] {
		allowed = allowed || o == op
	}
	if !allowed {
		return nil, syntaxErr(v.pos, "%s does not apply to %s", op, f)
	}
	c := &FieldCriterion{Field: f, Op: op}
	switch {
	case op == OpBefore || op == OpLast:
		s, err := span(v)
		if err != nil {
			return nil, err
		}
		c.Span = s
	case k == kindNumber:
		n, err := integer(v)
		if err != nil {
			return nil, err
		}
		c.Number = n
	case k == kindTime:
		t, err := instant(v)
		if err != nil {
			return nil, err
		}
		c.Time = t
	default:
		if v.kind != vString {
			return nil, syntaxErr(v.pos, "%s takes a string", f)
		}
		if k == kindType && v.text != "file" && v.text != "folder" && v.text != "any" {
			return nil, syntaxErr(v.pos, `type is "file", "folder" or "any", not %q`, v.text)
		}
		c.Text = v.text
	}
	return c, nil
}

func property(m member) (Node, error) {
	key := strings.TrimPrefix(m.key, "@")
	if key == "" {
		return nil, syntaxErr(m.pos, "property criterion without a key")
	}
	return comparisons(m, func(op Op, v value) (Node, error) {
		switch op {
		case OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpMatch, OpNMatch:
		default:
			return nil, syntaxErr(v.pos, "%s does not apply to properties", op)
		}
		if v.kind != vString {
			return nil, syntaxErr(v.pos, "property values are strings")
		}
		return &PropertyCriterion{Key: key, Op: op, Value: v.text}, nil
	})
}

// integer reads a number criterion: a JSON integer, or a string holding
// one, which some clients send.
func integer(v value) (int64, error) {
	if v.kind != vNumber && v.kind != vString {
		return 0, syntaxErr(v.pos, "expected a number")
	}
	n, err := strconv.ParseInt(v.text, 10, 64)
	if err != nil {
		return 0, syntaxErr(v.pos, "expected an integer, found %q", v.text)
	}
	return n, nil
}

// timeLayouts are the partial W3C ISO 8601 forms AQL accepts, most precise
// first. Without a zone a time is UTC.
var timeLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04Z07:00", "2006-01-02T15:04",
	"2006-01-02", "2006-01", "2006",
}

func instant(v value) (time.Time, error) {
	if v.kind != vString {
		return time.Time{}, syntaxErr(v.pos, "dates are strings")
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, v.text); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, syntaxErr(v.pos, "%q is not an ISO 8601 date", v.text)
}

// span reads a relative time such as "3d" or "10minutes".
func span(v value) (Span, error) {
	if v.kind != vString {
		return Span{}, syntaxErr(v.pos, "relative times are strings such as \"3d\"")
	}
	i := 0
	for i < len(v.text) && isDigit(v.text[i]) {
		i++
	}
	n, err := strconv.Atoi(v.text[:i])
	unit := v.text[i:]
	_, fixed := spanUnits[unit]
	if err != nil || (!fixed && unit != "mo" && unit != "y") {
		return Span{}, syntaxErr(v.pos,
			"%q is not a relative time (a number and ms, s, minutes, d, w, mo or y)", v.text)
	}
	return Span{N: n, Unit: unit}, nil
}
