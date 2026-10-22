package remote

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/source"
)

// The module registry's successful representation lives in its header, not its
// empty body. Preserve that exact value as private cache bytes, so the existing
// bounded admission, response-base and publication fences apply unchanged. This
// is not a general response transformation or arbitrary-header forwarding hook.
func projectModuleLocation(q source.Request, res *http.Response) error {
	if !q.Resource.ModuleDownloadLocation || res.StatusCode >= 400 {
		return nil
	}
	invalid := &source.HTTPError{Status: http.StatusBadGateway, Kind: source.ProtocolFailure}
	if res.StatusCode != http.StatusNoContent || res.ContentLength > 0 ||
		len(res.TransferEncoding) > 0 {
		return invalid
	}
	var values []string
	for key, vs := range res.Header {
		if strings.EqualFold(key, "X-Terraform-Get") {
			values = append(values, vs...)
		}
	}
	if len(values) != 1 || values[0] == "" || len(values[0]) > 8192 ||
		!utf8.ValidString(values[0]) ||
		strings.TrimSpace(values[0]) != values[0] ||
		strings.ContainsFunc(values[0], unicode.IsControl) {
		return invalid
	}
	body, _ := json.Marshal(map[string]string{"source": values[0]})
	if err := res.Body.Close(); err != nil {
		return policyError(err)
	}
	res.Body = io.NopCloser(bytes.NewReader(body))
	res.StatusCode = http.StatusOK
	res.Status = "200 OK"
	res.ContentLength = int64(len(body))
	// Only cache policy survives. Upstream validators/checksums describe the
	// protocol response, not these projected bytes. Refresh always fetches 204.
	res.Header = http.Header{
		"Cache-Control": res.Header.Values("Cache-Control"),
		"Vary":          res.Header.Values("Vary"),
	}
	res.Header.Set("Content-Type", "application/json")
	res.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}
