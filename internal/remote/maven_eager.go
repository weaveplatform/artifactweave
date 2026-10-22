package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const MavenCompanionKind = "maven.remote-companion"

type (
	eagerKey struct{}
	mavenJob struct {
		Repo      string                   `json:"repo"`
		Path      string                   `json:"path"`
		Origin    string                   `json:"origin"`
		Identity  types.SourceIdentity     `json:"identity"`
		Revision  string                   `json:"revision"`
		Principal auth.CredentialReference `json:"principal"`
	}
)

// MavenEager persists deduplicated companion work without retaining credentials.
// Two workers share the regular source admission limits. Pending/leased/parked
// jobs together are bounded to 256; failures park after three attempts. Shutdown
// returns interrupted leases so another process can safely resume them.
type MavenEager struct {
	service *source.Service
	policy  *Policy
	auth    *auth.Authenticator
	queue   *work.Queue
	cancel  context.CancelFunc
	workers sync.WaitGroup
}

func NewMavenEager(
	ctx context.Context,
	service *source.Service,
	policy *Policy,
	authenticator *auth.Authenticator,
	log *slog.Logger,
) (*MavenEager, error) {
	q, err := work.New(policy.Store, work.Options{Workers: 2, MaxAttempts: 3, Log: log})
	if err != nil {
		return nil, fmt.Errorf("maven companion queue: %w", err)
	}
	e := &MavenEager{service: service, policy: policy, auth: authenticator, queue: q}
	if err = q.Handle(MavenCompanionKind, e.run); err != nil {
		return nil, fmt.Errorf("maven companion handler: %w", err)
	}
	loop, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.cancel = cancel
	e.workers.Go(func() { _ = q.Run(loop) })
	return e, nil
}

func (e *MavenEager) Close() {
	if e != nil {
		e.cancel()
		e.workers.Wait()
	}
}

func mavenCompanion(c repo.Config, p string) string {
	switch {
	case strings.HasSuffix(p, ".pom") && enabled(c.FetchJarsEagerly):
		return strings.TrimSuffix(p, ".pom") + ".jar"
	case strings.HasSuffix(p, ".jar") && enabled(c.FetchSourcesEagerly) && !strings.HasSuffix(p, "-sources.jar") && !strings.HasSuffix(p, "-javadoc.jar"):
		return strings.TrimSuffix(p, ".jar") + "-sources.jar"
	}
	return ""
}

func (e *MavenEager) Queue(ctx context.Context, q source.Request, permit source.Permit) {
	if ctx.Value(eagerKey{}) != nil {
		return
	}
	target := mavenCompanion(q.Repo, q.Resource.Path)
	principal, ok := auth.FromContext(ctx)
	if target == "" || !ok {
		return
	}
	opts, err := e.policy.Options(ctx, q.Repo)
	if err != nil || e.policy.check(ctx, q, permit, opts.Revision) != nil {
		return
	}
	job := mavenJob{
		Repo:      q.Repo.Key,
		Path:      target,
		Origin:    q.Resource.Path,
		Identity:  permit.Identity,
		Revision:  opts.Revision,
		Principal: principal.CredentialReference(),
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return
	}
	digest := sha256.Sum256(payload)
	key := q.Repo.Key + "/" + hex.EncodeToString(digest[:])
	// Serialize admission on all store backends, so concurrent foreground reads
	// cannot turn a bounded background queue into unbounded durable work.
	_ = e.policy.Store.Tx(ctx, func(tx context.Context) error {
		now := time.Now().UTC()
		holder := rand.Text()
		held, err := e.policy.Store.Leases().
			Acquire(tx, types.Lease{Name: MavenCompanionKind, Holder: holder, Acquired: now, Expires: now.Add(time.Minute)}, now)
		if err != nil {
			return fmt.Errorf("companion admission lease: %w", err)
		}
		if !held {
			return nil
		}
		defer func() { _ = e.policy.Store.Leases().Release(tx, MavenCompanionKind, holder) }()
		if _, err = e.policy.Store.WorkQueue().Get(tx, MavenCompanionKind, key); err == nil {
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("companion lookup: %w", err)
		}
		stats, err := e.policy.Store.WorkQueue().Stats(tx)
		if err != nil {
			return fmt.Errorf("companion capacity: %w", err)
		}
		for _, stat := range stats {
			if stat.Kind == MavenCompanionKind && stat.Pending+stat.Leased+stat.Parked >= 256 {
				return nil
			}
		}
		return e.queue.Enqueue(tx, MavenCompanionKind, key, payload)
	})
}

func (e *MavenEager) run(ctx context.Context, it types.WorkItem) error {
	var job mavenJob
	if json.Unmarshal(it.Payload, &job) != nil {
		return errors.Join(work.ErrPermanent, source.ErrInvalidRequest)
	}
	ctx, cancel := context.WithTimeout(
		context.WithValue(ctx, eagerKey{}, job.Revision),
		time.Minute,
	)
	defer cancel()
	c, err := e.policy.Store.Repos().Get(ctx, job.Repo)
	if err != nil {
		return errors.Join(work.ErrPermanent, source.ErrSuperseded)
	}
	identity, err := e.policy.Store.RemoteCache().Source(ctx, job.Repo)
	if err != nil || identity != job.Identity {
		return errors.Join(work.ErrPermanent, source.ErrSuperseded)
	}
	if c.Class != repo.Remote || enabled(c.Offline) || enabled(c.BlackedOut) ||
		c.StoreArtifactsLocally != nil && !*c.StoreArtifactsLocally ||
		mavenCompanion(c, job.Origin) != job.Path {
		return errors.Join(work.ErrPermanent, source.ErrDenied)
	}
	opts, err := e.policy.Options(ctx, c)
	if err != nil || opts.Revision != job.Revision {
		return errors.Join(work.ErrPermanent, source.ErrSuperseded)
	}
	p, err := e.auth.Current(ctx, job.Principal.Principal(), e.policy.Store.Tokens())
	if err != nil {
		return errors.Join(work.ErrPermanent, source.ErrDenied)
	}
	p, err = auth.Authorize(
		ctx,
		authz.Sources{
			Permissions:      e.policy.Store.Permissions(),
			Groups:           e.policy.Store.Groups(),
			AnonymousEnabled: p.Anonymous,
		},
		p,
	)
	if err != nil {
		return errors.Join(work.ErrPermanent, source.ErrDenied)
	}
	ctx = auth.WithPrincipal(ctx, p)
	result, err := e.service.Read(
		ctx,
		source.Request{
			Repo:     c,
			Subject:  p.Subject(),
			Access:   p.Access(),
			Project:  c.ProjectKey,
			Resource: source.Resource{Kind: source.Artifact, Path: job.Path, Method: "GET"},
		},
	)
	if err != nil {
		if errors.Is(err, source.ErrDenied) || errors.Is(err, source.ErrSuperseded) ||
			errors.Is(err, source.ErrInvalidRequest) {
			return errors.Join(work.ErrPermanent, err)
		}
		return fmt.Errorf("maven companion retrieval: %w", err)
	}
	if result.Stream != nil {
		defer result.Stream.Body.Close()
		if _, err = io.Copy(io.Discard, result.Stream.Body); err != nil {
			return fmt.Errorf("maven companion body: %w", err)
		}
	}
	return nil
}

// The stored credential revision also fences the interval between restoring a
// job and the source backend acquiring its own transport options.
func mavenCompanionRevision(ctx context.Context, revision string) bool {
	expected, ok := ctx.Value(eagerKey{}).(string)
	return !ok || expected == revision
}
