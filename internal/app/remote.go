package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/nimmodel"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// openRemoteSources shares the runtime's transport pool and ingestion service.
// Protocol adapters remain opt-in; generic and Maven-family routes install this.
func (rt *runtime) openRemoteSources(
	ctx context.Context,
	items *item.Service,
	log *slog.Logger,
	allowedPrivateOrigins []string,
) (*source.Service, error) {
	key, err := rt.remotePolicyKey(ctx)
	if err != nil {
		return nil, err
	}
	resolver, err := remote.NewResolver(rt.store, rt.keys, remote.ResolverOptions{
		RevisionKey: key, AllowedPrivateOrigins: allowedPrivateOrigins,
	})
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
	revalidatePrincipal := func(ctx context.Context, q source.Request, permit source.Permit, registry bool) error {
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
		if registry && p.Registry != nil {
			p, err = rt.auth.CurrentRegistry(ctx, p, rt.store.Tokens())
		} else {
			p, err = rt.auth.Current(ctx, p, rt.store.Tokens())
		}
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
		allows := p.Access().Allows
		if q.Resource.PermissionRoot {
			path = ""
		} else if q.Resource.PermissionFolder {
			path = q.Resource.PermissionPath
			allows = p.Access().AllowsFolder
		}
		if q.Resource.ChecksumOnly {
			path = path[:len(path)-len(".sha256")]
		}
		if !allows(
			types.ResourceArtifact,
			authz.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)},
			path,
			types.ActionRead,
		) {
			return source.ErrDenied
		}
		return generic.CheckDownload(
			q.Repo,
			path,
			q.Resource.PermissionFolder || q.Resource.PermissionRoot,
		)
	}
	revalidate := func(ctx context.Context, q source.Request, permit source.Permit) error {
		return revalidatePrincipal(ctx, q, permit, false)
	}
	registryRevalidate := func(ctx context.Context, q source.Request, permit source.Permit) error {
		return revalidatePrincipal(ctx, q, permit, true)
	}
	checkCurrent := func(check func(context.Context, source.Request, source.Permit) error) func(context.Context, store.Store, source.Request, source.Permit, string) error {
		return func(ctx context.Context, _ store.Store, q source.Request, permit source.Permit, revision string) error {
			if err := check(ctx, q, permit); err != nil {
				return err
			}
			opts, err := resolver.Options(ctx, q.Repo)
			if err != nil || opts.Revision != revision {
				return source.ErrSuperseded
			}
			return nil
		}
	}
	policy := &remote.Policy{
		Store:        rt.store,
		Blobs:        rt.blobs,
		Items:        items,
		Pool:         &rt.remoteHTTP,
		Options:      resolver.Options,
		Key:          key,
		CheckCurrent: checkCurrent(revalidate),
	}
	registryPolicy := &remote.Policy{
		Store: rt.store, Blobs: rt.blobs, Items: items,
		Options: resolver.Options, CheckCurrent: checkCurrent(registryRevalidate), Key: key,
	}
	content := &remote.RegistryContent{
		Policy:  registryPolicy,
		Pool:    &rt.registryHTTP,
		Options: resolver.RegistryOptions,
	}
	content.Install()
	rt.registrySources = &source.Service{
		Resolve:    resolve,
		Revalidate: registryRevalidate,
		HTTP:       content,
		Cache:      content,
	}
	mavenBackend := &remote.MavenBackend{Policy: policy}
	policy.PrepareUpload = func(
		ctx context.Context, q source.Request, permit source.Permit,
		opts remote.Options, upload *item.CacheUpload,
	) error {
		if q.Repo.PackageType == repo.NIMModel {
			nimmodel.PrepareUpload(q, upload)
			return nil
		}
		return mavenBackend.PrepareUpload(ctx, q, permit, opts, upload)
	}
	service := &source.Service{
		Resolve:    resolve,
		Revalidate: revalidate,
		HTTP:       mavenBackend,
		Cache:      mavenBackend,
	}
	rt.mavenEager, err = remote.NewMavenEager(ctx, service, policy, rt.auth, log)
	if err != nil {
		return nil, fmt.Errorf("remote maven companions: %w", err)
	}
	mavenBackend.Eager = rt.mavenEager.Queue
	rt.smartRemote, err = remote.NewSmart(
		ctx,
		policy,
		&rt.remoteHTTP,
		rt.auth,
		"remote.smart",
		log,
	)
	if err != nil {
		return nil, fmt.Errorf("remote smart runtime: %w", err)
	}
	rt.smartRegistry, err = remote.NewSmart(
		ctx,
		registryPolicy,
		&rt.remoteHTTP,
		rt.auth,
		"remote.smart.registry",
		log,
	)
	if err != nil {
		return nil, fmt.Errorf("remote smart runtime: %w", err)
	}
	return service, nil
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
