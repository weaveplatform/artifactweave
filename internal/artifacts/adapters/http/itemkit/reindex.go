package itemkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// ReindexOp is how 7.x serves one package type's reindex operation.
type ReindexOp struct {
	// isAdminOnly: only an administrator may ask. Otherwise Manage on the
	// repository is enough.
	IsAdminOnly bool
	// Classes are the repository classes 7.x reindexes and artifactweave
	// serves. later are those 7.x reindexes that artifactweave does not
	// reindex yet (a remote's cache included), which answer 501 (decision
	// 0032). Any other class, or another package type, answers 400 with
	// refused, a format taking the key.
	Classes, later []repo.Class
	Refused        string
	// Answer is the 200's text, a format taking the key, or the text
	// itself when it names no repository, as RubyGems' OK.
	Answer string
	// RemoteExpires are the paths of a remote repository's cached upstream
	// metadata that its reindex expires, so that the next read fetches them
	// again. Such a reindex queues no work: it is done before the answer.
	RemoteExpires []string
}

// Text is op's answer for the repository key.
func (op ReindexOp) Text(key string) string {
	if !strings.Contains(op.Answer, "%s") {
		return op.Answer
	}
	return fmt.Sprintf(op.Answer, key)
}

// Hosted are the classes whose own items a reindex reads. A federated
// repository is a local one that federates. [I] Of the reindex pages only
// Helm's names it; RTFACT-27144 says the REST reindex reaches it (#216).
var Hosted = []repo.Class{repo.Local, repo.Federated}

