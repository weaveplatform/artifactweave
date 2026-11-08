package remote

import (
	"context"
	"net/http"
)

// Outbound holds request-scoped hooks that every upstream connection applies.
// Smart Remote installs them to mark its requests and narrow their targets,
// so the transport knows nothing of Smart sessions. They only add to the
// transport's rules: Do applies Header after the credential preparation and
// then asks Allow; on a redirect it asks Allow after the request's own
// CheckURL and before the redirect policy, and once that policy passes it,
// applies Header only when the redirect stays on the first request's origin,
// since a change of origin keeps only the transport's own headers (decision
// 0007). A refused target fails with ErrSecurity and is never sent. Hooks
// travel with one request's context, so requests sharing a pooled connection
// never see each other's.
type Outbound struct {
	// Header edits each request before sending and each same-origin redirect.
	Header func(*http.Request)
	// Allow reports whether target may be requested; nil allows every target.
	Allow func(target string) bool
}

type outboundKey struct{}

// WithOutbound returns a copy of ctx whose upstream requests apply o. It
// replaces any hooks ctx carries rather than combining with them.
func WithOutbound(ctx context.Context, o Outbound) context.Context {
	return context.WithValue(ctx, outboundKey{}, o)
}

// OutboundFrom returns the hooks installed by WithOutbound, or the zero value.
func OutboundFrom(ctx context.Context) Outbound {
	o, _ := ctx.Value(outboundKey{}).(Outbound)
	return o
}
