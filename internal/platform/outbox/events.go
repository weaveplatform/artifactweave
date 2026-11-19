package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ModuleEventStream reserves one ordered stream per origin for business events.
// It shares the peer outbox's sequence allocator without interpreting peer data.
const ModuleEventStream = "platform:events:v1"

// MaxEventPayload bounds the encoded module payload before its storage envelope.
const MaxEventPayload = 64 << 10

// Event is a committed module transition. Origin and Seq identify it durably;
// Seq is assigned by AppendEvent. Topic and Payload belong to the producing
// module's api package, not to a webhook or peer wire representation.
type Event struct {
	Origin  string
	Seq     int64
	Topic   string
	Key     string
	At      time.Time
	Payload []byte
}

// Position is a consumer's contiguous acknowledgement in one origin's module
// stream. It is separate from the independently replaceable peer cursor.
type Position struct {
	Origin   string
	Consumer string
	Seq      int64
}

// EventStore persists general module events in the existing ordered outbox.
// Every call joins the caller's metadata transaction. Consumer effects and Ack
// must share that transaction, propagating any conflict to roll both back.
// Perform domain effects before Ack, which is the transaction's last lock
// acquisition: producers also take domain locks before the outbox stream guard.
// Next does not reserve/acknowledge events or acquire that write guard.
type EventStore interface {
	// AppendEvent validates e, allocates its sequence and commits with the
	// business mutation. A rolled-back append returns its number to the stream.
	AppendEvent(ctx context.Context, e Event) (Event, error)
	// RegisterConsumer is idempotent. New consumers start at the retained floor,
	// explicitly acknowledging that an earlier, pruned prefix is unavailable.
	RegisterConsumer(ctx context.Context, origin, consumer string) (Position, error)
	// Next returns the current position and the next bounded ordered batch.
	// Register first; an unknown consumer returns kernel.ErrNotFound. Reading
	// does not advance the position. Zero limits use kernel.DefaultLimit;
	// requests are capped at kernel.MaxLimit.
	Next(ctx context.Context, origin, consumer string, limit int) ([]Event, Position, error)
	// Ack advances exactly one existing event: current must equal expected.Seq
	// and eventSeq must equal expected.Seq+1. A stale, skipped or reversed
	// acknowledgement returns kernel.ErrConflict, never a silent success.
	Ack(ctx context.Context, expected Position, eventSeq int64) error
	// PruneAcknowledged removes at most the requested prefix, capped by every
	// registered consumer. With no consumers it retains all events. Sequence
	// allocation is not reset; late registration sees the recorded floor.
	PruneAcknowledged(ctx context.Context, origin string, through int64) (int64, error)
}

// ValidateConsumer checks the bounded, UTF-8 identity of a consumer position.
func ValidateConsumer(origin, consumer string) error {
	if origin == "" || len(origin) > 256 || !utf8.ValidString(origin) || strings.IndexByte(origin, 0) >= 0 {
		return fmt.Errorf("event origin must contain 1 to 256 UTF-8 bytes without NUL: %w", kernel.ErrInvalid)
	}
	if consumer == "" || len(consumer) > 256 || !utf8.ValidString(consumer) || strings.IndexByte(consumer, 0) >= 0 {
		return fmt.Errorf("event consumer must contain 1 to 256 UTF-8 bytes without NUL: %w", kernel.ErrInvalid)
	}
	return nil
}

type eventEnvelope struct {
	Version int    `json:"version"`
	Key     string `json:"key"`
	Payload []byte `json:"payload,omitempty"`
}

// ValidateEvent checks a new event before assigning its sequence. Payload is
// bounded JSON or empty. Errors never include payload bytes.
func ValidateEvent(e Event) error {
	switch {
	case e.Origin == "" || len(e.Origin) > 256 || !utf8.ValidString(e.Origin) || strings.IndexByte(e.Origin, 0) >= 0:
		return fmt.Errorf("event origin must contain 1 to 256 UTF-8 bytes without NUL: %w", kernel.ErrInvalid)
	case e.Seq != 0:
		return fmt.Errorf("event sequence is allocated by the store: %w", kernel.ErrInvalid)
	case e.Topic == "" || len(e.Topic) > 128 || !utf8.ValidString(e.Topic) || strings.IndexByte(e.Topic, 0) >= 0:
		return fmt.Errorf("event topic must contain 1 to 128 UTF-8 bytes without NUL: %w", kernel.ErrInvalid)
	case e.Key == "" || len(e.Key) > 1024 || !utf8.ValidString(e.Key):
		return fmt.Errorf("event entity key must contain 1 to 1024 bytes: %w", kernel.ErrInvalid)
	case e.At.IsZero():
		return fmt.Errorf("event timestamp is required: %w", kernel.ErrInvalid)
	case len(e.Payload) > MaxEventPayload:
		return fmt.Errorf("event payload exceeds %d bytes: %w", MaxEventPayload, kernel.ErrInvalid)
	case len(e.Payload) > 0 && !json.Valid(e.Payload):
		return fmt.Errorf("event payload is not JSON: %w", kernel.ErrInvalid)
	default:
		return nil
	}
}

// EventEntry encodes a new event for the shared outbox. The versioned envelope
// keeps the entity key and exact module payload bytes without changing peer
// entries or committing a vendor webhook serialization contract.
func EventEntry(e Event) (OutboxEntry, error) {
	if err := ValidateEvent(e); err != nil {
		return OutboxEntry{}, err
	}
	// Strings and byte slices contain no unsupported JSON values or cycles.
	payload, _ := json.Marshal(eventEnvelope{Version: 1, Key: e.Key, Payload: e.Payload})
	return OutboxEntry{
		Origin: e.Origin, Stream: ModuleEventStream, EventType: e.Topic,
		Time: HLC{Wall: e.At.UnixMilli()}, Created: e.At, Payload: payload,
	}, nil
}

// EventFromEntry reads the module envelope and keeps its allocated identity.
// Peer entries, unknown envelope versions and malformed persisted values are
// refused rather than fabricated into events for a consumer to acknowledge.
func EventFromEntry(entry OutboxEntry) (Event, error) {
	if entry.Stream != ModuleEventStream || entry.Seq <= 0 {
		return Event{}, fmt.Errorf("entry is not an allocated module event: %w", kernel.ErrInvalid)
	}
	var env eventEnvelope
	if err := json.Unmarshal(entry.Payload, &env); err != nil || env.Version != 1 {
		return Event{}, fmt.Errorf("invalid module event envelope: %w", kernel.ErrInvalid)
	}
	e := Event{Origin: entry.Origin, Topic: entry.EventType, Key: env.Key, At: entry.Created, Payload: env.Payload}
	if err := ValidateEvent(e); err != nil {
		return Event{}, err
	}
	e.Seq = entry.Seq
	return e, nil
}
