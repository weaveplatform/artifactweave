package peer_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// fakeStreams answers the stream operations as the test scripts, and keeps
// what it was asked.
type fakeStreams struct {
	caller  model.Peer
	batch   streamsync.Batch
	receive func(streamsync.Batch) (int64, error)
	events  func(after int64, limit int) (streamsync.Page, error)
	blob    func(sha string) (io.ReadSeekCloser, blob.Info, error)
	// snapshot answers GET snapshot; stream is the stream asked for.
	snapshot func(stream, cursor string, limit int) (streamsync.SnapshotPage, error)
}

func (f *fakeStreams) Snapshot(_ context.Context, caller model.Peer, stream, cursor string, limit int) (streamsync.SnapshotPage, error) {
	f.caller = caller
	return f.snapshot(stream, cursor, limit)
}

func (f *fakeStreams) Receive(_ context.Context, caller model.Peer, b streamsync.Batch) (int64, error) {
	f.caller, f.batch = caller, b
	return f.receive(b)
}

func (f *fakeStreams) Events(_ context.Context, caller model.Peer, _ string, after int64, limit int) (streamsync.Page, error) {
	f.caller = caller
	return f.events(after, limit)
}

func (f *fakeStreams) Blob(_ context.Context, caller model.Peer, sha string) (io.ReadSeekCloser, blob.Info, error) {
	f.caller = caller
	return f.blob(sha)
}

// streamAPI is the peer API with the stream operations served, as the
// application mounts it.
type streamAPI struct {
	peergen.UnimplementedStrict
	s peeradapter.StreamServer
}

func (a streamAPI) PushEvents(ctx context.Context, r peergen.PushEventsRequestObject) (peergen.PushEventsResponseObject, error) {
	return a.s.PushEvents(ctx, r)
}

func (a streamAPI) PullEvents(ctx context.Context, r peergen.PullEventsRequestObject) (peergen.PullEventsResponseObject, error) {
	return a.s.PullEvents(ctx, r)
}

func (a streamAPI) GetSnapshot(ctx context.Context, r peergen.GetSnapshotRequestObject) (peergen.GetSnapshotResponseObject, error) {
	return a.s.GetSnapshot(ctx, r)
}

func (a streamAPI) GetBlob(ctx context.Context, r peergen.GetBlobRequestObject) (peergen.GetBlobResponseObject, error) {
	return a.s.GetBlob(ctx, r)
}

// streamRemote is a peer serving f behind the signed transport, and the
// StreamRemote that calls it.
func streamRemote(t *testing.T, f *fakeStreams) (*remote, peeradapter.StreamRemote) {
	t.Helper()
	rm := newRemote(t)
	r := chi.NewRouter()
	opts := peergen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
		ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
	}
	peergen.HandlerWithOptions(
		peergen.NewStrictHandlerWithOptions(streamAPI{s: peeradapter.StreamServer{Streams: f}}, nil, opts),
		peergen.ChiServerOptions{
			BaseURL: peeradapter.Prefix, BaseRouter: r,
			ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
		},
	)
	rm.handler = r.ServeHTTP
	return rm, peeradapter.StreamRemote{Factory: rm.factory(t, peeradapter.ClientOptions{Attempts: 1})}
}

var hlc = outbox.HLC{Wall: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC).UnixMilli(), Logical: 1}

func entry(seq int64, payload string) outbox.OutboxEntry {
	return outbox.OutboxEntry{Seq: seq, Time: hlc, EventType: "put", Payload: []byte(payload)}
}

