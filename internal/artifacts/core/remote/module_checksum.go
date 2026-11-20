package remote

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

func projectModuleChecksum(res *http.Response) error {
	if res.StatusCode >= 400 {
		return nil
	}
	invalid := &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindProtocol}
	if res.StatusCode != http.StatusOK || res.ContentLength > source.MaxModuleChecksumDocumentBytes {
		return invalid
	}
	media, params, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || (media != "text/plain" && media != "application/octet-stream") ||
		!moduleChecksumEncoding(media, params) {
		return invalid
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, source.MaxModuleChecksumDocumentBytes+1))
	if err != nil || len(body) > source.MaxModuleChecksumDocumentBytes || !utf8.Valid(body) ||
		res.ContentLength >= 0 && int64(len(body)) != res.ContentLength {
		return invalid
	}
	if strings.EqualFold(params["charset"], "us-ascii") &&
		bytes.IndexFunc(body, func(r rune) bool { return r > unicode.MaxASCII }) >= 0 {
		return invalid
	}
	if bytes.IndexFunc(body, func(r rune) bool { return unicode.IsControl(r) && r != '\t' && r != '\r' && r != '\n' }) >= 0 {
		return invalid
	}
	if err := res.Body.Close(); err != nil {
		return PolicyError(err)
	}
	projected, _ := json.Marshal(source.ModuleChecksumResponse{Content: body})
	res.Body = io.NopCloser(bytes.NewReader(projected))
	res.ContentLength = int64(len(projected))
	res.Header = http.Header{
		"Cache-Control": res.Header.Values("Cache-Control"),
		"Vary":          res.Header.Values("Vary"),
	}
	res.Header.Set("Content-Type", "application/json")
	res.Header.Set("Content-Length", strconv.Itoa(len(projected)))
	return nil
}

func moduleChecksumEncoding(media string, params map[string]string) bool {
	for key, value := range params {
		if media != "text/plain" || key != "charset" ||
			!strings.EqualFold(value, "utf-8") && !strings.EqualFold(value, "us-ascii") {
			return false
		}
	}
	return true
}
