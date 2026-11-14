package httpkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// errEnvelope reports a document EnvelopeOf cannot split.
var errEnvelope = errors.New("httpkit: list envelope")

// Envelope is the JSON a streamed list is written inside: the bytes before
// its first element and the bytes after its last.
type Envelope struct{ head, tail []byte }

// ArrayEnvelope is the envelope of a list answered as a top-level array.
func ArrayEnvelope() Envelope { return Envelope{head: []byte("["), tail: []byte("]")} }

// EnvelopeOf is the envelope of doc, a JSON object whose member key holds
// the list. doc is given with that member an empty array and is marshalled
// as the collected answer's encoder would marshal it, then split between
// the brackets, so the members around the list keep their order, their
// escaping and their omissions. Writing the elements between head and tail
// gives the collected answer's bytes.
func EnvelopeOf(doc any, key string) (Envelope, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: %w", errEnvelope, err)
	}
	// b is json.Marshal's output, so it decodes: only its shape is checked.
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, _ := dec.Token(); tok != json.Delim('{') {
		return Envelope{}, fmt.Errorf("%w: %T is not a JSON object", errEnvelope, doc)
	}
	for dec.More() {
		if name, _ := dec.Token(); name != key {
			var skip json.RawMessage
			_ = dec.Decode(&skip)
			continue
		}
		open, _ := dec.Token()
		at := dec.InputOffset()
		if closing, _ := dec.Token(); open != json.Delim('[') || closing != json.Delim(']') {
			return Envelope{}, fmt.Errorf("%w: member %q is not an empty array", errEnvelope, key)
		}
		return Envelope{head: b[:at], tail: b[at:]}, nil
	}
	return Envelope{}, fmt.Errorf("%w: no member %q", errEnvelope, key)
}

// ListOptions shape a streamed list's 200.
type ListOptions struct {
	// ContentType is the answer's media type; "" is application/json.
	ContentType string
	// Envelope holds the list; the zero value is ArrayEnvelope.
	Envelope Envelope
}

// ListStream is a complete list answered from store pages (decision 0043,
// "Streaming complete lists"): it is written element by element as the
// pages are read, never built whole in memory, and its bytes are the
// collected answer's, json.Encoder's with its default settings: the same
// order, HTML escaping and trailing newline. StreamList builds one after
// reading the first page; Write answers with it.
type ListStream struct {
	// ctx is the request's: Write runs in the generated servers' Visit
	// methods, which receive only the writer, and the rest of the list is
	// read under the request's deadline.
	ctx   context.Context
	opts  ListOptions
	first [][]byte
	next  func(context.Context) ([][]byte, bool, error)
	seen  bool
}

// StreamList starts a streamed list over pages. render turns each row into
// the element written for it and is the caller's visibility filter: a row
// it does not keep is left out. Before anything is written, StreamList
// reads and renders pages until one keeps an element or the list ends, so
// a store failure there, or a list with nothing to show, still answers a
// normal error: its error is the store's or render's, unwrapped.
func StreamList[S, T any](
	ctx context.Context,
	pages *kernel.Pages[S],
	render func(S) (T, bool, error),
	opts ListOptions,
) (*ListStream, error) {
	l := &ListStream{ctx: ctx, opts: opts}
	l.next = func(ctx context.Context) ([][]byte, bool, error) {
		rows, ok, err := pages.Next(ctx)
		if err != nil || !ok {
			return nil, ok, err //nolint:wrapcheck // the store's own error, for the caller to map
		}
		l.seen = l.seen || len(rows) > 0
		out := make([][]byte, 0, len(rows))
		for _, row := range rows {
			v, keep, err := render(row)
			if err != nil {
				return nil, false, err
			}
			if !keep {
				continue
			}
			b, err := json.Marshal(v)
			if err != nil {
				return nil, false, fmt.Errorf("httpkit: encode list element: %w", err)
			}
			out = append(out, b)
		}
		return out, true, nil
	}
	for len(l.first) == 0 {
		elems, ok, err := l.next(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		l.first = elems
	}
	return l, nil
}

// Empty reports that the list keeps no element at all: StreamList read
// every page and render kept none.
func (l *ListStream) Empty() bool { return len(l.first) == 0 }

// Seen reports whether the pages StreamList read held any row, kept or
// not, which tells an empty list from one the caller may see nothing of.
func (l *ListStream) Seen() bool { return l.seen }

// Write answers 200 with the list. It writes one page at a time without
// flushing, so net/http frames an answer that fits its buffer with
// Content-Length and a larger one in chunks, as it frames the collected
// answer. Once the status is out it cannot change: a failed read or an
// expired deadline notes the cause for the access log and aborts the
// handler (http.ErrAbortHandler), so the closing bracket is never written
// and no client takes a shorter list for the whole one. A write the client
// no longer reads ends the answer quietly. It returns nil: the generated
// servers would answer a returned error with a second status.
func (l *ListStream) Write(w http.ResponseWriter) error {
	media, env := l.opts.ContentType, l.opts.Envelope
	if media == "" {
		media = "application/json"
	}
	if env.head == nil {
		env = ArrayEnvelope()
	}
	w.Header().Set("Content-Type", media)
	w.WriteHeader(http.StatusOK)
	buf := append([]byte(nil), env.head...)
	elems, sep := l.first, false
	l.first = nil
	for {
		for _, e := range elems {
			if sep {
				buf = append(buf, ',')
			}
			buf, sep = append(buf, e...), true
		}
		if _, err := w.Write(buf); err != nil {
			return nil
		}
		buf = buf[:0]
		var (
			ok  bool
			err error
		)
		// The stores in memory never look at the context, so a request
		// that has expired is caught here, before each further page.
		if err = l.ctx.Err(); err == nil {
			elems, ok, err = l.next(l.ctx)
		}
		if err != nil {
			NoteCause(l.ctx, fmt.Errorf("streamed list aborted after its first page: %w", err))
			panic(http.ErrAbortHandler)
		}
		if !ok {
			break
		}
	}
	_, _ = w.Write(append(append(buf, env.tail...), '\n'))
	return nil
}
