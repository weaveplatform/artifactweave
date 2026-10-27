package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type smartJob struct {
	Key          types.RemoteCacheKey     `json:"key"`
	Identity     types.SourceIdentity     `json:"identity"`
	Epoch        int64                    `json:"epoch"`
	ItemID       int64                    `json:"item_id"`
	SHA256       string                   `json:"sha256"`
	Kind         source.ResourceKind      `json:"kind"`
	UpstreamPath string                   `json:"upstream_path,omitempty"`
	Principal    auth.CredentialReference `json:"principal"`
	Statistics   bool                     `json:"statistics"`
	Trace        []string                 `json:"trace,omitempty"`
}

func (s *Smart) enqueue(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	key types.RemoteCacheKey,
	statistics bool,
) (string, error) {
	principal, ok := auth.FromContext(ctx)
	if !ok {
		return "", source.ErrDenied
	}
	jobKey := smartWorkKey(key, permit.Identity, statistics)
	err := s.policy.Store.Tx(ctx, func(tx context.Context) error {
		if err := s.admit(tx, jobKey); err != nil {
			return smartError("operation", err)
		}
		entry, err := s.currentEntry(tx, q, permit, key)
		if err != nil {
			return smartError("operation", err)
		}
		if !statistics {
			previous, e := s.policy.Store.WorkQueue().Get(tx, s.kind, jobKey)
			if e != nil && !errors.Is(e, store.ErrNotFound) {
				return smartError("work status", e)
			}
			if e == nil && previous.State == types.WorkParked {
				var old smartJob
				if json.Unmarshal(previous.Payload, &old) != nil ||
					old.Epoch == entry.Token.Epoch && old.ItemID == entry.ItemID &&
						old.SHA256 == entry.Blob.Checksums.SHA256 {
					return source.ErrUnavailable
				}
			}
		}
		job := smartJob{
			Key:          key,
			Identity:     permit.Identity,
			Epoch:        entry.Token.Epoch,
			ItemID:       entry.ItemID,
			SHA256:       entry.Blob.Checksums.SHA256,
			Kind:         q.Resource.Kind,
			UpstreamPath: q.Resource.UpstreamPath,
			Principal:    principal.CredentialReference(),
			Statistics:   statistics,
		}
		trace, _ := ctx.Value(smartTraceKey{}).([]string)
		if len(trace) > 0 {
			job.Trace = slices.Clone(trace[:len(trace)-1])
		}
		if statistics {
			if entry.Smart == nil {
				entry.Smart = &types.RemoteSmartState{}
			}
			if entry.Smart.Downloads == math.MaxInt64 {
				return source.ErrUnavailable
			}
			entry.Smart.Downloads++
			entry.Smart.LastDownload, entry.Smart.LastDownloadBy = s.policy.now(), principal.Name
			entry.LastUsed = s.policy.now()
			if _, err = s.policy.Store.RemoteCache().Put(tx, entry.Token, entry); err != nil {
				return smartError("operation", err)
			}
		}
		payload, err := json.Marshal(job)
		if err != nil || len(payload) > 64<<10 {
			return source.ErrInvalidRequest
		}
		return s.queue.Enqueue(tx, s.kind, jobKey, payload)
	})
	return jobKey, smartError("enqueue", err)
}

// Admission is serialized across SQL processes and counts parked work too.
// Existing keys can coalesce without allowing a stream of new paths to grow it.
func (s *Smart) admit(ctx context.Context, key string) error {
	holder, now := rand.Text(), s.policy.now()
	held, err := s.policy.Store.Leases().
		Acquire(ctx, types.Lease{Name: s.kind, Holder: holder, Acquired: now, Expires: now.Add(time.Minute)}, now)
	if err != nil || !held {
		return source.ErrUnavailable
	}
	defer func() { _ = s.policy.Store.Leases().Release(ctx, s.kind, holder) }()
	if _, err = s.policy.Store.WorkQueue().Get(ctx, s.kind, key); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("smart remote work lookup: %w", err)
	}
	stats, err := s.policy.Store.WorkQueue().Stats(ctx)
	if err != nil {
		return fmt.Errorf("smart remote work capacity: %w", err)
	}
	for _, stat := range stats {
		if stat.Kind == s.kind && stat.Pending+stat.Leased+stat.Parked >= 256 {
			return ErrBusy
		}
	}
	return nil
}

