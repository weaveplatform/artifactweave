package itemkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	// serves. later are those 7.x reindexes that artifactweave serves only
	// from P9 (remote, a remote's cache included) and P10 (virtual), which
	// answer 501 until then (decision 0032). Any other class, or another
	// package type, answers 400 with refused, a format taking the key.
	Classes, later []repo.Class
	Refused        string
	// Answer is the 200's text, a format taking the key.
	Answer string
}

// Hosted are the classes whose own items a reindex reads. A federated
// repository is a local one that federates. [I] Of the reindex pages only
// Helm's names it; RTFACT-27144 says the REST reindex reaches it (#216).
var Hosted = []repo.Class{repo.Local, repo.Federated}

// ReindexOps are the operations by package type, each from 7.x's REST
// reference: the archived pages for the permission, the classes and the
// answer, and the current ones for the classes. [I] The order of the checks
// and the text of each refusal are inferred (#216).
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
	repo.Bower: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local bower repository",
		Answer:  "Reindex of bower repo %s was scheduled to run.",
	},
	repo.Cargo: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local cargo repository",
		Answer:  "Reindex of cargo repo %s was scheduled to run.",
	},
	repo.CocoaPods: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local CocoaPods repository",
		Answer:  "Reindex of CocoaPods repo %s was scheduled to run.",
	},
	repo.Pub: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local pub repository",
		Answer:  "Reindex of pub repo %s was scheduled to run.",
	},
	repo.Conan: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local Conan repository",
		Answer:  "Reindex of Conan repo %s was scheduled to run.",
	},
	repo.Cran: {
		Classes: []repo.Class{repo.Local},
		Refused: "Repository '%s' is not a local CRAN repository",
		Answer:  "Recalculating index for CRAN repository %s scheduled to run",
	},
	repo.Alpine: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local alpine repository",
		Answer:  "Reindex of alpine repo %s was scheduled to run.",
	},
	repo.Gems: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local RubyGems repository",
		Answer:  "Reindex of RubyGems repo %s was scheduled to run.",
	},
	// Local Debian metadata calculation is synchronous unless async=true;
	// writeProps=0 has its own work kind so it cannot annotate packages.
	repo.Debian: {
		Classes: []repo.Class{repo.Local},
		Refused: "Repository '%s' is not a local Debian repository",
		Answer:  "Debian repository %s metadata recalculation accepted",
	},
	repo.RPM: {
		Classes: []repo.Class{repo.Local, repo.Federated, repo.Virtual},
		later:   []repo.Class{repo.Remote},
		Refused: "Repository '%s' is not a local RPM repository",
		Answer:  "Reindex of RPM repo %s was scheduled to run.",
	},
	repo.Opkg: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local opkg repository",
		Answer:  "Reindex of opkg repo %s was scheduled to run.",
	},
	repo.Conda: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local conda repository",
		Answer:  "Conda repository %s metadata recalculation accepted",
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
		Classes: []repo.Class{repo.Local, repo.Federated, repo.Remote},
		later:   []repo.Class{repo.Virtual},
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
	repo.Swift: {
		Classes: Hosted,
		Refused: "Repository '%s' is not a local swift repository",
		Answer:  "Reindex of swift repo %s was scheduled to run.",
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
	if t == repo.NuGet && c.Class == repo.Remote {
		err = s.queueRemoteReindex(ctx, st, c, key)
	} else {
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
	return fmt.Sprintf(op.Answer, key), nil
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

// queueRemoteReindex locks the source lifetime before refreshing the caller and
// checking Manage again. The accepted payload and its source are one commit.
func (s *Kit) queueRemoteReindex(
	ctx context.Context,
	st Store,
	c repo.Config,
	requested string,
) error {
	err := st.Tx(
		ctx,
		func(ctx context.Context) error {
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
