package build

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

func TestUnitBuildEventsCommitWithTheRun(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	origin, err := instance.EnsureID(ctx, w.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Events().RegisterConsumer(ctx, origin, "proof"); err != nil {
		t.Fatal(err)
	}
	read := func() []outbox.Event {
		t.Helper()
		all, _, err := w.db.Events().Next(ctx, origin, "proof", 1000)
		if err != nil {
			t.Fatal(err)
		}
		var result []outbox.Event
		for _, event := range all {
			if strings.HasPrefix(event.Topic, "build.") {
				result = append(result, event)
			}
		}
		return result
	}
	published := w.publish(doc("job", "1", at(0)))
	events := read()
	if len(events) != 1 || events[0].Topic != artapi.BuildUploaded {
		t.Fatalf("uploaded=%+v", events)
	}
	var uploaded artapi.BuildEvent
	if err := json.Unmarshal(events[0].Payload, &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.Build.ID != published.Run.ID || uploaded.Build.Name != "job" || uploaded.Build.Number != "1" || uploaded.Actor.Name != "admin" {
		t.Fatalf("facts=%+v", uploaded)
	}
	p := Promotion{Name: "job", Number: "1", Status: "staged", By: "publisher", DryRun: true}
	if _, err := w.svc.Promote(ctx, p, everyone); err != nil {
		t.Fatal(err)
	}
	if len(read()) != 1 {
		t.Fatal("dry promotion emitted event")
	}
	p.DryRun = false
	if _, err := w.svc.Promote(ctx, p, everyone); err != nil {
		t.Fatal(err)
	}
	marker := errors.New("append failed")
	w.svc.Store = buildAppendFailure{Store: w.db, err: marker}
	if _, err := w.svc.Delete(ctx, Delete{Name: "job", All: true, By: "deleter"}, everyone); !errors.Is(err, marker) {
		t.Fatalf("append failure=%v", err)
	}
	if _, err := w.db.Builds().ByID(ctx, published.Run.ID); err != nil {
		t.Fatalf("append failure deleted run: %v", err)
	}
	if len(read()) != 2 {
		t.Fatal("failed delete emitted event")
	}
	w.svc.Store = w.db
	if _, err := w.svc.Delete(ctx, Delete{Name: "job", All: true, By: "deleter"}, everyone); err != nil {
		t.Fatal(err)
	}
	events = read()
	if len(events) != 3 || events[1].Topic != artapi.BuildPromoted || events[2].Topic != artapi.BuildDeleted {
		t.Fatalf("events=%+v", events)
	}
	var deleted artapi.BuildEvent
	if err := json.Unmarshal(events[2].Payload, &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.Build != uploaded.Build || deleted.Actor.Name != "deleter" {
		t.Fatalf("deleted=%+v", deleted)
	}
	if _, err := w.db.Builds().ByID(ctx, published.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("run retained: %v", err)
	}
}

type buildAppendFailure struct {
	Store
	err error
}

func (s buildAppendFailure) Events() outbox.EventStore {
	return buildFailingEvents{EventStore: s.Store.Events(), err: s.err}
}

type buildFailingEvents struct {
	outbox.EventStore
	err error
}

func (s buildFailingEvents) AppendEvent(ctx context.Context, e outbox.Event) (outbox.Event, error) {
	if _, err := s.EventStore.AppendEvent(ctx, e); err != nil {
		return outbox.Event{}, err
	}
	return outbox.Event{}, s.err
}