func (s *Smart) currentEntry(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	key types.RemoteCacheKey,
) (types.RemoteCacheEntry, error) {
	if err := s.policy.check(ctx, q, permit, key.SettingsRevision); err != nil {
		return types.RemoteCacheEntry{}, err
	}
	// Begin takes the source lifetime lock before reading mutable state. The
	// outer transaction keeps concurrent statistics and property merges serial.
	if _, err := s.policy.Store.RemoteCache().Begin(ctx, key); err != nil {
		return types.RemoteCacheEntry{}, fmt.Errorf("smart remote state lock: %w", err)
	}
	entry, err := s.policy.readEntry(ctx, key, permit)
	if err != nil || entry.ItemID == 0 {
		return types.RemoteCacheEntry{}, source.ErrSuperseded
	}
	return entry, nil
}

func (s *Smart) restore(
	ctx context.Context,
	job smartJob,
) (context.Context, source.Request, source.Permit, Options, error) {
	var q source.Request
	permit := source.Permit{
		Identity:               job.Identity,
		Intent:                 source.RetrievalIntentMayFetch,
		UpstreamNetworkAllowed: true,
	}
	c, err := s.policy.Store.Repos().Get(ctx, job.Key.RepoKey)
	if err != nil || c.Class != repo.Remote || enabled(c.Offline) || enabled(c.BlackedOut) ||
		c.StoreArtifactsLocally != nil && !*c.StoreArtifactsLocally {
		return ctx, q, permit, Options{}, source.ErrDenied
	}
	opts, err := s.policy.Options(ctx, c)
	if err != nil || opts.Revision != job.Key.SettingsRevision {
		return ctx, q, permit, opts, source.ErrSuperseded
	}
	p := job.Principal.Principal()
	if p.Registry != nil {
		p, err = s.auth.CurrentRegistry(ctx, p, s.policy.Store.Tokens())
	} else {
		p, err = s.auth.Current(ctx, p, s.policy.Store.Tokens())
	}
	if err != nil {
		return ctx, q, permit, opts, source.ErrDenied
	}
	if p.Registry != nil && !smartRegistryGrant(p.Registry, c.Key, job.UpstreamPath) {
		return ctx, q, permit, opts, source.ErrDenied
	}
	p, err = auth.Authorize(
		ctx,
		authz.Sources{
			Permissions:      s.policy.Store.Permissions(),
			Groups:           s.policy.Store.Groups(),
			AnonymousEnabled: p.Anonymous,
		},
		p,
	)
	if err != nil {
		return ctx, q, permit, opts, source.ErrDenied
	}
	ctx = auth.WithPrincipal(ctx, p)
	ctx = context.WithValue(ctx, smartActiveKey{}, true)
	ctx = context.WithValue(ctx, smartTraceKey{}, job.Trace)
	ctx, err = smartEnter(ctx, s.node+"/"+string(permit.Identity.Repository))
	q = source.Request{
		Repo:    c,
		Subject: p.Subject(),
		Access:  p.Access(),
		Project: c.ProjectKey,
		Resource: source.Resource{
			Kind:         job.Kind,
			Path:         job.Key.Path,
			Method:       http.MethodGet,
			UpstreamPath: job.UpstreamPath,
		},
	}
	if err == nil {
		err = s.policy.check(ctx, q, permit, opts.Revision)
	}
	return ctx, q, permit, opts, err
}

func smartRegistryGrant(access *types.RegistryAccess, key, path string) bool {
	name, _, _, err := registryResource(path)
	if err != nil {
		return false
	}
	if access.Realm == "" {
		name = key + "/" + name
	} else if !strings.EqualFold(access.Realm, key) {
		return false
	}
	return slices.ContainsFunc(access.Grants, func(grant types.RegistryGrant) bool {
		return grant.Type == "repository" && strings.EqualFold(grant.Name, name) &&
			(slices.Contains(grant.Actions, "pull") || slices.Contains(grant.Actions, "*"))
	})
}

