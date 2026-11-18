package dockerhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/registry"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func virtualPull(kind Kind, method string) bool {
	return (method == http.MethodGet || method == http.MethodHead) && (kind == KindManifest || kind == KindBlob)
}

func unsupportedVirtual(c repo.Config) error {
	return Errorf(http.StatusMethodNotAllowed, CodeUnsupported, "%s is a %s repository, which the registry does not serve yet", c.Key, c.Class)
}

// Virtual permissions come from real members, as the shared item/package walk
// does: a permission target cannot name a virtual repository. The original
// virtual image scope is checked once; member reads must not require new tokens.
func virtualReadable(ctx context.Context, rs virtual.Repos, c repo.Config, name string, d accessapi.Decider) (bool, error) {
	if d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: c.Key, Class: string(c.Class)}, name+"/", accessapi.ActionRead) {
		return true, nil
	}
	members, err := virtual.Members(ctx, rs, c, virtual.Options{Order: virtual.Stored})
	if err != nil {
		return false, fmt.Errorf("registry virtual permission members: %w", err)
	} //nolint:wrapcheck // member resolution identifies the lookup
	for _, m := range members {
		if memberReadable(d, m.Repo, name) {
			return true, nil
		}
	}
	return false, nil
}

func memberReadable(d accessapi.Decider, c repo.Config, name string) bool {
	return d == nil || d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: c.Key, Class: string(c.Class)}, name+"/", accessapi.ActionRead)
}

func (g *Registry) requireVirtual(r *http.Request, q Request) error {
	d, _ := accessapi.DeciderFrom(r.Context())
	if d == nil {
		return q.Require(r, q.Name+"/", accessapi.ActionRead)
	}
	allowed, err := virtualReadable(r.Context(), g.store.Repos(), q.Repo, q.Name, d)
	if err != nil {
		return err
	}
	p, _ := kernel.FromContext(r.Context())
	if !allowed {
		return q.refuse(r.Method, p)
	}
	if p.Registry != nil && !q.granted(p.Registry, r.Method, accessapi.ActionRead) {
		return Errorf(http.StatusUnauthorized, CodeUnauthorized, "the token does not grant this access to %s", q.scopeName())
	}
	return nil
}

type virtualRead struct {
	q      Request
	member virtual.Member
	item   model.Item
	result source.Result
	remote bool
}

func (g *Registry) serveVirtual(w http.ResponseWriter, r *http.Request, q Request) {
	if err := g.requireVirtual(r, q); err != nil {
		// A write-only pusher must be able to probe before uploading (Docker
		// 29/containerd). Hide unreadable content instead of disclosing it.
		var denied *apierror.Error
		if errors.As(err, &denied) && (denied.Status == http.StatusUnauthorized || denied.Status == http.StatusForbidden) &&
			r.Method == http.MethodHead && g.virtualPushProbe(r, q) {
			if q.Kind == KindBlob {
				Write(w, blobUnknown(q.Reference))
			} else {
				Write(w, manifestUnknown(q))
			}
			return
		}
		g.writeErr(w, r, err)
		return
	}
	if err := generic.CheckDownload(q.Repo, q.storagePath(), false); err != nil {
		refuse(w, err)
		return
	}
	if q.Kind == KindManifest && !manifestReference(q.Reference) {
		Write(w, manifestUnknown(q))
		return
	}
	if q.Kind == KindBlob {
		if _, err := blobDigest(q.Reference); err != nil {
			Write(w, err)
			return
		}
	}
	members, revision, err := layout.VirtualMembers(r.Context(), g.store.Repos(), q.Repo, q.storagePath())
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	read, err := g.findVirtual(r, q, members, revision)
	if errors.Is(err, kernel.ErrNotFound) && q.Kind == KindBlob && q.Repo.CachingLocalForeignLayersEnabled != nil && *q.Repo.CachingLocalForeignLayersEnabled {
		read, err = g.virtualForeign(r, q, members)
	}
	if err != nil {
		if errors.Is(err, kernel.ErrNotFound) {
			if q.Kind == KindBlob {
				Write(w, blobUnknown(q.Reference))
			} else {
				Write(w, manifestUnknown(q))
			}
			return
		}
		if read.remote {
			g.remoteError(w, r, read.q, err)
		} else {
			g.writeErr(w, r, err)
		}
		return
	}
	if read.remote {
		g.serveRemoteResult(w, r, read.q, read.result)
		return
	}
	if q.Kind == KindManifest {
		g.serveManifest(w, r, read.q, read.item)
		return
	}
	w.Header().Set(contentDigestHeader, q.Reference)
	setFileHeaders(w.Header(), read.q.Repo, read.item.Name)
	g.serveBlob(w, pullRequest(r), read.q.Repo, read.item)
}

