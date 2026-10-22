package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RegistryContent installs Distribution authentication and validation on a fresh
// shared policy. The pool and resolver belong to the application lifetime.
type RegistryContent struct {
	Policy      *Policy
	Pool        *RegistryPool
	Options     func(context.Context, repo.Config) (RegistryOptions, error)
	mu          sync.Mutex
	descriptors map[string]layout.Descriptor
}

func (c *RegistryContent) Install() {
	c.Policy.Fetch = c.fetch
	c.Policy.PrepareUpload = c.prepare
}

func (c *RegistryContent) fetch(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	request Request,
) (*http.Response, error) {
	image, operation, reference, err := registryResource(q.Resource.UpstreamPath)
	if err != nil {
		return nil, err
	}
	credentials, err := c.Options(ctx, q.Repo)
	if err != nil {
		return nil, err
	}
	if credentials.Transport.Revision != opts.Revision {
		return nil, source.ErrSuperseded
	}
	client, err := c.Pool.Client(q.Repo, permit.Identity, credentials)
	if err != nil {
		return nil, err
	}
	request.Path = operation + "/" + reference
	// A native HEAD resolves a verified complete representation. Shared ingestion
	// bounds metadata and spools blobs; a header alone cannot prove digest integrity.
	request.Method = http.MethodGet
	res, err := client.Do(ctx, image, request)
	if err != nil {
		if errors.Is(err, ErrRegistryDenied) {
			return nil, &source.HTTPError{Kind: source.HTTPFailure, Status: http.StatusUnauthorized}
		}
		return nil, err
	}
	if res.StatusCode == http.StatusNotFound && operation == "blobs" {
		d, ok := c.descriptor(permit.Identity, opts.Revision, image, reference)
		if ok && len(d.URLs) > 0 {
			_ = res.Body.Close()
			res, err = c.external(ctx, q.Repo, opts, d.URLs[0])
			if err != nil {
				return nil, err
			}
		}
	}
	if digest := res.Header.Get("Docker-Content-Digest"); digest != "" {
		value, ok := layout.SHA256Hex(digest)
		if !ok {
			_ = res.Body.Close()
			return nil, item.ErrBadChecksum
		}
		res.Header.Set("X-Checksum-Sha256", value)
	}
	if res.StatusCode == http.StatusOK && (operation == "tags" || operation == "referrers") {
		return c.pages(ctx, client, image, operation, reference, res)
	}
	return res, nil
}

func registryResource(path string) (image, operation, reference string, err error) {
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return "", "", "", ErrRequest
	}
	image = strings.Join(parts[:len(parts)-2], "/")
	operation, reference = parts[len(parts)-2], parts[len(parts)-1]
	if !registryName.MatchString(image) {
		return "", "", "", ErrRequest
	}
	switch operation {
	case "manifests":
		if _, ok := layout.SHA256Hex(
			reference,
		); !ok &&
			(!layout.ValidTag(reference) || layout.ReservedTag(reference)) {
			return "", "", "", ErrRequest
		}
	case "blobs", "referrers":
		if _, ok := layout.SHA256Hex(reference); !ok {
			return "", "", "", ErrRequest
		}
	case "tags":
		if reference != "list" {
			return "", "", "", ErrRequest
		}
	default:
		return "", "", "", ErrRequest
	}
	return image, operation, reference, nil
}

func (c *RegistryContent) prepare(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	u *item.CacheUpload,
) error {
	image, operation, reference, err := registryResource(q.Resource.UpstreamPath)
	if err != nil {
		return err
	}
	if operation == "blobs" {
		return c.prepareBlob(permit, opts.Revision, image, reference, u)
	}
	limit := int64(layout.MaxManifestSize)
	if operation == "tags" {
		limit = 16 << 20
	}
	body, err := io.ReadAll(io.LimitReader(u.Body, limit+1))
	if err != nil {
		return fmt.Errorf("read registry metadata: %w", err)
	}
	if int64(len(body)) > limit {
		return item.ErrRemoteLimit
	}
	if u.Length >= 0 && u.Length != int64(len(body)) {
		return item.ErrRemoteLength
	}
	u.Body, u.MaxBytes = bytes.NewReader(body), limit
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if (u.Expected.SHA256 != "" && u.Expected.SHA256 != digest) ||
		(u.Declared.SHA256 != "" && u.Declared.SHA256 != digest) {
		return item.ErrChecksumMismatch
	}
	u.Expected.SHA256 = digest
	if operation == "tags" {
		var list struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if err = json.Unmarshal(body, &list); err != nil {
			return ErrRequest
		}
		for _, tag := range list.Tags {
			if !layout.ValidTag(tag) || layout.ReservedTag(tag) {
				return ErrRequest
			}
		}
		return nil
	}
	manifest, err := layout.ParseManifest(u.ContentType, body, digest)
	if err != nil {
		return ErrRequest
	}
	if operation == "referrers" && !manifest.Index() {
		return ErrRequest
	}
	if err = c.validateDescriptors(ctx, q, opts, manifest); err != nil {
		return err
	}
	if operation == "manifests" {
		if d, ok := c.descriptor(
			permit.Identity,
			opts.Revision,
			image,
			reference,
		); ok {
			if d.Size != int64(len(body)) {
				return item.ErrRemoteLength
			}
			if d.MediaType != manifest.MediaType {
				return ErrRequest
			}
		}
	}
	u.ContentType = manifest.MediaType
	if operation == "manifests" {
		c.remember(permit.Identity, opts.Revision, image, manifest)
		config, childErr := c.children(ctx, q, permit, opts, image, manifest)
		if childErr != nil {
			return childErr
		}
		folder := reference
		if h, ok := layout.SHA256Hex(reference); ok {
			folder = layout.DigestFolder(h)
		}
		u.Properties = layout.Properties(manifest, image, folder, config)
		u.PublishMetadata = true
	}
	return nil
}

