package item

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// A PostgreSQL READ COMMITTED transaction can observe a missing destination,
// then encounter another transaction's committed row when inserting. Keep the
// competing row outside the losing transaction so its rollback cannot erase it.
type deployRaceStore struct {
	store.Store
	destination string
	missed      bool
	creates     int
}

func (s *deployRaceStore) Items() store.ItemStore {
	return deployRaceItems{ItemStore: s.Store.Items(), race: s}
}

type deployRaceItems struct {
	store.ItemStore
	race *deployRaceStore
}

func (s deployRaceItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (types.Item, error) {
	if path == s.race.destination && !s.race.missed {
		s.race.missed = true
		return types.Item{}, store.ErrNotFound
	}
	return s.ItemStore.Get(ctx, key, cached, path)
}

func (s deployRaceItems) Create(ctx context.Context, it types.Item) (types.Item, error) {
	if it.FullPath() == s.race.destination {
		s.race.creates++
	}
	return s.ItemStore.Create(ctx, it)
}

func TestUnitDeployIfAbsentRejectsRacingInsertWithoutPublication(t *testing.T) {
	w, hooks := recording(t)
	const destination = "model/manifest.json"
	competing := w.seeded(
		destination,
		"competing writer",
		types.Properties{"owner": {"competing writer"}},
	)
	retained := w.must("retained.bin", "losing writer")
	competing, _ = w.get(w.loc, destination)
	originalProps := w.props(destination)
	originalDownloads := w.downloads(destination)
	hooks.reset()
	racing := &deployRaceStore{Store: w.db, destination: destination}
	w.svc.Store = racing
	_, err := w.svc.Deploy(t.Context(), Deploy{
		Target: Target{Repo: w.loc, Path: destination},
		Body:   strings.NewReader("losing writer"), By: "loser", IfAbsent: true,
		Properties: types.Properties{"owner": {"loser"}},
	})
	if !errors.Is(err, ErrExists) {
		t.Fatalf("racing create returned %v, want ErrExists", err)
	}
	if !racing.missed || racing.creates != 1 {
		t.Fatalf(
			"did not perform atomic absent create: missed=%v creates=%d",
			racing.missed,
			racing.creates,
		)
	}
	got, ok := w.get(w.loc, destination)
	if !ok {
		t.Fatal("competing writer's item disappeared")
	}
	if diff := cmp.Diff(competing, got); diff != "" {
		t.Fatalf("competing writer changed (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(originalProps, w.props(destination)); diff != "" {
		t.Fatalf("competing properties changed:\n%s", diff)
	}
	if got := w.downloads(destination); got != originalDownloads {
		t.Fatalf("competing statistics changed: %d -> %d", originalDownloads, got)
	}
	if w.refs(competing.Actual.SHA256) != 1 || w.refs(retained.Actual.SHA256) != 1 {
		t.Fatal("losing deploy changed blob references")
	}
	for _, call := range hooks.order {
		if strings.HasPrefix(call, "deployed ") || strings.HasPrefix(call, "annotated ") {
			t.Fatalf("losing deploy ran publication hook: %s", call)
		}
	}
}
