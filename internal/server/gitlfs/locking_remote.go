package gitlfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxLockResponse = 256 << 10

type upstreamLockPage struct {
	Locks      []lockWire `json:"locks"`
	NextCursor string     `json:"next_cursor"`
}

func parseRemoteLockPage(body io.Reader, size int64, previous string) (upstreamLockPage, error) {
	if size < 0 || size > maxLockResponse {
		return upstreamLockPage{}, source.ErrInvalidResult
	}
	content, err := io.ReadAll(io.LimitReader(body, maxLockResponse+1))
	if err != nil || len(content) > maxLockResponse || int64(len(content)) != size {
		return upstreamLockPage{}, source.ErrInvalidResult
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(content, &shape) != nil || shape["locks"] == nil ||
		string(shape["locks"]) == "null" {
		return upstreamLockPage{}, source.ErrInvalidResult
	}
	var page upstreamLockPage
	if json.Unmarshal(content, &page) != nil || len(page.Locks) > store.MaxLimit ||
		len(page.NextCursor) > 2048 || page.NextCursor != "" && page.NextCursor == previous {
		return upstreamLockPage{}, source.ErrInvalidResult
	}
	seenID := make(map[string]bool, len(page.Locks))
	seenPath := make(map[string]bool, len(page.Locks))
	for _, lock := range page.Locks {
		if lock.ID == "" || len(lock.ID) > 256 || !validLockPath(lock.Path) ||
			len(lock.Owner.Name) > 256 || seenID[lock.ID] || seenPath[lock.Path] {
			return upstreamLockPage{}, source.ErrInvalidResult
		}
		stamp, err := time.Parse(time.RFC3339, lock.LockedAt)
		if err != nil || stamp.Format(time.RFC3339) != lock.LockedAt {
			return upstreamLockPage{}, source.ErrInvalidResult
		}
		seenID[lock.ID], seenPath[lock.Path] = true, true
	}
	return page, nil
}

func (h handler) remoteLockPage(
	r *http.Request, f lockFilter, limit int, after string, strict bool,
) ([]types.GitLFSLock, string, error) {
	if h.o.RemoteSources == nil {
		return nil, "", source.ErrUnsupported
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(limit))
	if f.Path != "" {
		query.Set("path", f.Path)
	}
	if f.ID != "" {
		query.Set("id", f.ID)
	}
	if after != "" {
		query.Set("cursor", after)
	}
	resource := source.Resource{
		Kind:                source.Metadata,
		Path:                "locks",
		UpstreamPath:        "locks",
		Method:              http.MethodGet,
		Query:               query.Encode(),
		Transient:           true,
		Variant:             "gitlfs-locks-v1",
		Accept:              mediaType,
		ExpectedContentType: mediaType,
		PermissionFolder:    true,
		ValidateMetadata: func(
			_ context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse,
		) error {
			_, err := parseRemoteLockPage(body, size, after)
			return err
		},
	}
	result, err := h.readSource(r, resource, false)
	if err != nil {
		return nil, "", err
	}
	if result.Stream == nil {
		return nil, "", source.ErrInvalidResult
	}
	defer closeSource(result)
	page, err := parseRemoteLockPage(result.Stream.Body, result.Stream.Size, after)
	if err != nil {
		return nil, "", err
	}
	out := make([]types.GitLFSLock, 0, len(page.Locks))
	for _, lock := range page.Locks {
		if err := result.CheckRead(r.Context(), lock.Path); err != nil {
			if errors.Is(err, source.ErrDenied) {
				if strict {
					return nil, "", fmt.Errorf("gitlfs: remote lock read denied: %w", err)
				}
				continue
			}
			return nil, "", fmt.Errorf("gitlfs: remote lock read: %w", err)
		}
		stamp, err := time.Parse(time.RFC3339, lock.LockedAt)
		if err != nil {
			return nil, "", source.ErrInvalidResult
		}
		out = append(out, types.GitLFSLock{
			ID: lock.ID, RepoKey: pkgroute.RequestFrom(r).Repo.Key, Path: lock.Path,
			Owner: lock.Owner.Name, LockedAt: stamp,
		})
	}
	return out, page.NextCursor, nil
}
