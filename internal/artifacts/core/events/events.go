package events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// Store supplies the current public identity, event stream and a secret-free
// repository snapshot. Every call uses the owner's metadata transaction.
type Store interface {
	Instance() instance.InstanceStore
	Events() outbox.EventStore
	Repos() port.RepoStore
}

// Metadata captures a committed outcome without retaining request credentials.
func Metadata(ctx context.Context, by string) api.EventMetadata {
	return api.EventMetadata{Version: 1, Outcome: "committed", Actor: outbox.ActorFrom(ctx, by)}
}

// Repository captures descriptive metadata without adding an authorization or
// lifetime lock after the mutation's existing domain locks.
func Repository(ctx context.Context, s Store, key string) (api.EventRepository, error) {
	current, err := s.Repos().EventContext(ctx, key)
	if err != nil {
		return api.EventRepository{}, fmt.Errorf("capture repository event context: %w", err)
	}
	return api.EventRepository{Key: current.Key, ProjectKey: current.ProjectKey, PackageType: current.PackageType}, nil
}

// Artifact preserves identity/location without retaining a mutable model.
func Artifact(repository api.EventRepository, it model.Item) api.EventArtifact {
	return api.EventArtifact{Repository: repository, ID: it.ID, Path: it.FullPath(), Cached: it.Cached, Folder: it.Folder}
}

// Key gives an unambiguous bounded identity even when paths contain delimiters
// or exceed the stream's entity-key size. Payloads retain the readable facts.
func Key(parts ...string) string {
	raw, _ := json.Marshal(parts)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// Emit freezes typed module facts before transaction finalization. The callback
// only appends; a failure rolls back the owner's business mutation.
func Emit(ctx context.Context, s Store, topic, key string, at time.Time, payload any) error {
	// Existing embedders can use a zero domain clock; event time still names
	// when the transition was recorded without changing those domain fields.
	if at.IsZero() {
		at = time.Now().UTC()
	}
	origin, err := instance.EnsureID(ctx, s)
	if err != nil {
		return fmt.Errorf("resolve artifact event origin: %w", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode artifact event: %w", err)
	}
	if err := outbox.Stage(ctx, s.Events(), outbox.Event{Origin: origin, Topic: topic, Key: key, At: at, Payload: raw}); err != nil {
		return fmt.Errorf("stage artifact event: %w", err)
	}
	return nil
}

// EmitArtifact records one file transition before its repository can disappear.
func EmitArtifact(ctx context.Context, s Store, topic string, it model.Item, source *api.EventArtifact, by string, at time.Time) error {
	repository, err := Repository(ctx, s, it.RepoKey)
	if err != nil {
		return err
	}
	facts := api.ArtifactEvent{EventMetadata: Metadata(ctx, by), Item: Artifact(repository, it), Source: source, Size: it.Size, SHA256: it.Actual.SHA256}
	return Emit(ctx, s, topic, Key("item", strconv.FormatInt(it.ID, 10)), at, facts)
}
