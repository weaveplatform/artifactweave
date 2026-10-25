package aieditorextensions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	format "github.com/weaveplatform/artifactweave/internal/format/aieditorextensions"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

const maxQueryBody = 64 << 10

const galleryAccept = "application/json;api-version=3.0-preview.1"

type criterion struct {
	//nolint:tagliatelle // Microsoft's gallery wire name
	FilterType int    `json:"filterType"`
	Value      string `json:"value"`
}

type filter struct {
	Criteria []criterion `json:"criteria"`
	//nolint:tagliatelle // Microsoft's gallery wire names
	PageNumber int `json:"pageNumber"`
	//nolint:tagliatelle // Microsoft's gallery wire names
	PageSize int `json:"pageSize"`
	//nolint:tagliatelle // Microsoft's gallery wire names
	SortBy int `json:"sortBy"`
	//nolint:tagliatelle // Microsoft's gallery wire names
	SortOrder int `json:"sortOrder"`
}

type galleryQuery struct {
	Filters []filter `json:"filters"`
	//nolint:tagliatelle // Microsoft's gallery wire name
	AssetTypes []string `json:"assetTypes"`
	Flags      int      `json:"flags"`
}

type parsedQuery struct {
	Body       []byte
	Path       string
	Publisher  string
	Name       string
	SearchText string
}

//nolint:gocyclo // Each branch rejects a distinct untrusted gallery criterion.
func parseQuery(r *http.Request) (parsedQuery, error) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.URL.RawQuery != "" ||
		r.ContentLength > maxQueryBody {
		return parsedQuery{}, badQuery()
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxQueryBody+1))
	if err != nil || len(body) == 0 || len(body) > maxQueryBody {
		return parsedQuery{}, badQuery()
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var q galleryQuery
	if dec.Decode(&q) != nil || !errors.Is(dec.Decode(new(any)), io.EOF) ||
		len(q.Filters) != 1 || len(q.Filters[0].Criteria) < 2 ||
		len(q.Filters[0].Criteria) > 8 || len(q.AssetTypes) > 16 ||
		q.Flags < 0 || q.Flags > 65535 {
		return parsedQuery{}, badQuery()
	}
	f := &q.Filters[0]
	if f.PageNumber < 1 || f.PageNumber > 100 || f.PageSize < 1 || f.PageSize > 50 ||
		f.SortBy < 0 || f.SortBy > 10 || f.SortOrder < 0 || f.SortOrder > 2 {
		return parsedQuery{}, badQuery()
	}
	for _, asset := range q.AssetTypes {
		if !safeAssetType(asset) {
			return parsedQuery{}, badQuery()
		}
	}
	out := parsedQuery{}
	seen := map[int]bool{}
	for i := range f.Criteria {
		c := &f.Criteria[i]
		if seen[c.FilterType] {
			return parsedQuery{}, badQuery()
		}
		seen[c.FilterType] = true
		switch c.FilterType {
		case 7: // ExtensionName: publisher.extension
			pub, name, ok := format.Identity(c.Value)
			if !ok {
				return parsedQuery{}, badQuery()
			}
			out.Publisher, out.Name = pub, name
			c.Value = pub + "." + name
		case 8: // Target
			if c.Value != "Microsoft.VisualStudio.Code" {
				return parsedQuery{}, badQuery()
			}
		case 10: // SearchText
			if len(c.Value) == 0 || len(c.Value) > 128 || !utf8.ValidString(c.Value) ||
				strings.ContainsAny(c.Value, "\r\n\x00") {
				return parsedQuery{}, badQuery()
			}
			out.SearchText = c.Value
		case 12: // ExcludeWithFlags
			n, e := strconv.Atoi(c.Value)
			if e != nil || n < 0 || n > 65535 {
				return parsedQuery{}, badQuery()
			}
		default:
			return parsedQuery{}, badQuery()
		}
	}
	if !seen[8] || (out.Publisher == "") == (out.SearchText == "") {
		return parsedQuery{}, badQuery()
	}
	out.Body, err = json.Marshal(q)
	if err != nil {
		return parsedQuery{}, badQuery()
	}
	out.Path = format.QueryPath(out.Body)
	return out, nil
}

func safeAssetType(raw string) bool {
	if raw == "" || len(raw) > 128 || strings.HasPrefix(raw, ".") || strings.HasSuffix(raw, ".") {
		return false
	}
	for _, ch := range raw {
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' ||
			ch >= '0' && ch <= '9' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

func badQuery() error { return apierror.New(http.StatusBadRequest, "invalid extension gallery query") }
