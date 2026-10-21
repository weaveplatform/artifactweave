package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// openRemoteSources shares the runtime's transport pool and ingestion service.
// Protocol adapters remain opt-in; only the generic download route installs this.
func (rt *runtime) openRemoteSources(
	ctx context.Context,
	items *item.Service,
) (*source.Service, error) {
	key, err := rt.remotePolicyKey(ctx)
	if err != nil {
		return nil, err
	}
	resolver, err := remote.NewResolver(rt.store, rt.keys, remote.ResolverOptions{RevisionKey: key})
	if err != nil {
		return nil, fmt.Errorf("remote resolver: %w", err)
	}
	resolve := func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
		current, err := rt.store.Repos().Get(ctx, c.Key)
		if err != nil {
			return types.SourceIdentity{}, source.ErrSuperseded
		}
		if store.RemotePolicyChanged(c, current) {
			return types.SourceIdentity{}, source.ErrSuperseded
		}
		id, err := rt.store.RemoteCache().Source(ctx, c.Key)
		if err != nil {
			return types.SourceIdentity{}, fmt.Errorf("remote identity: %w", err)
		}
		return id, nil
	}
	revalidate := func(ctx context.Context, q source.Request, permit source.Permit) error {
		id, err := resolve(ctx, q.Repo)
		if err != nil {
			return err
		}
		if id != permit.Identity {
			return source.ErrSuperseded
		}
		p, ok := auth.FromContext(ctx)
		if !ok {
			return source.ErrDenied
		}
		p, err = rt.auth.Current(ctx, p, rt.store.Tokens())
		if err != nil {
			return source.ErrDenied
		}
		p, err = auth.Authorize(ctx, authz.Sources{
			Permissions:      rt.store.Permissions(),
			Groups:           rt.store.Groups(),
			AnonymousEnabled: p.Anonymous,
		}, p)
		if err != nil {
			return source.ErrUnavailable
		}
		path := q.Resource.Path
		if q.Resource.ChecksumOnly {
			path = path[:len(path)-len(".sha256")]
		}
		if !p.Access().
			Allows(types.ResourceArtifact, authz.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)}, path, types.ActionRead) {
			return source.ErrDenied
		}
		return generic.CheckDownload(q.Repo, path, false)
	}
	policy := &remote.Policy{
		Store:   rt.store,
		Blobs:   rt.blobs,
		Items:   items,
		Pool:    &rt.remoteHTTP,
		Options: resolver.Options,
		Key:     key,
		CheckCurrent: func(ctx context.Context, _ store.Store, q source.Request, permit source.Permit, revision string) error {
			if err := revalidate(ctx, q, permit); err != nil {
				return err
			}
			opts, err := resolver.Options(ctx, q.Repo)
			if err != nil || opts.Revision != revision {
				return source.ErrSuperseded
			}
			return nil
		},
	}
	return &source.Service{
		Resolve:    resolve,
		Revalidate: revalidate,
		HTTP:       policy,
		Cache:      policy,
	}, nil
}

// This internal setting has no public settings endpoint or descriptor mapping.
// Without a master key the database already holds unencrypted authentication
// signing keys. Keep the same posture while preserving cache identity on restart.
const remotePolicyKeySetting = "remote.internal_policy_key"

func (rt *runtime) remotePolicyKey(ctx context.Context) ([]byte, error) {
	if rt.keys != nil {
		return rt.keys.Derive("remote-cache-variants"), nil
	}
	var key []byte
	err := rt.store.Tx(ctx, func(tx context.Context) error {
		// Serialize first-start initialization even on PostgreSQL where a missing
		// settings row cannot be locked. The lease is released in this transaction.
		holder := rand.Text()
		now := time.Now().UTC()
		held, err := rt.store.Leases().
			Acquire(tx, types.Lease{Name: remotePolicyKeySetting, Holder: holder, Acquired: now, Expires: now.Add(time.Minute)}, now)
		if err != nil {
			return fmt.Errorf("lock remote key: %w", err)
		}
		if !held {
			return source.ErrUnavailable
		}
		err = rt.store.Settings().Get(tx, remotePolicyKeySetting, &key)
		if errors.Is(err, store.ErrNotFound) {
			key = make([]byte, 32)
			if _, err = rand.Read(key); err != nil {
				return fmt.Errorf("generate remote key: %w", err)
			}
			err = rt.store.Settings().Put(tx, remotePolicyKeySetting, key)
		}
		if err != nil {
			return fmt.Errorf("persist remote key: %w", err)
		}
		if len(key) != 32 {
			return source.ErrUnavailable
		}
		return rt.store.Leases().Release(tx, remotePolicyKeySetting, holder)
	})
	if err != nil {
		return nil, fmt.Errorf("remote policy key: %w", err)
	}
	return key, nil
}