func (s *Smart) run(ctx context.Context, workItem types.WorkItem) error {
	var job smartJob
	if json.Unmarshal(workItem.Payload, &job) != nil || job.Key.Path == "" || job.ItemID <= 0 {
		return errors.Join(work.ErrPermanent, source.ErrInvalidRequest)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	ctx, q, permit, opts, err := s.restore(ctx, job)
	if err != nil {
		return errors.Join(work.ErrPermanent, err)
	}
	if job.Statistics && !smartStatistics(q.Repo) ||
		!job.Statistics && !smartProperties(q.Repo) && !smartAbsence(q.Repo) {
		return errors.Join(work.ErrPermanent, source.ErrDenied)
	}
	if err = s.policy.Store.Tx(ctx, func(tx context.Context) error {
		entry, e := s.currentEntry(tx, q, permit, job.Key)
		if e != nil {
			return smartError("operation", e)
		}
		if entry.Token.Epoch != job.Epoch || entry.ItemID != job.ItemID ||
			entry.Blob.Checksums.SHA256 != job.SHA256 {
			return source.ErrSuperseded
		}
		return nil
	}); err != nil {
		return errors.Join(work.ErrPermanent, err)
	}
	peer, err := s.Discover(ctx, q, permit)
	if err == nil {
		release, e := s.policy.capacity.acquire(ctx, q.Repo.Key)
		if e != nil {
			return policyError(e)
		}
		defer release()
		if job.Statistics {
			err = s.statistics(ctx, q, permit, opts, peer, job)
		} else {
			err = s.refresh(ctx, q, permit, opts, peer, job)
		}
	}
	if errors.Is(err, source.ErrDenied) || errors.Is(err, source.ErrSuperseded) ||
		errors.Is(err, source.ErrInvalidResult) || errors.Is(err, errSmartUncertain) {
		return errors.Join(work.ErrPermanent, err)
	}
	return smartError("operation", err)
}

func (s *Smart) update(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	job smartJob,
	change func(context.Context, *types.RemoteCacheEntry) error,
) error {
	err := s.policy.Store.Tx(ctx, func(tx context.Context) error {
		entry, err := s.currentEntry(tx, q, permit, job.Key)
		if err != nil {
			return smartError("operation", err)
		}
		if entry.Token.Epoch != job.Epoch || entry.ItemID != job.ItemID ||
			entry.Blob.Checksums.SHA256 != job.SHA256 {
			return source.ErrSuperseded
		}
		if entry.Smart == nil {
			entry.Smart = &types.RemoteSmartState{}
		}
		if err = change(tx, &entry); err != nil {
			return smartError("operation", err)
		}
		_, err = s.policy.Store.RemoteCache().Put(tx, entry.Token, entry)
		return smartError("operation", err)
	})
	return smartError("update", err)
}

func (s *Smart) refresh(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	peer SmartPeer,
	job smartJob,
) error {
	started := s.policy.now()
	properties := smartProperties(q.Repo) && smartPropertyPath(q)
	var incoming types.Properties
	var present bool
	var err error
	if properties {
		incoming, present, err = s.properties(ctx, q, permit, opts, peer)
	} else {
		present, err = s.present(ctx, q, permit, opts, peer)
	}
	if err != nil {
		return smartError("operation", err)
	}
	return s.update(
		ctx,
		q,
		permit,
		job,
		func(tx context.Context, entry *types.RemoteCacheEntry) error {
			// A later observation from another HTTP representation won publication.
			// Never replace it with this older in-flight response.
			if properties && entry.Smart.PropertiesAt.After(started) ||
				smartAbsence(q.Repo) && entry.Smart.OriginAt.After(started) {
				return nil
			}
			if properties && present {
				current, e := s.policy.Store.Items().Properties(tx, entry.ItemID)
				if e != nil {
					return smartError("operation", e)
				}
				merged, state := item.MergeRemoteProperties(
					current,
					entry.Smart,
					incoming,
					s.policy.now(),
				)
				var remove []string
				for key := range current {
					if _, ok := merged[key]; !ok {
						remove = append(remove, key)
					}
				}
				it, e := s.policy.Store.Items().GetByID(tx, entry.ItemID)
				if e != nil {
					return smartError("operation", e)
				}
				if e = s.policy.Items.Annotate(tx, q.Repo, it, merged, remove); e != nil {
					return smartError("operation", e)
				}
				entry.Smart = state
				if e = s.policy.Items.PropagateRemotePropertyState(tx, *entry); e != nil {
					return smartError("property representations", e)
				}
			}
			if smartAbsence(q.Repo) {
				entry.Smart.OriginAt, entry.Smart.OriginAbsent = s.policy.now(), !present
			}
			if properties && !present {
				entry.Smart.PropertiesAt = s.policy.now()
			}
			return nil
		},
	)
}

func smartWorkKey(key types.RemoteCacheKey, source types.SourceIdentity, statistics bool) string {
	identity, _ := json.Marshal(struct {
		Key        types.RemoteCacheKey `json:"key"`
		Source     types.SourceIdentity `json:"source"`
		Statistics bool                 `json:"statistics"`
	}{key, source, statistics})
	digest := sha256.Sum256(identity)
	return hex.EncodeToString(digest[:])
}
