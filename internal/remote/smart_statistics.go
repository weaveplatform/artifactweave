package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

// The historical receiver has no idempotency key. Claim before sending and
// never submit a claimed count again, including after a crash or lost reply.
// Uncertain counts remain visible for operator reconciliation.
func (s *Smart) statistics(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	peer SmartPeer,
	job smartJob,
) error {
	var snapshot types.RemoteSmartState
	var count int64
	err := s.update(
		ctx,
		q,
		permit,
		job,
		func(_ context.Context, entry *types.RemoteCacheEntry) error {
			state := entry.Smart
			count = state.Downloads - state.Claimed
			if count < 0 {
				return source.ErrInvalidResult
			}
			if count == 0 && state.Uncertain > 0 {
				return errSmartUncertain
			}
			state.Claimed = state.Downloads
			state.Uncertain += count
			snapshot = *state
			return nil
		},
	)
	if err != nil || count == 0 {
		return err
	}
	stats := []wire.SmartStatistics{
		{
			RepoPath:         smartStoragePath(q),
			Path:             q.Repo.Key + "/" + q.Resource.Path,
			DownloadCount:    count,
			LastDownloaded:   snapshot.LastDownload.UnixMilli(),
			LastDownloadedBy: snapshot.LastDownloadBy,
		},
	}
	body, err := json.Marshal(stats)
	if err != nil {
		return errSmartUncertain
	}
	res, err := s.request(ctx, q, permit, opts, http.MethodPut, peer.statisticsURL(), body)
	if err != nil {
		return errSmartUncertain
	}
	defer res.Body.Close()
	received, readErr := io.Copy(io.Discard, io.LimitReader(res.Body, (64<<10)+1))
	if res.StatusCode != http.StatusOK || readErr != nil || received > 64<<10 {
		return errSmartUncertain
	}
	return s.update(
		ctx,
		q,
		permit,
		job,
		func(_ context.Context, entry *types.RemoteCacheEntry) error {
			entry.Smart.Uncertain -= count
			return nil
		},
	)
}

func (p SmartPeer) statisticsURL() string {
	key := p.Repository
	if p.Class == repo.Remote {
		key += types.CacheSuffix
	}
	return p.BaseURL + "/api/storage/statistics/" + url.PathEscape(key)
}