func (g *Registry) findVirtual(r *http.Request, q Request, members []virtual.Member, revision string) (virtualRead, error) {
	kind := q.Kind.String()
	if _, digest := layout.SHA256Hex(q.Reference); !digest && q.Kind == KindManifest && q.Repo.ResolveDockerTagsByTimestamp != nil && *q.Repo.ResolveDockerTagsByTimestamp {
		read, found, err := g.latestVirtualTag(r, q, members)
		if err != nil {
			return read, err
		}
		if found {
			g.rememberVirtual(q, read, revision)
			return read, nil
		}
		// A remote cache retains its remote resolution step. Its stored
		// timestamp must not outrank any local or federated tag.
		remotes := make([]virtual.Member, 0, len(members))
		for _, member := range members {
			if member.Repo.Class == repo.Remote {
				remotes = append(remotes, member)
			}
		}
		members = remotes
	}
	if _, digest := layout.SHA256Hex(q.Reference); digest {
		if loc, ok := g.locations.Get(q.Repo.Key, kind, q.Reference, revision); ok {
			for _, m := range members {
				if m.Repo.Key != loc.Key || m.Upstream != loc.Upstream {
					continue
				}
				read, err := g.virtualMember(r, q, m)
				if err == nil {
					return read, nil
				}
				if !virtualPassedOver(err) {
					return read, fmt.Errorf("registry virtual member: %w", err)
				}
				break
			}
			g.locations.Drop(q.Repo.Key, kind, q.Reference)
		}
	}
	for _, m := range members {
		read, err := g.virtualMember(r, q, m)
		if err != nil {
			var upstream *source.HTTPError
			timestampFailure := q.Kind == KindManifest && q.Repo.ResolveDockerTagsByTimestamp != nil && *q.Repo.ResolveDockerTagsByTimestamp && !layout.ValidDigest(q.Reference) &&
				read.remote && errors.As(err, &upstream) && upstream.Status >= http.StatusBadRequest
			if virtualPassedOver(err) || timestampFailure {
				continue
			}
			return read, fmt.Errorf("registry virtual member: %w", err)
		}
		g.rememberVirtual(q, read, revision)
		return read, nil
	}
	return virtualRead{}, kernel.ErrNotFound
}

func (g *Registry) latestVirtualTag(r *http.Request, q Request, members []virtual.Member) (virtualRead, bool, error) {
	candidates := []layout.VirtualTagCandidate{}
	reads := map[string]virtualRead{}
	for _, member := range members {
		if member.Repo.Class != repo.Local && member.Repo.Class != repo.Federated {
			continue
		}
		read, err := g.virtualMember(r, q, member)
		if virtualPassedOver(err) {
			continue
		}
		if err != nil {
			return read, false, fmt.Errorf("registry timestamp member: %w", err)
		}
		candidates = append(candidates, layout.VirtualTagCandidate{Member: member, Item: read.item})
		reads[member.Repo.Key] = read
	}
	chosen, found := layout.LatestVirtualTag(candidates)
	return reads[chosen.Member.Repo.Key], found, nil
}

func (g *Registry) rememberVirtual(q Request, read virtualRead, revision string) {
	sum := read.item.Actual.SHA256
	if read.remote {
		if read.result.Item != nil {
			sum = read.result.Item.Actual.SHA256
		} else {
			sum = read.result.Stream.Checksums.SHA256
		}
	}
	g.locations.Put(q.Repo.Key, q.Kind.String(), "sha256:"+sum, revision, layout.MemberLocation{Key: read.member.Repo.Key, Upstream: read.member.Upstream})
}

