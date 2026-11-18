package docker

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type countedNames struct {
	values  []string
	read    int
	failure error
}

func (s *countedNames) Next(context.Context) (string, bool, error) {
	s.read++
	if s.failure != nil {
		return "", false, s.failure
	}
	if len(s.values) == 0 {
		return "", false, nil
	}
	v := s.values[0]
	s.values = s.values[1:]
	return v, true, nil
}

func TestUnitDockerMergedNamesBoundAndDedup(t *testing.T) {
	left := &countedNames{values: []string{"A", "a", "b", "same", "z"}}
	right := &countedNames{values: []string{"A", "a", "c", "same", "z"}}
	merged := MergeNames(left, right)
	var got []string
	for range 4 {
		v, ok, err := merged.Next(t.Context())
		if err != nil || !ok {
			t.Fatal(v, ok, err)
		}
		got = append(got, v)
	}
	if !slices.Equal(got, []string{"A", "a", "b", "c"}) || left.read > 4 || right.read > 4 {
		t.Fatal(got, left.read, right.read)
	}
	for {
		v, ok, err := merged.Next(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, v)
	}
	if !slices.Equal(got, []string{"A", "a", "b", "c", "same", "z"}) {
		t.Fatal(got)
	}
	boom := errors.New("member failed")
	if _, _, err := MergeNames(&countedNames{failure: boom}).Next(t.Context()); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := MergeNames().Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