func TestUnitStreamPush(t *testing.T) {
	f := &fakeStreams{receive: func(b streamsync.Batch) (int64, error) { return b.FromSeq + int64(len(b.Entries)) - 1, nil }}
	rm, sr := streamRemote(t, f)
	// An integer past 2^53 is carried as the origin wrote it.
	const payload = `{"n":12345678901234567891,"s":"x"}`
	sum := strings.Repeat("ab", 32)
	b := streamsync.Batch{Origin: callerID, Stream: "test:s", FromSeq: 3, Entries: []outbox.OutboxEntry{
		entry(3, payload),
		{Seq: 4, Time: hlc, EventType: "put", BlobSHA256: sum, BlobSize: 9},
	}}
	applied, err := sr.Push(t.Context(), rm.peer, b)
	if err != nil || applied != 4 {
		t.Fatalf("push: %d, %v", applied, err)
	}
	got := f.batch
	if f.caller.InstanceID != callerID || got.Origin != callerID || got.Stream != "test:s" || got.FromSeq != 3 {
		t.Fatalf("received %+v from %s", got, f.caller.InstanceID)
	}
	if string(got.Entries[0].Payload) != payload || got.Entries[0].Time != hlc {
		t.Fatalf("entry %+v, payload %s", got.Entries[0], got.Entries[0].Payload)
	}
	if got.Entries[1].BlobSHA256 != sum || got.Entries[1].BlobSize != 9 || got.Entries[1].Payload != nil {
		t.Fatalf("content entry %+v", got.Entries[1])
	}

	// A 409 carries the expected sequence; other refusals are errors that
	// name the answer.
	for _, c := range []struct {
		err    error
		status string
	}{
		{&streamsync.ConflictError{Expected: 7}, ""},
		{streamsync.ErrNoStream, "404"},
		{streamsync.ErrRefused, "403"},
		{fmt.Errorf("bad: %w", kernel.ErrInvalid), "400"},
		{errors.New("disk"), "500"},
	} {
		f.receive = func(streamsync.Batch) (int64, error) { return 0, c.err }
		_, err := sr.Push(t.Context(), rm.peer, b)
		var conflict *streamsync.ConflictError
		switch {
		case c.status == "":
			if !errors.As(err, &conflict) || conflict.Expected != 7 {
				t.Errorf("conflict: %v", err)
			}
		case err == nil || !strings.Contains(err.Error(), c.status):
			t.Errorf("%v: %v, want %s", c.err, err, c.status)
		}
	}
}

