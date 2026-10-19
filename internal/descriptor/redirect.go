package descriptor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// SectionDownloadRedirect is the system's direct cloud storage download
// settings: "Minimum Direct Cloud Storage Download Size" in 7.x's General
// settings, <downloadRedirectConfig><fileMinimumSize> in its descriptor.
// Unlike the named sections it is one mapping, not entries by name.
const SectionDownloadRedirect = "downloadRedirectConfig"

// fileMinimumSize is the section's one field: the smallest file, in MB,
// that a repository with downloadRedirect or cdnRedirect redirects.
const fileMinimumSize = "fileMinimumSize"

// The section is stored as one configuration document. The kind is this
// package's own: only the descriptor writes it and only
// DownloadRedirectMinSize reads it back.
const (
	downloadRedirectKind types.ConfigKind = "download_redirect"
	downloadRedirectKey  string           = "default"
)

// megabyte is the unit of fileMinimumSize. 6.20 converts it with
// StorageUnit.MB, and the 1 MB default is the 1 MiB that
// download.redirect.min_size defaults to.
const megabyte = 1 << 20

// maxFileMinimumSize keeps the size in bytes inside an int64.
const maxFileMinimumSize = (1<<63 - 1) / megabyte

// parseDownloadRedirect adds the downloadRedirectConfig section of top, when
// present, to p as a configuration change. Its fields merge into the stored
// document as a proxy's do, so omitting fileMinimumSize keeps the stored
// value. A null is refused, as on every field that is not a string.
func parseDownloadRedirect(top map[string]any, p *Patch) error {
	v, ok := top[SectionDownloadRedirect]
	if !ok {
		return nil
	}
	if v == nil {
		return nullRefused(SectionDownloadRedirect)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: %s must be a mapping", ErrInvalid, SectionDownloadRedirect)
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		where := SectionDownloadRedirect + "." + k
		if k != fileMinimumSize {
			return fmt.Errorf("%w: %s: unknown field %s", ErrInvalid, SectionDownloadRedirect, k)
		}
		if m[k] == nil {
			return nullRefused(where)
		}
		// YAML decodes a whole number as int; 1.5, "1" or a negative size
		// would leave the threshold to a guess.
		n, isInt := m[k].(int)
		if !isInt || n < 0 || n > maxFileMinimumSize {
			return fmt.Errorf("%w: %s must be a whole number of MB from 0 to %d",
				ErrInvalid, where, maxFileMinimumSize)
		}
	}
	p.Configs = append(p.Configs, ConfigChange{
		Section: SectionDownloadRedirect, Kind: downloadRedirectKind, Key: downloadRedirectKey,
		Fields: m,
	})
	return nil
}

// DownloadRedirectMinSize returns, in bytes, the minimum redirect size a
// descriptor set with downloadRedirectConfig.fileMinimumSize; ok is false
// when none has. It then takes precedence over download.redirect.min_size,
// the installation's default: the descriptor is the instance's own setting,
// which the API and its clients change at run time.
func DownloadRedirectMinSize(ctx context.Context, configs store.ConfigStore) (int64, bool, error) {
	d, err := configs.Get(ctx, downloadRedirectKind, downloadRedirectKey)
	if errors.Is(err, store.ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("descriptor: %s: %w", SectionDownloadRedirect, err)
	}
	var doc struct {
		FileMinimumSize *int64 `json:"fileMinimumSize"` //nolint:tagliatelle // the descriptor's own name
	}
	if err := json.Unmarshal(d.Document, &doc); err != nil {
		return 0, false, fmt.Errorf("descriptor: stored %s: %w", SectionDownloadRedirect, err)
	}
	if doc.FileMinimumSize == nil {
		return 0, false, nil
	}
	return *doc.FileMinimumSize * megabyte, true, nil
}
