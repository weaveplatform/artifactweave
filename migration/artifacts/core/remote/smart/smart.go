package smart

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
	"time"

	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type (
	smartSessionKey struct{}
	smartSession    struct {
		peer remote.SmartPeer
		opts remote.Options
	}
)

// Smart adds authenticated discovery and bounded durable side effects to
// one shared policy. Registry and ordinary policies keep separate validators.
type Smart struct {
	policy       *remote.Policy
	pool         remote.Upstream
	authn        accessapi.Authenticator
	authz        accessapi.Authorizer
	registryPath func(string) (image, operation, reference string, err error)
	queue        *work.Queue
	kind         string
	node         string
	discovery    remote.FlightGroup[remote.SmartPeer]
	cancel       context.CancelFunc
	workers      sync.WaitGroup
}

// NewSmart attaches smart remote discovery and side effects to p, with pool
// for peer requests, and starts the workers that run the side effects queued
// as kind. A side effect runs as the principal whose request queued it,
// rechecked when it runs: authn's CurrentRegistry for a registry token, or
// Current for any other credential, must still accept it, and authz must
// still grant it, or the side effect fails permanently with source.ErrDenied.
func NewSmart(
	ctx context.Context,
	p *remote.Policy,
	pool remote.Upstream,
	authn accessapi.Authenticator,
	authz accessapi.Authorizer,
	kind string,
	registryPath func(string) (image, operation, reference string, err error),
	log *slog.Logger,
) (*Smart, error) {
	queue, err := work.New(p.Store, work.Options{
		Workers: 2, MaxAttempts: 3, Log: log, Now: func() time.Time { return policyNow(p) },
	})
	if err != nil {
		return nil, fmt.Errorf("smart remote queue: %w", err)
	}
	digest := sha256.Sum256(p.Key)
	s := &Smart{
		policy:       p,
		pool:         pool,
		authn:        authn,
		authz:        authz,
		registryPath: registryPath,
		queue:        queue,
		kind:         kind,
		node:         hex.EncodeToString(digest[:12]),
	}
	if err = queue.Handle(kind, s.run); err != nil {
		return nil, fmt.Errorf("smart remote worker: %w", err)
	}
	old := p.PrepareUpload
	p.PrepareUpload = func(ctx context.Context, q source.Request, permit source.Permit, opts remote.Options, u *item.CacheUpload) error {
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

func smartPropertyPath(
	registryPath func(string) (image, operation, reference string, err error),
	q source.Request,
) bool {
	switch q.Repo.PackageType {
	case repo.Docker, repo.OCI, repo.HelmOCI:
		if registryPath == nil {
			return false
		}
		_, operation, ref, err := registryPath(q.Resource.UpstreamPath)
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
	ctx = remote.WithSmart(ctx, false)
	if q.Resource.SmartAQL != "" &&
		(permit.Intent == source.RetrievalIntentCacheOnly || !permit.UpstreamNetworkAllowed || enabled(q.Repo.Offline)) {
		return source.Result{}, source.ErrUnavailable
	}
	if permit.Intent == source.RetrievalIntentCacheOnly || !permit.UpstreamNetworkAllowed ||
		enabled(q.Repo.Offline) ||
		!q.Resource.RequireSmart && !smartEnabled(q.Repo) &&
			!enabled(q.Repo.SynchronizeProperties) {
		//nolint:wrapcheck // preserves upstream error identity
		return s.policy.Read(ctx, q, permit)
	}
	ctx = remote.WithSmart(ctx, true)
	ctx = remote.WithOutbound(ctx, remote.Outbound{Header: smartHeaders})
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
		return source.Result{}, remote.PolicyError(err)
	}
	if opts.Revision != peer.Revision() {
		return source.Result{}, source.ErrSuperseded
	}
	ctx = context.WithValue(ctx, smartSessionKey{}, smartSession{peer: peer, opts: opts})
	if q.Resource.RequireSmart {
		ctx = remote.WithOutbound(ctx, remote.Outbound{
			Header: smartHeaders,
			Allow:  func(target string) bool { return smartMetadataTarget(peer, target) },
		})
	}
	out, err := s.policy.Read(ctx, q, permit)
	if err != nil || out.Item == nil || q.Resource.Method != http.MethodGet ||
		q.Resource.RequireSmart {
		return out, smartError("status", err)
	}
	key := s.policy.CacheKey(q, opts.Revision)
	entry, err := s.policy.ReadEntry(ctx, key, permit)
	if err != nil {
		return source.Result{}, smartError("read", err)
	}
	if smartDue(s.registryPath, q, entry.Smart, policyNow(s.policy)) {
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

func smartMetadataTarget(peer remote.SmartPeer, target string) bool {
	base, err := peer.StorageURL("", nil)
	if err != nil {
		return false
	}
	u, err := remote.CheckedURL(target)
	b, baseErr := remote.CheckedURL(base)
	if err != nil || baseErr != nil || remote.Origin(u) != remote.Origin(b) ||
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
	opts remote.Options,
	u *item.CacheUpload,
) error {
	if !smartPropertyPath(s.registryPath, q) ||
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
	u.SmartPropertiesStarted = policyNow(s.policy)
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
