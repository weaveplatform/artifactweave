package terraform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	tf "github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (h handler) moduleMetadata(
	r *http.Request,
	path, target, folder, variant string,
	location bool,
	validate func(io.Reader, string) error,
) (source.Result, error) {
	resource := source.Resource{
		Kind:                   source.Metadata,
		Path:                   path,
		ExternalURL:            target,
		Method:                 http.MethodGet,
		PermissionFolder:       true,
		PermissionPath:         folder,
		Variant:                variant,
		Accept:                 "application/json",
		ExpectedContentType:    "application/json",
		ModuleDownloadLocation: location,
	}
	resource.ValidateMetadata = func(_ context.Context, body io.ReadSeeker, _ int64, m source.MetadataResponse) error {
		return validate(body, m.BaseURL)
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return result, fmt.Errorf("terraform module metadata: %w", err)
	}
	return result, nil
}

func closeModuleResult(result source.Result) {
	if result.Stream != nil {
		_ = result.Stream.Body.Close()
	}
}

func (h handler) moduleService(r *http.Request, folder string) (string, source.Result, error) {
	c := pkgroute.RequestFrom(r).Repo
	base, err := moduleURL(
		c.TerraformRegistryURL,
		strings.TrimRight(c.TerraformRegistryURL, "/")+"/",
	)
	if err != nil {
		return "", source.Result{}, err
	}
	u, _ := url.Parse(base)
	if u.RawQuery != "" {
		return "", source.Result{}, remoteModuleError()
	}
	// A configured API prefix (including Smart Remote) is already a registry
	// base. Host-root configurations use modules.v1 discovery with the standard
	// /v1/modules/ fallback only when discovery is absent.
	if u.Path != "/" {
		return base + "v1/modules/", source.Result{}, nil
	}
	target := base + ".well-known/terraform.json"
	result, err := h.moduleMetadata(
		r,
		"module-metadata/discovery",
		target,
		folder,
		"terraform-discovery-v1",
		false,
		func(body io.Reader, actual string) error {
			target, e := parseModuleDiscovery(body, actual)
			if e != nil {
				return e
			}
			return allowModuleURL(c, target)
		},
	)
	if err != nil {
		var api *apierror.Error
		if errors.As(err, &api) && api.Status == 404 {
			return base + "v1/modules/", source.Result{}, nil
		}
		return "", source.Result{}, err
	}
	defer closeModuleResult(result)
	if result.Stream == nil {
		return "", source.Result{}, remoteModuleError()
	}
	service, e := parseModuleDiscovery(result.Stream.Body, result.Metadata.BaseURL)
	if e != nil {
		return "", source.Result{}, e
	}
	if e = allowModuleURL(c, service); e != nil {
		return "", source.Result{}, e
	}
	return service, result, nil
}

func (h handler) upstreamModuleVersions(
	r *http.Request,
	m tf.Module,
) ([]string, source.Result, error) {
	service, discovery, err := h.moduleService(r, m.Package())
	if err != nil {
		return nil, source.Result{}, err
	}
	result, err := h.moduleMetadata(
		r,
		"module-metadata/"+m.Package()+"/versions",
		service+escapeModulePath(m.Package())+"/versions",
		m.Package(),
		"terraform-module-versions-v1",
		false,
		func(body io.Reader, _ string) error { _, e := parseModuleVersions(body, m); return e },
	)
	if err != nil {
		return nil, result, err
	}
	defer closeModuleResult(result)
	result.Revalidate = combineModuleFences([]source.Result{discovery, result}, nil)
	result.Used = combineModuleUsage([]source.Result{discovery, result})
	if result.Stream == nil {
		return nil, result, remoteModuleError()
	}
	versions, err := parseModuleVersions(result.Stream.Body, m)
	return versions, result, err
}

func (h handler) upstreamModuleLocation(
	r *http.Request,
	m tf.Module,
) (moduleSource, source.Result, error) {
	service, discovery, err := h.moduleService(r, m.Package())
	if err != nil {
		return moduleSource{}, source.Result{}, err
	}
	c := pkgroute.RequestFrom(r).Repo
	endpoint := service + escapeModulePath(
		m.Package(),
	) + "/" + url.PathEscape(
		m.Version,
	) + "/download"
	result, err := h.moduleMetadata(
		r,
		"module-metadata/"+m.Package()+"/"+m.Version+"/download",
		endpoint,
		m.Package(),
		"terraform-module-location-v1",
		true,
		func(body io.Reader, actual string) error { _, e := parseModuleLocation(body, actual, c); return e },
	)
	if err != nil {
		return moduleSource{}, result, err
	}
	defer closeModuleResult(result)
	result.Revalidate = combineModuleFences([]source.Result{discovery, result}, nil)
	result.Used = combineModuleUsage([]source.Result{discovery, result})
	if result.Stream == nil {
		return moduleSource{}, result, remoteModuleError()
	}
	location, err := parseModuleLocation(result.Stream.Body, result.Metadata.BaseURL, c)
	return location, result, err
}