func TestUnitStreamPull(t *testing.T) {
	f := &fakeStreams{events: func(after int64, limit int) (streamsync.Page, error) {
		if limit != 2 {
			return streamsync.Page{}, fmt.Errorf("limit %d: %w", limit, kernel.ErrInvalid)
		}
		return streamsync.Page{Origin: serverID, Entries: []outbox.OutboxEntry{
			entry(after+1, `{"a":1}`), {Seq: after + 2, Time: hlc, EventType: "x", BlobSHA256: strings.Repeat("0", 64), BlobSize: 1},
		}, NextAfter: after + 2}, nil
	}}
	rm, sr := streamRemote(t, f)
	page, err := sr.Pull(t.Context(), rm.peer, "test:s", 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.Origin != serverID || len(page.Entries) != 2 || page.NextAfter != 7 {
		t.Fatalf("page %+v", page)
	}
	if e := page.Entries[0]; e.Seq != 6 || e.Origin != serverID || e.Stream != "test:s" || string(e.Payload) != `{"a":1}` {
		t.Fatalf("entry %+v", e)
	}
	if page.Entries[1].BlobSize != 1 {
		t.Fatalf("content entry %+v", page.Entries[1])
	}
	// The last page has no next position, and an empty page names this
	// instance.
	f.events = func(int64, int) (streamsync.Page, error) { return streamsync.Page{Origin: serverID}, nil }
	if page, err := sr.Pull(t.Context(), rm.peer, "test:s", 5, 2); err != nil || page.NextAfter != 0 || len(page.Entries) != 0 {
		t.Fatalf("empty page %+v, %v", page, err)
	}
	// A page naming another origin is not this peer's.
	f.events = func(int64, int) (streamsync.Page, error) { return streamsync.Page{Origin: "jfrt@other"}, nil }
	if _, err := sr.Pull(t.Context(), rm.peer, "test:s", 5, 2); err == nil {
		t.Fatal("a page of another origin was taken")
	}
	f.events = func(int64, int) (streamsync.Page, error) { return streamsync.Page{}, streamsync.ErrGone }
	if _, err := sr.Pull(t.Context(), rm.peer, "test:s", 5, 2); !errors.Is(err, streamsync.ErrGone) {
		t.Fatalf("410: %v", err)
	}
	f.events = func(int64, int) (streamsync.Page, error) { return streamsync.Page{}, streamsync.ErrRefused }
	if _, err := sr.Pull(t.Context(), rm.peer, "test:s", 5, 2); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("403: %v", err)
	}
	// A limit outside the peer's range is the peer's 400.
	if _, err := sr.Pull(t.Context(), rm.peer, "test:s", 5, 2000); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("limit 2000: %v", err)
	}
	// Without the parameters the peer reads from 0 with its default.
	var asked []any
	f.events = func(after int64, limit int) (streamsync.Page, error) {
		asked = []any{after, limit}
		return streamsync.Page{Origin: serverID}, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := corepeer.WithCaller(req.Context(), model.Peer{InstanceID: callerID})
	if _, err := (peeradapter.StreamServer{Streams: f}).PullEvents(ctx, peergen.PullEventsRequestObject{Stream: "test:s"}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(asked) != fmt.Sprint([]any{int64(0), streamsync.DefaultBatch}) {
		t.Fatalf("asked %v", asked)
	}
}

type readSeekCloser struct{ *bytes.Reader }

func (readSeekCloser) Close() error { return nil }

func blobOf(s string) (io.ReadSeekCloser, blob.Info, string) {
	sum := sha256.Sum256([]byte(s))
	return readSeekCloser{bytes.NewReader([]byte(s))}, blob.Info{Size: int64(len(s))}, hex.EncodeToString(sum[:])
}

func TestUnitStreamBlobFetch(t *testing.T) {
	const content = "the blob's content"
	_, _, sum := blobOf(content)
	f := &fakeStreams{blob: func(sha string) (io.ReadSeekCloser, blob.Info, error) {
		r, info, _ := blobOf(content)
		return r, info, nil
	}}
	rm, sr := streamRemote(t, f)
	var got []byte
	read := func(r io.Reader) error {
		var err error
		got, err = io.ReadAll(r)
		return err
	}
	if err := sr.FetchBlob(t.Context(), rm.peer, sum, read); err != nil {
		t.Fatal(err)
	}
	if string(got) != content || f.caller.InstanceID != callerID {
		t.Fatalf("fetched %q for %s", got, f.caller.InstanceID)
	}

	// Content that is not what was asked for fails, even once read.
	_, _, other := blobOf("other")
	if err := sr.FetchBlob(t.Context(), rm.peer, other, read); !errors.Is(err, corepeer.ErrDigest) {
		t.Fatalf("other content: %v", err)
	}
	// into's own failure is returned as it is.
	boom := errors.New("disk full")
	if err := sr.FetchBlob(t.Context(), rm.peer, sum, func(io.Reader) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("into failed: %v", err)
	}
	// A refusal is an error naming the status.
	f.blob = func(string) (io.ReadSeekCloser, blob.Info, error) { return nil, blob.Info{}, streamsync.ErrNoStream }
	if err := sr.FetchBlob(t.Context(), rm.peer, sum, read); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("404: %v", err)
	}
	// An answer whose body was changed on the way fails its digest, and
	// one whose digest was changed too fails its signature.
	f.blob = func(string) (io.ReadSeekCloser, blob.Info, error) {
		r, info, _ := blobOf(content)
		return r, info, nil
	}
	rm.tamper = func(r *httptest.ResponseRecorder) {
		r.Body = bytes.NewBufferString(strings.ToUpper(content))
	}
	if err := sr.FetchBlob(t.Context(), rm.peer, sum, read); !errors.Is(err, corepeer.ErrDigest) {
		t.Fatalf("changed body: %v", err)
	}
	_, _, upper := blobOf(strings.ToUpper(content))
	rm.tamper = func(r *httptest.ResponseRecorder) {
		r.Body = bytes.NewBufferString(strings.ToUpper(content))
		b, _ := hex.DecodeString(upper)
		r.Header().Set(corepeer.HeaderContentDigest, corepeer.DigestFromSHA256(b))
	}
	var unverified *peeradapter.UnverifiedAnswerError
	if err := sr.FetchBlob(t.Context(), rm.peer, sum, read); !errors.As(err, &unverified) {
		t.Fatalf("changed body and digest: %v", err)
	}
	rm.tamper = func(r *httptest.ResponseRecorder) { r.Header().Set(corepeer.HeaderContentDigest, "sha-256=:AA==:") }
	if err := sr.FetchBlob(t.Context(), rm.peer, sum, read); !errors.As(err, &unverified) {
		t.Fatalf("digest replaced: %v", err)
	}
	rm.tamper = nil
	// A peer without a key, or a base URL the client cannot call, is not
	// fetched from.
	nokey := rm.peer
	nokey.PublicKey = nil
	if err := sr.FetchBlob(t.Context(), nokey, sum, read); !errors.Is(err, peeradapter.ErrNoPeerKey) {
		t.Fatalf("no key: %v", err)
	}
	bad := rm.peer
	bad.BaseURL = "ftp://x"
	if err := sr.FetchBlob(t.Context(), bad, sum, read); err == nil {
		t.Fatal("an ftp peer was called")
	}
	if _, err := sr.Push(t.Context(), bad, streamsync.Batch{}); err == nil {
		t.Fatal("an ftp peer was pushed to")
	}
	if _, err := sr.Pull(t.Context(), bad, "test:s", 0, 1); err == nil {
		t.Fatal("an ftp peer was pulled from")
	}
}

