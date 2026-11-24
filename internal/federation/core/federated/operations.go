package federated

import (
	"context"
	"errors"
	"fmt"
	"strings"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ErrNoRepository reports a key that names no repository.
var ErrNoRepository = fmt.Errorf("repository does not exist: %w", kernel.ErrNotFound)

// Operations are the federation API's repository operations that FED-01
// serves: configSync, migrate, convertToLocal and the member URL rewrite
// (research 40, section 7). Each is for administrators only and runs in one
// transaction; what it sends to members runs from the queue afterwards.
type Operations struct {
	Membership Membership
	// Repositories writes without consumer lifecycles: each operation
	// reconciles membership itself.
	Repositories artifactsapi.Repositories
	// Authorize refuses a caller who is not currently an administrator, as
	// 7.161.26's FederatedRepoResource allows ROLE_ADMIN only.
	Authorize func(ctx context.Context) error
	// Streams is the stream delivery engine, nil until the runtime opens
	// it: full sync, recovery and member removal answer ErrNoStreams
	// without it. A func, since the module hands out operations before the
	// runtime builds the engine.
	Streams func() *streamsync.Engine
}

// Administrator refuses a caller who may not run the operations, as each
// operation does first.
func (o Operations) Administrator(ctx context.Context) error {
	return o.Authorize(ctx)
}

func (o Operations) tx(ctx context.Context, fn func(context.Context) error) error {
	if err := o.Authorize(ctx); err != nil {
		return err
	}
	return o.Membership.Store.Tx(ctx, fn) //nolint:wrapcheck // the operation names its own errors
}

func (o Operations) federated(ctx context.Context, key string) (repo.Config, error) {
	c, err := o.Repositories.Get(ctx, key)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return repo.Config{}, ErrNoRepository
	case err != nil:
		return repo.Config{}, fmt.Errorf("read repository %s: %w", key, err)
	case c.Class != repo.Federated:
		return repo.Config{}, refuse("Repository '%s' is not a federated repository", key)
	}
	return c, nil
}

// ConfigSync queues the repository's configuration to every member
// (POST federation/configSync/{repoKey}), repairing drift a failed send
// left. The members' answers do not change the outcome, as 7.x answers
// "Configuration sync executed successfully" once it has asked.
func (o Operations) ConfigSync(ctx context.Context, key string) error {
	return o.tx(ctx, func(ctx context.Context) error {
		if _, err := o.federated(ctx, key); err != nil {
			return err
		}
		members, err := o.Membership.Store.Federation().Members(ctx, key)
		if err != nil {
			return fmt.Errorf("federated repository members: %w", err)
		}
		return o.Membership.Enqueue(ctx, key, members)
	})
}

// Migrate turns local repository key into a federated repository whose
// only member is this instance; its members arrive in a following
// configuration update (POST federation/migrate/{repoKey}). The federated
// class's rules apply, so a terraformbackend repository is refused with the
// message its federated create gets. A repository that push-replicates is
// refused: decision 0022 forbids one repository being both federated and
// the target of push replication from the same source, which members it
// replicates to would become.
func (o Operations) Migrate(ctx context.Context, key string) error {
	return o.tx(ctx, func(ctx context.Context) error {
		c, err := o.Repositories.Get(ctx, key)
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			return refuse("Repository '%s' does not exist", key)
		case err != nil:
			return fmt.Errorf("read repository %s: %w", key, err)
		case c.Class != repo.Local:
			return refuse("Repository '%s' is not a local repository", key)
		}
		replications, err := o.Membership.Store.Replications().ForRepo(ctx, key)
		if err != nil {
			return fmt.Errorf("replications of %s: %w", key, err)
		}
		if len(replications) > 0 {
			return refuse("Repository '%s' has push replication configured: remove it before converting the repository to federated", key)
		}
		c.Class = repo.Federated
		// A local repository's encryptStates default is not a federated
		// repository's; Defaults sets what the class still lacks.
		c.EncryptStates = nil
		repo.Defaults(&c)
		if err := repo.ValidateStored(&c); err != nil {
			return &RefusalError{Message: err.Error()}
		}
		return o.write(ctx, c)
	})
}

