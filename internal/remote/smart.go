package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type (
	smartSessionKey struct{}
	smartSession    struct {
		peer              SmartPeer
		opts              Options
		isMetadataRequest bool
	}
)

// Smart adds authenticated discovery and bounded durable side effects to
// one shared policy. Registry and ordinary policies keep separate validators.
type Smart struct {
	policy    *Policy
	pool      *Pool
	auth      *auth.Authenticator
	queue     *work.Queue
	kind      string
	node      string
	discovery flightGroup[SmartPeer]
	cancel    context.CancelFunc
	workers   sync.WaitGroup
}

func NewSmart(
	ctx context.Context,
	p *Policy,
	pool *Pool,
	a *auth.Authenticator,
	kind string,
	log *slog.Logger,
) (*Smart, error) {
	queue, err := work.New(p.Store, work.Options{Workers: 2, MaxAttempts: 3, Log: log, Now: p.now})
	if err != nil {
		return nil, fmt.Errorf("smart remote queue: %w", err)
	}
	digest := sha256.Sum256(p.Key)
	s := &Smart{
		policy: p,
		pool:   pool,
		auth:   a,
		queue:  queue,
		kind:   kind,
		node:   hex.EncodeToString(digest[:12]),
	}
	if err = queue.Handle(kind, s.run); err != nil {
		return nil, fmt.Errorf("smart remote worker: %w", err)
	}
	old := p.PrepareUpload
	p.PrepareUpload = func(ctx context.Context, q source.Request, permit source.Permit, opts Options, u *item.CacheUpload) error {
		if old != nil {
			if err := old(ctx, q, permit, opts, u); err != nil {
				return smartError("operation", err)
			}
		}
		return s.prepareUpload(ctx, q, permit, opts, u)
	}
	p.Smart = s
	loop, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.workers.Go(func() { _ = queue.Run(loop) })
	return s, nil
}

func (s *Smart) Close() {
	if s != nil {
		s.cancel()
		s.workers.Wait()
	}
}

func smartEnabled(c repo.Config) bool {
	return c.ContentSynchronisation != nil && c.ContentSynchronisation.Enabled
}

func smartProperties(c repo.Config) bool {
	return smartEnabled(c) && c.ContentSynchronisation.Properties != nil &&
		c.ContentSynchronisation.Properties.Enabled
}

func smartStatistics(c repo.Config) bool {
	return smartEnabled(c) && c.ContentSynchronisation.Statistics != nil &&
		c.ContentSynchronisation.Statistics.Enabled
}

func smartAbsence(c repo.Config) bool {
	return smartEnabled(c) && c.PackageType != repo.Generic &&
		c.ContentSynchronisation.Source != nil &&
		c.ContentSynchronisation.Source.OriginAbsenceDetection
}

func smartPropertyPath(q source.Request) bool {
	switch q.Repo.PackageType {
	case repo.Docker, repo.OCI, repo.HelmOCI:
		_, operation, ref, err := registryResource(q.Resource.UpstreamPath)
		return err == nil && operation == "manifests" && !strings.HasPrefix(ref, "sha256:")
	default:
		return q.Resource.Kind == source.ResourceKindArtifact
	}
}

