package peerstorage

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pull"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artifactory "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
)

const metadataLimit = 64 << 20

// Absent requires an existing namespace and an exact item-info 404. A virtual
// listing can omit a file for permission reasons, so it never proves deletion.
func (r *Reader) Absent(ctx context.Context, path string) (bool, error) {
	if path == "" || !within(r.root, path) {
		return false, source.ErrInvalidRequest
	}
	anchor, err := r.inspectRepository(ctx)
	if err != nil {
		if errors.Is(err, kernel.ErrNotFound) && !errors.Is(err, source.ErrAssumedOffline) {
			return false, source.ErrSuperseded
		}
		return false, err
	}
	if !anchor.Folder {
		return false, source.ErrInvalidResult
	}
	_, err = r.info(ctx, path)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, source.ErrAssumedOffline) || unsupportedInfo(err) {
		return false, source.ErrUnsupported
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return false, err
	}
	if err := r.Revalidate(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// Inspect verifies computed facts at the exact configured repository and path.
// Download URIs and original/client-declared checksums convey no authority.
func (r *Reader) Inspect(ctx context.Context, path string) (pull.Entry, error) {
	entry, err := r.info(ctx, path)
	if path != "" && unsupportedInfo(err) {
		return r.exactListedFile(ctx, path)
	}
	return entry, err
}

func (r *Reader) info(ctx context.Context, path string) (pull.Entry, error) {
	var entry pull.Entry
	err := r.read(ctx, path, nil, func(ctx context.Context, body io.Reader) error {
		var err error
		entry, err = decodeInfo(ctx, body, r.peer.Repository, path)
		return err
	})
	if err != nil {
		return pull.Entry{}, err
	}
	return entry, nil
}

func decodeInfo(ctx context.Context, body io.Reader, repository, path string) (pull.Entry, error) {
	var document map[string]json.RawMessage
	if err := decodeDocument(ctx, body, &document); err != nil {
		return pull.Entry{}, err
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return pull.Entry{}, invalid()
	}
	var info artifactory.FileInfo
	if json.Unmarshal(encoded, &info) != nil || info.Repo == nil ||
		(*info.Repo != repository && *info.Repo != repository+model.CacheSuffix) ||
		info.Path == nil || *info.Path != "/"+path {
		return pull.Entry{}, invalid()
	}
	entry := pull.Entry{Path: path}
	if children, folder := document["children"]; folder {
		var entries []artifactory.FolderChild
		if json.Unmarshal(children, &entries) != nil || entries == nil || info.Size != nil || info.Checksums != nil {
			return pull.Entry{}, invalid()
		}
		entry.Folder = true
		return entry, nil
	}
	if info.LastModified == nil || info.LastUpdated == nil || info.Size == nil ||
		info.Checksums == nil || info.Checksums.Sha256 == nil || !digest(*info.Checksums.Sha256, 32) {
		return pull.Entry{}, invalid()
	}
	entry.Size, err = strconv.ParseInt(*info.Size, 10, 64)
	if err != nil || entry.Size < 0 {
		return pull.Entry{}, invalid()
	}
	entry.Modified, entry.Updated = *info.LastModified, *info.LastUpdated
	entry.Checksums.SHA256 = strings.ToLower(*info.Checksums.Sha256)
	for _, sum := range []struct {
		from *string
		to   *string
		size int
	}{{info.Checksums.Sha1, &entry.Checksums.SHA1, 20}, {info.Checksums.Md5, &entry.Checksums.MD5, 16}} {
		if sum.from != nil {
			if !digest(*sum.from, sum.size) {
				return pull.Entry{}, invalid()
			}
			*sum.to = strings.ToLower(*sum.from)
		}
	}
	return entry, nil
}

func digest(value string, size int) bool {
	if len(value) != size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// Properties reads a separate, bounded management document. It never conflates
// file info and properties query modes or mutates either peer.
func (r *Reader) Properties(ctx context.Context, path string) (model.Properties, error) {
	var properties model.Properties
	err := r.read(ctx, path, url.Values{"properties": {""}}, func(ctx context.Context, body io.Reader) error {
		var document artifactory.ItemProperties
		if err := decodeDocument(ctx, body, &document); err != nil {
			return err
		}
		if document.Properties == nil || *document.Properties == nil || len(*document.Properties) > 256 {
			return invalid()
		}
		for key, values := range *document.Properties {
			if model.ValidatePropertyKey(key) != nil || len(values) == 0 || len(values) > 64 {
				return invalid()
			}
		}
		properties = model.Properties(*document.Properties).Clone()
		return nil
	})
	if err != nil {
		if errors.Is(err, kernel.ErrNotFound) {
			// 7.x answers an existing item with no properties as 404 too.
			// Confirm that exact file still exists under current authority;
			// a missing/denied file can never authorize an empty sync snapshot.
			entry, inspectErr := r.Inspect(ctx, path)
			if inspectErr == nil && !entry.Folder {
				return model.Properties{}, nil
			}
			if inspectErr != nil {
				return nil, inspectErr
			}
		}
		return nil, err
	}
	return properties, nil
}

func decodeDocument(ctx context.Context, body io.Reader, value any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("decode pull metadata: %w", err)
	}
	limited := &io.LimitedReader{R: body, N: metadataLimit + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(value); err != nil {
		return invalid()
	}
	return complete(ctx, decoder, limited)
}

func complete(ctx context.Context, decoder *json.Decoder, limited *io.LimitedReader) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("decode pull metadata: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || limited.N <= 0 {
		return invalid()
	}
	return nil
}