func (h handler) remoteModule(w http.ResponseWriter, r *http.Request, q operation) {
	versions, result, err := h.upstreamModuleVersions(r, q.module)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if q.list {
		h.remoteModuleVersions(w, r, q.module, versions, result)
		return
	}
	exists := false
	for _, v := range versions {
		if v == q.module.Version {
			exists = true
			break
		}
	}
	if !exists {
		h.k.Error(w, r, missing())
		return
	}
	location, metadata, err := h.upstreamModuleLocation(r, q.module)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	path := location.cachePath(q.module)
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	resource := moduleArtifactResource(r, q.module, location)
	resource.Method = http.MethodGet // issuing a signed digest requires validated complete bytes, including HEAD
	artifact, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	defer closeModuleResult(artifact)
	digest := moduleDigest(artifact)
	if digest == "" {
		h.k.Error(w, r, remoteModuleError())
		return
	}
	selected, err := h.moduleDirectory(r, artifact, location)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	public, err := h.remoteDownloadURL(
		r,
		path,
		digest,
		artifact.Identity,
		selected,
		location.Archive,
	)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	revalidate := combineModuleFences([]source.Result{result, metadata, artifact}, []string{path})
	control := source.Result{
		Stream: &source.Stream{
			Body:           http.NoBody,
			Status:         204,
			Representation: source.Representation{Size: 0},
		},
		Revalidate: revalidate,
	}
	response := &moduleLocationWriter{ResponseWriter: w, location: public}
	h.k.ServeResult(response, r, control)
	if response.status == 204 && r.Method == http.MethodGet {
		for _, entry := range []source.Result{result, metadata, artifact} {
			if entry.Used != nil {
				_ = entry.Used(r.Context())
			}
		}
	}
}

func (h handler) remoteModuleVersions(
	w http.ResponseWriter,
	r *http.Request,
	m tf.Module,
	versions []string,
	result source.Result,
) {
	if len(versions) > moduleDiscoveryLimit {
		h.k.Error(
			w,
			r,
			apierror.New(
				502,
				"upstream module has too many versions for bounded archive-path discovery",
			),
		)
		return
	}
	out := []map[string]string{}
	paths := []string{}
	results := []source.Result{result}
	for _, v := range versions {
		m.Version = v
		location, metadata, err := h.upstreamModuleLocation(r, m)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		path := location.cachePath(m)
		if !h.k.Can(r, path, types.ActionRead) {
			continue
		}
		paths = append(paths, path)
		out = append(out, map[string]string{"version": v})
		results = append(results, metadata)
	}
	if len(out) == 0 {
		h.k.Error(w, r, missing())
		return
	}
	body, _ := json.Marshal(map[string]any{"modules": []any{map[string]any{"versions": out}}})
	sum := sha256.Sum256(body)
	result.Stream = &source.Stream{
		Body:   &moduleBody{bytes.NewReader(body)},
		Status: 200,
		Representation: source.Representation{
			Size:      int64(len(body)),
			MediaType: "application/json",
			ETag:      `"` + hex.EncodeToString(sum[:]) + `"`,
		},
	}
	result.Revalidate = combineModuleFences(results, paths)
	h.k.ServeResult(w, r, result)
}

func combineModuleFences(results []source.Result, paths []string) func(context.Context) error {
	return func(ctx context.Context) error {
		for _, result := range results {
			if result.Revalidate != nil {
				if err := result.Revalidate(ctx); err != nil {
					return fmt.Errorf("terraform module delivery: %w", err)
				}
			}
		}
		if len(results) > 0 && results[0].CheckRead != nil {
			for _, path := range paths {
				if err := results[0].CheckRead(ctx, path); err != nil {
					return fmt.Errorf("terraform module delivery: %w", err)
				}
			}
		}
		return nil
	}
}

type moduleBody struct{ *bytes.Reader }

func (*moduleBody) Close() error { return nil }

type moduleLocationWriter struct {
	http.ResponseWriter
	location string
	status   int
}

func (w *moduleLocationWriter) WriteHeader(status int) {
	w.status = status
	if status == 204 {
		w.Header().Set("X-Terraform-Get", w.location)
	}
	w.ResponseWriter.WriteHeader(status)
}

