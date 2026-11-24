package peer

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// StreamService is the delivery engine the peer API's stream and blob
// operations call (streamsync.Engine).
type StreamService interface {
	Receive(ctx context.Context, caller model.Peer, b streamsync.Batch) (int64, error)
	Events(ctx context.Context, caller model.Peer, stream string, after int64, limit int) (streamsync.Page, error)
	Blob(ctx context.Context, caller model.Peer, sha256 string) (io.ReadSeekCloser, blob.Info, error)
}

// StreamServer serves POST and GET /streams/{stream}/events and GET
// /blobs/{sha256} (decision 0021, "Wire contract") for the caller
// Server.Middleware verified.
type StreamServer struct {
	Streams StreamService
}

// PushEvents applies a pushed batch.
func (s StreamServer) PushEvents(
	ctx context.Context,
	req peergen.PushEventsRequestObject,
) (peergen.PushEventsResponseObject, error) {
	caller, ok := corepeer.Caller(ctx)
	if !ok {
		return nil, apierror.New(http.StatusUnauthorized, notSigned)
	}
	if req.Body == nil {
		return nil, apierror.New(http.StatusBadRequest, "The body is required")
	}
	b := streamsync.Batch{Origin: req.Body.Origin, Stream: req.Stream, FromSeq: req.Body.FromSeq}
	for _, ev := range req.Body.Events {
		en, ok := entryOf(ev)
		if !ok {
			return nil, apierror.New(http.StatusBadRequest,
				"Event "+strconv.FormatInt(ev.Seq, 10)+": hlc must be 16 and 8 lowercase hex digits around a dot")
		}
		b.Entries = append(b.Entries, en)
	}
	applied, err := s.Streams.Receive(ctx, caller, b)
	var conflict *streamsync.ConflictError
	if errors.As(err, &conflict) {
		return peergen.PushEvents409JSONResponse{
			Code: "CONFLICT", Message: "The batch must start at sequence " + strconv.FormatInt(conflict.Expected, 10),
			ExpectedSeq: &conflict.Expected,
		}, nil
	}
	if err != nil {
		return nil, streamError(err)
	}
	return peergen.PushEvents200JSONResponse{Origin: b.Origin, Stream: b.Stream, AppliedSeq: applied}, nil
}

// PullEvents serves a peer that pulls.
func (s StreamServer) PullEvents(
	ctx context.Context,
	req peergen.PullEventsRequestObject,
) (peergen.PullEventsResponseObject, error) {
	caller, ok := corepeer.Caller(ctx)
	if !ok {
		return nil, apierror.New(http.StatusUnauthorized, notSigned)
	}
	var after int64
	if req.Params.After != nil {
		after = *req.Params.After
	}
	limit := streamsync.DefaultBatch
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	page, err := s.Streams.Events(ctx, caller, req.Stream, after, limit)
	if err != nil {
		return nil, streamError(err)
	}
	events := make([]peergen.Event, 0, len(page.Entries))
	for _, en := range page.Entries {
		events = append(events, eventOf(en))
	}
	out := peergen.PullEvents200JSONResponse{Origin: page.Origin, Stream: req.Stream, Events: events}
	if page.NextAfter > 0 {
		out.NextAfter = &page.NextAfter
	}
	return out, nil
}

// GetBlob serves content a stream the caller receives refers to. The
// answer is streamed: Content-Digest, which the signature covers, is the
// SHA-256 the caller asked for, set before the status, so the signing
// writer signs at once and the content is never held whole in memory.
func (s StreamServer) GetBlob(
	ctx context.Context,
	req peergen.GetBlobRequestObject,
) (peergen.GetBlobResponseObject, error) {
	caller, ok := corepeer.Caller(ctx)
	if !ok {
		return nil, apierror.New(http.StatusUnauthorized, notSigned)
	}
	r, info, err := s.Streams.Blob(ctx, caller, req.Sha256)
	if err != nil {
		return nil, streamError(err)
	}
	return blobAnswer{r: r, size: info.Size, sha256: req.Sha256}, nil
}

type blobAnswer struct {
	r      io.ReadCloser
	size   int64
	sha256 string
}

func (b blobAnswer) VisitGetBlobResponse(w http.ResponseWriter) error {
	defer b.r.Close()
	sum, _ := hex.DecodeString(b.sha256) // Blob checked the form
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(b.size, 10))
	w.Header().Set(corepeer.HeaderContentDigest, corepeer.DigestFromSHA256(sum))
	w.WriteHeader(http.StatusOK)
	_, err := io.Copy(w, b.r)
	return err //nolint:wrapcheck // the writer's own error
}

// streamError is the answer to an engine refusal: 404, 403, 410 or 400 in
// the flat envelope, or the error itself, which the strict error handler
// answers 500 and logs.
func streamError(err error) error {
	switch {
	case errors.Is(err, streamsync.ErrNoStream):
		return apierror.New(http.StatusNotFound, "This instance does not exchange the stream or the content with the caller")
	case errors.Is(err, streamsync.ErrRefused):
		return apierror.New(http.StatusForbidden, "This instance does not exchange the stream with the caller in this direction")
	case errors.Is(err, streamsync.ErrGone):
		return apierror.New(http.StatusGone, "The events after the position have been pruned; a full sync is needed")
	case errors.Is(err, kernel.ErrInvalid):
		return apierror.New(http.StatusBadRequest, invalidMessage(err))
	default:
		return err
	}
}

// invalidMessage is the engine's own account of an invalid request, which
// names only the request's numbers and fields.
func invalidMessage(err error) string { return err.Error() }

// entryOf reads one wire event into an outbox entry; false when its clock
// time does not parse.
func entryOf(ev peergen.Event) (outbox.OutboxEntry, bool) {
	t, err := outbox.ParseHLC(ev.Hlc)
	if err != nil {
		return outbox.OutboxEntry{}, false
	}
	en := outbox.OutboxEntry{Seq: ev.Seq, Time: t, EventType: ev.Type}
	if ev.Payload != nil && string(*ev.Payload) != "null" {
		en.Payload = []byte(*ev.Payload)
	}
	if ev.Blob != nil {
		en.BlobSHA256, en.BlobSize = ev.Blob.Sha256, ev.Blob.Size
	}
	return en, true
}

// eventOf writes an outbox entry as a wire event.
func eventOf(en outbox.OutboxEntry) peergen.Event {
	ev := peergen.Event{Seq: en.Seq, Hlc: en.Time.String(), Type: en.EventType}
	if len(en.Payload) > 0 {
		p := json.RawMessage(en.Payload)
		ev.Payload = &p
	}
	if en.BlobSHA256 != "" {
		ev.Blob = &peergen.BlobReference{Sha256: en.BlobSHA256, Size: en.BlobSize}
	}
	return ev
}
