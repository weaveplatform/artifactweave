package remote

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/url"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// Options contains resolved connection settings. Revision must change whenever
// externally resolved settings (proxy, trust, credentials) change. Prepare runs
// for initial requests and redirects, after the transport removes credentials
// from requests crossing origins. It must not forward downstream credentials.
type Options struct {
	ProxyURL  *url.URL
	TLSConfig *tls.Config
	Revision  string
	Jar       http.CookieJar
	// CheckURL authorizes metadata-provided absolute destinations.
	CheckURL      func(context.Context, *url.URL) error
	WrapTransport func(*http.Transport) (http.RoundTripper, func())
	Prepare       func(*http.Request) error
	CheckRedirect func(*http.Request, []*http.Request) error
	// MaxResponseHeaderBytes defaults to 1 MiB; tests can lower it.
	MaxResponseHeaderBytes int64
}

// Request is one upstream exchange for Connection.Do and Connection.Fetch.
// Path and Query resolve against the repository URL; ExternalURL instead names
// an absolute destination, which only a guarded connection sends (ErrSecurity
// otherwise). Only GET and HEAD without a body are sent, apart from the closed
// format-owned POSTs marked below; anything else is ErrRequest. Metadata
// bounds the whole exchange, body included, by the repository's metadata
// retrieval timeout.
type Request struct {
	// GitUploadPack is set only by the closed CocoaPods Git-object adapter
	// (cocoapods.FetchCocoaPodsGit), to mark its upload-pack POST. It is
	// exported only so that the adapter, outside this package, can mark the
	// request: Do still admits the POST only when the whole Git guard holds
	// (a CocoaPods repository, a metadata request without cross-origin
	// credentials, an external URL with no path or query, a body of 1 to
	// 4096 bytes, the upload-pack Accept header and a destination check).
	GitUploadPack bool
	// ExternalURL is a metadata/token-realm URL, never combined with Path/Query.
	// It requires a resolved destination policy.
	ExternalURL string
	// CheckURL is a request-scoped restriction, never retained by the pooled client.
	// It applies before the initial request and each redirected request is sent.
	CheckURL          func(context.Context, *url.URL) error
	NoCrossOriginAuth bool
	// GitLFSActionHeaders belong only to the exact, validated download action.
	// They are never copied to a redirected request.
	GitLFSActionHeaders http.Header
	Method              string
	Path                string
	Query               string
	Header              http.Header
	Metadata            bool
	// Body is admitted only for closed format-owned metadata POST contracts.
	Body []byte
}

// Upstream opens pooled connections to remote origins. It is the port through
// which the policy, Smart Remote and the format rules reach an upstream, so
// that none of them holds the transport; *Pool implements it, and it is safe
// for concurrent use. It sits beside Options, Request and the transport errors
// that its callers share, not in a separate ports package (decision 0044,
// "Artifacts and remote").
type Upstream interface {
	// Connection returns the connection for c configured by opts. A repository
	// whose configuration or opts.Revision changed gets a new one. On error
	// the Connection is a nil interface, never a nil pointer inside one, so
	// that a caller can compare it with nil.
	Connection(c repo.Config, opts Options) (Connection, error)
}

// Connection sends requests to one remote origin. Every successful Fetch, Do
// or Send transfers response-body ownership to the caller, including non-2xx
// responses. It is safe for concurrent use.
type Connection interface {
	// Fetch reads r, probing with HEAD first unless the repository bypasses
	// HEAD requests or r must be sent once as it is. A probe answered with 304
	// or a definitive error status is the result; otherwise the GET is, and
	// nothing of the probe's response is reused.
	Fetch(ctx context.Context, r Request) (*http.Response, error)
	// Do sends r once as it is. Both Fetch and Do keep the transport's
	// destination, redirect and credential rules and apply the hooks that
	// WithOutbound installed in ctx.
	Do(ctx context.Context, r Request) (*http.Response, error)
	// Guarded reports whether the connection checks absolute destinations
	// that metadata names (Options.CheckURL). An unguarded connection sends
	// no ExternalURL request.
	Guarded() bool
	// CheckURL applies that destination check to u, and returns ErrSecurity
	// when the connection is unguarded.
	CheckURL(ctx context.Context, u *url.URL) error
	// Prepare applies the connection's request preparation (Options.Prepare),
	// which adds the configured credentials, to a request its caller built;
	// without one it leaves req as it is and returns nil.
	Prepare(req *http.Request) error
	// Send sends a request its caller built, checked and prepared, over the
	// connection's transport, and refuses every redirect with ErrRedirect.
	// None of Do's checks or hooks apply, and the error is the HTTP client's
	// own, not one of this package's classifications.
	Send(req *http.Request) (*http.Response, error)
}
