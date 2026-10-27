package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// statsFields are the download statistics PATCH /api/metadata merges, named
// as the ?stats view and 6.20's StatsInfo name them. Counts are whole
// numbers and times Unix milliseconds, as ?stats renders them; null clears
// a field, as a JSON merge patch does.
var statsFields = map[string]func(*types.ItemStats, json.RawMessage) error{
	"downloadCount": func(s *types.ItemStats, raw json.RawMessage) error {
		return statsCount(&s.DownloadCount, "downloadCount", raw)
	},
	"lastDownloaded": func(s *types.ItemStats, raw json.RawMessage) error {
		return statsTime(&s.LastDownloaded, "lastDownloaded", raw)
	},
	"lastDownloadedBy": func(s *types.ItemStats, raw json.RawMessage) error {
		return statsName(&s.LastDownloadedBy, "lastDownloadedBy", raw)
	},
	"remoteDownloadCount": func(s *types.ItemStats, raw json.RawMessage) error {
		return statsCount(&s.RemoteDownloadCount, "remoteDownloadCount", raw)
	},
	"remoteLastDownloaded": func(s *types.ItemStats, raw json.RawMessage) error {
		return statsTime(&s.RemoteLastDownloaded, "remoteLastDownloaded", raw)
	},
	"remoteLastDownloadedBy": func(s *types.ItemStats, raw json.RawMessage) error {
		return statsName(&s.RemoteLastDownloadedBy, "remoteLastDownloadedBy", raw)
	},
}

// mergeStats applies patch's fields to s. A field it does not know is
// refused rather than dropped, so a misspelt count is not silently lost.
func mergeStats(s *types.ItemStats, patch map[string]json.RawMessage) error {
	// Sorted, so the first bad field named is the same on every call.
	for _, name := range slices.Sorted(maps.Keys(patch)) {
		set, ok := statsFields[name]
		if !ok {
			return badRequestf("stats has no field %q", name)
		}
		if err := set(s, patch[name]); err != nil {
			return err
		}
	}
	return nil
}

// setStats merges patch into the statistics of it, which must be a file:
// statistics are kept for files only, as the ?stats view answers.
func setStats(
	ctx context.Context,
	st store.Store,
	it types.Item,
	patch map[string]json.RawMessage,
) error {
	if it.Folder {
		return badRequestf("%s is a folder; statistics are kept for files", it.StoragePath())
	}
	cur, err := st.Items().Stats(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("stats of %s: %w", it.FullPath(), err)
	}
	if err := mergeStats(&cur, patch); err != nil {
		return err
	}
	if err := st.Items().PutStats(ctx, it.ID, cur); err != nil {
		return fmt.Errorf("stats of %s: %w", it.FullPath(), err)
	}
	return nil
}

func statsCount(dst *int64, name string, raw json.RawMessage) error {
	var n *int64
	if err := json.Unmarshal(raw, &n); err != nil || n != nil && *n < 0 {
		return badRequestf("stats field %q must be a whole number of 0 or more, or null", name)
	}
	*dst = 0
	if n != nil {
		*dst = *n
	}
	return nil
}

// statsTime reads Unix milliseconds; 0, like null, means never, which is
// how ?stats renders a file never downloaded.
func statsTime(dst *time.Time, name string, raw json.RawMessage) error {
	var ms *int64
	if err := json.Unmarshal(raw, &ms); err != nil || ms != nil && *ms < 0 {
		return badRequestf(
			"stats field %q must be a time in Unix milliseconds of 0 or more, or null", name)
	}
	*dst = time.Time{}
	if ms != nil && *ms > 0 {
		*dst = time.UnixMilli(*ms).UTC()
	}
	return nil
}

func statsName(dst *string, name string, raw json.RawMessage) error {
	var s *string
	if err := json.Unmarshal(raw, &s); err != nil {
		return badRequestf("stats field %q must be a string or null", name)
	}
	*dst = ""
	if s != nil {
		*dst = *s
	}
	return nil
}
