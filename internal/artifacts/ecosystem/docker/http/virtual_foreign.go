package dockerhttp

import (
	"fmt"
	"io"
	"net/http"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// virtualForeign runs only after the ordinary blob walk misses. The local
// manifest supplies evidence, while each remote retains its own cache, source
// identity, external URL policy and publication permissions.
func (g *Registry) virtualForeign(r *http.Request, q Request, members []virtual.Member) (virtualRead, error) {
	if g.sources == nil {
		return virtualRead{}, kernel.ErrNotFound
	}
	descriptor, found, err := g.virtualForeignDescriptor(r, q, members)
	if err != nil || !found {
		if err != nil {
			return virtualRead{}, err
		}
		return virtualRead{}, kernel.ErrNotFound
	}
	resource, err := layout.ForeignLayerResource(q.Name, descriptor)
	if err != nil {
		return virtualRead{}, kernel.ErrNotFound
	}
	p, _ := kernel.FromContext(r.Context())
	d, _ := accessapi.DeciderFrom(r.Context())
	for _, member := range members {
		if member.Repo.Class != repo.Remote || !memberReadable(d, member.Repo, q.Name) || !layout.AllowsForeignLayer(member.Repo, resource.ExternalURL) {
			continue
		}
		memberQ := q
		memberQ.Repo = member.Repo
		read := virtualRead{q: memberQ, member: member, remote: true}
		result, err := g.sources.Read(r.Context(), source.Request{
			Repo: member.Repo, Subject: accessapi.SubjectOf(p), Access: d,
			Resource: resource, Project: member.Repo.ProjectKey, CacheOnly: !member.Upstream,
		})
		if err == nil && result.Revalidate != nil {
			err = result.Revalidate(r.Context())
		}
		if err != nil {
			if result.Stream != nil {
				_ = result.Stream.Body.Close()
			}
			if virtualPassedOver(err) {
				continue
			}
			return read, fmt.Errorf("registry virtual foreign layer: %w", err)
		}
		read.result = result
		return read, nil
	}
	return virtualRead{}, kernel.ErrNotFound
}

func (g *Registry) virtualForeignDescriptor(r *http.Request, q Request, members []virtual.Member) (layout.Descriptor, bool, error) {
	d, _ := accessapi.DeciderFrom(r.Context())
	for _, member := range members {
		if (member.Repo.Class != repo.Local && member.Repo.Class != repo.Federated) || !memberReadable(d, member.Repo, q.Name) {
			continue
		}
		hits, err := layout.ImageManifests(r.Context(), g.store.Items(), member.Repo.Key, q.Name)
		if err != nil {
			return layout.Descriptor{}, false, fmt.Errorf("registry foreign manifests: %w", err)
		}
		seen := map[string]bool{}
		for _, hit := range hits {
			if seen[hit.Item.Actual.SHA256] {
				continue
			}
			if generic.CheckDownload(q.Repo, hit.Item.FullPath(), false) != nil || generic.CheckDownload(member.Repo, hit.Item.FullPath(), false) != nil {
				continue
			}
			eligible, _, err := layout.VirtualMembers(r.Context(), g.store.Repos(), q.Repo, hit.Item.FullPath())
			if err != nil {
				return layout.Descriptor{}, false, fmt.Errorf("registry foreign manifest members: %w", err)
			}
			admitted := false
			for _, candidate := range eligible {
				if candidate.Repo.Key == member.Repo.Key {
					admitted = true
					break
				}
			}
			if !admitted {
				continue
			}
			seen[hit.Item.Actual.SHA256] = true
			body, _, err := g.blobs.Open(r.Context(), hit.Item.Actual.SHA256)
			if err != nil {
				return layout.Descriptor{}, false, fmt.Errorf("registry foreign manifest: %w", err)
			}
			content, err := io.ReadAll(io.LimitReader(body, layout.MaxManifestSize+1))
			_ = body.Close()
			if err != nil {
				return layout.Descriptor{}, false, fmt.Errorf("registry foreign manifest: %w", err)
			}
			mediaType := layout.MediaTypeOCIManifest
			if types := hit.Properties[layout.ManifestTypeProperty]; len(types) > 0 {
				mediaType = types[0]
			}
			manifest, err := layout.ParseManifest(mediaType, content, hit.Item.Actual.SHA256)
			if err != nil {
				continue // Non-manifest REST items cannot authorize a foreign fetch.
			}
			if descriptor, found := layout.ForeignLayer(manifest, q.Reference); found {
				return descriptor, true, nil
			}
		}
	}
	return layout.Descriptor{}, false, nil
}
