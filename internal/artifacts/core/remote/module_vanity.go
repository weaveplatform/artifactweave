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

func projectModuleResponse(q source.Request, res *http.Response) error {
	if q.Resource.ModuleVanityDocument {
		return projectModuleVanity(res)
	}
	return projectModuleLocation(q, res)
}

func projectModuleVanity(res *http.Response) error {
	if res.StatusCode >= 400 {
		return nil
	}
	invalid := &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindProtocol}
	if res.StatusCode != http.StatusOK || res.ContentLength > source.MaxModuleVanityDocumentBytes {
		return invalid
	}
	var values []string
	for key, entries := range res.Header {
		if strings.EqualFold(key, "X-Terraform-Get") {
			values = append(values, entries...)
		}
	}
	var control source.ModuleVanityResponse
	if res.Request == nil || res.Request.URL == nil {
		return invalid
	}
	identity, err := source.ModuleVanityIdentity(res.Request.URL.String())
	if err != nil {
		return invalid
	}
	control.Identity = identity
	if len(values) != 0 {
		if len(values) != 1 || values[0] == "" || len(values[0]) > 8192 ||
			!utf8.ValidString(values[0]) || strings.TrimSpace(values[0]) != values[0] ||
			strings.ContainsFunc(values[0], unicode.IsControl) {
			return invalid
		}
		control.Source = values[0]
	}
	// Read the complete response even when its header already selected a source.
	// A truncated or oversized document must never publish a reusable success.
	body, err := io.ReadAll(io.LimitReader(res.Body, source.MaxModuleVanityDocumentBytes+1))
	if err != nil || len(body) > source.MaxModuleVanityDocumentBytes ||
		res.ContentLength >= 0 && int64(len(body)) != res.ContentLength {
		return invalid
	}
	if control.Source == "" {
		if !validModuleVanityHTML(res.Header.Get("Content-Type"), body) {
			return invalid
		}
		control.HTML = body
	}
	if err := res.Body.Close(); err != nil {
		return PolicyError(err)
	}
	projected, _ := json.Marshal(control)
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

func validModuleVanityHTML(contentType string, body []byte) bool {
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil || (media != "text/html" && media != "application/xhtml+xml") || !utf8.Valid(body) {
		return false
	}
	for key, value := range params {
		charset := strings.ToLower(value)
		if key != "charset" || charset != "utf-8" && charset != "us-ascii" {
			return false
		}
		if charset == "us-ascii" && bytes.IndexFunc(body, func(r rune) bool { return r > unicode.MaxASCII }) >= 0 {
			return false
		}
	}
	return true
}
