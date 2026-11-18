package terraformhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func (h handler) virtualMirrorVersion(r *http.Request, q providerOp, v virtualProviderVersion) (any, []source.Result, []disclosed, error) {
	if err := h.virtualProviderVersionFence(r, v); err != nil {
		return nil, nil, nil, err
	}
	out := map[string]remoteMirrorArchive{}
	reads := append([]source.Result(nil), v.reads...)
	paths := []disclosed{}
	if v.local != nil {
		for _, a := range v.local.archives {
			q.os, q.arch = a.os, a.arch
			target, err := h.virtualLocalProviderURL(r, v.member, q, a.item, 0)
			if err != nil {
				return nil, nil, nil, err
			}
			entry := remoteMirrorArchive{URL: target}
			if a.h1 != "" {
				entry.Hashes = []string{a.h1}
			}
			out[a.os+"_"+a.arch] = entry
		}
	} else {
		platforms := v.wire.Platforms
		if v.upstreamMirror {
			platforms = []remotePlatform{}
			for platform := range v.mirror {
				os, arch, _ := strings.Cut(platform, "_")
				platforms = append(platforms, remotePlatform{OS: os, Arch: arch})
			}
		}
		if len(platforms) == 0 {
			platforms = providerPlatforms
		}
		for _, platform := range platforms {
			q.os, q.arch = platform.OS, platform.Arch
			q.isUpstreamMirror = v.upstreamMirror
			var bundle providerBundle
			var err error
			if v.upstreamMirror {
				entry := v.mirror[platform.OS+"_"+platform.Arch]
				if err = h.virtualProviderAdmit(r, v.member, []string{entry.path}); err == nil {
					bundle, err = h.providerMirrorArchive(v.member, q, entry, v.mirrorMetadata, v.mirrorDigest)
				}
			} else {
				bundle, err = h.providerBundle(v.member, q, func(paths []string) error { return h.virtualProviderAdmit(r, v.member, paths) })
			}
			defer bundle.close()
			if err != nil {
				var answer *apierror.Error
				if errors.As(err, &answer) && answer.Status == 404 {
					continue
				}
				return nil, nil, nil, err
			}
			target, err := h.virtualRemoteProviderURL(r, v.member, bundle, 0)
			if err != nil {
				return nil, nil, nil, err
			}
			entry := remoteMirrorArchive{URL: target, Hashes: []string{bundle.h1, "zh:" + strings.ToLower(bundle.metadata.Shasum)}}
			if v.upstreamMirror {
				entry = v.mirror[platform.OS+"_"+platform.Arch]
				entry.URL = target
				if len(entry.Hashes) == 0 {
					entry.Hashes = []string{bundle.h1}
				}
			}
			out[platform.OS+"_"+platform.Arch] = entry
			reads = append(reads, bundle.results...)
			paths = append(paths, bundle.disclosed()...)
			if err := h.virtualProviderFileFence(r, v.member, bundle.paths); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	if len(out) == 0 {
		return nil, nil, nil, missing()
	}
	value := v
	reads = append(reads, source.Result{Revalidate: func(ctx context.Context) error { return h.virtualProviderVersionFence(r.WithContext(ctx), value) }})
	return map[string]any{"archives": out}, reads, paths, nil
}
