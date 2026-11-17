package virtual

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// Repos is where Members and Walk read a member's configuration by key. A
// key no repository has is kernel.ErrNotFound. port.RepoStore satisfies it,
// and so does Configs.
type Repos interface {
	Get(ctx context.Context, key string) (repo.Config, error)
}

// Configs is a Repos over configurations already read, by key, for callers
// that hold every repository's configuration for the whole request.
type Configs map[string]repo.Config

// Get returns the configuration of key, or kernel.ErrNotFound.
func (c Configs) Get(_ context.Context, key string) (repo.Config, error) {
	if r, ok := c[key]; ok {
		return r, nil
	}
	return repo.Config{}, kernel.ErrNotFound
}

// Order is the order Members lists a virtual repository's real members in.
// Each is the list one of 7.161.26's callers walks, and every order starts
// from the same flattening (Members).
type Order int

const (
	// Resolve is a download's order (R1;
	// VirtualRepositoriesResolver.assembleSearchRepositoriesList 104-143), in
	// six groups: local and federated members with priorityResolution, the
	// caches of remote members with it, their upstreams, then the same three
	// groups for the other members. A remote member is two steps, its cache
	// and its upstream, so every cache is searched before any upstream.
	Resolve Order = iota
	// Package is a package handler's order
	// (PackageRepoServiceImpl.getSubRepositories 318-339): Resolve's, with
	// the cache steps left out while an upstream step remains, because a
	// package handler asks a remote member once, through its source, which
	// reads its own cache first.
	Package
	// Stored is the storage API's order
	// (VirtualRepo.getResolvedLocalFederatedAndCachedRepos 390-395): the
	// local and federated members, then the remote members' caches. It has
	// no priority groups and no upstream steps, since the storage API never
	// lists a live remote.
	Stored
)

// Member is one step of a walk over a virtual repository's members: a local
// or federated member, a remote member's cache, or, with Upstream, a remote
// member's upstream. Repo is never a virtual repository.
type Member struct {
	Repo     repo.Config
	Upstream bool
}

// Options are what a walk's caller knows about the request.
type Options struct {
	// Path is the request's path in the virtual repository. When set, a
	// nested virtual repository whose own patterns reject it is left out
	// with its members (VirtualResolverRequestFilter.accepts 90-108). The
	// storage API leaves it empty: 7.x's storage walk applies no filter.
	Path string
	// Folder is whether the request names a folder, which the patterns
	// admit when an include could match below it.
	Folder bool
	Order  Order
	// NoUpstream leaves out every upstream step and keeps the caches, as
	// 7.x does for a request from another Artifactory when the virtual's
	// artifactoryRequestsCanRetrieveRemoteArtifacts is off (R7;
	// VirtualRepositoriesResolver.addRemoteRepo 145-159).
	NoUpstream bool
	// NoPriority ignores priorityResolution, for a package type whose 7.x
	// virtual does not support it: JFrog's RubyGems page says "Priority
	// resolution is currently not supported for RubyGems virtual
	// repositories".
	NoPriority bool
}

