package docker

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
	"regexp"
	"strings"
	"sync"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RegistryContent is the Docker, OCI and Helm OCI rules of remote repositories
// (decision 0031): it fetches manifests, blobs, tag lists and referrers from
// the upstream registry, checks every descriptor and foreign layer before
// the cache publishes them, and joins paginated listings into one. Policy is
// the shared policy of registry remotes, apart from the ordinary one;
// Registries opens the upstream clients, whose pool and credentials belong to
// the application lifetime. It remembers the descriptors of the manifests it
// reads, by source identity and credential revision, so that a blob or a
// child manifest is held to the descriptor that named it. It is safe for
// concurrent use.
type RegistryContent struct {
	Policy      *remote.Policy
	Registries  remote.Registries
	mu          sync.Mutex
	descriptors map[string]Descriptor
}

// Install makes c the policy's upstream fetch and its check of every cache
// upload, replacing both hooks. Call it before anything wraps the policy's
// PrepareUpload, as smart.NewSmart does, and before the policy serves a
// request.
func (c *RegistryContent) Install() {
	c.Policy.Fetch = c.fetch
	c.Policy.PrepareUpload = c.prepare
}

func (c *RegistryContent) fetch(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts remote.Options,
	request remote.Request,
) (*http.Response, error) {
	image, operation, reference, err := ParseRegistryPath(q.Resource.UpstreamPath)
	if err != nil {
		return nil, err
	}
	client, err := c.Registries.Open(ctx, q.Repo, permit.Identity, opts.Revision)
	if err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return nil, err
	}
	request.Path = operation + "/" + reference
	// A native HEAD resolves a verified complete representation. Shared ingestion
	// bounds metadata and spools blobs; a header alone cannot prove digest integrity.
	request.Method = http.MethodGet
	res, err := client.Do(ctx, image, request)
	if err != nil {
		if errors.Is(err, remote.ErrRegistryDenied) {
			return nil, &source.HTTPError{
				Kind:   source.FailureKindHTTP,
				Status: http.StatusUnauthorized,
			}
		}
		//nolint:wrapcheck // preserves upstream error identity
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
		value, ok := SHA256Hex(digest)
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

// registryName is the Distribution name grammar the registry transport's
// (*remote.Registry).Image checks before it sends a request. The parser must
// accept exactly the names the transport does, or a cached path could name an
// image the upstream is never asked for; Image also refuses a name over 255
// characters, which this grammar leaves to it.
// TestUnitRegistryPathNameGrammarMatchesTransport holds the two together.
var registryName = regexp.MustCompile(
	`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`,
)

// ParseRegistryPath splits a registry API path, <image>/<operation>/<reference>,
// as RegistryResource builds it for a remote read, into its image name,
// operation and reference. The image must match the Distribution name
// grammar the transport sends; the operation must be manifests with a tag or
// a sha256 digest, blobs or referrers with a sha256 digest, or tags with
// list. Anything else is remote.ErrRequest. Smart Remote is given it as its
// registry path parser (smart.NewSmart), so that core remote never imports
// this package. It reads neither store nor network.
func ParseRegistryPath(path string) (image, operation, reference string, err error) {
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return "", "", "", remote.ErrRequest
	}
	image = strings.Join(parts[:len(parts)-2], "/")
	operation, reference = parts[len(parts)-2], parts[len(parts)-1]
	if !registryName.MatchString(image) {
		return "", "", "", remote.ErrRequest
	}
	switch operation {
	case "manifests":
		if _, ok := SHA256Hex(
			reference,
		); !ok &&
			(!ValidTag(reference) || ReservedTag(reference)) {
			return "", "", "", remote.ErrRequest
		}
	case "blobs", "referrers":
		if _, ok := SHA256Hex(reference); !ok {
			return "", "", "", remote.ErrRequest
		}
	case "tags":
		if reference != "list" {
			return "", "", "", remote.ErrRequest
		}
	default:
		return "", "", "", remote.ErrRequest
	}
	return image, operation, reference, nil
}

func (c *RegistryContent) prepare(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts remote.Options,
	u *item.CacheUpload,
) error {
	image, operation, reference, err := ParseRegistryPath(q.Resource.UpstreamPath)
	if err != nil {
		return err
	}
	if operation == "blobs" {
		return c.prepareBlob(permit, opts.Revision, image, reference, u)
	}
	limit := int64(MaxManifestSize)
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
			return remote.ErrRequest
		}
		for _, tag := range list.Tags {
			if !ValidTag(tag) || ReservedTag(tag) {
				return remote.ErrRequest
			}
		}
		return nil
	}
	manifest, err := ParseManifest(u.ContentType, body, digest)
	if err != nil {
		return remote.ErrRequest
	}
	if operation == "referrers" && !manifest.IsIndex() {
		return remote.ErrRequest
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
				return remote.ErrRequest
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
		if h, ok := SHA256Hex(reference); ok {
			folder = DigestFolder(h)
		}
		u.Properties = Properties(manifest, image, folder, config)
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
	opts remote.Options,
	m Manifest,
) error {
	descriptors := append(append([]Descriptor(nil), m.Layers...), m.Manifests...)
	if m.Config != nil {
		descriptors = append(descriptors, *m.Config)
	}
	if m.Subject != nil {
		descriptors = append(descriptors, *m.Subject)
	}
	if len(descriptors) > 4096 {
		return remote.ErrRequest
	}
	for _, d := range descriptors {
		if _, ok := SHA256Hex(
			d.Digest,
		); !ok || d.Size < 0 || d.Size == math.MaxInt64 ||
			len(d.URLs) > 16 {
			return remote.ErrRequest
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
	if _, _, _, err := ParseRegistryPath(upstream); err != nil {
		return source.Resource{}, err
	}
	r := source.Resource{
		Method:       http.MethodGet,
		UpstreamPath: upstream,
		Variant:      "registry-v1",
		Kind:         source.ResourceKindArtifact,
	}
	switch operation {
	case "manifests":
		r.Accept = strings.Join(
			[]string{
				MediaTypeOCIIndex,
				MediaTypeOCIManifest,
				MediaTypeDockerList,
				MediaTypeDockerManifest,
			},
			", ",
		)
		folder := reference
		if h, ok := SHA256Hex(reference); ok {
			folder = DigestFolder(h)
			r.Checksums = types.Checksums{SHA256: h}
		} else {
			r.Kind = source.ResourceKindMetadata
		}
		r.Path = image + "/" + folder + "/" + ManifestFile
	case "blobs":
		h, _ := SHA256Hex(reference)
		r.Path = UploadPath(image, h)
		r.Checksums = types.Checksums{SHA256: h}
	case "tags":
		r.Path = image + "/_remote/tags.json"
		r.Kind = source.ResourceKindVersions
		r.PermissionFolder, r.PermissionPath = true, image
	case "referrers":
		h, _ := SHA256Hex(reference)
		r.Path = image + "/_remote/referrers/" + h + ".json"
		r.Kind = source.ResourceKindMetadata
		r.PermissionFolder, r.PermissionPath = true, image
		r.Accept = MediaTypeOCIIndex
	}
	return r, nil
}
