package federated

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// The event types of a repo:<key> stream (decision 0022, What
// propagates). Both carry an item's path; a put carries its whole state,
// so the change that wins the conflict rule leaves the same item on every
// member whatever order the changes arrive in. A deploy, a copy or move
// landing, a restore, a recorded checksum and a property change are puts;
// a delete, and a move's source, are deletes. The strings are on the wire
// and in every member's outbox, so they never change.
const (
	EventPut    = "item.put"
	EventDelete = "item.delete"
)

// payload is an entry's payload. A file's content is the entry's blob,
// whose SHA-256 and size are the entry's own.
type payload struct {
	Path       string              `json:"path"`
	Folder     bool                `json:"folder,omitempty"`
	Actual     kernel.Checksums    `json:"actual,omitzero"`
	Original   kernel.Checksums    `json:"original,omitzero"`
	Created    time.Time           `json:"created,omitzero"`
	CreatedBy  string              `json:"created_by,omitempty"`
	Modified   time.Time           `json:"modified,omitzero"`
	ModifiedBy string              `json:"modified_by,omitempty"`
	Properties map[string][]string `json:"properties,omitempty"`
}

// errRaced reports a record another transaction changed between the
// apply's read and its write. The apply rolls back and the sender's retry
// applies the entry again against what committed.
var errRaced = errors.New("the item changed while the entry applied; it applies again on retry")

// StreamStore is what the repository stream reads and writes, in the
// transaction its context carries.
type StreamStore interface {
	Peers() port.PeerStore
	Federation() port.FederationStore
	Stamps() port.ItemStampStore
}

// Stream is the repo:<key> stream kind (decision 0022): which members
// exchange a federated repository's changes, the producer that appends
// this instance's changes in their transactions (artifactsapi.ItemFeed),
// and the applier that settles other members' changes by the conflict
// rule and applies the winners through the artifacts module.
type Stream struct {
	Store  StreamStore
	Engine *streamsync.Engine
	// Items applies the winners (the artifacts module's FederatedItems).
	Items artifactsapi.FederatedItems
	// Repositories reads a repository's configuration, for its own
	// member's enabled flag.
	Repositories interface {
		Get(ctx context.Context, key string) (repo.Config, error)
	}
	// SelfBase is this instance's own member base URL
	// (Membership.SelfBase).
	SelfBase func(ctx context.Context) (string, error)
	Log      *slog.Logger
}

var (
	_ streamsync.Kind       = Stream{}
	_ streamsync.Pauser     = Stream{}
	_ artifactsapi.ItemFeed = Stream{}
)

func (s Stream) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// Name is the stream kind's prefix.
func (Stream) Name() string { return string(model.StreamRepository) }

// UseCases is two-way federated repository trust in both directions;
// receiving-only trust is FED-03's.
func (Stream) UseCases(streamsync.Direction) []model.UseCase {
	return []model.UseCase{model.UseFederatedRepo}
}

// key is the repository a stream of this kind names.
func key(stream string) (string, bool) {
	kind, rest, err := model.ParseStream(stream)
	return rest, err == nil && kind == model.StreamRepository
}

