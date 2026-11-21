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
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artifactory "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
)

type cursor struct {
	Binding string `json:"binding"`
	Last    string `json:"last"`
}

type listedEntry struct {
	file     pull.Entry
	complete bool
	key      string
}

func unsupportedInfo(err error) bool {
	var failure *source.HTTPError
	return errors.As(err, &failure) && failure.Kind == source.FailureKindProtocol
}

// Files streams the documented recursive list into a bounded local window.
// Storage API has no offset/limit wire contract; every page scans a complete
// document and independently inspects its selected entries before returning.
func (r *Reader) Files(ctx context.Context, page kernel.Page) ([]pull.Entry, string, error) {
	page, last, err := r.page(page)
	if err != nil {
		return nil, "", err
	}
	anchor, err := r.info(ctx, r.root)
	if unsupportedInfo(err) {
		return r.virtualFiles(ctx, page, last)
	}
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
	listed, err := r.readList(ctx, last, page.Limit+1, "", true)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(listed) > page.Limit {
		listed = listed[:page.Limit]
		raw, err := json.Marshal(cursor{Binding: r.binding, Last: listed[len(listed)-1].key})
		if err != nil {
			return nil, "", invalid()
		}
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	entries := make([]pull.Entry, 0, len(listed))
	for _, selected := range listed {
		entry, err := r.Inspect(ctx, selected.file.Path)
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
	entry, err := anchor.info(ctx, "")
	if unsupportedInfo(err) {
		if _, err = anchor.readList(ctx, "", 1, "", false); err == nil {
			return pull.Entry{Folder: true}, nil
		}
	}
	return entry, err
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
	if _, err = peerStorageURL(r.peer, saved.Last, nil); err != nil {
		return page, "", kernel.ErrInvalidCursor
	}
	return page, saved.Last, nil
}

func (r *Reader) list(ctx context.Context, body io.Reader, last string, limit int, exact string) ([]listedEntry, error) {
	limited := &io.LimitedReader{R: body, N: metadataLimit + 1}
	decoder := json.NewDecoder(limited)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, invalid()
	}
	var entries []listedEntry
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
		entries, err = r.listEntries(ctx, decoder, last, limit, exact)
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
	return entries, nil
}

func (r *Reader) listEntries(ctx context.Context, decoder *json.Decoder, last string, limit int, exact string) ([]listedEntry, error) {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, invalid()
	}
	entries := make([]listedEntry, 0, limit)
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
		if _, err = peerStorageURL(r.peer, path, nil); err != nil || path == r.root || !within(r.root, path) {
			return nil, invalid()
		}
		entry, err := listedFile(row, path)
		if err != nil {
			return nil, err
		}
		key := r.canonical(path)
		if key <= last || exact != "" && path != exact || r.filePath != nil && !within(r.selection, key) {
			continue
		}
		entry.key = key
		index, duplicate := slices.BinarySearchFunc(entries, key, func(entry listedEntry, path string) int { return strings.Compare(entry.key, path) })
		if duplicate {
			previous := entries[index]
			if r.filePath == nil || previous.file.Path == path {
				return nil, invalid()
			}
			// Prefer the native canonical spelling, otherwise the lexically
			// first equivalent copy. Wire ordering cannot change a saved intent.
			if path == key || previous.file.Path != key && path < previous.file.Path {
				entries[index] = entry
			}
			continue
		}
		if index < limit {
			entries = slices.Insert(entries, index, entry)
			if len(entries) > limit {
				entries = entries[:limit]
			}
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
		return nil, invalid()
	}
	return entries, nil
}

func listedFile(row artifactory.FileListItem, path string) (listedEntry, error) {
	entry := pull.Entry{Path: path, Size: -1}
	if row.Size != nil {
		if *row.Size < 0 {
			return listedEntry{}, invalid()
		}
		entry.Size = *row.Size
	}
	if row.LastModified != nil {
		entry.Modified, entry.Updated = *row.LastModified, *row.LastModified
	}
	if row.MdTimestamps != nil && row.MdTimestamps.Properties != nil {
		entry.Updated = *row.MdTimestamps.Properties
	}
	if row.Sha2 != nil {
		if !digest(*row.Sha2, 32) {
			return listedEntry{}, invalid()
		}
		entry.Checksums.SHA256 = strings.ToLower(*row.Sha2)
	}
	if row.Sha1 != nil {
		if !digest(*row.Sha1, 20) {
			return listedEntry{}, invalid()
		}
		entry.Checksums.SHA1 = strings.ToLower(*row.Sha1)
	}
	return listedEntry{file: entry, complete: entry.Size >= 0 && entry.Checksums.SHA256 != "" && !entry.Modified.IsZero()}, nil
}

func (r *Reader) readList(ctx context.Context, last string, limit int, exact string, deep bool) ([]listedEntry, error) {
	depth := "0"
	if deep {
		depth = "1"
	}
	var entries []listedEntry
	err := r.read(ctx, r.root, url.Values{"list": {""}, "deep": {depth}, "listFolders": {"0"}, "mdTimestamps": {"1"}},
		func(ctx context.Context, body io.Reader) error {
			var err error
			entries, err = r.list(ctx, body, last, limit, exact)
			return err
		})
	return entries, err
}

// Virtual storage exposes listing metadata rather than ordinary file/folder
// info. Complete computed list facts are sufficient; absent facts never become
// guessed sizes/digests or a fake successful cache transfer.
func (r *Reader) virtualFiles(ctx context.Context, page kernel.Page, last string) ([]pull.Entry, string, error) {
	listed, err := r.readList(ctx, last, page.Limit+1, "", true)
	if err != nil {
		if r.root != "" && (unsupportedInfo(err) || errors.Is(err, kernel.ErrNotFound)) {
			entry, inspectErr := r.exactListedFile(ctx, r.root)
			if inspectErr != nil {
				return nil, "", inspectErr
			}
			if entry.Path <= last {
				return []pull.Entry{}, "", nil
			}
			return []pull.Entry{entry}, "", nil
		}
		return nil, "", err
	}
	next := ""
	if len(listed) > page.Limit {
		listed = listed[:page.Limit]
		raw, _ := json.Marshal(cursor{Binding: r.binding, Last: listed[len(listed)-1].key})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	entries := make([]pull.Entry, 0, len(listed))
	for _, entry := range listed {
		if !entry.complete {
			return nil, "", invalid()
		}
		entries = append(entries, entry.file)
	}
	if err = r.Revalidate(ctx); err != nil {
		return nil, "", err
	}
	return entries, next, nil
}

func (r *Reader) exactListedFile(ctx context.Context, selected string) (pull.Entry, error) {
	if !within(r.root, selected) {
		return pull.Entry{}, source.ErrInvalidRequest
	}
	parent, _, _, err := kernel.SplitItemPath(selected)
	if err != nil {
		return pull.Entry{}, source.ErrInvalidRequest
	}
	if parent == kernel.RootPath {
		parent = ""
	}
	// The upstream parent lookup returns only the exact selected file. The
	// original canonical scope/authority stays fixed; sibling facts are never
	// exposed, even when a file selection requires its parent's list operation.
	lookup := *r
	lookup.root = parent
	entries, err := lookup.readList(ctx, "", 1, selected, false)
	if err != nil {
		return pull.Entry{}, err
	}
	if len(entries) != 1 {
		return pull.Entry{}, source.ErrSuperseded
	}
	if !entries[0].complete {
		return pull.Entry{}, invalid()
	}
	return entries[0].file, nil
}