func TestUnitStreamServerRefusals(t *testing.T) {
	f := &fakeStreams{}
	s := peeradapter.StreamServer{Streams: f}
	signed := corepeer.WithCaller(t.Context(), model.Peer{InstanceID: callerID})
	if _, err := s.PushEvents(t.Context(), peergen.PushEventsRequestObject{}); statusOf(t, err) != 401 {
		t.Error("unsigned push")
	}
	if _, err := s.PullEvents(t.Context(), peergen.PullEventsRequestObject{}); statusOf(t, err) != 401 {
		t.Error("unsigned pull")
	}
	if _, err := s.GetBlob(t.Context(), peergen.GetBlobRequestObject{}); statusOf(t, err) != 401 {
		t.Error("unsigned blob")
	}
	if _, err := s.PushEvents(signed, peergen.PushEventsRequestObject{}); statusOf(t, err) != 400 {
		t.Error("no body")
	}
	bad := &peergen.PushEventsJSONRequestBody{Origin: callerID, FromSeq: 1, Events: []peergen.Event{{Seq: 1, Hlc: "now", Type: "put"}}}
	if _, err := s.PushEvents(signed, peergen.PushEventsRequestObject{Body: bad}); statusOf(t, err) != 400 {
		t.Error("bad hlc")
	}
	f.blob = func(string) (io.ReadSeekCloser, blob.Info, error) {
		return nil, blob.Info{}, fmt.Errorf("sha: %w", kernel.ErrInvalid)
	}
	if _, err := s.GetBlob(signed, peergen.GetBlobRequestObject{Sha256: "x"}); statusOf(t, err) != 400 {
		t.Error("bad sha")
	}
	f.events = func(int64, int) (streamsync.Page, error) { return streamsync.Page{}, streamsync.ErrGone }
	if _, err := s.PullEvents(signed, peergen.PullEventsRequestObject{}); statusOf(t, err) != 410 {
		t.Error("gone")
	}
}