// Streams lists the streams of every repository with members.
func (s Stream) Streams(ctx context.Context) ([]string, error) {
	peers, err := s.Store.Peers().List(ctx, port.PeerFilter{})
	if err != nil {
		return nil, fmt.Errorf("federated repository streams: %w", err)
	}
	var out []string
	for _, p := range peers {
		ms, err := s.Store.Federation().MembersWithPeer(ctx, p.InstanceID)
		if err != nil {
			return nil, fmt.Errorf("federated repository streams: %w", err)
		}
		for _, m := range ms {
			out = append(out, model.RepositoryStream(m.RepoKey))
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// Subscribers lists every member of the stream's repository, disabled
// ones included: they stay subscribed, so pruning keeps what they have
// not received, and Paused stops what is sent to them.
func (s Stream) Subscribers(ctx context.Context, stream string) ([]string, error) {
	k, ok := key(stream)
	if !ok {
		return nil, nil
	}
	ms, err := s.Store.Federation().Members(ctx, k)
	if err != nil {
		return nil, fmt.Errorf("members of %s: %w", k, err)
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.PeerInstanceID)
	}
	return out, nil
}

// Paused stops sending the stream to a member that is disabled, and to
// every member while this instance's own member is: 7.x describes
// enabled false as "the local mirror pausing federating events to remote
// mirrors", and the configuration every member holds names which one is
// paused, so each instance stops its side.
func (s Stream) Paused(ctx context.Context, stream, peer string) (bool, error) {
	k, ok := key(stream)
	if !ok {
		return false, nil
	}
	ms, err := s.Store.Federation().Members(ctx, k)
	if err != nil {
		return false, fmt.Errorf("members of %s: %w", k, err)
	}
	for _, m := range ms {
		if m.PeerInstanceID == peer && !m.Enabled {
			return true, nil
		}
	}
	return s.selfDisabled(ctx, k)
}

func (s Stream) selfDisabled(ctx context.Context, k string) (bool, error) {
	if s.Repositories == nil || s.SelfBase == nil {
		return false, nil
	}
	c, err := s.Repositories.Get(ctx, k)
	if errors.Is(err, kernel.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("repository %s: %w", k, err)
	}
	self, err := s.SelfBase(ctx)
	if err != nil {
		return false, err
	}
	for _, m := range c.Members {
		if model.IsSelfMember(m.URL, k, self) {
			return m.Enabled != nil && !m.Enabled.Bool(), nil
		}
	}
	return false, nil
}

// References reports whether an item of the stream's repository holds
// the content here, so its members may fetch it.
func (s Stream) References(ctx context.Context, stream, sha256 string) (bool, error) {
	k, ok := key(stream)
	if !ok {
		return false, nil
	}
	held, err := s.Store.Stamps().Holds(ctx, k, sha256)
	if err != nil {
		return false, fmt.Errorf("content of %s: %w", k, err)
	}
	return held, nil
}

// ItemChanged is the producer: inside the change's transaction it appends
// the item's state to the repository's stream, when the repository has
// members, and records the change's stamp. The clock first sees the stamp
// the change replaces, so the change orders after everything this
// instance knew of the item, and the members agree that it wins.
func (s Stream) ItemChanged(ctx context.Context, c artifactsapi.ItemChange) error {
	ms, err := s.Store.Federation().Members(ctx, c.Repository)
	if err != nil {
		return fmt.Errorf("members of %s: %w", c.Repository, err)
	}
	if len(ms) == 0 {
		return nil
	}
	prev, err := s.Store.Stamps().Stamp(ctx, c.Repository, c.Path)
	switch {
	case err == nil:
		s.Engine.Observe(prev.Time)
	case !errors.Is(err, kernel.ErrNotFound):
		return fmt.Errorf("stamp of %s:%s: %w", c.Repository, c.Path, err)
	}
	en := outbox.OutboxEntry{Stream: model.RepositoryStream(c.Repository), EventType: EventPut}
	p := payload{Path: c.Path, Folder: c.Folder}
	if c.Deleted {
		en.EventType = EventDelete
	} else {
		p.Actual, p.Original, p.Properties = c.Actual, c.Original, c.Properties
		p.Created, p.CreatedBy, p.Modified, p.ModifiedBy = c.Created, c.CreatedBy, c.Modified, c.ModifiedBy
		if !c.Folder {
			en.BlobSHA256, en.BlobSize = c.Actual.SHA256, c.Size
		}
	}
	if en.Payload, err = json.Marshal(p); err != nil {
		return fmt.Errorf("encode %s:%s: %w", c.Repository, c.Path, err)
	}
	stored, err := s.Engine.Append(ctx, en)
	if err != nil {
		return err //nolint:wrapcheck // the engine names the stream
	}
	if err := s.Store.Stamps().Record(ctx, model.ItemStamp{
		RepoKey: c.Repository, Path: c.Path, Stamp: model.Stamp{Time: stored.Time, Origin: stored.Origin},
		Deleted: c.Deleted, Folder: c.Folder, SHA256: en.BlobSHA256, Size: en.BlobSize,
	}); err != nil {
		return fmt.Errorf("record stamp of %s:%s: %w", c.Repository, c.Path, err)
	}
	return nil
}

// Apply settles another member's change by the conflict rule (decision
// 0021): the later clock time wins, the higher origin instance id breaks
// a tie, and a delete's tombstone stops an older change from bringing the
// item back. A winning change applies through the artifacts module; a
// winning content change whose blob did not arrive is kept pending, and
// the item stays as it was until Publish. An entry this kind cannot read
// is skipped, as an unknown event type is, rather than stopping the
// stream behind it.
func (s Stream) Apply(ctx context.Context, e outbox.OutboxEntry, held bool) error {
	k, ok := key(e.Stream)
	if !ok || e.EventType != EventPut && e.EventType != EventDelete {
		return nil
	}
	var p payload
	if err := json.Unmarshal(e.Payload, &p); err != nil || p.Path == "" {
		s.log().WarnContext(ctx, "federated repository entry skipped: unreadable payload",
			"stream", e.Stream, "origin", e.Origin, "seq", e.Seq)
		return nil
	}
	stamp := model.Stamp{Time: e.Time, Origin: e.Origin}
	var prev *model.Stamp
	cur, err := s.Store.Stamps().Stamp(ctx, k, p.Path)
	switch {
	case err == nil:
		if !stamp.After(cur.Stamp) {
			return nil
		}
		prev = &cur.Stamp
	case !errors.Is(err, kernel.ErrNotFound):
		return fmt.Errorf("stamp of %s:%s: %w", k, p.Path, err)
	}
	next := model.ItemStamp{
		RepoKey: k, Path: p.Path, Stamp: stamp, Deleted: e.EventType == EventDelete, Folder: p.Folder,
		SHA256: e.BlobSHA256, Size: e.BlobSize,
	}
	err = nil
	switch {
	case next.Deleted:
		err = s.Items.Delete(ctx, k, p.Path, p.Folder)
	case !p.Folder && !held:
		next.Pending = e.Payload
	default:
		err = s.Items.Put(ctx, change(k, p, e.BlobSHA256, e.BlobSize))
	}
	if skipped(err) {
		s.log().WarnContext(ctx, "federated repository entry skipped",
			"repository", k, "path", p.Path, "origin", e.Origin, "seq", e.Seq, "error", err)
		return nil
	}
	if err != nil {
		return fmt.Errorf("apply %s:%s: %w", k, p.Path, err)
	}
	return s.replace(ctx, prev, next)
}

// skipped reports an apply that cannot succeed however often it is
// retried, so the stream moves past it rather than stopping behind it: a
// file where a folder must go, or the reverse, which two members made
// concurrently and no stamp orders (the item keeps what is here); or a
// repository that is not federated here (any more), whose membership
// change has not reached the sender yet. The stamp is not recorded, so a
// full sync still settles the item.
func skipped(err error) bool {
	return errors.Is(err, kernel.ErrConflict) || errors.Is(err, kernel.ErrNotFound)
}

func (s Stream) replace(ctx context.Context, prev *model.Stamp, next model.ItemStamp) error {
	done, err := s.Store.Stamps().Replace(ctx, prev, next)
	if err != nil {
		return fmt.Errorf("record stamp of %s:%s: %w", next.RepoKey, next.Path, err)
	}
	if !done {
		return fmt.Errorf("%s:%s: %w", next.RepoKey, next.Path, errRaced)
	}
	return nil
}

// change is the artifacts module's form of an entry's put.
func change(repoKey string, p payload, sha256 string, size int64) artifactsapi.ItemChange {
	actual := p.Actual
	actual.SHA256 = sha256
	return artifactsapi.ItemChange{
		Repository: repoKey, Path: p.Path, Folder: p.Folder, Size: size, Actual: actual, Original: p.Original,
		Created: p.Created, CreatedBy: p.CreatedBy, Modified: p.Modified, ModifiedBy: p.ModifiedBy,
		Properties: p.Properties,
	}
}

// Publish applies the changes of the stream's repository that waited for
// the blob sha256, now that it has arrived. One a later entry deleted or
// replaced meanwhile no longer waits, so it is not among them.
func (s Stream) Publish(ctx context.Context, stream, sha256 string) error {
	k, ok := key(stream)
	if !ok {
		return nil
	}
	waiting, err := s.Store.Stamps().PendingOn(ctx, k, sha256)
	if err != nil {
		return fmt.Errorf("items of %s pending %s: %w", k, sha256, err)
	}
	for _, w := range waiting {
		var p payload
		if err := json.Unmarshal(w.Pending, &p); err != nil {
			return fmt.Errorf("pending change of %s:%s: %w", k, w.Path, err)
		}
		err := s.Items.Put(ctx, change(k, p, w.SHA256, w.Size))
		if skipped(err) {
			s.log().WarnContext(ctx, "federated repository change skipped",
				"repository", k, "path", w.Path, "error", err)
		} else if err != nil {
			return fmt.Errorf("publish %s:%s: %w", k, w.Path, err)
		}
		published := w
		published.Pending = nil
		if err := s.replace(ctx, &w.Stamp, published); err != nil {
			return err
		}
	}
	return nil
}

// Pending reports whether a change of the stream's repository waits for
// the blob sha256, and the size its entry gave.
func (s Stream) Pending(ctx context.Context, stream, sha256 string) (int64, bool, error) {
	k, ok := key(stream)
	if !ok {
		return 0, false, nil
	}
	waiting, err := s.Store.Stamps().PendingOn(ctx, k, sha256)
	if err != nil {
		return 0, false, fmt.Errorf("items of %s pending %s: %w", k, sha256, err)
	}
	if len(waiting) == 0 {
		return 0, false, nil
	}
	return waiting[0].Size, true, nil
}

// PruneTombstones removes the tombstones older than retention, after
// which the members' outboxes have pruned every entry they could still
// order before (decision 0021: tombstones are kept for the retention
// window).
func (s Stream) PruneTombstones(ctx context.Context, now time.Time, retention time.Duration) (int64, error) {
	n, err := s.Store.Stamps().PruneTombstones(ctx, outbox.HLC{Wall: now.Add(-retention).UnixMilli()})
	if err != nil {
		return 0, fmt.Errorf("prune federated repository tombstones: %w", err)
	}
	return n, nil
}