// ReindexOps are the operations by package type, each from 7.x's REST
// reference: the archived pages for the permission, the classes and the
// answer, and the current ones for the classes and the timing. Where the
// two disagree the current page wins. An archived sample answers one call,
// with or without async; the other's answer is the same [I]. [I] The order
// of the checks and the text of each refusal are inferred (#216).
var ReindexOps = map[repo.PackageType]ReindexOp{
	repo.Chef: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a hosted Chef repository",
		Answer:  "Reindex of Chef repo %s completed.",
	},
	repo.Puppet: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a hosted Puppet repository",
		Answer:  "Reindex of Puppet repo %s completed.",
	},
	// Manage ("From version 4.8 only requires the set of permissions
	// assumed by Manage") and the sample answer: the archived
	// jfrog-rest-apis/calculate-bower-index (copy of 2025-10-17).
	repo.Bower: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local bower repository",
		Answer:  "Bower index for refresh for %s has been accepted",
	},
	// "Requires a valid admin user" and the sample answer, to a call
	// without async: the archived
	// jfrog-rest-apis/calculate-cargo-repository-metadata (copy of
	// 2026-01-18). The current page states neither.
	//
	// The current page supports local and remote repositories. On a remote
	// one 7.x re-creates config.json and config-original.json, the
	// upstream's (RTFACT-29403, RTFACT-29679). This server derives
	// config.json from the repository's settings on every read, so the
	// reindex expires the cached upstream config.json, which the next read
	// fetches again. It leaves the cached index files and crates as they are
	// [I] (#216).
	repo.Cargo: {
		IsAdminOnly:   true,
		Classes:       []repo.Class{repo.Local, repo.Federated, repo.Remote},
		Refused:       "Repository '%s' is not a local cargo repository",
		Answer:        "Recalculating index for Cargo repository %s scheduled to run.",
		RemoteExpires: []string{"index/config.json"},
	},
	// "Requires a valid admin user" and the sample answer: the archived
	// jfrog-rest-apis/calculate-cocoapods-index (copy of 2026-02-07). The
	// sample's request names cocoapods-local and its answer cocoa-local;
	// the answer names the repository asked for [I].
	repo.CocoaPods: {
		IsAdminOnly: true,
		Classes:     Hosted,
		Refused:     "Repository '%s' is not a local CocoaPods repository",
		Answer:      "Reindexing of CocoaPods repository %s was scheduled to run.",
	},
	// "Requires a valid admin user": the archived
	// jfrog-rest-apis/calculate-pub-repository-metadata (copy of
	// 2026-02-09), whose sample shows no answer, so the text is ours [I].
	// The current page supports remote repositories too, but no page,
	// release note or issue says what a remote one rebuilds, so a remote
	// repository and its cache answer 501 [I] (#216).
	repo.Pub: {
		IsAdminOnly: true,
		Classes:     Hosted,
		later:       []repo.Class{repo.Remote},
		Refused:     "Repository '%s' is not a local pub repository",
		Answer:      "Reindex of pub repo %s was scheduled to run.",
	},
	// "Requires a valid admin user" and the sample answer, which is that of
	// the default, synchronous call: the archived 7.x reference
	// (jfrog-rest-apis/calculate-conan-repository-metadata, copies of
	// 2023-06-04 to 2026-01-13). The current page, of 2026-11-12, states
	// neither.
	repo.Conan: {
		IsAdminOnly: true,
		Classes:     Hosted,
		Refused:     "Repository '%s' is not a local Conan repository",
		Answer:      "Recalculating index for Conan repository %s scheduled to run.",
	},
	// Manage and the sample answer, with its closing full stop: the archived
	// jfrog-rest-apis/calculate-cran-repository-metadata (copy of
	// 2026-03-13), as the REST reference of 2023 printed it too.
	repo.Cran: {
		Classes: []repo.Class{repo.Local},
		Refused: "Repository '%s' is not a local CRAN repository",
		Answer:  "Recalculating index for CRAN repository %s scheduled to run.",
	},
	// "A valid admin user or a user with Manage permissions" and the sample
	// answer: the archived jfrog-rest-apis/calculate-alpine-repository-metadata
	// (copy of 2025-12-15). The sample has two spaces after "of", where the
	// other "Reindexing of" answers have one; one is kept [I].
	repo.Alpine: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local alpine repository",
		Answer:  "Reindexing of Alpine repository %s was scheduled to run.",
	},
	// "Manage, Delete/ Overwrite, Deploy/ Cache, Annotate, Read", the set
	// Manage assumes, and the sample answer: the archived
	// jfrog-rest-apis/calculate-rubygems-index (copy of 2025-11-07).
	repo.Gems: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local RubyGems repository",
		Answer:  "OK",
	},
	// Manage and the sample answer, to a call without async: the archived
	// jfrog-rest-apis/calculate-debian-repository-metadata (copy of
	// 2026-01-18). Local metadata calculation is synchronous unless
	// async=true; writeProps=0 has its own work kind so it cannot annotate
	// packages.
	repo.Debian: {
		Classes: []repo.Class{repo.Local},
		Refused: "Repository '%s' is not a local Debian repository",
		Answer:  "Recalculating index for Debian repository %s scheduled to run.",
	},
	// Manage and the sample answer, to a local call with async=1: the
	// archived jfrog-rest-apis/calculate-yum-repository-metadata (copy of
	// 2025-11-08).
	repo.RPM: {
		Classes: []repo.Class{repo.Local, repo.Federated, repo.Virtual},
		later:   []repo.Class{repo.Remote},
		Refused: "Repository '%s' is not a local RPM repository",
		Answer:  "YUM metadata calculation for repository '%s' accepted.",
	},
	// Manage and the sample answer, to a call without async: the archived
	// jfrog-rest-apis/calculate-opkg-repository-metadata (copy of
	// 2026-02-15).
	repo.Opkg: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local opkg repository",
		Answer:  "Recalculating index for Opkg repository %s scheduled to run.",
	},
	// "Requires a valid admin user" and the sample answer, to a call
	// without async: the archived
	// jfrog-rest-apis/calculate-conda-repository-metadata (copy of
	// 2025-11-18). The current page states neither.
	repo.Conda: {
		IsAdminOnly: true,
		Classes:     Hosted,
		Refused:     "Repository '%s' is not a local conda repository",
		Answer:      "Recalculating index for Conda repository %s scheduled to run.",
	},
	// Manage, local repositories only, scheduled, and 7.x's answer word for
	// word (decision 0034; KB 000005641). Federated ones answer 400, as #293
	// left them.
	repo.NPM: {
		Classes: []repo.Class{repo.Local},
		Refused: "Repository '%s' is not a local npm repository",
		Answer:  "Recalculating index for npm repository %s scheduled to run",
	},
	// "Requires a user with Manage permissions", "Supported for local
	// repositories only", synchronous unless async is set, and the sample
	// answer. The text of a synchronous run's answer is the same [I].
	repo.PyPI: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local pypi repository",
		Answer:  "Recalculating index for pypi repository %s scheduled to run",
	},
	// "From version 4.8 only requires the set of permissions assumed by
	// Manage", "Supported by local, local-cache, remote and virtual
	// repositories", and the sample answer, which accepts it to run later.
	repo.NuGet: {
		Classes: []repo.Class{repo.Local, repo.Federated, repo.Remote, repo.Virtual},
		Refused: "Repository '%s' is not a NuGet repository",
		Answer:  "NuGet reindex calculation for repository '%s' accepted.",
	},
	// "Requires the set of permissions assumed by Manage", "local and
	// Federated repositories only". The archived sample shows only OK; the
	// text is what Artifactory answered the helm-push-artifactory plugin in
	// 2018, as its README prints it [I].
	repo.Helm: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local helm repository",
		Answer:  "Reindex of helm repo %s was scheduled to run.",
	},
	// "Requires a valid admin user" and the sample answer: the archived
	// jfrog-rest-apis/calculate-swift-index (copy of 2025-12-17).
	repo.Swift: {
		IsAdminOnly: true,
		Classes:     Hosted,
		Refused:     "Repository '%s' is not a local swift repository",
		Answer:      "Reindexing of Swift repository %s was scheduled to run.",
	},
	// "Requires a valid admin user", a local repository (the Terraform
	// page: "re-indexing of a local Terraform repository"), and the sample
	// answer.
	repo.Terraform: {
		IsAdminOnly: true,
		Classes:     Hosted,
		Refused:     "Repository '%s' is not a local terraform repository",
		Answer:      "Reindexing of Terraform repository %s was scheduled to run.",
	},
}

