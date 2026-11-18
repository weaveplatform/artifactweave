package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/kernel/antpath"
)

const foreignVariant = "docker-local-foreign-v1:"

// AllowsForeignLayer checks the remote's current external-layer switch and
// allowlist, including warm reads. The external transport separately checks DNS
// and every redirect, so this does not authorize a network destination.
func AllowsForeignLayer(c repo.Config, raw string) bool {
	if !enabled(c.ExternalDependenciesEnabled) {
		return false
	}
	for _, pattern := range c.ExternalDependenciesPatterns {
		if pattern != "" && antpath.CompilePattern(pattern).Match(raw) {
			return true
		}
	}
	return false
}

// foreignEvidence keeps URL and descriptor length in the source identity, so
// replacing a local manifest cannot reuse another representation's validation.
// Variant is opaque to logging; a foreign URL may contain a signed query.
type foreignEvidence struct {
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// ForeignLayerResource requests a local manifest's foreign layer through a
// remote member's ordinary cache and authority fences. Its external transport
// remains RegistryContent.external, including same-origin secret stripping.
func ForeignLayerResource(image string, d Descriptor) (source.Resource, error) {
	r, err := RegistryResource(image, "blobs", d.Digest)
	if err != nil || !elsewhere(d.MediaType) || len(d.URLs) == 0 || d.Size < 0 {
		return source.Resource{}, remote.ErrRequest
	}
	u, err := url.Parse(d.URLs[0])
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return source.Resource{}, remote.ErrSecurity
	}
	spec := foreignEvidence{URL: d.URLs[0], Size: d.Size}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return source.Resource{}, remote.ErrRequest
	}
	hex, _ := SHA256Hex(d.Digest)
	r.UpstreamPath, r.ExternalURL = "", spec.URL
	r.NoCrossOriginAuth = true
	r.Checksums = kernel.Checksums{SHA256: hex}
	r.Variant = foreignVariant + base64.RawURLEncoding.EncodeToString(encoded)
	r.Validate = func(_ context.Context, _ io.ReadSeeker, size int64) error {
		if size != spec.Size {
			return source.ErrInvalidResult
		}
		return nil
	}
	return r, nil
}

func foreignResource(r source.Resource) (foreignEvidence, error) {
	var spec foreignEvidence
	if !strings.HasPrefix(r.Variant, foreignVariant) || r.ExternalURL == "" || r.UpstreamPath != "" || r.Checksums.SHA256 == "" {
		return spec, remote.ErrRequest
	}
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(r.Variant, foreignVariant))
	if err != nil || json.Unmarshal(encoded, &spec) != nil || spec.URL != r.ExternalURL || spec.Size < 0 {
		return spec, remote.ErrRequest
	}
	return spec, nil
}

func validateForeignResult(r source.Resource, out source.Result) (source.Result, error) {
	spec, err := foreignResource(r)
	size, digest := int64(0), ""
	if out.Item != nil {
		size, digest = out.Item.Size, out.Item.Actual.SHA256
	} else if out.Stream != nil {
		size, digest = out.Stream.Size, out.Stream.Checksums.SHA256
	}
	if err != nil || size != spec.Size || digest != r.Checksums.SHA256 {
		if out.Stream != nil {
			_ = out.Stream.Body.Close()
		}
		return source.Result{}, source.ErrInvalidResult
	}
	return out, nil
}

// ForeignLayer selects only a non-distributable layer, never a config or an
// ordinary layer carrying an unrelated URL. Manifest parsing owns its shape.
func ForeignLayer(m Manifest, digest string) (Descriptor, bool) {
	for _, layer := range m.Layers {
		if layer.Digest == digest && elsewhere(layer.MediaType) && len(layer.URLs) > 0 {
			return layer, true
		}
	}
	return Descriptor{}, false
}