func (c *RegistryContent) prepareBlob(
	permit source.Permit,
	revision, image, reference string,
	u *item.CacheUpload,
) error {
	// Protocol digest headers remain integrity evidence when optional generic
	// checksum headers are configured to be ignored.
	if u.Declared.SHA256 != "" && u.Declared.SHA256 != u.Expected.SHA256 {
		return item.ErrChecksumMismatch
	}
	if d, ok := c.descriptor(permit.Identity, revision, image, reference); ok {
		u.MaxBytes = max(1, d.Size)
		u.Validate = func(_ context.Context, _ io.ReadSeeker, size int64) error {
			if size != d.Size {
				return item.ErrRemoteLength
			}
			return nil
		}
	}
	return nil
}

func (c *RegistryContent) validateDescriptors(
	ctx context.Context,
	q source.Request,
	opts Options,
	m layout.Manifest,
) error {
	descriptors := append(append([]layout.Descriptor(nil), m.Layers...), m.Manifests...)
	if m.Config != nil {
		descriptors = append(descriptors, *m.Config)
	}
	if m.Subject != nil {
		descriptors = append(descriptors, *m.Subject)
	}
	if len(descriptors) > 4096 {
		return ErrRequest
	}
	for _, d := range descriptors {
		if _, ok := layout.SHA256Hex(
			d.Digest,
		); !ok || d.Size < 0 || d.Size == math.MaxInt64 ||
			len(d.URLs) > 16 {
			return ErrRequest
		}
		for _, raw := range d.URLs {
			if err := registryExternal(ctx, q.Repo, opts, raw); err != nil {
				return err
			}
		}
	}
	return nil
}

// RegistryResource fixes cache and authorization paths independently of an
// upstream namespace prefix. Manifest Accept is canonical so supported clients
// share exact bytes; delivery separately checks each caller's accepted types.
func RegistryResource(image, operation, reference string) (source.Resource, error) {
	upstream := image + "/" + operation + "/" + reference
	if _, _, _, err := registryResource(upstream); err != nil {
		return source.Resource{}, err
	}
	r := source.Resource{
		Method:       http.MethodGet,
		UpstreamPath: upstream,
		Variant:      "registry-v1",
		Kind:         source.Artifact,
	}
	switch operation {
	case "manifests":
		r.Accept = strings.Join(
			[]string{
				layout.MediaTypeOCIIndex,
				layout.MediaTypeOCIManifest,
				layout.MediaTypeDockerList,
				layout.MediaTypeDockerManifest,
			},
			", ",
		)
		folder := reference
		if h, ok := layout.SHA256Hex(reference); ok {
			folder = layout.DigestFolder(h)
			r.Checksums = types.Checksums{SHA256: h}
		} else {
			r.Kind = source.Metadata
		}
		r.Path = image + "/" + folder + "/" + layout.ManifestFile
	case "blobs":
		h, _ := layout.SHA256Hex(reference)
		r.Path = layout.UploadPath(image, h)
		r.Checksums = types.Checksums{SHA256: h}
	case "tags":
		r.Path = image + "/_remote/tags.json"
		r.Kind = source.Versions
		r.PermissionFolder, r.PermissionPath = true, image
	case "referrers":
		h, _ := layout.SHA256Hex(reference)
		r.Path = image + "/_remote/referrers/" + h + ".json"
		r.Kind = source.Metadata
		r.PermissionFolder, r.PermissionPath = true, image
		r.Accept = layout.MediaTypeOCIIndex
	}
	return r, nil
}
