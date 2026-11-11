package aql

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// TransitiveTarget validates the documented transitive query shape before
// Resolve replaces its single repository equality with local cache locations.
func (q *Query) TransitiveTarget() (string, error) {
	if q.Domain != DomainItems || q.Action != Find || q.Sort != nil || q.HasOffset ||
		len(q.Stats()) != 0 ||
		q.Relates() {
		return "", fmt.Errorf(
			"%w: transitive requires items.find without sort/offset or non-item includes",
			ErrInvalid,
		)
	}
	key, bound, err := transitiveRepository(q.Criteria)
	if err != nil {
		return "", err
	}
	if !bound {
		return "", fmt.Errorf(
			"%w: every alternative must require the same repository equality",
			ErrInvalid,
		)
	}
	if key == "" {
		return "", fmt.Errorf("%w: transitive requires one repository equality", ErrInvalid)
	}
	return key, nil
}

func transitiveRepository(node Node) (string, bool, error) {
	switch n := node.(type) {
	case And:
		return transitiveRepositoryGroup(n, false)
	case Or:
		return transitiveRepositoryGroup(n, true)
	case *Joined:
		return transitiveRepositoryGroup(n.Criteria, false)
	case *FieldCriterion:
		if n.Field == FieldRepo {
			if n.Op != OpEq || n.Text == "" {
				return "", false, fmt.Errorf(
					"%w: transitive requires repository equality",
					ErrInvalid,
				)
			}
			return n.Text, true, nil
		}
	}
	return "", false, nil
}

func transitiveRepositoryGroup(nodes []Node, disjunction bool) (string, bool, error) {
	var key string
	bound := disjunction && len(nodes) > 0
	for _, node := range nodes {
		other, child, err := transitiveRepository(node)
		if err != nil {
			return "", false, err
		}
		if other != "" {
			if key != "" && key != other {
				return "", false, fmt.Errorf(
					"%w: transitive requires a single repository",
					ErrInvalid,
				)
			}
			key = other
		}
		if disjunction {
			bound = bound && child
		} else {
			bound = bound || child
		}
	}
	return key, bound, nil
}

// PeerQuery serializes parsed criteria with only the repository identity changed.
// The complete item/property projection supports local authorization and output
// selection. It never forwards a mutation, transitive recursion or caller syntax.
func (q *Query) PeerQuery(repository string, limit int) (string, error) {
	if _, err := q.TransitiveTarget(); err != nil {
		return "", err
	}
	if repository == "" || limit < 0 {
		return "", ErrInvalid
	}
	criteria, err := peerCriteria(q.Criteria, repository)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(criteria)
	if err != nil {
		return "", fmt.Errorf("serialize peer query: %w", err)
	}
	text := "items.find(" + string(
		raw,
	) + ").include(\"*\",\"property\").limit(" + strconv.Itoa(
		limit,
	) + ")"
	if _, err = Parse(text); err != nil {
		return "", err
	}
	return text, nil
}

func peerCriteria(node Node, repository string) (any, error) {
	switch n := node.(type) {
	case And:
		return peerGroup("$and", n, repository)
	case Or:
		return peerGroup("$or", n, repository)
	case *Joined:
		return peerGroup("$and", n.Criteria, repository)
	case *FieldCriterion:
		value := criterionValue(
			fieldKinds[n.Field],
			n.Op,
			n.Text,
			n.Number,
			n.Time.Format(time.RFC3339Nano),
			n.Span,
		)
		if n.Field == FieldRepo {
			value = repository
		}
		return map[string]any{string(n.Field): map[Op]any{n.Op: value}}, nil
	case *PropertyCriterion:
		return peerProperty(n), nil
	case MSP:
		children := make(And, len(n))
		for i, c := range n {
			children[i] = c
		}
		group, err := peerChildren(children, repository)
		return map[string]any{"$msp": group}, err
	case *StatCriterion:
		var value any
		if !n.Null {
			value = criterionValue(
				statKinds[n.Field],
				n.Op,
				n.Text,
				n.Number,
				n.Time.Format(time.RFC3339Nano),
				n.Span,
			)
		}
		return map[string]any{"stat." + string(n.Field): map[Op]any{n.Op: value}}, nil
	case *PathCriterion:
		field := n.Field
		if n.Path != "" {
			field = n.Path + "." + field
		}
		return map[string]any{
			field: map[Op]any{
				n.Op: criterionValue(
					n.kind,
					n.Op,
					n.Text,
					n.Number,
					n.Time.Format(time.RFC3339Nano),
					n.Span,
				),
			},
		}, nil
	case *PathProperty:
		out := peerProperty(n.Criterion)
		for k, v := range out {
			delete(out, k)
			out[n.Path+"."+k] = v
		}
		return out, nil
	case *PathMSP:
		children := make(And, len(n.MSP))
		for i, c := range n.MSP {
			children[i] = &PathProperty{Path: n.Path, Criterion: c}
		}
		group, err := peerChildren(children, repository)
		return map[string]any{"$msp": group}, err
	default:
		return nil, ErrUnsupported
	}
}

func peerGroup(operator string, nodes []Node, repository string) (any, error) {
	children, err := peerChildren(nodes, repository)
	return map[string]any{operator: children}, err
}

func peerChildren(nodes []Node, repository string) ([]any, error) {
	children := make([]any, 0, len(nodes))
	for _, n := range nodes {
		value, err := peerCriteria(n, repository)
		if err != nil {
			return nil, err
		}
		children = append(children, value)
	}
	return children, nil
}

func peerProperty(n *PropertyCriterion) map[string]any {
	key := "@" + n.Key
	if n.Part == PartKey {
		key = "property.key"
	}
	if n.Part == PartValue {
		key = "property.value"
	}
	return map[string]any{key: map[Op]string{n.Op: n.Value}}
}

func criterionValue(k kind, op Op, text string, number int64, timestamp string, span Span) any {
	if op == OpBefore || op == OpLast {
		return strconv.Itoa(span.N) + span.Unit
	}
	switch k {
	case kindNumber:
		return number
	case kindTime:
		return timestamp
	default:
		return text
	}
}
