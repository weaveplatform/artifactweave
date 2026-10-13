package server

import "context"

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxOriginalContentType
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
