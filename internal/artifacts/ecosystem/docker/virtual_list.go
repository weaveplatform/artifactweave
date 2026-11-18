package docker

import (
	"context"
	"fmt"
)

// NameStream yields names in Compare order without retaining a complete member
// listing. Local pages and decoded remote documents satisfy the same boundary.
type NameStream interface {
	Next(context.Context) (string, bool, error)
}

type (
	nameHead struct {
		stream      NameStream
		name        string
		ready, done bool
	}
	mergedNames struct{ heads []nameHead }
)

// MergeNames merges and deduplicates sorted member streams. It holds one head
// per member and advances all equal heads together, including page boundaries.
func MergeNames(streams ...NameStream) NameStream {
	m := &mergedNames{heads: make([]nameHead, len(streams))}
	for i, s := range streams {
		m.heads[i].stream = s
	}
	return m
}

func (m *mergedNames) Next(ctx context.Context) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, fmt.Errorf("docker name stream: %w", err)
	}
	best := ""
	found := false
	for i := range m.heads {
		s := &m.heads[i]
		if !s.ready && !s.done {
			name, ok, err := s.stream.Next(ctx)
			if err != nil {
				return "", false, fmt.Errorf("docker name stream: %w", err)
			}
			s.name, s.ready, s.done = name, ok, !ok
		}
		if s.ready && (!found || Compare(s.name, best) < 0) {
			best, found = s.name, true
		}
	}
	if !found {
		return "", false, nil
	}
	for i := range m.heads {
		if m.heads[i].ready && m.heads[i].name == best {
			m.heads[i].ready = false
		}
	}
	return best, true, nil
}
