package docker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel/antpath"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (c *RegistryContent) Read(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
) (source.Result, error) {
	out, err := c.Policy.Read(ctx, q, permit)
	if err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return out, err
	}
	image, operation, reference, err := ParseRegistryPath(q.Resource.UpstreamPath)
	if err != nil || operation != "manifests" && operation != "blobs" {
		return out, err
	}
	if permit.Intent == source.RetrievalIntentCacheOnly || !permit.UpstreamNetworkAllowed ||
		enabled(q.Repo.Offline) {
		return out, nil
	}
	opts, err := c.Policy.Options(ctx, q.Repo)
	if err == nil && out.Revalidate != nil {
		err = out.Revalidate(ctx)
	}
	if err != nil {
		if out.Stream != nil {
			_ = out.Stream.Body.Close()
		}
		return source.Result{}, source.ErrSuperseded
	}

	if operation == "blobs" {
		if d, ok := c.descriptor(permit.Identity, opts.Revision, image, reference); ok {
			size := int64(0)
			if out.Item != nil {
				size = out.Item.Size
			} else {
				size = out.Stream.Size
			}
			if size != d.Size {
				if out.Stream != nil {
					_ = out.Stream.Body.Close()
				}
				return source.Result{}, source.ErrInvalidResult
			}
		}
	}
	if operation != "manifests" {
		return out, nil
	}
	var reader io.ReadSeeker
	contentType := ""
	if out.Item != nil {
		body, _, openErr := c.Policy.Blobs.Open(ctx, out.Item.Actual.SHA256)
		if openErr != nil {
			return source.Result{}, source.ErrUnavailable
		}
		defer body.Close()
		reader = body
		props, propsErr := c.Policy.Store.Items().Properties(ctx, out.Item.ID)
		if propsErr != nil {
			return source.Result{}, source.ErrUnavailable
		}
		if values := props[ManifestTypeProperty]; len(values) > 0 {
			contentType = values[0]
		}
	} else {
		contentType = out.Stream.MediaType
		var ok bool
		reader, ok = out.Stream.Body.(io.ReadSeeker)
		if !ok {
			_ = out.Stream.Body.Close()
			return source.Result{}, source.ErrInvalidResult
		}
	}
	body, err := io.ReadAll(io.LimitReader(reader, MaxManifestSize+1))
	if err == nil {
		_, err = reader.Seek(0, io.SeekStart)
	}
	var m Manifest
	if err == nil {
		m, err = ParseManifest(contentType, body, "")
	}
	if err != nil {
		if out.Stream != nil {
			_ = out.Stream.Body.Close()
		}
		return source.Result{}, source.ErrInvalidResult
	}
	c.remember(permit.Identity, opts.Revision, image, m)
	return out, nil
}

func descriptorKey(id types.SourceIdentity, revision, image, digest string) string {
	b, _ := json.Marshal([]any{id, revision, image, digest})
	return string(b)
}

func (c *RegistryContent) remember(
	id types.SourceIdentity,
	revision, image string,
	m Manifest,
) {
	descriptors := append(append([]Descriptor(nil), m.Layers...), m.Manifests...)
	if m.Config != nil {
		descriptors = append(descriptors, *m.Config)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.descriptors == nil {
		c.descriptors = make(map[string]Descriptor)
	}
	if len(c.descriptors)+len(descriptors) > 4096 {
		clear(c.descriptors)
	}
	for _, d := range descriptors {
		c.descriptors[descriptorKey(id, revision, image, d.Digest)] = d
	}
}

func (c *RegistryContent) descriptor(
	id types.SourceIdentity,
	revision, image, digest string,
) (Descriptor, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.descriptors[descriptorKey(id, revision, image, digest)]
	return d, ok
}

func registryExternal(ctx context.Context, c repo.Config, opts remote.Options, raw string) error {
	if !enabled(c.ExternalDependenciesEnabled) || opts.CheckURL == nil {
		return remote.ErrSecurity
	}
	u, err := remote.CheckedURL(raw)
	if err != nil {
		return remote.ErrSecurity
	}
	allowed := false
	for _, pattern := range c.ExternalDependenciesPatterns {
		if pattern != "" && antpath.CompilePattern(pattern).Match(raw) {
			allowed = true
			break
		}
	}
	if !allowed {
		return remote.ErrSecurity
	}
	//nolint:wrapcheck // preserves upstream error identity
	return opts.CheckURL(ctx, u)
}

func (c *RegistryContent) external(
	ctx context.Context,
	config repo.Config,
	opts remote.Options,
	raw string,
) (*http.Response, error) {
	if err := registryExternal(ctx, config, opts, raw); err != nil {
		return nil, err
	}
	originalPrepare, originalRedirect := opts.Prepare, opts.CheckRedirect
	opts.Prepare = func(req *http.Request) error {
		if err := registryExternal(req.Context(), config, opts, req.URL.String()); err != nil {
			return err
		}
		if originalPrepare != nil {
			if err := originalPrepare(req); err != nil {
				return err
			}
		}
		// A metadata-selected foreign layer never inherits registry credentials or
		// custom secret headers, even under allowAnyHostAuth.
		for key := range req.Header {
			switch key {
			case "Accept", "Accept-Encoding", "If-None-Match", "If-Modified-Since":
			default:
				req.Header.Del(key)
			}
		}
		return nil
	}
	opts.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := registryExternal(req.Context(), config, opts, req.URL.String()); err != nil {
			return err
		}
		if originalRedirect != nil {
			return originalRedirect(req, via)
		}
		return nil
	}
	opts.Jar = nil
	//nolint:wrapcheck // preserves upstream error identity
	return c.Registries.Foreign(
		ctx,
		config,
		opts,
		remote.Request{Method: http.MethodGet, ExternalURL: raw},
	)
}

func registryNext(
	client remote.RegistryClient,
	image, operation, reference, link string,
) (string, error) {
	if len(link) > 8192 {
		return "", remote.ErrRequest
	}
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		end := strings.IndexByte(part, '>')
		if !strings.HasPrefix(part, "<") || end < 1 {
			return "", remote.ErrRequest
		}
		next, err := url.Parse(part[1:end])
		if err != nil {
			return "", remote.ErrRequest
		}
		upstream, err := client.Image(image)
		if err != nil {
			//nolint:wrapcheck // preserves upstream error identity
			return "", err
		}
		expected := strings.TrimRight(
			client.Base().Path,
			"/",
		) + "/v2/" + upstream + "/" + operation + "/" + reference
		base := *client.Base()
		base.Path = expected
		next = base.ResolveReference(next)
		if remote.Origin(next) != remote.Origin(client.Base()) || next.Path != expected ||
			next.User != nil ||
			next.Fragment != "" ||
			next.RawQuery == "" {
			return "", remote.ErrSecurity
		}
		values, err := url.ParseQuery(next.RawQuery)
		if err != nil {
			return "", remote.ErrRequest
		}
		for key := range values {
			if key != "n" && key != "last" && key != "artifactType" {
				return "", remote.ErrRequest
			}
		}
		return next.RawQuery, nil
	}
	return "", nil
}

// enabled reports a repository switch that is set and on, as core remote's
// own helper does; an unset switch is off.
func enabled(v *bool) bool { return v != nil && *v }