func (o Operations) write(ctx context.Context, c repo.Config) error {
	if err := o.Membership.Admit(ctx, &c); err != nil {
		return err
	}
	if err := o.Repositories.Update(ctx, c); err != nil {
		return fmt.Errorf("update repository %s: %w", c.Key, err)
	}
	return o.Membership.Reconcile(ctx, c, true)
}

// ConvertToLocal turns each federated repository of keys back into a local
// repository, keeping its content and dropping its members (POST
// federation/convertToLocal). As 7.x documents, a repository whose
// federation still has another enabled member is not converted. It returns
// the keys converted and, by key, why each other one was not; each
// conversion commits on its own, as 7.x converts what it can.
func (o Operations) ConvertToLocal(ctx context.Context, keys []string) ([]string, map[string]string, error) {
	if err := o.Authorize(ctx); err != nil {
		return nil, nil, err
	}
	if len(keys) == 0 {
		return nil, nil, refuse("A list of federated repository keys is required")
	}
	var converted []string
	failed := map[string]string{}
	for _, key := range keys {
		err := o.Membership.Store.Tx(ctx, func(ctx context.Context) error {
			c, err := o.federated(ctx, key)
			if err != nil {
				return err
			}
			members, err := o.Membership.Store.Federation().Members(ctx, key)
			if err != nil {
				return fmt.Errorf("federated repository members: %w", err)
			}
			for _, m := range members {
				if m.Enabled {
					return refuse("Repository '%s' is federated with the enabled member '%s': remove it first",
						key, repo.MemberURL(m.BaseURL, m.RemoteRepoKey))
				}
			}
			c.Class, c.Members, c.Proxy, c.DisableProxy = repo.Local, nil, "", nil
			repo.Defaults(&c)
			if err := o.Repositories.Update(ctx, c); err != nil {
				return fmt.Errorf("update repository %s: %w", key, err)
			}
			return o.Membership.Clear(ctx, key)
		})
		var refusal *RefusalError
		switch {
		case err == nil:
			converted = append(converted, key)
		case errors.Is(err, ErrNoRepository):
			failed[key] = fmt.Sprintf("Repository '%s' does not exist", key)
		case errors.As(err, &refusal):
			failed[key] = refusal.Message
		default:
			return converted, failed, err //nolint:wrapcheck // the conversion names what failed
		}
	}
	return converted, failed, nil
}

// MsgReplaceURL is 7.x's answer to a member URL rewrite
// (FederatedReplaceUrlResource.java:137-140).
const MsgReplaceURL = "Binding URL from URL %s to new %s finished."

// ReplaceURL rewrites, on every federated repository, each member whose
// base URL is oldBase to newBase, keeping its repository key (POST
// federation/replaceUrl and federation/updateMemberUrl, research 40
// section 7, [I]). The rewritten members are checked as any member list
// is, so newBase must belong to a paired peer, and every rewritten
// repository's configuration is sent to its members.
func (o Operations) ReplaceURL(ctx context.Context, oldBase, newBase string) error {
	from, to := repo.NormalizeMemberBase(oldBase), repo.NormalizeMemberBase(newBase)
	return o.tx(ctx, func(ctx context.Context) error {
		// Checked after the caller is, so only an administrator learns
		// what the body lacks.
		if strings.TrimSpace(oldBase) == "" || strings.TrimSpace(newBase) == "" {
			return refuse("oldBaseUrl and newBaseUrl are required")
		}
		all, err := o.Repositories.List(ctx, artifactsapi.RepoFilter{})
		if err != nil {
			return fmt.Errorf("list repositories: %w", err)
		}
		for _, c := range all {
			if c.Class != repo.Federated {
				continue
			}
			changed := false
			members := make([]repo.Member, len(c.Members))
			copy(members, c.Members)
			for i, m := range members {
				base, key, err := model.ParseMemberURL(m.URL)
				if err == nil && base == from {
					members[i].URL, changed = repo.MemberURL(to, key), true
				}
			}
			if !changed {
				continue
			}
			c.Members = members
			if err := o.write(ctx, c); err != nil {
				return err
			}
		}
		return nil
	})
}