// Members returns the steps of a download through the virtual repository v,
// in o.Order.
//
// It first flattens v's members depth first, in configured order (R2;
// VirtualRepositoryConfigResolverImpl.VirtualConfigResolverHelper.resolve
// 109-143). One set of visited virtual keys spans the walk, v's own key
// included, so a virtual reached a second time, through a cycle, as its own
// member or through a diamond, is skipped without an error; 7.x saves a
// cycle and resolves it this way. A real member reached twice keeps its
// first position. A key no repository has is skipped
// (RepositoryConfigServiceImpl.getRepoConfigsByKeysInSpecificOrder 916-937).
// With o.Path set, a nested virtual whose own patterns reject the path is
// dropped with its members. v's own patterns are the caller's check: they
// answer the request (generic.CheckDownload) rather than empty the walk.
//
// The error is rs's for a member it could not read.
func Members(ctx context.Context, rs Repos, v repo.Config, o Options) ([]Member, error) {
	locals, remotes, err := flatten(ctx, rs, v, o)
	if err != nil {
		return nil, err
	}
	out := make([]Member, 0, len(locals)+2*len(remotes))
	if o.Order == Stored {
		for _, c := range locals {
			out = append(out, Member{Repo: c})
		}
		for _, c := range remotes {
			out = append(out, Member{Repo: c})
		}
		return out, nil
	}
	upstream := !o.NoUpstream && len(remotes) > 0
	caches := o.Order != Package || !upstream
	priority := func(c repo.Config) bool { return !o.NoPriority && c.PriorityResolution.Bool() }
	// Each half is the local members, then the caches, then the upstreams:
	// first of the members with priorityResolution, then of the others.
	for _, half := range []bool{true, false} {
		for _, c := range locals {
			if priority(c) == half {
				out = append(out, Member{Repo: c})
			}
		}
		for _, c := range remotes {
			if priority(c) == half && caches {
				out = append(out, Member{Repo: c})
			}
		}
		for _, c := range remotes {
			if priority(c) == half && upstream {
				out = append(out, Member{Repo: c, Upstream: true})
			}
		}
	}
	return out, nil
}

// flatten returns v's real members, local and federated ones apart from
// remote ones, each in the order Members describes.
func flatten(
	ctx context.Context,
	rs Repos,
	v repo.Config,
	o Options,
) (locals, remotes []repo.Config, err error) {
	visited := map[string]bool{v.Key: true}
	reached := map[string]bool{}
	var walk func(c repo.Config) error
	walk = func(c repo.Config) error {
		for _, key := range c.Repositories {
			m, ok, err := member(ctx, rs, c, key)
			switch {
			case err != nil:
				return err
			case !ok:
			case m.Class == repo.Virtual:
				if visited[m.Key] {
					continue
				}
				visited[m.Key] = true
				if o.Path != "" && !repo.PatternsOf(m).AcceptsRequest(o.Path, o.Folder) {
					continue
				}
				if err := walk(m); err != nil {
					return err
				}
			case reached[m.Key]:
			case m.Class == repo.Remote:
				reached[m.Key] = true
				remotes = append(remotes, m)
			default:
				reached[m.Key] = true
				locals = append(locals, m)
			}
		}
		return nil
	}
	return locals, remotes, walk(v)
}

// member reads the member key of c; ok is false, with no error, when no
// repository has the key.
func member(ctx context.Context, rs Repos, c repo.Config, key string) (repo.Config, bool, error) {
	m, err := rs.Get(ctx, key)
	if errors.Is(err, kernel.ErrNotFound) {
		return repo.Config{}, false, nil
	}
	if err != nil {
		return repo.Config{}, false, fmt.Errorf("member %s of %s: %w", key, c.Key, err)
	}
	return m, true, nil
}

// Walk calls visit for every route from the virtual repository v to a
// member: chain is the virtual repositories the route passes through, v
// first and m's own virtual last, and m is the member, in configured order,
// depth first. A nested virtual member is visited before its own members,
// so a caller can bound the walk, or keep the virtual's configuration;
// returning an error from visit stops the walk with that error.
//
// Unlike Members, Walk keeps every route to a member, for the searches that
// decide which patterns admit a member by the route that reached it: a
// member reached through two virtuals is visited twice. A virtual already
// on the chain is skipped without an error, so a cycle ends the route that
// repeats it as 7.x's resolution skips a virtual it has visited (R2), and a
// key no repository has is skipped as Members skips it.
//
// chain is valid only during the call; a visit that keeps it clones it.
// The error is visit's, or rs's for a member it could not read.
func Walk(
	ctx context.Context,
	rs Repos,
	v repo.Config,
	visit func(chain []repo.Config, m repo.Config) error,
) error {
	chain := []repo.Config{v}
	var walk func() error
	walk = func() error {
		c := chain[len(chain)-1]
		for _, key := range c.Repositories {
			if slices.ContainsFunc(chain, func(x repo.Config) bool { return x.Key == key }) {
				continue
			}
			m, ok, err := member(ctx, rs, c, key)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			// Clipped, so a visit that appends to chain cannot write over
			// the route Walk goes on with.
			if err := visit(slices.Clip(chain), m); err != nil {
				return err
			}
			if m.Class != repo.Virtual {
				continue
			}
			chain = append(chain, m)
			if err := walk(); err != nil {
				return err
			}
			chain = chain[:len(chain)-1]
		}
		return nil
	}
	return walk()
}

