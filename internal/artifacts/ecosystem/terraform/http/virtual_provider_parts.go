package terraformhttp

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type virtualProviderVersion struct {
	member         *http.Request
	wire           remoteProviderVersion
	local          *providerVersion
	reads          []source.Result
	paths, files   []string
	mirror         map[string]remoteMirrorArchive
	mirrorMetadata source.Result
	mirrorDigest   string
	upstreamMirror bool
}

func providerMember(c repo.Config) bool {
	return c.Class == repo.Remote || (c.Class == repo.Local || c.Class == repo.Federated) && c.TerraformType == repo.TerraformProvider
}

// Metadata has canonical listing identities; remote files retain their actual
// upstream cache paths and source policy. Never apply a remote's file patterns
// to an invented canonical archive path before its descriptor is read.
func (h handler) virtualProviderAllowed(r, mr *http.Request, path string) (bool, error) {
	if !h.k.Can(mr, path, api.ActionRead) {
		return false, nil
	}
	v, err := h.k.DB().Repos().Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key)
	if errors.Is(err, kernel.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("terraform current provider virtual: %w", err)
	}
	if v.Class != repo.Virtual || v.PackageType != repo.Terraform {
		return false, nil
	}
	caller, _ := kernel.FromContext(mr.Context())
	decider, _ := api.DeciderFrom(mr.Context())
	current := r.WithContext(api.WithCaller(r.Context(), caller, decider))
	members, err := h.k.DownloadMembers(h.k.Bind(current, v), path)
	if err != nil {
		if modulePassedOver(err) {
			return false, nil
		}
		return false, fmt.Errorf("terraform provider member walk: %w", err)
	}
	for _, member := range members {
		c := pkgroute.RequestFrom(member).Repo
		if c.Key == pkgroute.RequestFrom(mr).Repo.Key && providerMember(c) {
			return h.k.Can(member, path, api.ActionRead), nil
		}
	}
	return false, nil
}

func (h handler) virtualProviderPart(r, mr *http.Request, q providerOp) ([]tf.ProviderVersion[virtualProviderVersion], error) {
	if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
		return h.virtualRemoteProviderPart(r, mr, q)
	}
	if !h.k.CanFolder(mr, "", api.ActionRead) {
		return nil, missing()
	}
	versions, err := h.providerVersions(mr, q.name, q.isMirror, true)
	if err != nil {
		return nil, err
	}
	out := []tf.ProviderVersion[virtualProviderVersion]{}
	for _, v := range versions {
		if !q.isList && v.version != q.version {
			continue
		}
		kept := []providerArchive{}
		paths := []string{}
		for _, archive := range v.archives {
			allowed, e := h.virtualProviderAllowed(r, mr, archive.item.FullPath())
			if e != nil {
				return nil, e
			}
			if allowed {
				kept = append(kept, archive)
				paths = append(paths, archive.item.FullPath())
			}
		}
		if len(kept) == 0 {
			continue
		}
		v.archives = kept
		if v.manifest.ID != 0 {
			allowed, e := h.virtualProviderAllowed(r, mr, v.manifest.FullPath())
			if e != nil {
				return nil, e
			}
			if !allowed {
				v.manifest = model.Item{}
			} else {
				paths = append(paths, v.manifest.FullPath())
			}
		}
		protocols, e := h.protocols(mr, v.manifest)
		if e != nil {
			return nil, e
		}
		wire := remoteProviderVersion{Version: v.version, Protocols: protocols, Platforms: []remotePlatform{}}
		for _, a := range kept {
			wire.Platforms = append(wire.Platforms, remotePlatform{OS: a.os, Arch: a.arch})
		}
		out = append(out, tf.ProviderVersion[virtualProviderVersion]{Version: v.version, Value: virtualProviderVersion{member: mr, wire: wire, local: &v, paths: paths}})
	}
	return out, nil
}

func (h handler) virtualProviderMode(mr *http.Request, q providerOp) (providerOp, error) {
	if !q.isMirror {
		return q, nil
	}
	registry, err := url.Parse(pkgroute.RequestFrom(mr).Repo.TerraformRegistryURL)
	if err != nil {
		return q, remoteProviderError("invalid registry URL")
	}
	q.isUpstreamMirror = strings.Trim(registry.Path, "/") != ""
	if !q.isUpstreamMirror && !strings.EqualFold(q.host, registry.Host) && !strings.EqualFold(q.host, registry.Hostname()) {
		return q, missing()
	}
	return q, nil
}

