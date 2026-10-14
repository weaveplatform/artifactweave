// Package cdn signs download URLs on a CDN in front of an object store
// (decision 0029). A blob store that has a CDN takes one of these signers in
// its Options and answers blob.ViaCDN with the signer's URL for the blob's
// object key, so the store is never wrapped and keeps its other interfaces.
//
// Every signer issues URLs on time windows: a URL minted in a window expires
// when the window ends plus the TTL, so a URL lives at least the TTL and at
// most twice it, and every client in one window is handed the same URL for
// a blob when the signature is deterministic.
package cdn

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// ErrConfig reports an unusable CDN configuration.
var ErrConfig = errors.New("cdn: invalid configuration")

// Request describes one signed URL.
type Request struct {
	// TTL is the least time the URL stays valid once handed out.
	TTL time.Duration
	// Filename and ContentType, when set, travel as response overrides the
	// CDN passes to the origin and keys its cache on.
	Filename    string
	ContentType string
}

// Signer signs URLs for objects behind one CDN distribution.
type Signer interface {
	// Provider names the CDN for logs and errors, for example "cloudfront".
	Provider() string
	// Covers returns an error unless every object key under storePrefix
	// has a path on the CDN: a CDN whose origin is a folder of the bucket
	// reaches only the keys inside it.
	Covers(storePrefix string) error
	// Sign returns a URL for objectKey, the key under the bucket's root.
	Sign(objectKey string, r Request) (string, error)
}

// Expiry is when a URL issued at now with ttl expires: the end of the
// current window plus ttl, where windows are ttl long and start on a
// multiple of ttl since the Unix epoch. Whole seconds, because every CDN
// expresses expiry in epoch seconds.
func Expiry(now time.Time, ttl time.Duration) (time.Time, error) {
	sec := int64(ttl / time.Second)
	if sec < 1 {
		return time.Time{}, fmt.Errorf(
			"%w: a signed URL must last at least 1s, not %s",
			ErrConfig,
			ttl,
		)
	}
	start := now.Unix() / sec * sec
	return time.Unix(start+2*sec, 0).UTC(), nil
}

// ParseDomain reads a CDN domain: a host such as "d111.cloudfront.net",
// which means https, or an http or https URL with no path, such as
// "http://localhost:8080" for a test edge.
func ParseDomain(domain string) (*url.URL, error) {
	raw := domain
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil, u.Host == "", u.Hostname() == "":
		return nil, fmt.Errorf("%w: domain %q is not a host", ErrConfig, domain)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("%w: domain %q must be http or https", ErrConfig, domain)
	case strings.Trim(u.Path, "/") != "", u.RawQuery != "", u.Fragment != "", u.User != nil:
		return nil, fmt.Errorf(
			"%w: domain %q must be a host, with no path or query",
			ErrConfig,
			domain,
		)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

// origin maps object keys to URLs on one CDN host.
type origin struct {
	base *url.URL
	// prefix is the CDN origin's path inside the bucket, with a trailing
	// slash, or empty when the origin is the bucket root.
	prefix string
}

func newOrigin(domain, pathPrefix string) (origin, error) {
	base, err := ParseDomain(domain)
	if err != nil {
		return origin{}, err
	}
	return origin{base: base, prefix: folder(pathPrefix)}, nil
}

// folder normalises a key prefix to no leading slash and one trailing slash.
func folder(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

func (o origin) covers(provider, storePrefix string) error {
	if !strings.HasPrefix(folder(storePrefix), o.prefix) {
		return fmt.Errorf(
			"%w: %s path_prefix %q does not contain the store prefix %q, so its blobs are unreachable",
			ErrConfig,
			provider,
			o.prefix,
			storePrefix,
		)
	}
	return nil
}

// url returns the unsigned URL for key with r's overrides in its query.
func (o origin) url(key string, r Request) (string, error) {
	rest, ok := strings.CutPrefix(key, o.prefix)
	if !ok {
		return "", fmt.Errorf(
			"%w: object key %q is outside path_prefix %q",
			ErrConfig,
			key,
			o.prefix,
		)
	}
	u := *o.base
	u.Path = "/" + rest
	q := url.Values{}
	if r.Filename != "" {
		q.Set("response-content-disposition", blob.Disposition(r.Filename))
	}
	if r.ContentType != "" {
		q.Set("response-content-type", r.ContentType)
	}
	// url.Values encodes a space as "+", which object stores read
	// literally in an override; "%20" means a space everywhere. Encode
	// already turned every literal "+" into "%2B".
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	return u.String(), nil
}
