package conda

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const maxShardSnapshotBytes = 64 << 20

var errNoShardIndex = errors.New("conda: upstream shard index absent")

// A metadata URL cannot broaden the configured remote's destination scope.
// Transport still applies DNS/private-address policy and credential stripping.
func shardDestination(root, target *url.URL) bool {
	return root != nil && target != nil && (target.Scheme == "https" || target.Scheme == "http") &&
		target.Scheme == root.Scheme && target.Host == root.Host && target.User == nil &&
		target.RawQuery == "" && target.Fragment == "" && target.Opaque == "" && target.RawPath == "" &&
		!strings.ContainsAny(target.Path, "\\\x00\r\n") &&
		strings.HasPrefix(target.Path, strings.TrimRight(root.Path, "/")+"/") &&
		(target.Path == "/" || path.Clean(target.Path) == strings.TrimSuffix(target.Path, "/"))
}

func shardURLs(
	root string,
	id condafmt.MetadataPath,
	base string,
	index condafmt.ShardIndex,
) (*url.URL, error) {
	configured, err := url.Parse(root)
	if err != nil {
		return nil, source.ErrInvalidResult
	}
	configured.User = nil
	response, err := url.Parse(base)
	if err != nil || !shardDestination(configured, response) {
		return nil, source.ErrInvalidResult
	}
	packages, err := url.Parse(index.BaseURL)
	if err != nil {
		return nil, source.ErrInvalidResult
	}
	packages = response.ResolveReference(packages)
	wanted := strings.TrimRight(configured.Path, "/") + "/" + path.Dir(id.Path) + "/"
	// Package URLs must retain their canonical archive paths so every later
	// package request uses exactly the same permission and integrity identity.
	if !shardDestination(configured, packages) || packages.Path != wanted {
		return nil, source.ErrInvalidResult
	}
	shards, err := url.Parse(index.ShardsBaseURL)
	if err != nil {
		return nil, source.ErrInvalidResult
	}
	shards = response.ResolveReference(shards)
	if !shardDestination(configured, shards) || !strings.HasSuffix(shards.Path, "/") {
		return nil, source.ErrInvalidResult
	}
	return shards, nil
}

func readShardMetadata(body io.Reader, size int64) ([]byte, error) {
	if size <= 0 || size > maxShardSnapshotBytes {
		return nil, source.ErrInvalidResult
	}
	data, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil || int64(len(data)) != size {
		return nil, source.ErrInvalidResult
	}
	return data, nil
}

