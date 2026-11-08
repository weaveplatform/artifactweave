package remote

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/url"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
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

// Upstream opens pooled connections to remote origins.
type Upstream interface {
	// Connection returns the connection for c configured by opts.
	Connection(c repo.Config, opts Options) (Connection, error)
}

// Connection sends requests to one remote origin. Every successful call
// transfers response-body ownership to the caller.
type Connection interface {
	Fetch(ctx context.Context, r Request) (*http.Response, error)
	Do(ctx context.Context, r Request) (*http.Response, error)
	Guarded() bool
	CheckURL(ctx context.Context, u *url.URL) error
	Prepare(req *http.Request) error
	Send(req *http.Request) (*http.Response, error)
}
