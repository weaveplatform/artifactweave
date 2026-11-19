package item

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

func watchEvents(t *testing.T, w *world) func() []outbox.Event {
	t.Helper()
	origin, err := instance.EnsureID(t.Context(), w.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Events().RegisterConsumer(t.Context(), origin, "proof"); err != nil {
		t.Fatal(err)
	}
	return func() []outbox.Event {
		t.Helper()
		events, _, err := w.db.Events().Next(t.Context(), origin, "proof", 1000)
		if err != nil {
			t.Fatal(err)
		}
		return events
	}
}

func TestUnitArtifactEventsUseCurrentSnapshotAndImmutableFacts(t *testing.T) {
	w := newWorld(t)
	read := watchEvents(t, w)
	current := w.loc
	current.ProjectKey = "current-project"
	if err := w.db.Repos().Update(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	it, err := w.deploy("file.txt", "data", kernel.Checksums{})
	if err != nil {
		t.Fatal(err)
	}
	events := read()
	if len(events) != 1 || events[0].Topic != artapi.ArtifactDeployed || events[0].Seq != 1 {
		t.Fatalf("deployed events=%+v", events)
	}
	var facts artapi.ArtifactEvent
	if err := json.Unmarshal(events[0].Payload, &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Item.Repository.ProjectKey != "current-project" || facts.Item.Path != "file.txt" || facts.Item.ID != it.ID || facts.SHA256 != it.Actual.SHA256 || facts.Size != 4 ||
		facts.Actor.Name != "alice" ||
		facts.Version != 1 ||
		facts.Outcome != "committed" {
		t.Fatalf("deployed facts=%+v", facts)
	}
	current.ProjectKey = "later-project"
	if err := w.db.Repos().Update(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Delete(t.Context(), Target{Repo: w.loc, Path: "file.txt"}, "bob"); err != nil {
		t.Fatal(err)
	}
	events = read()
	if len(events) != 2 || events[1].Topic != artapi.ArtifactDeleted {
		t.Fatalf("deleted events=%+v", events)
	}
	var after artapi.ArtifactEvent
	if err := json.Unmarshal(events[0].Payload, &after); err != nil || after != facts {
		t.Fatal("later mutation changed deployed facts")
	}
	if err := json.Unmarshal(events[1].Payload, &after); err != nil {
		t.Fatal(err)
	}
	if after.Item.ID != it.ID || after.Item.Repository.ProjectKey != "later-project" || after.Actor.Name != "bob" {
		t.Fatalf("deleted snapshot=%+v", after)
	}
}

type itemAppendFailure struct {
	Store
	err error
}

func (s itemAppendFailure) Events() outbox.EventStore {
	return appendFailure{EventStore: s.Store.Events(), err: s.err}
}

type appendFailure struct {
	outbox.EventStore
	err error
}

func (s appendFailure) AppendEvent(ctx context.Context, event outbox.Event) (outbox.Event, error) {
	if _, err := s.EventStore.AppendEvent(ctx, event); err != nil {
		return outbox.Event{}, err
	}
	return outbox.Event{}, s.err
}

func TestUnitArtifactAppendAndMutationFailuresEmitNothing(t *testing.T) {
	w := newWorld(t)
	read := watchEvents(t, w)
	if _, err := w.deploy("bad.txt", "data", kernel.Checksums{SHA1: strings.Repeat("0", 40)}); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("validation=%v", err)
	}
	marker := errors.New("event append failure")
	w.svc.Store = itemAppendFailure{Store: w.db, err: marker}
	if _, err := w.deploy("failed.txt", "data", kernel.Checksums{}); !errors.Is(err, marker) {
		t.Fatalf("append failure=%v", err)
	}
	if _, err := w.db.Items().Get(t.Context(), "libs", false, "failed.txt"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("append failure retained item: %v", err)
	}
	if _, err := w.db.Blobs().Get(t.Context(), sums("data").SHA256); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("append failure retained blob references: %v", err)
	}
	w.svc.Store = w.db
	if err := w.db.Tx(t.Context(), func(ctx context.Context) error {
		_, err := w.svc.Deploy(ctx, Deploy{Target: Target{Repo: w.loc, Path: "rolled-back.txt"}, Body: strings.NewReader("data"), By: "alice"})
		if err != nil {
			return err
		}
		return marker
	}); !errors.Is(err, marker) {
		t.Fatal(err)
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("failed changes emitted=%+v", got)
	}
	if _, err := w.deploy("ok.txt", "data", kernel.Checksums{}); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("failed append consumed sequence: %+v", got)
	}
}

func TestUnitTransferEventsKeepOrderedSourceFacts(t *testing.T) {
	w := newWorld(t)
	read := watchEvents(t, w)
	for _, p := range []string{"tree/a.txt", "tree/b.txt"} {
		if _, err := w.deploy(p, "data", kernel.Checksums{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.svc.Transfer(
		t.Context(),
		Transfer{From: Target{Repo: w.loc, Path: "tree"}, To: Target{Repo: w.loc, Path: "copied"}, CanOverwrite: true, By: "alice"},
	); err != nil {
		t.Fatal(err)
	}
	var sourcePaths, destinationPaths []string
	for _, e := range read() {
		if e.Topic != artapi.ArtifactCopied {
			continue
		}
		var facts artapi.ArtifactEvent
		if err := json.Unmarshal(e.Payload, &facts); err != nil {
			t.Fatal(err)
		}
		if facts.Source == nil {
			t.Fatal("transfer lost its source snapshot")
		}
		sourcePaths = append(sourcePaths, facts.Source.Path)
		destinationPaths = append(destinationPaths, facts.Item.Path)
	}
	if !slices.Equal(sourcePaths, []string{"tree", "tree/a.txt", "tree/b.txt"}) || !slices.Equal(destinationPaths, []string{"copied", "copied/a.txt", "copied/b.txt"}) {
		t.Fatalf("transfer facts=%v ->%v", sourcePaths, destinationPaths)
	}
	if _, err := w.svc.Transfer(
		t.Context(),
		Transfer{From: Target{Repo: w.loc, Path: "copied"}, To: Target{Repo: w.loc, Path: "moved"}, Move: true, CanOverwrite: true, By: "alice"},
	); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range read() {
		if e.Topic == artapi.ArtifactMoved {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("move recorded%d facts", n)
	}
}

func TestUnitPropertyEventsDescribeOnlyActualNormalizedDeltas(t *testing.T) {
	w := newWorld(t)
	read := watchEvents(t, w)
	it, err := w.deploy("file.txt", "data", kernel.Checksums{})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		set    model.Properties
		remove []string
	}{
		{set: model.Properties{"key": {"b", "a", "a"}}},
		{set: model.Properties{"key": {"a", "b"}}},
		{remove: []string{"absent"}},
		{set: model.Properties{"key": {"b", "a"}}, remove: []string{"key"}},
		{remove: []string{"key"}},
	} {
		if err := w.svc.Annotate(t.Context(), w.loc, it, change.set, change.remove); err != nil {
			t.Fatal(err)
		}
	}
	var topics []string
	for _, event := range read() {
		if event.Topic != artapi.PropertyAdded && event.Topic != artapi.PropertyDeleted {
			continue
		}
		topics = append(topics, event.Topic)
		var facts artapi.PropertyEvent
		if err := json.Unmarshal(event.Payload, &facts); err != nil {
			t.Fatal(err)
		}
		if facts.Property != "key" || facts.Item.ID != it.ID || !facts.Actor.System {
			t.Fatalf("property facts=%+v", facts)
		}
		if strings.Contains(string(event.Payload), `"values"`) {
			t.Fatal("property values entered event payload")
		}
	}
	if !slices.Equal(topics, []string{artapi.PropertyAdded, artapi.PropertyDeleted}) {
		t.Fatalf("phantom property events=%v", topics)
	}
}

func TestUnitCacheEventsCoverRawAndItemPublication(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(map[bool]string{false: "item", true: "raw"}[raw], func(t *testing.T) {
			w, u := remoteWorld(t)
			read := watchEvents(t, w)
			if raw {
				u.Kind = model.RemoteMetadata
			}
			if _, err := w.svc.Cache(t.Context(), u); err != nil {
				t.Fatal(err)
			}
			events := read()
			if len(events) != 1 || events[0].Topic != artapi.ArtifactCached {
				t.Fatalf("cache=%+v", events)
			}
			var facts artapi.ArtifactEvent
			if err := json.Unmarshal(events[0].Payload, &facts); err != nil {
				t.Fatal(err)
			}
			if !facts.Item.Cached || facts.Item.Repository.Key != "remote" || facts.Item.Path != "a.bin" || facts.Actor.Name != "reader" || facts.Size != 7 ||
				(facts.Item.ID == 0) != raw {
				t.Fatalf("facts=%+v", facts)
			}
		})
	}
}

func TestUnitDerivedAndMaintenanceEventsFollowMutation(t *testing.T) {
	w := newWorld(t)
	read := watchEvents(t, w)
	it, err := w.svc.derive(
		t.Context(),
		format.Derived{Repo: w.loc, Path: "index.txt", Body: strings.NewReader("index"), Properties: model.Properties{"kind": {"metadata"}}, By: "worker"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.svc.underive(t.Context(), w.loc, false, it.FullPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.deploy("purge.txt", "bytes", kernel.Checksums{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Purge(t.Context(), Target{Repo: w.loc, Path: "purge.txt"}); err != nil {
		t.Fatal(err)
	}
	var topics []string
	for _, event := range read() {
		topics = append(topics, event.Topic)
	}
	if !slices.Equal(topics, []string{artapi.ArtifactDeployed, artapi.PropertyAdded, artapi.ArtifactDeleted, artapi.ArtifactDeployed, artapi.ArtifactDeleted}) {
		t.Fatalf("derived/maintenance=%v", topics)
	}
}

func TestUnitRawCacheDeletionEventsAndAppendRollback(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "cleanup"}[cleanup], func(t *testing.T) {
			w, u := remoteWorld(t)
			read := watchEvents(t, w)
			hours := 1
			u.Repo.UnusedArtifactsCleanupPeriodHours = &hours
			if err := w.db.Repos().Update(t.Context(), u.Repo); err != nil {
				t.Fatal(err)
			}
			cached := cacheContent(t, w, u, "index", model.RemoteMetadata, "metadata")
			source, err := w.db.RemoteCache().Source(t.Context(), u.Repo.Key)
			if err != nil {
				t.Fatal(err)
			}
			w.svc.Now = func() time.Time { return at.Add(2 * time.Hour) }
			remove := func() error {
				if cleanup {
					return w.svc.cleanupCacheEntry(t.Context(), cacheCleanupCursor{Repository: u.Repo.Key, Incarnation: source.Repository}, cached.Entry)
				}
				_, err := w.svc.Delete(t.Context(), Target{Repo: u.Repo, Cached: true, Path: "index"}, "deleter")
				return err
			}
			marker := errors.New("append failed")
			w.svc.Store = itemAppendFailure{Store: w.db, err: marker}
			if err := remove(); !errors.Is(err, marker) {
				t.Fatalf("failed raw delete=%v", err)
			}
			if _, err := w.db.RemoteCache().Read(t.Context(), cached.Entry.Token.Key); err != nil || w.refs(cached.Info.Digests.SHA256) != 1 || len(read()) != 1 {
				t.Fatalf("failed append retained deletion: %v", err)
			}
			w.svc.Store = w.db
			if err := remove(); err != nil {
				t.Fatal(err)
			}
			events := read()
			if len(events) != 2 || events[1].Topic != artapi.ArtifactDeleted {
				t.Fatalf("raw deletion=%+v", events)
			}
			var facts artapi.ArtifactEvent
			if err := json.Unmarshal(events[1].Payload, &facts); err != nil {
				t.Fatal(err)
			}
			if facts.Item.ID != 0 || !facts.Item.Cached || facts.Item.Path != "index" || facts.SHA256 != cached.Info.Digests.SHA256 {
				t.Fatalf("raw deleted facts=%+v", facts)
			}
			if !cleanup && facts.Actor.Name != "deleter" {
				t.Fatalf("internal delete actor=%+v", facts.Actor)
			}
			if w.refs(cached.Info.Digests.SHA256) != 0 {
				t.Fatal("raw reference retained")
			}
		})
	}
}

func TestUnitExistingFolderRestoreRetainsInternalActor(t *testing.T) {
	w := newWorld(t)
	read := watchEvents(t, w)
	folder, err := w.db.Items().Put(t.Context(), model.Item{RepoKey: w.loc.Key, Name: "folder", Folder: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Restore(
		t.Context(),
		Target{Repo: w.loc, Path: "folder"},
		model.TrashRecord{Item: folder, Properties: model.Properties{"key": {"value"}}},
		"restorer",
	); err != nil {
		t.Fatal(err)
	}
	all := read()
	if len(all) != 3 {
		t.Fatalf("restore property facts=%+v", all)
	}
	for _, event := range all {
		var facts artapi.PropertyEvent
		if err := json.Unmarshal(event.Payload, &facts); err != nil {
			t.Fatal(err)
		}
		if event.Topic != artapi.PropertyAdded || facts.Actor.Name != "restorer" || facts.Actor.System {
			t.Fatalf("restored actor/facts=%+v topic=%s", facts, event.Topic)
		}
	}
}

func TestUnitFolderPropertyEventsRetainInternalCreator(t *testing.T) {
	w := newWorld(t)
	read := watchEvents(t, w)
	if _, err := w.svc.MakeFolder(t.Context(), Target{Repo: w.loc, Path: "folder"}, model.Properties{"key": {"value"}}, "creator"); err != nil {
		t.Fatal(err)
	}
	all := read()
	if len(all) != 1 || all[0].Topic != artapi.PropertyAdded {
		t.Fatalf("folder facts=%+v", all)
	}
	var facts artapi.PropertyEvent
	if err := json.Unmarshal(all[0].Payload, &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Actor.Name != "creator" || facts.Actor.System {
		t.Fatalf("folder actor=%+v", facts.Actor)
	}
}