func (h handler) remoteShardedIndex(r *http.Request, id condafmt.MetadataPath) (
	condafmt.Repodata, func(context.Context) error, error,
) {
	indexPath := condafmt.IndexPath(id.Channel, id.Subdir, condafmt.ShardIndexFilename)
	root := pkgroute.RequestFrom(r).Repo.URL
	resource := source.Resource{
		Kind:             source.ResourceKindMetadata,
		Path:             indexPath,
		Method:           http.MethodGet,
		PermissionFolder: true,
		PermissionPath:   path.Dir(indexPath),
		Variant:          "conda-shard-index-v1",
		ExpectedContentTypes: []string{
			"application/zstd",
			"application/octet-stream",
			"binary/octet-stream",
		},
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, size int64, response source.MetadataResponse) error {
			data, err := readShardMetadata(body, size)
			if err != nil {
				return err
			}
			index, err := condafmt.DecodeShardIndex(data)
			if err != nil {
				return source.ErrInvalidResult
			}
			_, err = shardURLs(root, id, response.BaseURL, index)
			return err
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		if shardNotFound(err) {
			return condafmt.Repodata{}, nil, errNoShardIndex
		}
		return condafmt.Repodata{}, nil, fmt.Errorf("conda: shard index: %w", err)
	}
	body, err := h.resultBytes(r.Context(), result)
	if err != nil {
		return condafmt.Repodata{}, nil, err
	}
	index, err := condafmt.DecodeShardIndex(body)
	if err != nil {
		return condafmt.Repodata{}, nil, source.ErrInvalidResult
	}
	base, err := shardURLs(root, id, result.Metadata.BaseURL, index)
	if err != nil {
		return condafmt.Repodata{}, nil, err
	}
	combined := condafmt.EmptyRepodata(id.Subdir)
	fences := []func(context.Context) error{result.Revalidate}
	remaining := maxShardSnapshotBytes - len(body)
	for _, name := range slices.Sorted(maps.Keys(index.Shards)) {
		digest := hex.EncodeToString(index.Shards[name])
		shard, finish, size, err := h.remoteShard(r, id, name, digest, base)
		if err != nil {
			return condafmt.Repodata{}, nil, err
		}
		// JSON length bounds the retained records as well as compressed transfer.
		// Repodata contains only concrete strings, integers, slices and maps.
		retained, _ := json.Marshal(shard)
		remaining -= max(size, len(retained))
		if remaining < 0 ||
			len(
				combined.Packages,
			)+len(
				combined.PackagesConda,
			)+len(
				shard.Packages,
			)+len(
				shard.PackagesConda,
			) > 200000 {
			return condafmt.Repodata{}, nil, source.ErrInvalidResult
		}
		maps.Copy(combined.Packages, shard.Packages)
		maps.Copy(combined.PackagesConda, shard.PackagesConda)
		fences = append(fences, finish)
	}
	filtered, err := h.filterIndex(r.Context(), r, combined, id, result.CheckRead)
	if err != nil {
		return condafmt.Repodata{}, nil, err
	}
	return filtered, func(ctx context.Context) error {
		for _, fence := range fences {
			if fence != nil {
				if err := fence(ctx); err != nil {
					return fmt.Errorf("conda: shard authority: %w", err)
				}
			}
		}
		return nil
	}, nil
}

func (h handler) remoteShard(
	r *http.Request,
	id condafmt.MetadataPath,
	name, digest string,
	base *url.URL,
) (
	condafmt.Repodata, func(context.Context) error, int, error,
) {
	filename := digest + ".msgpack.zst"
	target := base.ResolveReference(&url.URL{Path: filename})
	root, err := url.Parse(pkgroute.RequestFrom(r).Repo.URL)
	if err != nil {
		return condafmt.Repodata{}, nil, 0, source.ErrInvalidResult
	}
	resource := source.Resource{
		Kind:              source.ResourceKindMetadata,
		Path:              condafmt.IndexPath(id.Channel, id.Subdir, filename),
		Method:            http.MethodGet,
		PermissionFolder:  true,
		PermissionPath:    path.Dir(id.Path),
		Variant:           "conda-shard-v1:" + name,
		ExternalURL:       target.String(),
		NoCrossOriginAuth: true,
		ExpectedContentTypes: []string{
			"application/zstd",
			"application/octet-stream",
			"binary/octet-stream",
		},
		CheckURL: func(_ context.Context, target *url.URL) error {
			if !shardDestination(root, target) {
				return source.ErrInvalidResult
			}
			return nil
		},
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			data, err := readShardMetadata(body, size)
			if err != nil {
				return err
			}
			if _, err := condafmt.DecodeShard(data, id, name, digest); err != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		if shardNotFound(err) {
			err = source.ErrInvalidResult
		}
		return condafmt.Repodata{}, nil, 0, fmt.Errorf("conda: package shard: %w", err)
	}
	body, err := h.resultBytes(r.Context(), result)
	if err != nil {
		return condafmt.Repodata{}, nil, 0, err
	}
	index, err := condafmt.DecodeShard(body, id, name, digest)
	if err != nil {
		return condafmt.Repodata{}, nil, 0, source.ErrInvalidResult
	}
	filtered, err := h.filterIndex(r.Context(), r, index, id, result.CheckRead)
	return filtered, result.Revalidate, len(body), err
}

func shardNotFound(err error) bool {
	var response *apierror.Error
	return errors.Is(err, store.ErrNotFound) ||
		errors.As(err, &response) && response.Status == http.StatusNotFound
}