func (h handler) virtualRemoteProviderPart(r, mr *http.Request, q providerOp) ([]tf.ProviderVersion[virtualProviderVersion], error) {
	var err error
	q, err = h.virtualProviderMode(mr, q)
	if err != nil {
		return nil, err
	}
	if !h.k.CanFolder(mr, providerFolder(q), api.ActionRead) {
		return nil, missing()
	}
	if q.isUpstreamMirror {
		return h.virtualUpstreamMirrorPart(r, mr, q)
	}
	if err := h.k.CheckDocument(mr, providerDocument(q)); err != nil {
		return nil, fmt.Errorf("terraform provider metadata policy: %w", err)
	}
	versions, result, err := h.upstreamProviderVersions(mr, q)
	if err != nil {
		return nil, err
	}
	out := []tf.ProviderVersion[virtualProviderVersion]{}
	lookups := 0
	for _, v := range versions {
		if !q.isList && v.Version != q.version {
			continue
		}
		q.version = v.Version
		platforms := v.Platforms
		if len(platforms) == 0 {
			platforms = providerPlatforms
		}
		kept := []remotePlatform{}
		paths := []string{}
		reads := []source.Result{result}
		for _, p := range platforms {
			q.os, q.arch = p.OS, p.Arch
			path := providerListingPath(q)
			allowed, e := h.virtualProviderAllowed(r, mr, path)
			if e != nil {
				return nil, e
			}
			if !allowed {
				continue
			}
			if len(v.Platforms) == 0 {
				lookups++
				if lookups > moduleDiscoveryLimit {
					return nil, remoteProviderError("versions without platform hints exceed the bounded package lookup limit")
				}
				_, metadata, _, e := h.upstreamProviderPackage(mr, q)
				if e != nil {
					if modulePassedOver(e) {
						continue
					}
					return nil, e
				}
				reads = append(reads, metadata)
				paths = append(paths, path)
				break
			}
			kept = append(kept, p)
			paths = append(paths, path)
		}
		if len(paths) == 0 {
			continue
		}
		if len(v.Platforms) > 0 {
			v.Platforms = kept
		}
		out = append(out, tf.ProviderVersion[virtualProviderVersion]{Version: v.Version, Value: virtualProviderVersion{member: mr, wire: v, reads: reads, paths: paths}})
	}
	return out, nil
}

func (h handler) virtualUpstreamMirrorPart(r, mr *http.Request, q providerOp) ([]tf.ProviderVersion[virtualProviderVersion], error) {
	index, err := h.upstreamMirrorMetadata(mr, q, true)
	if err != nil {
		return nil, err
	}
	if index.Stream == nil {
		return nil, remoteProviderError("missing mirror index")
	}
	versions, err := parseMirrorVersions(index.Stream.Body, q)
	closeModuleResult(index)
	index.Stream = nil
	if err != nil {
		return nil, err
	}
	if len(versions) > moduleDiscoveryLimit {
		return nil, remoteProviderError("mirror version discovery exceeds its bound")
	}
	out := []tf.ProviderVersion[virtualProviderVersion]{}
	for _, version := range versions {
		if !q.isList && version != q.version {
			continue
		}
		q.version = version
		archives, metadata, digest, e := h.mirrorArchives(mr, q)
		if e != nil {
			if modulePassedOver(e) {
				continue
			}
			return nil, e
		}
		kept := map[string]remoteMirrorArchive{}
		paths, files := []string{}, []string{}
		for platform, entry := range archives {
			q.os, q.arch, _ = strings.Cut(platform, "_")
			allowed, e := h.virtualProviderAllowed(r, mr, providerListingPath(q))
			if e != nil {
				return nil, e
			}
			if !allowed {
				continue
			}
			allowed, e = h.virtualProviderAllowed(r, mr, entry.path)
			if e != nil {
				return nil, e
			}
			if !allowed || generic.CheckDownload(pkgroute.RequestFrom(mr).Repo, entry.path, false) != nil {
				continue
			}
			kept[platform] = entry
			paths = append(paths, providerListingPath(q))
			files = append(files, entry.path)
		}
		if len(kept) == 0 {
			continue
		}
		out = append(
			out,
			tf.ProviderVersion[virtualProviderVersion]{
				Version: version,
				Value: virtualProviderVersion{
					member:         mr,
					wire:           remoteProviderVersion{Version: version},
					reads:          []source.Result{index, metadata},
					paths:          paths,
					files:          files,
					mirror:         kept,
					mirrorMetadata: metadata,
					mirrorDigest:   digest,
					upstreamMirror: true,
				},
			},
		)
	}
	return out, nil
}
