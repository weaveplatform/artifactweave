package remote

import (
	"context"
	"net/http"
)

// Outbound holds request-scoped hooks that every upstream connection applies.
type Outbound struct {
	// Header edits each request before sending and each same-origin redirect.
	Header func(*http.Request)
	// Allow reports whether target may be requested; nil allows every target.
	Allow func(target string) bool
}

type outboundKey struct{}

// WithOutbound returns a copy of ctx whose upstream requests apply o.
func WithOutbound(ctx context.Context, o Outbound) context.Context {
	return context.WithValue(ctx, outboundKey{}, o)
}

// OutboundFrom returns the hooks installed by WithOutbound, or the zero value.
func OutboundFrom(ctx context.Context) Outbound {
	o, _ := ctx.Value(outboundKey{}).(Outbound)
	return o
}
