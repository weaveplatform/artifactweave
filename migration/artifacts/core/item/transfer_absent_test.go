package item

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type transferRaceStore struct {
	store.Store
	before func()
}

func (s *transferRaceStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	if s.before != nil {
		before := s.before
		s.before = nil
		before()
	}
	return s.Store.Tx(ctx, fn)
}

func TestUnitTransferIfAbsentRacingDestination(t *testing.T) {
	w := newWorld(t)
	source := w.must("source.txt", "source")
	otherWriter := *w.svc
	var competing types.Item
	w.svc.Store = &transferRaceStore{Store: w.db, before: func() {
		var err error
		competing, err = otherWriter.Deploy(t.Context(), Deploy{
			Target: Target{Repo: w.loc, Path: "destination.txt"},
			Body:   strings.NewReader("other writer"), By: "concurrent writer",
		})
		if err != nil {
			t.Fatal(err)
		}
	}}
	_, err := w.svc.Transfer(t.Context(), Transfer{
		From: Target{Repo: w.loc, Path: source.FullPath()},
		To:   Target{Repo: w.loc, Path: "destination.txt"},
		Move: true, IfAbsent: true, By: "migrator",
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("racing destination: %v", err)
	}
	got, ok := w.get(w.loc, source.FullPath())
	if !ok || got.ID != source.ID {
		t.Fatal("source changed after collision")
	}
	got, ok = w.get(w.loc, "destination.txt")
	if !ok || got.Actual != competing.Actual {
		t.Fatal("competing writer's bytes were overwritten")
	}
	if w.refs(source.Actual.SHA256) != 1 {
		t.Fatal("failed transfer changed blob references")
	}
}

func TestUnitTransferIfAbsentSuccess(t *testing.T) {
	w := newWorld(t)
	source := w.must("source.txt", "source")
	result, err := w.svc.Transfer(t.Context(), Transfer{
		From: Target{Repo: w.loc, Path: source.FullPath()},
		To:   Target{Repo: w.loc, Path: "destination.txt"},
		Move: true, IfAbsent: true, By: "migrator",
	})
	if err != nil || result.Files != 1 || result.Failed != 0 {
		t.Fatalf("move: %+v %v", result, err)
	}
	if _, ok := w.get(w.loc, source.FullPath()); ok {
		t.Fatal("source was not removed")
	}
	got, ok := w.get(w.loc, "destination.txt")
	if !ok || got.Actual != source.Actual {
		t.Fatal("destination does not hold source bytes")
	}
	if w.refs(source.Actual.SHA256) != 1 {
		t.Fatal("move changed blob reference count")
	}
}
