package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func TestUnitActorAllowlist(t *testing.T) {
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "alice", TokenID: "private-token-id", Groups: []string{"private-group"}})
	actor := outbox.ActorFrom(ctx, "ignored")
	raw, err := json.Marshal(actor)
	if err != nil || string(raw) != `{"name":"alice"}` {
		t.Fatalf("actor=%s,error=%v", raw, err)
	}
	if got := outbox.ActorFrom(t.Context(), "maintenance"); got.Name != "maintenance" || got.System {
		t.Fatalf("named internal actor=%+v", got)
	}
	if got := outbox.ActorFrom(t.Context(), ""); !got.System || got.Name != "" {
		t.Fatalf("unnamed maintenance actor=%+v", got)
	}
	if got := outbox.ActorFrom(kernel.WithPrincipal(t.Context(), kernel.Anonymous), ""); !got.Anonymous {
		t.Fatalf("anonymous actor=%+v", got)
	}
}

func TestUnitStageSnapshotsPayloadAndRollsBack(t *testing.T) {
	s := inmem.New(memtx.New())
	ctx := t.Context()
	if _, err := s.Events().RegisterConsumer(ctx, "origin", "test"); err != nil {
		t.Fatal(err)
	}
	e := outbox.Event{Origin: "origin", Topic: "artifact.deployed", Key: "item:1", At: time.Now(), Payload: []byte(`{"value":"before"}`)}
	if err := outbox.Stage(ctx, s.Events(), e); !errors.Is(err, tx.ErrBeforeCommit) {
		t.Fatalf("outside transaction=%v", err)
	}
	if err := s.Tx(ctx, func(ctx context.Context) error {
		if err := s.Settings().Put(ctx, "business", true); err != nil {
			return err
		}
		if err := outbox.Stage(ctx, s.Events(), e); err != nil {
			return err
		}
		e.Payload[10] = 'X'
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	batch, _, err := s.Events().Next(ctx, "origin", "test", 10)
	if err != nil || len(batch) != 1 || string(batch[0].Payload) != `{"value":"before"}` {
		t.Fatalf("staged payload changed: %+v,%v", batch, err)
	}
	marker := errors.New("append failure")
	if err := s.Tx(ctx, func(ctx context.Context) error {
		if err := s.Settings().Put(ctx, "business", false); err != nil {
			return err
		}
		e.Payload = []byte(`{"value":"after"}`)
		return outbox.Stage(ctx, failedAppend{EventStore: s.Events(), err: marker}, e)
	}); !errors.Is(err, marker) {
		t.Fatalf("append failure=%v", err)
	}
	var business bool
	if err := s.Settings().Get(ctx, "business", &business); err != nil || !business {
		t.Fatalf("failed append changed business=%t,%v", business, err)
	}
	batch, _, err = s.Events().Next(ctx, "origin", "test", 10)
	if err != nil || len(batch) != 1 {
		t.Fatalf("failed staged append remained: %+v,%v", batch, err)
	}
	e.Topic = ""
	if err := s.Tx(ctx, func(ctx context.Context) error { return outbox.Stage(ctx, s.Events(), e) }); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("invalid stage=%v", err)
	}
	if strings.Contains(string(batch[0].Payload), "private") {
		t.Fatal("credentials entered staged event")
	}
}

type failedAppend struct {
	outbox.EventStore
	err error
}

func (s failedAppend) AppendEvent(ctx context.Context, e outbox.Event) (outbox.Event, error) {
	if _, err := s.EventStore.AppendEvent(ctx, e); err != nil {
		return outbox.Event{}, err
	}
	return outbox.Event{}, s.err
}