func (s *Smart) Read(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
) (source.Result, error) {
	ctx = context.WithValue(ctx, smartActiveKey{}, false)
	if q.Resource.SmartAQL != "" &&
		(permit.Intent == source.RetrievalIntentCacheOnly || !permit.UpstreamNetworkAllowed || enabled(q.Repo.Offline)) {
		return source.Result{}, source.ErrUnavailable
	}
	if permit.Intent == source.RetrievalIntentCacheOnly || !permit.UpstreamNetworkAllowed ||
		enabled(q.Repo.Offline) ||
		!q.Resource.RequireSmart && !smartEnabled(q.Repo) &&
			!enabled(q.Repo.SynchronizeProperties) {
		return s.policy.Read(ctx, q, permit)
	}
	ctx = context.WithValue(ctx, smartActiveKey{}, true)
	ctx, err := smartEnter(ctx, s.node+"/"+string(permit.Identity.Repository))
	if err != nil {
		return source.Result{}, smartError("read", err)
	}
	peer, err := s.Discover(ctx, q, permit)
	if err != nil {
		return source.Result{}, smartError("read", err)
	}
	if q.Resource.SmartAQL != "" {
		return s.query(ctx, q, permit, peer)
	}
	if q.Resource.RequireSmart && !smartMetadataTarget(peer, q.Resource.ExternalURL) {
		return source.Result{}, source.ErrDenied
	}
	opts, err := s.policy.Options(ctx, q.Repo)
	if err != nil {
		return source.Result{}, policyError(err)
	}
	if opts.Revision != peer.revision {
		return source.Result{}, source.ErrSuperseded
	}
	ctx = context.WithValue(
		ctx,
		smartSessionKey{},
		smartSession{peer: peer, opts: opts, isMetadataRequest: q.Resource.RequireSmart},
	)
	out, err := s.policy.Read(ctx, q, permit)
	if err != nil || out.Item == nil || q.Resource.Method != http.MethodGet ||
		q.Resource.RequireSmart {
		return out, smartError("status", err)
	}
	key := s.policy.cacheKey(q, opts.Revision)
	entry, err := s.policy.readEntry(ctx, key, permit)
	if err != nil {
		return source.Result{}, smartError("read", err)
	}
	if smartDue(q, entry.Smart, s.policy.now()) {
		jobKey, e := s.enqueue(ctx, q, permit, key, false)
		if e == nil {
			e = s.queue.EnsureFresh(ctx, s.kind, jobKey)
		}
		if e != nil {
			return source.Result{}, smartError("refresh", e)
		}
	}
	used := out.Used
	out.Used = func(current context.Context) error {
		if used != nil {
			if err := used(current); err != nil {
				return smartError("operation", err)
			}
		}
		if smartStatistics(q.Repo) {
			current = context.WithValue(current, smartTraceKey{}, ctx.Value(smartTraceKey{}))
			_, err := s.enqueue(current, q, permit, key, true)
			return smartError("operation", err)
		}
		return nil
	}
	return out, nil
}

func smartMetadataTarget(peer SmartPeer, target string) bool {
	base, err := peer.StorageURL("", nil)
	if err != nil {
		return false
	}
	u, err := checkedURL(target)
	b, baseErr := checkedURL(base)
	if err != nil || baseErr != nil || origin(u) != origin(b) ||
		strings.ContainsAny(u.Path, "%?#") ||
		strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") {
		return false
	}
	if u.Path == b.Path {
		return true
	}
	path, ok := strings.CutPrefix(u.Path, b.Path+"/")
	if !ok || path == "" {
		return false
	}
	_, _, _, err = types.SplitItemPath(path)
	return err == nil
}

func (s *Smart) prepareUpload(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	u *item.CacheUpload,
) error {
	if !smartPropertyPath(q) ||
		!enabled(q.Repo.SynchronizeProperties) && !smartProperties(q.Repo) ||
		u.NoStore ||
		q.Repo.StoreArtifactsLocally != nil && !*q.Repo.StoreArtifactsLocally {
		return nil
	}
	session, ok := ctx.Value(smartSessionKey{}).(smartSession)
	if !ok {
		return source.ErrUnavailable
	}
	if !smartProperties(q.Repo) {
		done, err := s.policy.Items.HasRemoteProperties(ctx, u.Token)
		if err != nil {
			return smartError("initial properties", err)
		}
		if done {
			return nil
		}
	}
	u.SmartPropertiesStarted = s.policy.now()
	props, present, err := s.properties(ctx, q, permit, opts, session.peer)
	if err != nil {
		return smartError("operation", err)
	}
	if !present {
		return store.ErrNotFound
	}
	u.SmartProperties = &props
	return nil
}

var errSmartUncertain = errors.New(
	"remote: smart remote statistics submission is uncertain; claimed counts will not be retried",
)
