package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/registry"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

func TestAccDockerEventsFollowLogicalOperations(t *testing.T) {
	for _, backend := range backends() {
		t.Run(backend.name, func(t *testing.T) {
			db, _ := backend.open(t)
			w := newWorld(t, db)
			ctx := t.Context()
			origin, err := instance.EnsureID(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Events().RegisterConsumer(ctx, origin, "proof"); err != nil {
				t.Fatal(err)
			}
			read := func() []outbox.Event {
				t.Helper()
				all, _, err := db.Events().Next(ctx, origin, "proof", 1000)
				if err != nil {
					t.Fatal(err)
				}
				var result []outbox.Event
				for _, event := range all {
					if strings.HasPrefix(event.Topic, "docker.") {
						result = append(result, event)
					}
				}
				return result
			}
			w.upload(imageConfig, layerA)
			body, digest := manifestOf(layerA)
			if err := w.push(body, digest, "v1", layout.DigestFolder(digest)); err != nil {
				t.Fatal(err)
			}
			if err := w.push(body, digest, "v1", layout.DigestFolder(digest)); err != nil {
				t.Fatal(err)
			}
			events := read()
			if len(events) != 1 || events[0].Topic != artapi.DockerPushed {
				t.Fatalf("push=%+v", events)
			}
			var pushed artapi.DockerEvent
			if err := json.Unmarshal(events[0].Payload, &pushed); err != nil {
				t.Fatal(err)
			}
			if pushed.Image.Name != "app" || pushed.Image.Tag != "v1" || pushed.Image.Digest != digest || pushed.Actor.Name != "alice" {
				t.Fatalf("push facts=%+v", pushed)
			}
			if err := w.svc.Promote(
				ctx,
				registry.Promotion{
					From: w.owner("app"),
					To:   registry.Owner{Repo: w.oci, Name: "release-app", By: "bob"},
					Tags: []registry.Rename{{From: "v1", To: "release"}},
					Copy: true,
				},
			); err != nil {
				t.Fatal(err)
			}
			events = read()
			var promoted artapi.DockerEvent
			if len(events) != 2 || events[1].Topic != artapi.DockerPromoted {
				t.Fatalf("promotion=%+v", events)
			}
			if err := json.Unmarshal(events[1].Payload, &promoted); err != nil {
				t.Fatal(err)
			}
			if !promoted.Copy || promoted.Source == nil || promoted.Source.Name != "app" || promoted.Source.Tag != "v1" || promoted.Image.Repository.Key != w.oci.Key ||
				promoted.Actor.Name != "bob" {
				t.Fatalf("promotion facts=%+v", promoted)
			}
			if err := w.svc.DeleteTag(ctx, w.owner("app"), "v1"); err != nil {
				t.Fatal(err)
			}
			if err := w.svc.DeleteManifest(ctx, w.owner("app"), digest); err != nil {
				t.Fatal(err)
			}
			events = read()
			if len(events) != 4 || events[2].Topic != artapi.DockerDeleted || events[3].Topic != artapi.DockerDeleted {
				t.Fatalf("delete=%+v", events)
			}
			marker := errors.New("append failed")
			w.svc.Store = registryAppendFailure{Store: db, err: marker}
			if err := w.push(body, digest, "failed"); !errors.Is(err, marker) {
				t.Fatalf("append failure=%v", err)
			}
			if _, err := db.Items().Get(ctx, w.docker.Key, false, "app/failed/manifest.json"); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("failed push retained item: %v", err)
			}
			if len(read()) != 4 {
				t.Fatal("failed push emitted a Docker event")
			}
		})
	}
}

type registryAppendFailure struct {
	registry.Store
	err error
}

func (s registryAppendFailure) Events() outbox.EventStore {
	return registryFailingEvents{EventStore: s.Store.Events(), err: s.err}
}

type registryFailingEvents struct {
	outbox.EventStore
	err error
}

func (s registryFailingEvents) AppendEvent(ctx context.Context, e outbox.Event) (outbox.Event, error) {
	if _, err := s.EventStore.AppendEvent(ctx, e); err != nil {
		return outbox.Event{}, err
	}
	return outbox.Event{}, s.err
}

func TestAccMultiArchDeletionEmitsOneDockerEvent(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() registry.Store) {
		platforms := w.platforms()
		body, digest := indexOf(layout.MediaTypeOCIIndex, platforms[0], platforms[1])
		if err := w.pushAs(layout.MediaTypeOCIIndex, body, digest, "multi"); err != nil {
			t.Fatal(err)
		}
		origin, err := instance.EnsureID(t.Context(), w.db)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.db.Events().RegisterConsumer(t.Context(), origin, "proof"); err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.DeleteMultiArch(t.Context(), w.owner("app"), "multi"); err != nil {
			t.Fatal(err)
		}
		all, _, err := w.db.Events().Next(t.Context(), origin, "proof", 1000)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, event := range all {
			if event.Topic != artapi.DockerDeleted {
				continue
			}
			count++
			var facts artapi.DockerEvent
			if err := json.Unmarshal(event.Payload, &facts); err != nil {
				t.Fatal(err)
			}
			if facts.Image.Tag != "multi" || facts.Image.Digest != digest {
				t.Fatalf("multiarch deletion=%+v", facts)
			}
		}
		if count != 1 {
			t.Fatalf("Docker delete events=%d", count)
		}
	})
}
