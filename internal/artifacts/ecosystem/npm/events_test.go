package npm_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

func TestUnitDistTagTransferRecordsOtherArtifactPropertyDeletion(t *testing.T) {
	w := newWorld(t)
	first := "left-pad/-/left-pad-1.0.0.tgz"
	second := "left-pad/-/left-pad-2.0.0.tgz"
	w.deploy(first, pkg("left-pad", "1.0.0", ""), nil)
	w.deploy(second, pkg("left-pad", "2.0.0", ""), nil)
	// Put latest on the first version, then observe the transfer to the second.
	w.annotate(first, model.Properties{npm.PropDistTag: {"latest"}})
	origin, err := instance.EnsureID(w.ctx, w.st)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.Events().RegisterConsumer(w.ctx, origin, "proof"); err != nil {
		t.Fatal(err)
	}
	before, _, err := w.st.Events().Next(w.ctx, origin, "proof", 1000)
	if err != nil {
		t.Fatal(err)
	}
	marker := errors.New("append failed")
	w.items.Store = npmEventFailure{Store: w.st, err: marker}
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, second)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.items.Annotate(w.ctx, w.c, it, model.Properties{npm.PropDistTag: {"latest"}}, nil); !errors.Is(err, marker) {
		t.Fatalf("append failure=%v", err)
	}
	w.items.Store = w.st
	if !w.props(first).Has(npm.PropDistTag, "latest") || w.props(second).Has(npm.PropDistTag, "latest") {
		t.Fatal("failed append retained cross-artifact property changes")
	}
	retained, _, err := w.st.Events().Next(w.ctx, origin, "proof", 1000)
	if err != nil || len(retained) != len(before) {
		t.Fatalf("failed append retained facts: %v", err)
	}
	w.annotate(second, model.Properties{npm.PropDistTag: {"latest"}})
	events, _, err := w.st.Events().Next(w.ctx, origin, "proof", 1000)
	if err != nil {
		t.Fatal(err)
	}
	events = events[len(before):]
	counts := map[string]int{}
	for _, event := range events {
		if event.Topic != artapi.PropertyAdded && event.Topic != artapi.PropertyDeleted {
			continue
		}
		var facts artapi.PropertyEvent
		if err := json.Unmarshal(event.Payload, &facts); err != nil {
			t.Fatal(err)
		}
		if facts.Property == npm.PropDistTag {
			counts[event.Topic+":"+facts.Item.Path]++
		}
	}
	if counts[artapi.PropertyDeleted+":"+first] != 1 || counts[artapi.PropertyAdded+":"+second] != 1 || len(counts) != 2 {
		t.Fatalf("dist-tag property facts=%v", counts)
	}
	end, _, err := w.st.Events().Next(w.ctx, origin, "proof", 1000)
	if err != nil {
		t.Fatal(err)
	}
	w.annotate(second, model.Properties{npm.PropDistTag: {"latest", "latest"}})
	again, _, err := w.st.Events().Next(w.ctx, origin, "proof", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(end) {
		t.Fatal("normalized tag no-op emitted extra facts")
	}
	if len(w.props(first)[npm.PropDistTag]) != 0 || !w.props(second).Has(npm.PropDistTag, "latest") {
		t.Fatal("dist-tag behavior changed")
	}
}

type npmEventFailure struct {
	item.Store
	err error
}

func (s npmEventFailure) Events() outbox.EventStore {
	return npmFailingEvents{EventStore: s.Store.Events(), err: s.err}
}

type npmFailingEvents struct {
	outbox.EventStore
	err error
}

func (s npmFailingEvents) AppendEvent(ctx context.Context, event outbox.Event) (outbox.Event, error) {
	if _, err := s.EventStore.AppendEvent(ctx, event); err != nil {
		return outbox.Event{}, err
	}
	return outbox.Event{}, s.err
}
