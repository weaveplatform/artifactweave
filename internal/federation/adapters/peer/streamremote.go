package peer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	peerclient "github.com/weaveplatform/artifactweave/pkg/client/peer"
)

// errAnswer reports a peer answer that is neither the operation's success
// nor one of the refusals the engine acts on.
var errAnswer = errors.New("unexpected peer answer")

// StreamRemote is the delivery engine's transport (streamsync.Remote): the
// signed peer client calling another instance's stream and blob
// operations.
type StreamRemote struct{ Factory *Factory }

var _ streamsync.Remote = StreamRemote{}

// Push sends a batch; a 409 is a *streamsync.ConflictError with the
// sequence the peer expects.
func (r StreamRemote) Push(ctx context.Context, peer model.Peer, b streamsync.Batch) (int64, error) {
	api, done, err := r.api(ctx, peer)
	if err != nil {
		return 0, err
	}
	defer done()
	body := peerclient.EventBatch{Origin: b.Origin, FromSeq: b.FromSeq, Events: make([]peerclient.Event, 0, len(b.Entries))}
	for _, en := range b.Entries {
		body.Events = append(body.Events, wireEvent(en))
	}
	resp, err := api.PushEventsWithResponse(ctx, b.Stream, &peerclient.PushEventsParams{}, body)
	if err != nil {
		return 0, fmt.Errorf("push %s to %s: %w", b.Stream, peer.InstanceID, err)
	}
	switch {
	case resp.StatusCode() == http.StatusOK && resp.JSON200 != nil:
		return resp.JSON200.AppliedSeq, nil
	case resp.StatusCode() == http.StatusConflict && resp.JSON409 != nil && resp.JSON409.ExpectedSeq != nil:
		return 0, &streamsync.ConflictError{Expected: *resp.JSON409.ExpectedSeq}
	default:
		return 0, answerError("push "+b.Stream, peer, resp.StatusCode(), resp.Body)
	}
}

// Pull reads a page of the peer's entries; a 410 is streamsync.ErrGone.
func (r StreamRemote) Pull(ctx context.Context, peer model.Peer, stream string, after int64, limit int) (streamsync.Page, error) {
	api, done, err := r.api(ctx, peer)
	if err != nil {
		return streamsync.Page{}, err
	}
	defer done()
	resp, err := api.PullEventsWithResponse(ctx, stream, &peerclient.PullEventsParams{After: &after, Limit: &limit})
	if err != nil {
		return streamsync.Page{}, fmt.Errorf("pull %s from %s: %w", stream, peer.InstanceID, err)
	}
	switch {
	case resp.StatusCode() == http.StatusOK && resp.JSON200 != nil:
	case resp.StatusCode() == http.StatusGone:
		return streamsync.Page{}, streamsync.ErrGone
	default:
		return streamsync.Page{}, answerError("pull "+stream, peer, resp.StatusCode(), resp.Body)
	}
	page := streamsync.Page{Origin: resp.JSON200.Origin}
	if resp.JSON200.Origin != peer.InstanceID {
		return streamsync.Page{}, fmt.Errorf("%w: pull %s from %s: the page names origin %s",
			errAnswer, stream, peer.InstanceID, resp.JSON200.Origin)
	}
	for _, ev := range resp.JSON200.Events {
		en, ok := entryOfClient(ev)
		if !ok {
			return streamsync.Page{}, fmt.Errorf("%w: pull %s from %s: event %d has hlc %q",
				errAnswer, stream, peer.InstanceID, ev.Seq, ev.Hlc)
		}
		en.Origin, en.Stream = peer.InstanceID, stream
		page.Entries = append(page.Entries, en)
	}
	if resp.JSON200.NextAfter != nil {
		page.NextAfter = *resp.JSON200.NextAfter
	}
	return page, nil
}

// FetchBlob streams the peer's blob to into. The answer's signature covers
// its Content-Digest, which must be the SHA-256 asked for; the content is
// hashed as into reads it, and content that does not match is an error
// even when into accepted it, so into must not make it visible before it
// returns (the blob store's commit checks the SHA-256 itself).
func (r StreamRemote) FetchBlob(ctx context.Context, peer model.Peer, sum string, into func(io.Reader) error) error {
	c, err := r.Factory.Client(ctx, peer)
	if err != nil {
		return fmt.Errorf("peer %s: %w", peer.InstanceID, err)
	}
	defer c.Close()
	// The trailing slash keeps the prefix when the operation's path is
	// resolved against it, as the generated client's constructor adds.
	req, err := peerclient.NewGetBlobRequest(c.base.String()+Prefix+"/", sum, &peerclient.GetBlobParams{})
	if err != nil {
		return fmt.Errorf("blob %s request: %w", sum, err)
	}
	req = req.WithContext(ctx)
	return c.Stream(req, func(resp *http.Response) error {
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			return answerError("fetch blob "+sum, peer, resp.StatusCode, body)
		}
		want, err := corepeer.DigestSHA256(resp.Header.Get(corepeer.HeaderContentDigest))
		if err != nil {
			return fmt.Errorf("fetch blob %s from %s: %w", sum, peer.InstanceID, err)
		}
		if hex.EncodeToString(want) != sum {
			return fmt.Errorf("fetch blob %s from %s: %w: the answer carries other content",
				sum, peer.InstanceID, corepeer.ErrDigest)
		}
		h := sha256.New()
		if err := into(io.TeeReader(resp.Body, h)); err != nil {
			return err
		}
		return checkSum(h, want, sum, peer)
	})
}

func checkSum(h hash.Hash, want []byte, sum string, peer model.Peer) error {
	if !bytes.Equal(h.Sum(nil), want) {
		return fmt.Errorf("fetch blob %s from %s: %w", sum, peer.InstanceID, corepeer.ErrDigest)
	}
	return nil
}

func (r StreamRemote) api(ctx context.Context, peer model.Peer) (*peerclient.ClientWithResponses, func(), error) {
	c, err := r.Factory.Client(ctx, peer)
	if err != nil {
		return nil, nil, fmt.Errorf("peer %s: %w", peer.InstanceID, err)
	}
	api, err := c.API()
	if err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("peer %s: %w", peer.InstanceID, err)
	}
	return api, c.Close, nil
}

// answerError names an answer the engine does not act on, with the start
// of its body, which on the flat envelope says why.
func answerError(what string, peer model.Peer, status int, body []byte) error {
	const shown = 512
	if len(body) > shown {
		body = body[:shown]
	}
	return fmt.Errorf("%w: %s at %s: %d %s", errAnswer, what, peer.InstanceID, status, bytes.TrimSpace(body))
}

// wireEvent writes an outbox entry as the client's wire event.
func wireEvent(en outbox.OutboxEntry) peerclient.Event {
	ev := eventOf(en)
	out := peerclient.Event{Seq: ev.Seq, Hlc: ev.Hlc, Type: ev.Type, Payload: ev.Payload}
	if ev.Blob != nil {
		out.Blob = &peerclient.BlobReference{Sha256: ev.Blob.Sha256, Size: ev.Blob.Size}
	}
	return out
}

// entryOfClient reads the client's wire event into an outbox entry.
func entryOfClient(ev peerclient.Event) (outbox.OutboxEntry, bool) {
	return entryOf(peergenEvent(ev))
}

func peergenEvent(ev peerclient.Event) peergen.Event {
	out := peergen.Event{Seq: ev.Seq, Hlc: ev.Hlc, Type: ev.Type, Payload: ev.Payload}
	if ev.Blob != nil {
		out.Blob = &peergen.BlobReference{Sha256: ev.Blob.Sha256, Size: ev.Blob.Size}
	}
	return out
}
