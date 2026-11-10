package nuget

import (
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

const emptyNuGetFeed = `<feed xmlns="http://www.w3.org/2005/Atom"/>`

// ProjectEmptyFeed is the response hook (remote.Policy.ProjectResponse) of
// NuGet v2 feeds and queries. NuGet v2 recognizes a bodyless 204 as an empty
// feed, so for a request with Resource.NuGetEmptyFeed set it rewrites an
// upstream 204 as a 200 carrying the one fixed empty Atom feed, and the
// policy's ordinary bounded schema admission and cache and delivery fences
// then apply to it. Every other request and status passes unchanged: this is
// not a general successful-status override.
//
// A 204 that declares a body (a positive length, a transfer encoding or a
// content encoding), or a media type other than application/atom+xml,
// application/xml or text/xml, answers a 502 protocol failure
// (*source.HTTPError); a failure to close the upstream body answers its
// remote.PolicyError. The projection keeps Cache-Control and Vary and drops
// the 204's validators and checksums. It closes the upstream body it
// replaces; the policy closes whichever body res holds when the fetch ends.
func ProjectEmptyFeed(q source.Request, res *http.Response) error {
	if !q.Resource.NuGetEmptyFeed || res.StatusCode != http.StatusNoContent {
		return nil
	}
	invalid := &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindProtocol}
	if res.ContentLength > 0 || len(res.TransferEncoding) > 0 ||
		res.Header.Get("Content-Encoding") != "" {
		return invalid
	}
	if raw := res.Header.Get("Content-Type"); raw != "" {
		mt, _, err := mime.ParseMediaType(raw)
		if err != nil ||
			(mt != "application/atom+xml" && mt != "application/xml" && mt != "text/xml") {
			return invalid
		}
	}
	original := res.Body
	res.Body = http.NoBody
	if err := original.Close(); err != nil {
		return remote.PolicyError(err)
	}
	res.Body = io.NopCloser(strings.NewReader(emptyNuGetFeed))
	res.StatusCode = http.StatusOK
	res.Status = "200 OK"
	res.ContentLength = int64(len(emptyNuGetFeed))
	// A 204's validators and checksums describe no body, not the projected Atom.
	// Cache policy and response base still apply; refresh has no synthetic ETag.
	res.Header = http.Header{
		"Cache-Control": res.Header.Values("Cache-Control"),
		"Vary":          res.Header.Values("Vary"),
	}
	res.Header.Set("Content-Type", "application/atom+xml")
	res.Header.Set("Content-Length", strconv.Itoa(len(emptyNuGetFeed)))
	return nil
}
