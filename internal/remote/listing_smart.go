package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func parseSmartListing(ctx context.Context, r io.Reader, key, folder string) (listingPage, error) {
	var page listingPage
	data, err := io.ReadAll(io.LimitReader(r, listingBytes+1))
	if err != nil || len(data) > listingBytes {
		return page, listingError()
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return page, listingError()
	}
	var reportedRepo, reportedPath string
	var children []map[string]json.RawMessage
	if json.Unmarshal(raw["repo"], &reportedRepo) != nil ||
		json.Unmarshal(raw["path"], &reportedPath) != nil ||
		json.Unmarshal(raw["children"], &children) != nil || children == nil ||
		(reportedRepo != key && reportedRepo != key+types.CacheSuffix) ||
		reportedPath != "/"+folder || len(children) > 10000 {
		return page, listingError()
	}
	page.count = len(children)
	for _, child := range children {
		if err = ctx.Err(); err != nil {
			return page, fmt.Errorf("parse smart listing: %w", err)
		}
		var uri string
		var isFolder *bool
		if json.Unmarshal(child["uri"], &uri) != nil ||
			json.Unmarshal(child["folder"], &isFolder) != nil || isFolder == nil {
			return page, listingError()
		}
		name := strings.TrimPrefix(uri, "/")
		if !strings.HasPrefix(uri, "/") || strings.ContainsAny(name, "/\\;") ||
			strings.IndexFunc(name, unicode.IsControl) >= 0 {
			continue
		}
		if _, _, _, err = types.SplitItemPath(name); err != nil || name == "" {
			continue
		}
		childPath := name
		if folder != "" {
			childPath = folder + "/" + name
		}
		page.entries = append(page.entries, ListingEntry{Path: childPath, Folder: *isFolder})
	}
	return page, nil
}