func (g *Registry) virtualMember(r *http.Request, q Request, m virtual.Member) (virtualRead, error) {
	cacheOnly := !m.Upstream
	mustBeFresh := cacheOnly && (m.Repo.Offline == nil || !*m.Repo.Offline) && !remote.NoOriginUpstream(r.Context(), q.Repo)
	q.Repo = m.Repo
	read := virtualRead{q: q, member: m, remote: m.Repo.Class == repo.Remote}
	d, _ := accessapi.DeciderFrom(r.Context())
	if !memberReadable(d, m.Repo, q.Name) {
		return read, kernel.ErrNotFound
	}
	if err := generic.CheckBlackedOut(m.Repo, q.storagePath()); err != nil {
		return read, fmt.Errorf("registry virtual member: %w", err)
	}
	if read.remote {
		if g.sources == nil {
			return read, kernel.ErrNotFound
		}
		result, err := g.readRegistry(r, q, !m.Upstream)
		if err != nil {
			return read, fmt.Errorf("registry virtual member: %w", err)
		}
		if mustBeFresh {
			fresh, err := g.virtualCacheFresh(r.Context(), q, result)
			if err != nil || !fresh {
				if result.Stream != nil {
					_ = result.Stream.Body.Close()
				}
				if err != nil {
					return read, err
				}
				return read, kernel.ErrNotFound
			}
		}
		if result.Revalidate != nil {
			if err = result.Revalidate(r.Context()); err != nil {
				if result.Stream != nil {
					_ = result.Stream.Body.Close()
				}
				return read, fmt.Errorf("registry virtual member: %w", err)
			}
		}
		read.result = result
		return read, nil
	}
	var err error
	if q.Kind == KindManifest {
		read.item, err = g.findManifest(r, q)
		if err == nil {
			err = generic.CheckDownload(m.Repo, read.item.FullPath(), false)
		}
	} else {
		hex, _ := layout.SHA256Hex(q.Reference)
		read.item, err = g.findBlob(r.Context(), q, hex)
	}
	if err != nil {
		return read, fmt.Errorf("registry virtual member: %w", err)
	}
	return read, nil
}

func (s DockerAPI) virtualParents(ctx context.Context, c repo.Config, name, tag string) ([]registry.Parent, error) {
	if s.Registry == nil {
		return nil, apierror.New(http.StatusNotImplemented, "virtual registry reads are not configured")
	}
	g := s.Registry
	r := (&http.Request{Method: http.MethodGet}).WithContext(ctx)
	q := Request{Kind: KindManifest, Repo: c, Name: name, Reference: tag, authenticated: g.authenticated}
	if err := generic.CheckDownload(c, q.storagePath(), false); err != nil {
		return nil, fmt.Errorf("registry virtual parents: %w", err)
	}
	members, revision, err := layout.VirtualMembers(ctx, g.store.Repos(), c, q.storagePath())
	if err != nil {
		return nil, fmt.Errorf("registry virtual parents: %w", err)
	}
	read, err := g.findVirtual(r, q, members, revision)
	if err != nil {
		return nil, fmt.Errorf("registry virtual parents: %w", err)
	}
	child := read.item
	if read.remote {
		if read.result.Stream != nil {
			defer read.result.Stream.Body.Close()
		}
		if read.result.Item != nil {
			child = *read.result.Item
		} else {
			child = model.Item{Actual: read.result.Stream.Checksums, Cached: true}
		}
	}
	return g.uploads.ParentsOfManifest(ctx, registry.Owner{Repo: read.q.Repo, Name: name}, child) //nolint:wrapcheck // registry service names its own failures
}

func manifestReference(ref string) bool {
	if strings.Contains(ref, ":") {
		_, ok := layout.SHA256Hex(ref)
		return ok
	}
	return layout.ValidTag(ref) && !layout.ReservedTag(ref)
}

// An upstream's credentials are the remote's, not the caller's. Its refusal
// cannot prevent a later member from serving a virtual pull.
func virtualPassedOver(err error) bool {
	var upstream *source.HTTPError
	return pkgroute.PassedOver(err) || errors.As(err, &upstream) && (upstream.Status == http.StatusUnauthorized || upstream.Status == http.StatusForbidden)
}

// Cache-only source reads intentionally allow stale copies. An online virtual
// cache step must pass expired content to the remote's upstream step instead.
func (g *Registry) virtualCacheFresh(ctx context.Context, q Request, result source.Result) (bool, error) {
	operation := "manifests"
	if q.Kind == KindBlob {
		operation = "blobs"
	}
	resource, err := layout.RegistryResource(q.Name, operation, q.Reference)
	if err != nil {
		return false, fmt.Errorf("registry cache resource: %w", err)
	}
	sha := ""
	if result.Item != nil {
		sha = result.Item.Actual.SHA256
	} else if result.Stream != nil {
		sha = result.Stream.Checksums.SHA256
	}
	now := time.Now().UTC()
	page := kernel.Page{Limit: 128}
	for {
		entries, next, err := g.store.RemoteCache().Exact(ctx, q.Repo.Key, resource.Path, page)
		if err != nil {
			return false, fmt.Errorf("registry cache entries: %w", err)
		}
		for _, entry := range entries {
			if entry.Token.Source != result.Identity || entry.Blob.Checksums.SHA256 != sha {
				continue
			}
			if result.Item != nil && entry.ItemID != result.Item.ID {
				continue
			}
			if remote.Fresh(q.Repo, entry, now) {
				return true, nil
			}
		}
		if next == "" {
			return false, nil
		}
		page.After = next
	}
}
