package aql

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// criteria builds the tree for an object: an implicit $and of its members,
// whose keys name fields of d or paths from it.
func criteria(v value, depth int, d Domain) (Node, error) {
	if depth > maxDepth {
		return nil, syntaxErrf(v.pos, "nesting deeper than %d levels", maxDepth)
	}
	out := make(And, 0, len(v.members))
	for _, m := range v.members {
		n, err := criterion(m, depth, d)
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

func criterion(m member, depth int, d Domain) (Node, error) {
	switch {
	case m.key == "$and" || m.key == "$or":
		if m.val.kind != vArray {
			return nil, syntaxErrf(m.val.pos, "%s takes an array of objects", m.key)
		}
		if depth+1 > maxDepth {
			return nil, syntaxErrf(m.val.pos, "nesting deeper than %d levels", maxDepth)
		}
		// The operator joins every member of every object in its array:
		// {"$or":[{"path":{"$nmatch":"a"},"name":{"$nmatch":"b"}}]} is
		// path-or-name, not path-and-name. The JFrog CLI builds each
		// --exclusions pattern that way, so reading the members as an
		// implicit $and would exclude everything.
		children := make([]Node, 0, len(m.val.items))
		for _, item := range m.val.items {
			if item.kind != vObject {
				return nil, syntaxErrf(item.pos, "%s takes an array of objects", m.key)
			}
			for _, im := range item.members {
				n, err := criterion(im, depth+1, d)
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
	case m.key == "$msp":
		return msp(m, d)
	case m.key == "$rf":
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, m.key)
	case strings.HasPrefix(m.key, "$"):
		return nil, syntaxErrf(m.pos, "unknown operator %q", m.key)
	case d != DomainItems:
		return relCriterion(m, d.rel())
	case strings.HasPrefix(m.key, "@"), m.key == "property.key", m.key == "property.value":
		return property(m)
	case strings.HasPrefix(m.key, "stat."):
		return stat(m)
	case m.key == FieldVirtualRepos:
		return nil, fmt.Errorf("%w: criteria on %s (it can only be included)",
			ErrUnsupported, FieldVirtualRepos)
	}
	f := Field(m.key)
	k, ok := fieldKinds[f]
	if !ok {
		if strings.Contains(m.key, ".") {
			return relCriterion(m, relItem)
		}
		return nil, syntaxErrf(m.pos, "unknown field %q", m.key)
	}
	return comparisons(m, func(op Op, v value) (Node, error) { return field(f, k, op, v) })
}

// msp reads $msp: an array of objects whose members are property criteria,
// all of which one property must satisfy (research 22 §1.1). The
// properties are the found item's or build's, or those of the item, module
// or build one relation path reaches; every member must name the same.
func msp(m member, d Domain) (Node, error) {
	if m.val.kind != vArray {
		return nil, syntaxErrf(m.val.pos, "$msp takes an array of objects")
	}
	var out MSP
	path, to, first := "", d.rel(), true
	for _, item := range m.val.items {
		if item.kind != vObject {
			return nil, syntaxErrf(item.pos, "$msp takes an array of objects")
		}
		for _, im := range item.members {
			p, r, rest, _ := splitPath(d.rel(), im.key)
			if !isPropertyKey(rest) || !r.hasProperties() {
				return nil, syntaxErrf(im.pos, "$msp takes property criteria, not %q", im.key)
			}
			if !first && p != path {
				return nil, syntaxErrf(im.pos, "$msp criteria must name the properties of one row")
			}
			path, to, first = p, r, false
			n, err := property(member{key: rest, pos: im.pos, val: im.val})
			if err != nil {
				return nil, err
			}
			out = appendProperty(out, n)
		}
	}
	if len(out) == 0 {
		return nil, syntaxErrf(m.val.pos, "$msp has no criteria")
	}
	if d == DomainItems && path == "" {
		return out, nil
	}
	return &PathMSP{Path: path, MSP: out, rel: to}, nil
}

// appendProperty adds the property criteria of n, which property built: one
// criterion, or the And of several operators on one key.
func appendProperty(out MSP, n Node) MSP {
	if a, ok := n.(And); ok {
		for _, c := range a {
			out = append(out, c.(*PropertyCriterion))
		}
		return out
	}
	return append(out, n.(*PropertyCriterion))
}

// comparisons applies build to the member's value: a scalar is $eq, and an
// object of operators is the $and of each.
func comparisons(m member, build func(Op, value) (Node, error)) (Node, error) {
	if m.val.kind != vObject {
		return build(OpEq, m.val)
	}
	if len(m.val.members) == 0 {
		return nil, syntaxErrf(m.val.pos, "%q has no operator", m.key)
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
		return nil, syntaxErrf(v.pos, "unknown operator %q", op)
	}
	allowed := false
	for _, o := range opsByKind[k] {
		allowed = allowed || o == op
	}
	if !allowed {
		return nil, syntaxErrf(v.pos, "%s does not apply to %s", op, f)
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
			return nil, syntaxErrf(v.pos, "%s takes a string", f)
		}
		if k == kindType && v.text != "file" && v.text != "folder" && v.text != "any" {
			return nil, syntaxErrf(v.pos, `type is "file", "folder" or "any", not %q`, v.text)
		}
		c.Text = v.text
	}
	return c, nil
}

// property reads "@<key>", "property.key" or "property.value".
func property(m member) (Node, error) {
	part, key := PartPair, strings.TrimPrefix(m.key, "@")
	switch m.key {
	case "property.key":
		part, key = PartKey, ""
	case "property.value":
		part, key = PartValue, ""
	default:
		if key == "" {
			return nil, syntaxErrf(m.pos, "property criterion without a key")
		}
	}
	return comparisons(m, func(op Op, v value) (Node, error) {
		switch op {
		case OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpMatch, OpNMatch:
		default:
			return nil, syntaxErrf(v.pos, "%s does not apply to properties", op)
		}
		if v.kind != vString {
			return nil, syntaxErrf(v.pos, "property values are strings")
		}
		return &PropertyCriterion{Part: part, Key: key, Op: op, Value: v.text}, nil
	})
}

// stat reads a "stat.<field>" criterion. A JSON null compares with $eq or $ne
// against "no value": research 22 §1.1 and JFrog's own advice to write null,
// not 0, for an item never downloaded.
func stat(m member) (Node, error) {
	f := StatField(strings.TrimPrefix(m.key, "stat."))
	k, ok := statKinds[f]
	if !ok {
		return nil, syntaxErrf(m.pos, "unknown field %q", m.key)
	}
	return comparisons(m, func(op Op, v value) (Node, error) {
		if v.kind == vNull {
			if op != OpEq && op != OpNe {
				return nil, syntaxErrf(v.pos, "null takes $eq or $ne, not %s", op)
			}
			return &StatCriterion{Field: f, Op: op, Null: true}, nil
		}
		// The item-field parser checks the operator and reads the value by
		// kind; its field name only appears in errors.
		n, err := field(Field(m.key), k, op, v)
		if err != nil {
			return nil, err
		}
		fc := n.(*FieldCriterion)
		return &StatCriterion{
			Field: f, Op: op, Text: fc.Text, Number: fc.Number, Time: fc.Time, Span: fc.Span,
		}, nil
	})
}

// integer reads a number criterion: a JSON integer, or a string holding
// one, which some clients send.
func integer(v value) (int64, error) {
	if v.kind != vNumber && v.kind != vString {
		return 0, syntaxErrf(v.pos, "expected a number")
	}
	n, err := strconv.ParseInt(v.text, 10, 64)
	if err != nil {
		return 0, syntaxErrf(v.pos, "expected an integer, found %q", v.text)
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
		return time.Time{}, syntaxErrf(v.pos, "dates are strings")
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, v.text); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, syntaxErrf(v.pos, "%q is not an ISO 8601 date", v.text)
}

// span reads a relative time such as "3d", "3days" or "10minutes".
func span(v value) (Span, error) {
	if v.kind != vString {
		return Span{}, syntaxErrf(v.pos, "relative times are strings such as \"3d\"")
	}
	i := 0
	for i < len(v.text) && isDigit(v.text[i]) {
		i++
	}
	n, err := strconv.Atoi(v.text[:i])
	unit, ok := spanAliases[v.text[i:]]
	if err != nil || !ok {
		return Span{}, syntaxErrf(v.pos, "%q is not a relative time (a number and ms, mills, "+
			"millis, s, seconds, minutes, mi, d, days, w, weeks, mo, months, y or years)", v.text)
	}
	return Span{N: n, Unit: unit}, nil
}
