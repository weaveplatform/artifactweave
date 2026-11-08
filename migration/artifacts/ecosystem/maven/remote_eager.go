package maven

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

	"github.com/weaveplatform/artifactweave/internal/source"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// MavenCompanionKind is the persistent work-queue kind of a companion
// fetch (decision 0033), and the name of the lease that serializes their
// admission. It is stored with every queued job, so it never changes: a
// renamed kind would strand the jobs already queued.
const MavenCompanionKind = "maven.remote-companion"

type mavenJob struct {
	Repo      string                     `json:"repo"`
	Path      string                     `json:"path"`
	Origin    string                     `json:"origin"`
	Identity  types.SourceIdentity       `json:"identity"`
	Revision  string                     `json:"revision"`
	Principal kernel.CredentialReference `json:"principal"`
}

// MavenEager persists deduplicated companion work without retaining credentials.
// Two workers share the regular source admission limits. Pending/leased/parked
// jobs together are bounded to 256; failures park after three attempts. Shutdown
// returns interrupted leases so another process can safely resume them.
type MavenEager struct {
	service *source.Service
	policy  *remote.Policy
	authn   accessapi.Authenticator
	authz   accessapi.Authorizer
	queue   *work.Queue
	cancel  context.CancelFunc
	workers sync.WaitGroup
}

// NewMavenEager starts two workers that fetch, through service and under
// policy, the companions that Queue records, on a work queue over the
// policy's store. A job runs as the principal whose read queued it,
// rechecked when it runs: authn's Current must still accept that
// principal's credential and authz must still grant it, or the job fails
// permanently with source.ErrDenied, as does one whose repository no
// longer fetches it (offline, blacked out or not caching). A job whose
// repository is gone, or whose source or credential revision changed,
// fails permanently with source.ErrSuperseded; any other failure is
// retried, three attempts in all, before the job is parked. Queue admits
// no job while 256 are pending, leased or parked. NewMavenEager returns an
// error when the queue or its handler cannot be set up.
func NewMavenEager(
	ctx context.Context,
	service *source.Service,
	policy *remote.Policy,
	authn accessapi.Authenticator,
	authz accessapi.Authorizer,
	log *slog.Logger,
) (*MavenEager, error) {
	q, err := work.New(policy.Store, work.Options{Workers: 2, MaxAttempts: 3, Log: log})
	if err != nil {
		return nil, fmt.Errorf("maven companion queue: %w", err)
	}
	e := &MavenEager{service: service, policy: policy, authn: authn, authz: authz, queue: q}
	if err = q.Handle(MavenCompanionKind, e.run); err != nil {
		return nil, fmt.Errorf("maven companion handler: %w", err)
	}
	loop, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.cancel = cancel
	e.workers.Go(func() { _ = q.Run(loop) })
	return e, nil
}

// Close stops the workers and returns once every run has settled; the
// leases of runs it cuts short are handed back, so another process can
// resume those jobs. A nil MavenEager closes as a no-op.
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

// Queue records, at most once, the companion of the file q read: the .jar
// of a .pom when the repository fetches jars eagerly, and the -sources.jar
// of a .jar when it fetches sources eagerly. The job holds the caller's
// credential reference and the credential revision, never a credential.
// Queue does nothing inside a companion job (remote.EagerKey set), for a
// file with no companion or a caller with no principal, when the policy's
// options or its Check fail, or when 256 jobs are pending, leased or
// parked; a failure to record the job is not reported, since the read it
// follows has already succeeded.
func (e *MavenEager) Queue(ctx context.Context, q source.Request, permit source.Permit) {
	if ctx.Value(remote.EagerKey{}) != nil {
		return
	}
	target := mavenCompanion(q.Repo, q.Resource.Path)
	principal, ok := kernel.FromContext(ctx)
	if target == "" || !ok {
		return
	}
	opts, err := e.policy.Options(ctx, q.Repo)
	if err != nil || e.policy.Check(ctx, q, permit, opts.Revision) != nil {
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
		context.WithValue(ctx, remote.EagerKey{}, job.Revision),
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
	p, err := e.authn.Current(ctx, job.Principal.Principal())
	if err != nil {
		return errors.Join(work.ErrPermanent, source.ErrDenied)
	}
	p, d, err := e.authz.Authorize(ctx, p, p.Anonymous)
	if err != nil {
		return errors.Join(work.ErrPermanent, source.ErrDenied)
	}
	ctx = accessapi.WithCaller(ctx, p, d)
	result, err := e.service.Read(
		ctx,
		source.Request{
			Repo:    c,
			Subject: accessapi.SubjectOf(p),
			Access:  d,
			Project: c.ProjectKey,
			Resource: source.Resource{
				Kind:   source.ResourceKindArtifact,
				Path:   job.Path,
				Method: "GET",
			},
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