// Resolution is what a download's walk over a virtual repository's members
// found: whether a member served the path, and, when none did, the first
// member that refused the caller Read. 7.x's walk passes over every member
// that does not serve the path, a Read refusal included, and keeps only the
// first refusal for the answer (R3; ArtifactoryVirtualDownloadStrategy.
// processStandard 209-260, checkIfForbidden 436-442).
type Resolution struct {
	// Missing is the format's miss reason; empty retains the standard answer.
	Missing   string
	Found     bool
	Forbidden *Denial
}

// Deny records d as the walk's refusal unless an earlier member refused, as
// processStandard keeps the first, and reports whether d is the one kept.
func (r *Resolution) Deny(d Denial) bool {
	if r.Forbidden != nil {
		return false
	}
	r.Forbidden = &d
	return true
}

// Denial is a member's refusal of Read in a walk, in the words of
// RealRepoBase.assertReadPermissions 145-153: the member and path it checked
// (a remote member's "<key>-cache", where 7.x keeps a remote's permissions)
// and the caller's name.
type Denial struct {
	Member, Path, User string
}

// Message is the refusal's text, which the 403 answers and a trace logs.
func (d Denial) Message() string {
	return fmt.Sprintf("Download request for repo:path '%s:%s' is forbidden for user: '%s'.",
		d.Member, d.Path, d.User)
}

// Answer is the item route's answer to a file download through the virtual
// repository v of path that the walk r found nowhere, or nil when r found
// it. path is the resource path the walk looked for: the archive of an
// archive entry and the file of a checksum, as 7.x's request names them
// (DownloadRequestContext.getResourcePath). hideGlobal is the security
// configuration's hideUnauthorizedResources, which artifactweave does not
// have yet, so callers pass false.
//
// The answers are DownloadServiceImpl.respondResourceNotFound's (1465-1497)
// for the unfound resource processStandard returns (newUnfoundResource
// 468-475), each with the virtual's path appended:
//   - no member refused: 404 "Could not find resource";
//   - a member refused: 403 with that member's refusal, the first one;
//   - a refusal while v's hideUnauthorizedResources or hideGlobal is on:
//     404 "Resource not found" (shouldHideUnauthorisedResponse 1499-1518;
//     R4), to an anonymous caller too, since the 404 sends no challenge;
//   - a refusal to an anonymous caller, not hidden: the 401 challenge
//     (shouldSendAuthRequired 1460-1463). sendAuthorizationRequired writes
//     "Unauthorized" in place of the reason in 6.20's
//     HttpArtifactoryResponse; 7.x's is closed code [I].
func Answer(v repo.Config, path string, r Resolution, anonymous, hideGlobal bool) error {
	if r.Found {
		return nil
	}
	suffix := "; Path: '" + v.Key + ":" + path + "'"
	d := r.Forbidden
	switch {
	case d == nil:
		reason := r.Missing
		if reason == "" {
			reason = "Could not find resource"
		}
		return apierror.New(http.StatusNotFound, reason+suffix)
	case hideGlobal || v.HideUnauthorizedResources != nil && *v.HideUnauthorizedResources:
		return apierror.New(http.StatusNotFound, "Resource not found"+suffix)
	case anonymous:
		return apierror.New(http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
	}
	return apierror.New(http.StatusForbidden, d.Message()+suffix)
}
