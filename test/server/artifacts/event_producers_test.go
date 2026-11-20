package artifacts_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccServiceEventsAgreeAcrossHTTPAndInternalCalls(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := remoteFolderWorld(t, backend)
			db := w.Runtime.Store
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
				return all
			}
			for _, key := range []string{"events-local", "events-target"} {
				if err := db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.Generic})); err != nil {
					t.Fatal(err)
				}
			}
			local, err := db.Repos().Get(ctx, "events-local")
			if err != nil {
				t.Fatal(err)
			}
			target, err := db.Repos().Get(ctx, "events-target")
			if err != nil {
				t.Fatal(err)
			}
			service := &item.Service{Store: db, Blobs: w.Runtime.Blobs, Formats: ecosystem.For}
			internal := kernel.WithPrincipal(ctx, kernel.Principal{Name: "admin"})
			response := w.Request(http.MethodPut, "/artifactory/events-local/file.bin", "admin", "payload")
			wantStatus(t, response, http.StatusCreated)
			if _, err := service.Deploy(internal, item.Deploy{Target: item.Target{Repo: local, Path: "file.bin"}, Body: strings.NewReader("payload"), By: "admin"}); err != nil {
				t.Fatal(err)
			}
			events := read()
			if len(events) != 2 || events[0].Topic != artapi.ArtifactDeployed || events[1].Topic != artapi.ArtifactDeployed ||
				string(events[0].Payload) != string(events[1].Payload) {
				t.Fatalf("HTTP/internal deploy facts disagree: %+v", events)
			}
			response = w.Request(http.MethodPost, "/artifactory/api/copy/events-local/file.bin?to=/events-target/copy.bin", "admin", "")
			wantStatus(t, response, http.StatusOK)
			if _, err := service.Transfer(
				internal,
				item.Transfer{From: item.Target{Repo: local, Path: "file.bin"}, To: item.Target{Repo: target, Path: "copy.bin"}, CanOverwrite: true, FailFast: true, By: "admin"},
			); err != nil {
				t.Fatal(err)
			}
			events = read()
			if len(events) != 4 || events[2].Topic != artapi.ArtifactCopied || events[3].Topic != artapi.ArtifactCopied || string(events[2].Payload) != string(events[3].Payload) {
				t.Fatalf("HTTP/internal copy facts disagree: %+v", events)
			}
			wantStatus(t, w.Request(http.MethodPost, "/artifactory/api/move/events-target/copy.bin?to=/events-target/moved.bin", "admin", ""), http.StatusOK)
			wantStatus(t, w.Request(http.MethodDelete, "/artifactory/events-target/moved.bin", "admin", ""), http.StatusNoContent)
			if _, err := service.Purge(ctx, item.Target{Repo: local, Path: "file.bin"}); err != nil {
				t.Fatal(err)
			}
			w.Upstream.Set("/cached.bin", fakeupstream.Resource{Body: []byte("cached")})
			wantStatus(t, w.Request(http.MethodGet, "/artifactory/remote/cached.bin", "reader", ""), http.StatusOK)
			events = read()
			want := []string{
				artapi.ArtifactDeployed,
				artapi.ArtifactDeployed,
				artapi.ArtifactCopied,
				artapi.ArtifactCopied,
				artapi.ArtifactMoved,
				artapi.ArtifactDeleted,
				artapi.ArtifactDeleted,
				artapi.ArtifactCached,
			}
			if len(events) != len(want) {
				t.Fatalf("events=%+v", events)
			}
			for i, event := range events {
				if event.Topic != want[i] || event.Origin != origin || event.Seq != int64(i+1) {
					t.Fatalf("event %d=%+v", i, event)
				}
			}
			var cached artapi.ArtifactEvent
			if err := json.Unmarshal(events[7].Payload, &cached); err != nil {
				t.Fatal(err)
			}
			if cached.Actor.Name != "reader" || cached.Item.Path != "cached.bin" || !cached.Item.Cached || cached.Item.Repository.Key != "remote" {
				t.Fatalf("cache facts=%+v", cached)
			}
			fail := eventFailureStore{Store: db, err: errors.New("append failed")}
			broken := harness.Router(harness.Config{Store: fail, Blobs: w.Runtime.Blobs, Auth: w.Runtime.Auth, Tokens: w.Runtime.Tokens, Keys: w.Runtime.Keys})
			response = do(t, broken, http.MethodPut, "/artifactory/events-local/failed.bin", basic("admin", "pw"), "payload")
			wantStatus(t, response, http.StatusInternalServerError)
			if _, err := db.Items().Get(ctx, local.Key, false, "failed.bin"); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("failed HTTP deploy retained metadata: %v", err)
			}
			if len(read()) != len(want) {
				t.Fatal("failed HTTP deploy emitted an event")
			}
		})
	}
}

type eventFailureStore struct {
	harness.Store
	err error
}

func (s eventFailureStore) Events() outbox.EventStore {
	return eventFailingStream{EventStore: s.Store.Events(), err: s.err}
}

type eventFailingStream struct {
	outbox.EventStore
	err error
}

func (s eventFailingStream) AppendEvent(ctx context.Context, event outbox.Event) (outbox.Event, error) {
	if _, err := s.EventStore.AppendEvent(ctx, event); err != nil {
		return outbox.Event{}, err
	}
	return outbox.Event{}, s.err
}