// Reindex queues the reindex operation of package type t on the repository
// key names, or on path in it when path is not empty, for its caller. It
// answers 7.x's text once the work is queued, or once it has run when wait.
// A remote repository whose operation names RemoteExpires has that metadata
// expired before the answer, whatever wait says.
func (s *Kit) Reindex(
	ctx context.Context,
	t repo.PackageType,
	key, path string,
	wait bool,
) (string, error) {
	return s.ReindexAs(ctx, t, key, path, wait, format.KindReindex)
}

// ReindexAs is Reindex with the work kind to queue, for the operations
// that queue another kind than format.KindReindex.
func (s *Kit) ReindexAs(
	ctx context.Context,
	t repo.PackageType,
	key, path string,
	wait bool,
	kind string,
) (string, error) {
	op, ok := ReindexOps[t]
	if _, indexed := s.Format(t).(format.Reindexer); !ok || !indexed {
		return "", apierror.ErrNotImplemented
	}
	st, c, by, err := s.OperationRepo(ctx, t, op, key)
	if err != nil {
		return "", err
	}
	workKey := c.Key
	if path != "" {
		kind, workKey = format.KindReindexPath, c.Key+"/"+path
		// [I] 7.x's answer to an overlong path is unknown (#216).
		if len(workKey) > work.MaxWorkKey {
			return "", httpkit.BadRequestf("The path '%s' is too long to reindex", path)
		}
		// Decision 0039: a mistyped partial-reindex path must not claim
		// that useful work was scheduled. This status is inferred (#216).
		if t == repo.Helm {
			if _, err := st.Items().Get(ctx, c.Key, false, path); err != nil {
				if errors.Is(err, kernel.ErrNotFound) {
					return "", apierror.New(http.StatusNotFound, "Could not find resource")
				}
				return "", fmt.Errorf("helm: reindex path %s: %w", path, err)
			}
		}
	}
	switch {
	case t == repo.NuGet && c.Class == repo.Virtual:
		// A virtual owns no package index. Accept after the existing Manage
		// gate without scheduling work in any concrete member [I, #216].
		return op.Text(key), nil
	case c.Class == repo.Remote && op.RemoteExpires != nil:
		if err = s.expireRemote(ctx, st, c, op.RemoteExpires); err != nil {
			return "", err
		}
		return op.Text(key), nil
	case t == repo.NuGet && c.Class == repo.Remote:
		err = s.queueRemoteReindex(ctx, st, c, key)
	default:
		err = s.Enqueue(ctx, st, kind, workKey, by)
	}
	if err != nil {
		return "", err
	}
	if wait {
		if err := s.Await(ctx, st, kind, workKey); err != nil {
			return "", s.ItemError(ctx, "reindex", err)
		}
	}
	return op.Text(key), nil
}

// OperationRepo resolves the repository a JFrog operation of package type
// t names and checks it as op says: 404 when there is none, 401 or 403 to a
// caller without the right, 400 for a repository op refuses, and 501 for
// one it serves later. It returns the caller's name.
func (s *Kit) OperationRepo(
	ctx context.Context,
	t repo.PackageType,
	op ReindexOp,
	key string,
) (Store, repo.Config, string, error) {
	st, err := s.Content()
	if err != nil {
		return nil, repo.Config{}, "", err
	}
	c, cached, err := ResolveRepo(ctx, st, key)
	if err != nil {
		return nil, c, "", s.ItemError(ctx, "resolve repository", err)
	}
	p, err := httpkit.Principal(ctx)
	if err != nil {
		return nil, c, "", err
	}
	if op.IsAdminOnly && !p.Admin {
		return nil, c, "", httpkit.Denied(p)
	}
	if !op.IsAdminOnly {
		d, _ := accessapi.DeciderFrom(ctx)
		acc, err := AccessFor(ctx, s.Access.Authorizer, p, d)
		if err != nil {
			return nil, c, "", s.InternalError(ctx, "operation rights", err)
		}
		if !acc.File(c, "", accessapi.ActionManage) {
			return nil, c, "", httpkit.Denied(p)
		}
	}
	class := c.Class
	if cached {
		class = repo.Remote
	}
	switch {
	case c.PackageType == t && slices.Contains(op.later, class):
		return nil, c, "", apierror.New(http.StatusNotImplemented, fmt.Sprintf(
			"'%s' is a %s repository, which artifactweave does not reindex yet", key, class))
	case c.PackageType != t || !slices.Contains(op.Classes, class):
		return nil, c, "", httpkit.BadRequestf(op.Refused, key)
	}
	return st, c, p.Name, nil
}

