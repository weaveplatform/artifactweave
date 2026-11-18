package dockerhttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/registry"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func virtualWrite(kind Kind, method string) bool {
	return kind == KindUploads || kind == KindUpload || kind == KindManifest && method == http.MethodPut
}

func (g *Registry) virtualDeployment(r *http.Request, q Request) (Request, error) {
	v := q.Repo
	target, err := virtual.DeployTarget(r.Context(), g.store.Repos(), v)
	if errors.Is(err, virtual.ErrNoDeployTarget) {
		e := Errorf(http.StatusMethodNotAllowed, CodeUnsupported, "%s", itemkit.NoDeployTarget(v.Key))
		e.Allow = http.MethodGet
		return q, e
	}
	if err != nil {
		return q, fmt.Errorf("registry deploy target: %w", err)
	}
	q.Repo, q.virtualRepo = target, &v
	if err = q.Require(r, q.Name+"/", accessapi.ActionWrite); err != nil {
		return q, err
	}
	if _, err = layout.ImageWriteConfig(v, q.Name); err == nil {
		q.Repo, err = layout.ImageWriteConfig(target, q.Name)
	}
	return q, err //nolint:wrapcheck // the image policy owns its refusal
}

func (g *Registry) virtualPushProbe(r *http.Request, q Request) bool {
	target, err := virtual.DeployTarget(r.Context(), g.store.Repos(), q.Repo)
	if err != nil {
		return false
	}
	q.Repo = target
	return q.Require(r, q.Name+"/", accessapi.ActionWrite) == nil
}

// syncVirtual imports only what the DDR lacks, including index children clients
// may skip after HEAD. The normal PutManifest checks still validate every
// descriptor. Limits match remote registry graph traversal, bounding malicious
// indexes without changing direct pushes.
func (g *Registry) syncVirtual(r *http.Request, q Request, m layout.Manifest, seen map[string]bool, depth int) error {
	if depth >= 8 {
		return &registry.BlobsUnknownError{Digests: []string{"sha256:" + m.Digest}}
	}
	for _, ref := range layout.VirtualImports(m) {
		hexSum, ok := layout.SHA256Hex(ref.Digest)
		if !ok {
			continue
		}
		var err error
		if ref.Manifest {
			_, err = layout.FindManifest(r.Context(), g.store.Items(), q.Repo.Key, q.Name, hexSum)
		} else {
			_, err = layout.FindBlob(r.Context(), g.store.Items(), q.Repo.Key, q.Name, hexSum)
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, kernel.ErrNotFound) {
			return err //nolint:wrapcheck // the layout owns its lookup
		}
		key := fmt.Sprintf("%t:%s", ref.Manifest, ref.Digest)
		if seen[key] {
			continue
		}
		if len(seen) >= 256 {
			return &registry.BlobsUnknownError{Digests: []string{ref.Digest}}
		}
		seen[key] = true
		src := q
		src.Repo, src.Reference, src.Kind = *q.virtualRepo, ref.Digest, KindBlob
		if ref.Manifest {
			src.Kind = KindManifest
		}
		read, err := g.virtualImportSource(r, src)
		if err != nil {
			if virtualPassedOver(err) || read.remote && retrievalFailed(err) {
				continue
			}
			return err
		}
		if ref.Manifest {
			err = g.importManifest(r, q, read, hexSum, seen, depth+1)
		} else {
			_, err = g.copyVirtualBlob(r, q, read, hexSum)
		}
		if err != nil {
			var missing *registry.BlobsUnknownError
			if errors.As(err, &missing) || read.remote && (virtualPassedOver(err) || retrievalFailed(err)) {
				continue
			}
			return err
		}
	}
	return nil
}

func (g *Registry) virtualImportSource(r *http.Request, q Request) (virtualRead, error) {
	return g.virtualSource(r, q, q.Name)
}

func (g *Registry) virtualSource(r *http.Request, q Request, path string) (virtualRead, error) {
	if err := generic.CheckDownload(q.Repo, path, false); err != nil {
		return virtualRead{}, err //nolint:wrapcheck // the download policy owns its refusal
	}
	members, revision, err := layout.VirtualMembers(r.Context(), g.store.Repos(), q.Repo, path)
	if err != nil {
		return virtualRead{}, err //nolint:wrapcheck // member resolution names its lookup
	}
	return g.findVirtual(r, q, members, revision)
}

func (g *Registry) copyVirtualBlob(r *http.Request, q Request, read virtualRead, hexSum string) (bool, error) {
	var from *model.Item
	if read.remote {
		from = read.result.Item
	} else {
		from = &read.item
	}
	if read.result.Stream != nil {
		defer read.result.Stream.Body.Close()
	}
	var copied bool
	err := g.store.Tx(r.Context(), func(ctx context.Context) error {
		if read.result.Revalidate != nil {
			if err := read.result.Revalidate(ctx); err != nil {
				return err //nolint:wrapcheck // the source owns its refusal
			}
		}
		var err error
		if from != nil {
			_, copied, err = g.uploads.CopyBlob(ctx, owner(r, q), *from)
		} else {
			_, err = g.uploads.Upload(ctx, owner(r, q), read.result.Stream.Body, hexSum)
			copied = err == nil
		}
		return err //nolint:wrapcheck // the registry service names its failure
	})
	return copied, err //nolint:wrapcheck // the source/registry service names its failure
}

func (g *Registry) importManifest(r *http.Request, q Request, read virtualRead, hexSum string, seen map[string]bool, depth int) error {
	var body io.ReadCloser
	var mediaType string
	if read.remote {
		stream, err := g.remoteStream(r, read.result)
		if err != nil {
			return err
		}
		body, mediaType = stream.Body, stream.MediaType
	} else {
		stream, _, err := g.blobs.Open(r.Context(), read.item.Actual.SHA256)
		if err != nil {
			return fmt.Errorf("open virtual child: %w", err)
		}
		body = stream
	}
	defer body.Close()
	bytes, err := io.ReadAll(io.LimitReader(body, layout.MaxManifestSize+1))
	if err != nil {
		return fmt.Errorf("read virtual child: %w", err)
	}
	sum := sha256.Sum256(bytes)
	if hex.EncodeToString(sum[:]) != hexSum {
		return &registry.BlobsUnknownError{Digests: []string{"sha256:" + hexSum}}
	}
	m, err := layout.ParseManifest(mediaType, bytes, hexSum)
	if err != nil {
		return &registry.BlobsUnknownError{Digests: []string{"sha256:" + hexSum}}
	}
	if err = g.syncVirtual(r, q, m, seen, depth); err != nil {
		return err
	}
	err = g.store.Tx(r.Context(), func(ctx context.Context) error {
		if read.result.Revalidate != nil {
			if err := read.result.Revalidate(ctx); err != nil {
				return err //nolint:wrapcheck // the source owns its refusal
			}
		}
		return g.uploads.PutManifest(ctx, registry.Push{Owner: owner(r, q), Manifest: m, Body: bytes, Folders: []string{layout.DigestFolder(hexSum)}})
	})
	return err //nolint:wrapcheck // the source/registry service names its failure
}
