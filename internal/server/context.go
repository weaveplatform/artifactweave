package server

import (
	"context"
	"strings"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxOriginalContentType
	ctxAccept
	ctxRawBody
	ctxCause
	ctxSearchQuery
	ctxUser
)

// WithRequestID returns ctx carrying the request's correlation id, which
// RequestIDFrom reads back for log lines.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxRequestID, id)
}

// RequestIDFrom returns the correlation id WithRequestID stored, or "" for a
// context that never passed the request ID middleware.
func RequestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxRequestID).(string)
	return v
}

// WithOriginalContentType returns ctx carrying the vendor media type a client
// sent before the content-type middleware rewrote it to application/json.
func WithOriginalContentType(ctx context.Context, mt string) context.Context {
	return context.WithValue(ctx, ctxOriginalContentType, mt)
}

// OriginalContentType returns the vendor media type a client sent before
// the content-type middleware rewrote it, or "" when the client sent a plain
// type.
func OriginalContentType(ctx context.Context) string {
	v, _ := ctx.Value(ctxOriginalContentType).(string)
	return v
}

// WithAccept keeps the Accept header for strict handlers, which see only
// their decoded request: several operations answer with a vendor media type
// when the client asks for one, and their specs do not declare Accept.
func WithAccept(ctx context.Context, accept string) context.Context {
	return context.WithValue(ctx, ctxAccept, accept)
}

// Accepts reports whether the Accept header WithAccept kept names mediaType.
func Accepts(ctx context.Context, mediaType string) bool {
	v, _ := ctx.Value(ctxAccept).(string)
	return v != "" && strings.Contains(v, mediaType)
}

// WithRawBody returns ctx carrying a request body as read before the
// generated binding decoded it, for a handler that needs the bytes as sent.
func WithRawBody(ctx context.Context, b []byte) context.Context {
	return context.WithValue(ctx, ctxRawBody, b)
}

// RawBody returns the request body WithRawBody kept, or nil.
func RawBody(ctx context.Context) []byte {
	v, _ := ctx.Value(ctxRawBody).([]byte)
	return v
}

// WithCause gives the access log's handlers a slot for the error behind a
// 500 that the client sees only as "internal error".
func WithCause(ctx context.Context, slot *error) context.Context {
	return context.WithValue(ctx, ctxCause, slot)
}

// NoteCause leaves err for the access log to log with the request. Without
// it an untyped error that apierror.Write hides reaches no log at all.
func NoteCause(ctx context.Context, err error) {
	if slot, ok := ctx.Value(ctxCause).(*error); ok {
		*slot = err
	}
}

// WithUser gives the access log's handlers a slot for the name of the user
// the request authenticated as.
func WithUser(ctx context.Context, slot *string) context.Context {
	return context.WithValue(ctx, ctxUser, slot)
}

// NoteUser leaves the user the middleware authenticated for the access log,
// which runs outside it and so never sees the principal. It records the
// user that any credential named, an X-NuGet-ApiKey's included, and never
// the credential.
func NoteUser(ctx context.Context, name string) {
	if slot, ok := ctx.Value(ctxUser).(*string); ok {
		*slot = name
	}
}

// WithSearchQuery returns ctx carrying a request's raw query string.
func WithSearchQuery(ctx context.Context, raw string) context.Context {
	return context.WithValue(ctx, ctxSearchQuery, raw)
}

// RawSearchQuery returns the raw query WithSearchQuery kept, for the property
// search, whose parameters are property keys the generated binding drops.
func RawSearchQuery(ctx context.Context) string {
	v, _ := ctx.Value(ctxSearchQuery).(string)
	return v
}
