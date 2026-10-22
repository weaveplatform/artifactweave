package remote

import (
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/source"
)

const emptyNuGetFeed = `<feed xmlns="http://www.w3.org/2005/Atom"/>`

// NuGet v2 recognizes a bodyless 204 as an empty feed. Give it the one fixed
// Atom representation so ordinary bounded schema admission and cache/delivery
// fences apply. This is not a general successful-status override.
func projectNuGetEmptyFeed(q source.Request, res *http.Response) error {
	if !q.Resource.NuGetEmptyFeed || res.StatusCode != http.StatusNoContent {
		return nil
	}
	invalid := &source.HTTPError{Status: http.StatusBadGateway, Kind: source.ProtocolFailure}
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
		return policyError(err)
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
