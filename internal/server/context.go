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
)

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxRequestID, id)
}

func requestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxRequestID).(string)
	return v
}

func withOriginalContentType(ctx context.Context, mt string) context.Context {
	return context.WithValue(ctx, ctxOriginalContentType, mt)
}

// OriginalContentType returns the vendor media type a client sent before
// normalizeContentType rewrote it, or "" when the client sent a plain type.
func OriginalContentType(ctx context.Context) string {
	v, _ := ctx.Value(ctxOriginalContentType).(string)
	return v
}

// withAccept keeps the Accept header for strict handlers, which see only
// their decoded request: several operations answer with a vendor media type
// when the client asks for one, and their specs do not declare Accept.
func withAccept(ctx context.Context, accept string) context.Context {
	return context.WithValue(ctx, ctxAccept, accept)
}

// accepts reports whether the request's Accept header names mediaType.
func accepts(ctx context.Context, mediaType string) bool {
	v, _ := ctx.Value(ctxAccept).(string)
	return v != "" && strings.Contains(v, mediaType)
}

func withRawBody(ctx context.Context, b []byte) context.Context {
	return context.WithValue(ctx, ctxRawBody, b)
}

// rawBody returns the request body keepRepositoryBody saved, or nil.
func rawBody(ctx context.Context) []byte {
	v, _ := ctx.Value(ctxRawBody).([]byte)
	return v
}
