package docker

import (
	"context"
	"io"
	"sync/atomic"

	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
)

type (
	registryDepthKey  struct{}
	registryBudgetKey struct{}
)

// Children establish the descriptor graph before publishing an index and make
// configuration metadata available to the native package-index hook. Layers
// remain lazy, independently verified complete blobs.
func (c *RegistryContent) children(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts remote.Options,
	image string,
	m Manifest,
) (Config, error) {
	depth, _ := ctx.Value(registryDepthKey{}).(int)
	if depth >= 8 || len(m.Manifests) > 64 {
		return Config{}, remote.ErrRequest
	}
	ctx = context.WithValue(ctx, registryDepthKey{}, depth+1)
	budget, _ := ctx.Value(registryBudgetKey{}).(*atomic.Int32)
	if budget == nil {
		budget = new(atomic.Int32)
		ctx = context.WithValue(ctx, registryBudgetKey{}, budget)
	}
	for _, d := range m.Manifests {
		if budget.Add(1) > 256 {
			return Config{}, remote.ErrRequest
		}
		child := q
		resource, err := RegistryResource(image, "manifests", d.Digest)
		if err != nil {
			return Config{}, err
		}
		child.Resource = resource
		if err = c.Policy.Check(ctx, child, permit, opts.Revision); err != nil {
			//nolint:wrapcheck // preserves upstream error identity
			return Config{}, err
		}
		result, err := c.Policy.Read(ctx, child, permit)
		if err != nil {
			//nolint:wrapcheck // preserves upstream error identity
			return Config{}, err
		}
		size := int64(0)
		mediaType := ""
		if result.Item != nil {
			size = result.Item.Size
			props, propsErr := c.Policy.Store.Items().Properties(ctx, result.Item.ID)
			if propsErr != nil {
				return Config{}, source.ErrUnavailable
			}
			if values := props[ManifestTypeProperty]; len(values) > 0 {
				mediaType = values[0]
			}
		} else {
			size = result.Stream.Size
			mediaType = result.Stream.MediaType
			_ = result.Stream.Body.Close()
		}
		if size != d.Size {
			return Config{}, item.ErrRemoteLength
		}
		if mediaType != d.MediaType {
			return Config{}, remote.ErrRequest
		}
	}
	if m.Config == nil {
		return Config{}, nil
	}
	if m.Config.Size > MaxManifestSize {
		return Config{}, remote.ErrRequest
	}
	child := q
	resource, err := RegistryResource(image, "blobs", m.Config.Digest)
	if err != nil {
		return Config{}, err
	}
	child.Resource = resource
	if err = c.Policy.Check(ctx, child, permit, opts.Revision); err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return Config{}, err
	}
	result, err := c.Policy.Read(ctx, child, permit)
	if err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return Config{}, err
	}
	var reader io.ReadCloser
	var size int64
	if result.Item != nil {
		size = result.Item.Size
		reader, _, err = c.Policy.Blobs.Open(ctx, result.Item.Actual.SHA256)
		if err != nil {
			return Config{}, source.ErrUnavailable
		}
	} else {
		reader = result.Stream.Body
		size = result.Stream.Size
	}
	defer reader.Close()
	if size != m.Config.Size {
		return Config{}, item.ErrRemoteLength
	}
	return ReadConfig(m.ConfigType(), reader), nil
}
