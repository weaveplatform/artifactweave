package peerstorage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pull"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artifactory "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
)

type cursor struct {
	Binding string `json:"binding"`
	Last    string `json:"last"`
}

// Files streams the documented recursive list into a bounded local window.
// Storage API has no offset/limit wire contract; every page scans a complete
// document and independently inspects its selected entries before returning.
func (r *Reader) Files(ctx context.Context, page kernel.Page) ([]pull.Entry, string, error) {
	page, last, err := r.page(page)
	if err != nil {
		return nil, "", err
	}
	anchor, err := r.Inspect(ctx, r.root)
	if errors.Is(err, kernel.ErrNotFound) && r.root != "" {
		// An absent selected path is not authoritative absence of a whole
		// repository. Revalidate that namespace before returning an empty page.
		anchor, err = r.inspectRepository(ctx)
		if err == nil && !anchor.Folder {
			err = invalid()
		}
		return []pull.Entry{}, "", err
	}
	if err != nil {
		return nil, "", err
	}
	if !anchor.Folder {
		if anchor.Path <= last {
			return []pull.Entry{}, "", nil
		}
		return []pull.Entry{anchor}, "", nil
	}
	var paths []string
	err = r.read(ctx, r.root, url.Values{"list": {""}, "deep": {"1"}, "listFolders": {"0"}},
		func(ctx context.Context, body io.Reader) error {
			var err error
			paths, err = r.list(ctx, body, last, page.Limit+1)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(paths) > page.Limit {
		paths = paths[:page.Limit]
		raw, err := json.Marshal(cursor{Binding: r.binding, Last: paths[len(paths)-1]})
		if err != nil {
			return nil, "", invalid()
		}
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	entries := make([]pull.Entry, 0, len(paths))
	for _, path := range paths {
		entry, err := r.Inspect(ctx, path)
		if err != nil {
			return nil, "", fmt.Errorf("inspect selected pull file: %w", err)
		}
		if entry.Folder {
			return nil, "", invalid()
		}
		entries = append(entries, entry)
	}
	if err = r.Revalidate(ctx); err != nil {
		return nil, "", err
	}
	return entries, next, nil
}

func (r *Reader) inspectRepository(ctx context.Context) (pull.Entry, error) {
	// This scoped copy permits only the namespace-anchor read. Its original
	// canonical permission scope remains unchanged; it grants no file access.
	anchor := *r
	anchor.root = ""
	return anchor.Inspect(ctx, "")
}

func (r *Reader) page(page kernel.Page) (kernel.Page, string, error) {
	if len(page.After) > 64<<10 {
		return page, "", kernel.ErrInvalidCursor
	}
	page, err := page.Normalize()
	if err != nil {
		return page, "", fmt.Errorf("pull page: %w", err)
	}
	if page.After == "" {
		return page, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(page.After)
	var saved cursor
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.Binding != r.binding || saved.Last == "" ||
		!within(r.root, saved.Last) {
		return page, "", kernel.ErrInvalidCursor
	}
	if _, err = r.peer.StorageURL(saved.Last, nil); err != nil {
		return page, "", kernel.ErrInvalidCursor
	}
	return page, saved.Last, nil
}

func (r *Reader) list(ctx context.Context, body io.Reader, last string, limit int) ([]string, error) {
	limited := &io.LimitedReader{R: body, N: metadataLimit + 1}
	decoder := json.NewDecoder(limited)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, invalid()
	}
	var paths []string
	found := false
	for decoder.More() {
		if err = ctx.Err(); err != nil {
			return nil, fmt.Errorf("scan pull listing: %w", err)
		}
		field, err := decoder.Token()
		if err != nil {
			return nil, invalid()
		}
		if field != "files" {
			if err = decoder.Decode(new(any)); err != nil {
				return nil, invalid()
			}
			continue
		}
		if found {
			return nil, invalid()
		}
		found = true
		paths, err = r.listEntries(ctx, decoder, last, limit)
		if err != nil {
			return nil, err
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || !found {
		return nil, invalid()
	}
	if err = complete(ctx, decoder, limited); err != nil {
		return nil, err
	}
	return paths, nil
}

func (r *Reader) listEntries(ctx context.Context, decoder *json.Decoder, last string, limit int) ([]string, error) {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, invalid()
	}
	paths := make([]string, 0, limit)
	for decoder.More() {
		if err = ctx.Err(); err != nil {
			return nil, fmt.Errorf("scan pull listing entries: %w", err)
		}
		var row artifactory.FileListItem
		if err = decoder.Decode(&row); err != nil || row.Uri == nil || !strings.HasPrefix(*row.Uri, "/") ||
			row.Folder == nil || *row.Folder {
			return nil, invalid()
		}
		path := strings.TrimPrefix(*row.Uri, "/")
		if r.root != "" {
			path = r.root + "/" + path
		}
		if _, err = r.peer.StorageURL(path, nil); err != nil || path == r.root || !within(r.root, path) {
			return nil, invalid()
		}
		if path <= last {
			continue
		}
		index, duplicate := slices.BinarySearch(paths, path)
		if duplicate {
			return nil, invalid()
		}
		if index < limit {
			paths = slices.Insert(paths, index, path)
			if len(paths) > limit {
				paths = paths[:limit]
			}
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
		return nil, invalid()
	}
	return paths, nil
}