func escapeModulePath(path string) string {
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func moduleDigest(result source.Result) string {
	if result.Item != nil {
		return result.Item.Actual.SHA256
	}
	if result.Stream != nil {
		return result.Stream.Checksums.SHA256
	}
	return ""
}

func moduleArtifactResource(r *http.Request, m tf.Module, s moduleSource) source.Resource {
	resource := pkgroute.Resource(r, s.cachePath(m), "")
	resource.Query = ""
	resource.Accept = "application/octet-stream"
	resource.ExternalURL = s.Target
	resource.Checksums = s.Checksums
	descriptor, _ := json.Marshal(s)
	resource.Variant = "terraform-module-archive-v1:" + string(descriptor)
	resource.Validate = s.validateArchive
	return resource
}

func (h handler) remoteSignedModule(w http.ResponseWriter, r *http.Request, c claims) {
	if h.o.Auth == nil || c.Credential == nil {
		h.k.Error(w, r, forbidden)
		return
	}
	principal, err := h.o.Auth.RestoreDownload(
		r.Context(),
		c.Subject,
		*c.Credential,
		h.k.DB().Tokens(),
	)
	if err == nil {
		principal, err = auth.Authorize(
			r.Context(),
			authz.Sources{
				Permissions:      h.k.DB().Permissions(),
				Groups:           h.k.DB().Groups(),
				AnonymousEnabled: principal.Anonymous,
			},
			principal,
		)
	}
	if err != nil {
		h.k.Error(w, r, forbidden)
		return
	}
	ctx := auth.WithPrincipal(r.Context(), principal)
	r = r.WithContext(ctx)
	if !h.k.Require(w, r, c.Path, types.ActionRead) {
		return
	}
	slash := strings.LastIndexByte(c.Path, '/')
	if slash < 0 {
		h.k.Error(w, r, forbidden)
		return
	}
	ext := moduleArchiveType(c.Path)
	if ext == "" {
		h.k.Error(w, r, forbidden)
		return
	}
	m, ok := tf.ParseModule(c.Path[:len(c.Path)-len(ext)] + "zip")
	if !ok || c.Source == nil {
		h.k.Error(w, r, forbidden)
		return
	}
	identity, err := h.o.RemoteSources.Resolve(ctx, pkgroute.RequestFrom(r).Repo)
	if err != nil || identity != *c.Source {
		h.k.Error(w, r, forbidden)
		return
	}
	location, metadata, err := h.upstreamModuleLocation(r, m)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if location.cachePath(m) != c.Path || metadata.Identity != *c.Source {
		h.k.Error(w, r, forbidden)
		return
	}
	resource := moduleArtifactResource(r, m, location)
	resource.Method = http.MethodGet
	if resource.Checksums.SHA256 != "" && !strings.EqualFold(resource.Checksums.SHA256, c.SHA256) {
		h.k.Error(w, r, forbidden)
		return
	}
	resource.Checksums.SHA256 = c.SHA256
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if moduleDigest(result) != c.SHA256 || result.Identity != *c.Source {
		closeModuleResult(result)
		h.k.Error(w, r, forbidden)
		return
	}
	result.Revalidate = combineModuleFences([]source.Result{metadata, result}, []string{c.Path})
	h.k.ServeResult(w, r, result)
}

// Terraform's registry installer appends a literal directory after retrieving
// the complete package. Resolve upstream getter globs against validated bytes
// here, while preserving the original package and all sibling modules.
func (h handler) moduleDirectory(
	r *http.Request,
	result source.Result,
	s moduleSource,
) (string, error) {
	if s.Subdir == "" {
		return "", nil
	}
	var body io.ReadSeeker
	var size int64
	switch {
	case result.Item != nil:
		opened, info, err := h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return "", remoteModuleError()
		}
		defer opened.Close()
		body, size = opened, info.Size
	case result.Stream != nil:
		var ok bool
		body, ok = result.Stream.Body.(io.ReadSeeker)
		if !ok {
			return "", remoteModuleError()
		}
		size = result.Stream.Size
	default:
		return "", remoteModuleError()
	}
	var selected string
	err := validateSelectedArchive(
		r.Context(),
		archive.ReaderAt(body),
		size,
		"module."+s.Archive,
		remoteArchiveEntries,
		remoteArchiveInflated,
		s.Subdir,
		&selected,
	)
	return selected, err
}

func combineModuleUsage(results []source.Result) func(context.Context) error {
	return func(ctx context.Context) error {
		for _, r := range results {
			if r.Used != nil {
				if err := r.Used(ctx); err != nil {
					return fmt.Errorf("terraform module delivery: %w", err)
				}
			}
		}
		return nil
	}
}
