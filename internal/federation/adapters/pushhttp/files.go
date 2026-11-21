package pushhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artifactory "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
)

type fileKey struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type fileCursor struct {
	Repository string  `json:"repository"`
	Root       string  `json:"root"`
	Last       fileKey `json:"last"`
}

func keyFor(path string) fileKey {
	parent, name := ".", path
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		parent, name = path[:index], path[index+1:]
	}
	return fileKey{Path: parent, Name: name}
}

func (k fileKey) path() string {
	if k.Path == "." {
		return k.Name
	}
	return k.Path + "/" + k.Name
}

func compareKey(a, b fileKey) int {
	if order := strings.Compare(a.Path, b.Path); order != 0 {
		return order
	}
	return strings.Compare(a.Name, b.Name)
}

func inRoot(root, path string) bool {
	return root == "" || path == root || strings.HasPrefix(path, root+"/")
}

func (c *connection) cursor(root string, page kernel.Page) (kernel.Page, fileKey, error) {
	if len(page.After) > 64<<10 {
		return page, fileKey{}, kernel.ErrInvalidCursor
	}
	if root != "" {
		if _, err := c.endpoint.ItemURL(root); err != nil {
			return page, fileKey{}, fmt.Errorf("replication listing root: %w", err)
		}
	}
	page, err := page.Normalize()
	if err != nil {
		return page, fileKey{}, fmt.Errorf("replication listing page: %w", err)
	}
	if page.After == "" {
		return page, fileKey{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(page.After)
	var cursor fileCursor
	if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Repository != c.endpoint.Repository() || cursor.Root != root ||
		cursor.Last.Name == "" || keyFor(cursor.Last.path()) != cursor.Last || !inRoot(root, cursor.Last.path()) {
		return page, fileKey{}, kernel.ErrInvalidCursor
	}
	if _, err := c.endpoint.ItemURL(cursor.Last.path()); err != nil {
		return page, fileKey{}, kernel.ErrInvalidCursor
	}
	return page, cursor.Last, nil
}

func (c *connection) Files(ctx context.Context, root string, page kernel.Page) ([]port.PushTargetItem, string, error) {
	page, last, err := c.cursor(root, page)
	if err != nil {
		return nil, "", err
	}
	keys, complete, err := c.aql(ctx, root, last, page.Limit+1)
	if err != nil {
		return nil, "", err
	}
	if !complete {
		// Native AQL filters permissions after its scan limit. Underfilled,
		// empty or capped results cannot prove exhaustion. Verify with the
		// storage listing, streaming it into a bounded next-page selection.
		keys, err = c.fileList(ctx, root, last, page.Limit+1)
		if err != nil {
			return nil, "", err
		}
	}
	next := ""
	if len(keys) > page.Limit {
		keys = keys[:page.Limit]
		raw, err := json.Marshal(fileCursor{Repository: c.endpoint.Repository(), Root: root, Last: keys[len(keys)-1]})
		if err != nil {
			return nil, "", port.PushTargetInvalid
		}
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	items := make([]port.PushTargetItem, 0, len(keys))
	for _, key := range keys {
		item, err := c.info(ctx, key.path())
		if err != nil {
			return nil, "", err
		}
		if item.Snapshot.Folder {
			return nil, "", port.PushTargetInvalid
		}
		items = append(items, item)
	}
	return items, next, nil
}

func (c *connection) aql(ctx context.Context, root string, last fileKey, limit int) ([]fileKey, bool, error) {
	criteria := map[string]any{"repo": c.endpoint.Repository(), "type": "file"}
	clauses := make([]any, 0, 2)
	if root != "" {
		exact := keyFor(root)
		clauses = append(clauses, map[string]any{"$or": []any{
			map[string]any{"path": exact.Path, "name": exact.Name},
			map[string]any{"path": root},
			map[string]any{"$and": []any{
				map[string]any{"path": map[string]string{"$gte": root + "/"}},
				map[string]any{"path": map[string]string{"$lt": root + "0"}},
			}},
		}})
	}
	if last.Name != "" {
		clauses = append(clauses, map[string]any{"$or": []any{
			map[string]any{"path": map[string]string{"$gt": last.Path}},
			map[string]any{"path": last.Path, "name": map[string]string{"$gt": last.Name}},
		}})
	}
	if len(clauses) > 0 {
		criteria["$and"] = clauses
	}
	raw, err := json.Marshal(criteria)
	if err != nil {
		return nil, false, port.PushTargetInvalid
	}
	query := fmt.Sprintf(`items.find(%s).include("repo","path","name","type").sort({"$asc":["path","name"]}).limit(%d)`, raw, limit)
	if len(query) > 6000 {
		// Long literal paths can exceed the server's default query-size cap.
		// The storage API still addresses them without inventing AQL syntax.
		return nil, false, nil
	}
	target, err := c.endpoint.AQLURL()
	if err != nil {
		return nil, false, fmt.Errorf("address replication search: %w", err)
	}
	response, err := c.request(ctx, http.MethodPost, target, strings.NewReader(query), int64(len(query)), http.Header{"Content-Type": {"text/plain"}})
	if err != nil {
		return nil, false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false, statusError(response.StatusCode)
	}
	var document struct {
		Results *[]struct {
			Repository string `json:"repo"`
			Path       string `json:"path"`
			Name       string `json:"name"`
			Type       string `json:"type"`
		} `json:"results"`
		Range struct {
			Notification json.RawMessage `json:"notification"`
		} `json:"range"`
	}
	if err := decodeDocument(response.Body, &document); err != nil {
		return nil, false, err
	}
	if document.Results == nil || len(*document.Results) > limit {
		return nil, false, port.PushTargetInvalid
	}
	keys := make([]fileKey, 0, len(*document.Results))
	previous := last
	for _, row := range *document.Results {
		key := fileKey{Path: row.Path, Name: row.Name}
		if row.Repository != c.endpoint.Repository() || row.Type != "file" || keyFor(key.path()) != key || !inRoot(root, key.path()) ||
			compareKey(previous, key) >= 0 {
			return nil, false, port.PushTargetInvalid
		}
		if _, err := c.endpoint.ItemURL(key.path()); err != nil {
			return nil, false, port.PushTargetInvalid
		}
		keys = append(keys, key)
		previous = key
	}
	return keys, len(keys) == limit && len(document.Range.Notification) == 0, nil
}

func (c *connection) fileList(ctx context.Context, root string, last fileKey, limit int) ([]fileKey, error) {
	if root != "" {
		item, err := c.info(ctx, root)
		if err != nil {
			return nil, err
		}
		if !item.Snapshot.Folder {
			key := keyFor(root)
			if compareKey(key, last) > 0 {
				return []fileKey{key}, nil
			}
			return []fileKey{}, nil
		}
	}
	target, err := c.endpoint.StorageRootURL(root)
	if err != nil {
		return nil, fmt.Errorf("address replication file list: %w", err)
	}
	target.RawQuery = "list&deep=1&listFolders=0"
	response, err := c.request(ctx, http.MethodGet, target, nil, 0, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError(response.StatusCode)
	}
	// [I] Bound even a malicious single JSON field. Exceeding this wire cap
	// fails the page rather than silently publishing partial enumeration.
	reader := &io.LimitedReader{R: response.Body, N: (64 << 20) + 1}
	decoder := json.NewDecoder(reader)
	token, err := decoder.Token()
	if err != nil {
		return nil, documentError(err)
	}
	if token != json.Delim('{') {
		return nil, port.PushTargetInvalid
	}
	var keys []fileKey
	found := false
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return nil, documentError(err)
		}
		if field != "files" {
			if err := decoder.Decode(new(any)); err != nil {
				return nil, documentError(err)
			}
			continue
		}
		if found {
			return nil, port.PushTargetInvalid
		}
		found = true
		keys, err = c.listEntries(decoder, root, last, limit)
		if err != nil {
			return nil, err
		}
	}
	if token, err = decoder.Token(); err != nil {
		return nil, documentError(err)
	}
	if token != json.Delim('}') || !found {
		return nil, port.PushTargetInvalid
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, documentError(err)
	}
	if reader.N <= 0 {
		return nil, port.PushTargetInvalid
	}
	return keys, nil
}

func (c *connection) listEntries(decoder *json.Decoder, root string, last fileKey, limit int) ([]fileKey, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, documentError(err)
	}
	if token != json.Delim('[') {
		return nil, port.PushTargetInvalid
	}
	keys := make([]fileKey, 0, limit)
	for decoder.More() {
		var row artifactory.FileListItem
		if err := decoder.Decode(&row); err != nil {
			return nil, documentError(err)
		}
		if row.Uri == nil || !strings.HasPrefix(*row.Uri, "/") || row.Folder == nil || *row.Folder {
			return nil, port.PushTargetInvalid
		}
		path := strings.TrimPrefix(*row.Uri, "/")
		if root != "" {
			path = root + "/" + path
		}
		if _, err := c.endpoint.ItemURL(path); err != nil || !inRoot(root, path) {
			return nil, port.PushTargetInvalid
		}
		key := keyFor(path)
		if compareKey(key, last) <= 0 {
			continue
		}
		index, duplicate := slices.BinarySearchFunc(keys, key, compareKey)
		if duplicate {
			return nil, port.PushTargetInvalid
		}
		if index < limit {
			keys = slices.Insert(keys, index, key)
			if len(keys) > limit {
				keys = keys[:limit]
			}
		}
	}
	if token, err = decoder.Token(); err != nil {
		return nil, documentError(err)
	}
	if token != json.Delim(']') {
		return nil, port.PushTargetInvalid
	}
	return keys, nil
}
