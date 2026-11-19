package outbox_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

func moduleEvent() outbox.Event {
	return outbox.Event{
		Origin: "local-instance", Topic: "artifact.deployed", Key: "libs/a.jar",
		At: time.Date(2026, 11, 19, 12, 0, 0, 123000, time.UTC), Payload: []byte(" { \"size\": 7 } \n"),
	}
}

func TestUnitModuleEventConsumerIdentityBounds(t *testing.T) {
	for _, bad := range []string{"", strings.Repeat("x", 257), string([]byte{0xff}), "has\x00nul"} {
		if err := outbox.ValidateConsumer(bad, "reader"); !errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("invalid origin accepted: %v", err)
		}
		if err := outbox.ValidateConsumer("origin", bad); !errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("invalid consumer accepted: %v", err)
		}
	}
	if err := outbox.ValidateConsumer(strings.Repeat("é", 128), strings.Repeat("x", 256)); err != nil {
		t.Fatalf("bounded UTF-8 identities rejected: %v", err)
	}
}

func TestUnitModuleEventEnvelopePreservesPayloadAndIdentity(t *testing.T) {
	want := moduleEvent()
	entry, err := outbox.EventEntry(want)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Stream != outbox.ModuleEventStream || entry.Seq != 0 || entry.EventType != want.Topic {
		t.Fatalf("outbox entry: %+v", entry)
	}
	entry.Seq = 41
	got, err := outbox.EventFromEntry(entry)
	if err != nil {
		t.Fatal(err)
	}
	want.Seq = 41
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event round trip: got %+v, want %+v", got, want)
	}
	got.Payload[0] = 'X'
	again, err := outbox.EventFromEntry(entry)
	if err != nil || !bytes.Equal(again.Payload, want.Payload) {
		t.Fatalf("consumer mutated persisted representation: %+v, %v", again, err)
	}
}

func TestUnitModuleEventRejectsInvalidNewValuesWithoutPayloadDisclosure(t *testing.T) {
	cases := map[string]func(*outbox.Event){
		"empty origin":        func(e *outbox.Event) { e.Origin = "" },
		"long origin":         func(e *outbox.Event) { e.Origin = strings.Repeat("a", 257) },
		"bad origin encoding": func(e *outbox.Event) { e.Origin = string([]byte{0xff}) },
		"NUL origin":          func(e *outbox.Event) { e.Origin = "has\x00nul" },
		"sequence":            func(e *outbox.Event) { e.Seq = 3 },
		"empty topic":         func(e *outbox.Event) { e.Topic = "" },
		"long topic":          func(e *outbox.Event) { e.Topic = strings.Repeat("a", 129) },
		"bad topic encoding":  func(e *outbox.Event) { e.Topic = string([]byte{0xff}) },
		"NUL topic":           func(e *outbox.Event) { e.Topic = "has\x00nul" },
		"empty key":           func(e *outbox.Event) { e.Key = "" },
		"long key":            func(e *outbox.Event) { e.Key = strings.Repeat("a", 1025) },
		"bad key encoding":    func(e *outbox.Event) { e.Key = string([]byte{0xff}) },
		"missing time":        func(e *outbox.Event) { e.At = time.Time{} },
		"large data":          func(e *outbox.Event) { e.Payload = bytes.Repeat([]byte(" "), outbox.MaxEventPayload+1) },
		"invalid JSON":        func(e *outbox.Event) { e.Payload = []byte("private-fixture-value") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := moduleEvent()
			mutate(&e)
			_, err := outbox.EventEntry(e)
			if !errors.Is(err, kernel.ErrInvalid) || strings.Contains(err.Error(), "private-fixture-value") {
				t.Fatalf("invalid event error: %v", err)
			}
		})
	}
}

func TestUnitModuleEventRejectsPeerAndCorruptEntries(t *testing.T) {
	entry, err := outbox.EventEntry(moduleEvent())
	if err != nil {
		t.Fatal(err)
	}
	entry.Seq = 1
	cases := map[string]outbox.OutboxEntry{}
	peer := entry
	peer.Stream = "repo:libs"
	cases["peer stream"] = peer
	unallocated := entry
	unallocated.Seq = 0
	cases["no sequence"] = unallocated
	missingTime := entry
	missingTime.Created = time.Time{}
	cases["no timestamp"] = missingTime
	for name, body := range map[string]string{
		"invalid envelope": "{", "future version": `{"version":2,"key":"libs/a.jar"}`,
		"no entity": `{"version":1}`, "invalid module JSON": `{"version":1,"key":"x","payload":"eA=="}`,
	} {
		bad := entry
		bad.Payload = []byte(body)
		cases[name] = bad
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := outbox.EventFromEntry(bad); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatalf("invalid stored event: %v", err)
			}
		})
	}
}
