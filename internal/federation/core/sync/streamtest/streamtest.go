package streamtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	gosync "sync"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// Name is the kind's stream prefix: its streams are "test:<name>".
const Name = "test"

// Event types: Put sets an item's value and, with a blob, its content;
// Delete removes it. Any other type is skipped and still counts as
// applied.
const (
	Put    = "put"
	Delete = "delete"
)

// Store is where the kind keeps what it has applied: the settings store,
// in the transaction that applies the entry.
type Store interface {
	tx.Runner
	Settings() settings.SettingsStore
}

// Item is one key's state on an instance. Pending is set while its blob
// fetch has failed; a pending item is not visible.
type Item struct {
	Value   string `json:"value,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Pending bool   `json:"pending,omitempty"`
}

// State is what an instance holds of one origin's stream: every entry it
// applied, in the order applied as "<seq>", and the items.
type State struct {
	Applied []string        `json:"applied,omitempty"`
	Items   map[string]Item `json:"items,omitempty"`
}

// Kind is the test stream kind. UseCases defaults to federated-repo/* for
// both directions; Subscribe sets who exchanges a stream.
type Kind struct {
	Store Store
	// Send and Receive replace the use cases a peer must hold.
	Send, Receive []model.UseCase

	mu   gosync.Mutex
	subs map[string][]string
}

var _ streamsync.Kind = (*Kind)(nil)

// Name is the kind's stream prefix.
func (*Kind) Name() string { return Name }

// UseCases are Send or Receive, federated-repo/* by default.
func (k *Kind) UseCases(d streamsync.Direction) []model.UseCase {
	use := k.Send
	if d == streamsync.Receive {
		use = k.Receive
	}
	if use == nil {
		return []model.UseCase{model.UseFederatedRepo}
	}
	return use
}

// Subscribe makes stream exchanged with peers, replacing its subscribers.
func (k *Kind) Subscribe(stream string, peers ...string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.subs == nil {
		k.subs = map[string][]string{}
	}
	k.subs[stream] = slices.Clone(peers)
}

// Streams lists the subscribed streams, ordered.
func (k *Kind) Streams(context.Context) ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Sorted(maps.Keys(k.subs)), nil
}

// Subscribers lists stream's subscribers.
func (k *Kind) Subscribers(_ context.Context, stream string) ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.subs[stream]), nil
}

// key is where the state of origin's stream is kept.
func key(stream, origin string) string { return "streamtest/" + stream + "/" + origin }

// State reads what this instance holds of origin's stream.
func (k *Kind) State(ctx context.Context, stream, origin string) (State, error) {
	var s State
	err := k.Store.Settings().Get(ctx, key(stream, origin), &s)
	if errors.Is(err, kernel.ErrNotFound) {
		return State{Items: map[string]Item{}}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("streamtest state: %w", err)
	}
	if s.Items == nil {
		s.Items = map[string]Item{}
	}
	return s, nil
}

func (k *Kind) update(ctx context.Context, stream, origin string, fn func(*State)) error {
	s, err := k.State(ctx, stream, origin)
	if err != nil {
		return err
	}
	fn(&s)
	if err := k.Store.Settings().Put(ctx, key(stream, origin), s); err != nil {
		return fmt.Errorf("streamtest state: %w", err)
	}
	return nil
}

// payload is an entry's payload.
type payload struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

// Apply applies an entry: Put and Delete change the item, any other type
// is only recorded as applied.
func (k *Kind) Apply(ctx context.Context, e outbox.OutboxEntry, held bool) error {
	var p payload
	if len(e.Payload) > 0 {
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("streamtest payload: %w", err)
		}
	}
	return k.update(ctx, e.Stream, e.Origin, func(s *State) {
		s.Applied = append(s.Applied, strconv.FormatInt(e.Seq, 10))
		switch e.EventType {
		case Put:
			s.Items[p.Key] = Item{Value: p.Value, SHA256: e.BlobSHA256, Pending: !held}
		case Delete:
			delete(s.Items, p.Key)
		}
	})
}

// Publish makes items still pending sha256 visible. An item deleted, or put
// again with other content, since it was pending no longer is.
func (k *Kind) Publish(ctx context.Context, stream, sha256 string) error {
	keys, err := k.Store.Settings().Keys(ctx)
	if err != nil {
		return fmt.Errorf("streamtest keys: %w", err)
	}
	for _, sk := range keys {
		origin, ok := strings.CutPrefix(sk, key(stream, ""))
		if !ok {
			continue
		}
		if err := k.update(ctx, stream, origin, func(s *State) {
			for name, it := range s.Items {
				if it.Pending && it.SHA256 == sha256 {
					it.Pending = false
					s.Items[name] = it
				}
			}
		}); err != nil {
			return err
		}
	}
	return nil
}

// Pending reports whether an item of stream, of any origin, still waits
// for the content.
func (k *Kind) Pending(ctx context.Context, stream, sha256 string) (bool, error) {
	return k.find(ctx, stream, func(it Item) bool { return it.Pending && it.SHA256 == sha256 })
}

// References reports whether a visible item of stream, of any origin, has
// the content.
func (k *Kind) References(ctx context.Context, stream, sha256 string) (bool, error) {
	return k.find(ctx, stream, func(it Item) bool { return !it.Pending && it.SHA256 == sha256 })
}

// find reports whether an item of stream, of any origin, matches.
func (k *Kind) find(ctx context.Context, stream string, match func(Item) bool) (bool, error) {
	keys, err := k.Store.Settings().Keys(ctx)
	if err != nil {
		return false, fmt.Errorf("streamtest keys: %w", err)
	}
	for _, sk := range keys {
		origin, ok := strings.CutPrefix(sk, key(stream, ""))
		if !ok {
			continue
		}
		s, err := k.State(ctx, stream, origin)
		if err != nil {
			return false, err
		}
		for _, it := range s.Items {
			if match(it) {
				return true, nil
			}
		}
	}
	return false, nil
}

// Change is one change a test makes on the origin.
type Change struct {
	Type  string
	Key   string
	Value string
	// Content is stored and referenced; SHA256 and Size, when Content is
	// nil, reference content without storing it.
	Content []byte
	SHA256  string
	Size    int64
}

// Commit makes c on this instance and appends its entry through e, in one
// transaction, as a producer does; fail, when not nil, is returned from
// the transaction after the append, rolling both back.
func (k *Kind) Commit(ctx context.Context, e *streamsync.Engine, stream string, c Change, fail error) (outbox.OutboxEntry, error) {
	if c.Content != nil {
		sum := sha256.Sum256(c.Content)
		c.SHA256, c.Size = hex.EncodeToString(sum[:]), int64(len(c.Content))
		if err := store(ctx, e.Blobs, c.Content, c.SHA256); err != nil {
			return outbox.OutboxEntry{}, err
		}
	}
	body, _ := json.Marshal(payload{Key: c.Key, Value: c.Value}) //nolint:errchkjson // strings only
	var out outbox.OutboxEntry
	err := k.Store.Tx(ctx, func(ctx context.Context) error {
		en, err := e.Append(ctx, outbox.OutboxEntry{
			Stream: stream, EventType: c.Type, Payload: body, BlobSHA256: c.SHA256, BlobSize: c.Size,
		})
		if err != nil {
			return err //nolint:wrapcheck // the engine names its failures
		}
		out = en
		if err := k.Apply(ctx, en, true); err != nil {
			return err
		}
		return fail
	})
	if err != nil {
		return outbox.OutboxEntry{}, err //nolint:wrapcheck // the test's own error or the store's
	}
	return out, nil
}

func store(ctx context.Context, blobs blob.Store, content []byte, sum string) error {
	up, err := blobs.BeginUpload(ctx)
	if err != nil {
		return fmt.Errorf("streamtest blob: %w", err)
	}
	if _, err := up.Append(ctx, bytes.NewReader(content)); err != nil {
		return fmt.Errorf("streamtest blob: %w", err)
	}
	if _, err := up.Commit(ctx, blob.Digests{SHA256: sum}); err != nil {
		return fmt.Errorf("streamtest blob: %w", err)
	}
	return nil
}