// Enqueue queues kind's work on key, asked for by by, through the runtime's
// queue, which wakes its workers; a server without one, in tests, leaves
// the item in the store for whichever queue polls it.
func (s *Kit) Enqueue(
	ctx context.Context,
	st Store,
	kind, key, by string,
) error {
	if err := s.Queue(ctx, st, kind, key, by); err != nil {
		return s.InternalError(ctx, "queue "+kind, err)
	}
	return nil
}

// Queue is enqueue's queueing, which joins ctx's transaction, for a caller
// that reports the failure itself.
func (s *Kit) Queue(ctx context.Context, st Store, kind, key, by string) error {
	return s.queueRequest(ctx, st, kind, key, format.WorkRequest{By: by})
}

func (s *Kit) queueRequest(
	ctx context.Context,
	st Store,
	kind, key string,
	req format.WorkRequest,
) error {
	// The credential reference is internal JSON, never client input.
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", kind, err)
	}
	if s.Work != nil {
		err = s.Work.Enqueue(ctx, kind, key, payload)
	} else {
		err = st.WorkQueue().Enqueue(ctx, kind, key, payload, time.Now())
	}
	if err != nil {
		return fmt.Errorf("queue %s of %s: %w", kind, key, err)
	}
	return nil
}

// expireRemote expires the cached upstream metadata at paths in c's remote
// cache, as zap does, so that the next read revalidates it upstream.
func (s *Kit) expireRemote(ctx context.Context, st Store, c repo.Config, paths []string) error {
	err := st.Tx(ctx, func(ctx context.Context) error {
		for _, p := range paths {
			if _, err := st.RemoteCache().Invalidate(ctx, c.Key, p, true); err != nil {
				return fmt.Errorf("expire %s: %w", p, err)
			}
		}
		return nil
	})
	if err != nil {
		return s.InternalError(ctx, "remote reindex", err)
	}
	return nil
}

// queueRemoteReindex takes current authority before the source lifetime lock,
// refreshes the caller and checks Manage again. The payload and source commit together.
func (s *Kit) queueRemoteReindex(
	ctx context.Context,
	st Store,
	c repo.Config,
	requested string,
) error {
	err := st.Tx(
		ctx,
		func(ctx context.Context) error {
			if s.Access.Projects != nil {
				if err := s.Access.Projects.Guard(ctx); err != nil {
					return s.InternalError(ctx, "reindex authority", err)
				}
			}
			identity, err := st.RemoteCache().Source(ctx, c.Key)
			if err != nil {
				return s.InternalError(ctx, "reindex source", err)
			}
			p, err := httpkit.Principal(ctx)
			if err != nil {
				return err
			}
			if s.Access.Authenticator == nil {
				return httpkit.Denied(p)
			}
			current, err := s.Access.Authenticator.Current(ctx, p)
			if err != nil {
				return httpkit.Denied(p)
			}
			ctx = kernel.WithPrincipal(ctx, current)
			_, latest, by, err := s.OperationRepo(
				ctx,
				repo.NuGet,
				ReindexOps[repo.NuGet],
				requested,
			)
			if err != nil {
				return err
			}
			if latest.Class != repo.Remote || latest.Key != c.Key {
				return s.ItemError(ctx, "reindex repository changed", kernel.ErrConflict)
			}
			reference, _ := json.Marshal(current.CredentialReference())
			if err = s.queueRequest(
				ctx,
				st,
				format.KindReindex,
				c.Key,
				format.WorkRequest{
					By:         by,
					Source:     &identity,
					Credential: reference,
				},
			); err != nil {
				return s.InternalError(ctx, "queue remote reindex", err)
			}
			return nil
		},
	)
	if err != nil {
		return fmt.Errorf("remote reindex scheduling: %w", err)
	}
	return nil
}

// Await waits for the work of kind on key queued just now: it runs it here,
// or waits for the worker already running it, as a read of derived work
// does. A server without the runtime's queue, in tests, runs it through a
// queue of its own.
func (s *Kit) Await(ctx context.Context, st Store, kind, key string) error {
	q := s.Work
	if q == nil {
		var err error
		if q, err = work.New(st, work.Options{}); err == nil {
			err = s.Items(st).RegisterWork(q)
		}
		if err != nil {
			return fmt.Errorf("work queue: %w", err)
		}
	}
	if err := q.EnsureFresh(ctx, kind, key); err != nil {
		return fmt.Errorf("%s of %s: %w", kind, key, err)
	}
	return nil
}
