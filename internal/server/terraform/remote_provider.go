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
	tf "github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (h handler) providerMetadata(
	r *http.Request,
	path, target, folder, variant string,
	validate func(io.Reader, string) error,
) (source.Result, error) {
	resource := source.Resource{
		Kind:                source.ResourceKindMetadata,
		Path:                path,
		ExternalURL:         target,
		Method:              http.MethodGet,
		PermissionFolder:    true,
		PermissionPath:      folder,
		Variant:             variant + ":url-policy-v2",
		Accept:              "application/json",
		ExpectedContentType: "application/json",
		CheckURL:            providerURLGuard(pkgroute.RequestFrom(r).Repo, target),
	}
	resource.ValidateMetadata = func(_ context.Context, body io.ReadSeeker, _ int64, m source.MetadataResponse) error {
		return validate(body, m.BaseURL)
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return result, providerReadError(
			err,
			"metadata could not be retrieved or validated; check its schema, configured destinations and external-host policy",
		)
	}
	return result, nil
}

// Only adapter-owned stage descriptions cross the error boundary. Upstream
// errors can contain URLs, credentials or bodies and remain sanitized by source.
func providerReadError(err error, message string) error {
	var failure *apierror.Error
	if errors.As(err, &failure) && failure.Status == http.StatusBadGateway {
		return remoteProviderError(message)
	}
	return err
}

func (h handler) providerService(r *http.Request, folder string) (string, source.Result, error) {
	c := pkgroute.RequestFrom(r).Repo
	base, err := moduleURL(
		c.TerraformRegistryURL,
		strings.TrimRight(c.TerraformRegistryURL, "/")+"/",
	)
	if err != nil {
		return "", source.Result{}, remoteProviderError("invalid configured registry URL")
	}
	u, _ := url.Parse(base)
	if u.RawQuery != "" {
		return "", source.Result{}, remoteProviderError("invalid configured registry URL")
	}
	if u.Path != "/" {
		return base + "v1/providers/", source.Result{}, nil
	}
	result, err := h.providerMetadata(
		r,
		"provider-metadata/discovery",
		base+".well-known/terraform.json",
		folder,
		"terraform-provider-discovery-v1",
		func(body io.Reader, actual string) error {
			target, e := parseProviderDiscovery(body, actual)
			if e != nil {
				return e
			}
			return allowProviderURL(c, target)
		},
	)
	if err != nil {
		var api *apierror.Error
		if errors.As(err, &api) && api.Status == 404 {
			return base + "v1/providers/", source.Result{}, nil
		}
		return "", source.Result{}, err
	}
	defer closeModuleResult(result)
	if result.Stream == nil {
		return "", result, remoteProviderError("missing discovery bytes")
	}
	target, err := parseProviderDiscovery(result.Stream.Body, result.Metadata.BaseURL)
	if err == nil {
		err = allowProviderURL(c, target)
	}
	result.Stream = nil
	return target, result, err
}

func (h handler) upstreamProviderVersions(
	r *http.Request,
	q providerOp,
) ([]remoteProviderVersion, source.Result, error) {
	service, discovery, err := h.providerService(r, q.name)
	if err != nil {
		return nil, source.Result{}, err
	}
	result, err := h.providerMetadata(
		r,
		"provider-metadata/"+q.name+"/versions",
		service+escapeModulePath(q.name)+"/versions",
		q.name,
		"terraform-provider-versions-v1",
		func(body io.Reader, _ string) error { _, e := parseRemoteProviderVersions(body, q); return e },
	)
	if err != nil {
		return nil, result, err
	}
	defer closeModuleResult(result)
	if result.Stream == nil {
		return nil, result, remoteProviderError("missing version bytes")
	}
	versions, err := parseRemoteProviderVersions(result.Stream.Body, q)
	result.Revalidate = combineModuleFences([]source.Result{discovery, result}, nil)
	result.Used = combineModuleUsage([]source.Result{discovery, result})
	result.Stream = nil
	return versions, result, err
}

func (h handler) upstreamProviderPackage(
	r *http.Request,
	q providerOp,
) (remoteProviderPackage, source.Result, string, error) {
	service, discovery, err := h.providerService(r, q.name)
	if err != nil {
		return remoteProviderPackage{}, source.Result{}, "", err
	}
	c := pkgroute.RequestFrom(r).Repo
	suffix := escapeModulePath(
		q.name,
	) + "/" + url.PathEscape(
		q.version,
	) + "/download/" + q.os + "/" + q.arch
	result, err := h.providerMetadata(
		r,
		"provider-metadata/"+suffix,
		service+suffix,
		q.name,
		"terraform-provider-package-v1",
		func(body io.Reader, actual string) error {
			_, e := parseRemoteProviderPackage(body, actual, c, q)
			return e
		},
	)
	if err != nil {
		return remoteProviderPackage{}, result, "", err
	}
	defer closeModuleResult(result)
	if result.Stream == nil {
		return remoteProviderPackage{}, result, "", remoteProviderError("missing package bytes")
	}
	metadata, err := parseRemoteProviderPackage(result.Stream.Body, result.Metadata.BaseURL, c, q)
	digest := moduleDigest(result)
	result.Revalidate = combineModuleFences([]source.Result{discovery, result}, nil)
	result.Used = combineModuleUsage([]source.Result{discovery, result})
	result.Stream = nil
	return metadata, result, digest, err
}

type providerBundle struct {
	q        providerOp
	metadata remoteProviderPackage
	digest   string
	results  []source.Result
	paths    []string
	h1       string
}

func (b *providerBundle) close() {
	for _, result := range b.results {
		closeModuleResult(result)
	}
}

func (h handler) providerBytes(r *http.Request, result source.Result, limit int64) ([]byte, error) {
	var body io.Reader
	switch {
	case result.Item != nil:
		opened, info, err := h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return nil, fmt.Errorf("terraform provider blob: %w", err)
		}
		defer opened.Close()
		if info.Size > limit {
			return nil, remoteProviderError("oversized checksum or signature")
		}
		body = opened
	case result.Stream != nil:
		body = result.Stream.Body
	default:
		return nil, remoteProviderError("missing package bytes")
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, remoteProviderError("invalid checksum or signature bytes")
	}
	if seeker, ok := body.(io.Seeker); ok {
		_, err = seeker.Seek(0, io.SeekStart)
	}
	if err != nil {
		return nil, fmt.Errorf("terraform provider rewind: %w", err)
	}
	return data, nil
}

func providerCompanionPath(q providerOp, signature bool) string {
	_, typ, _ := strings.Cut(q.name, "/")
	value := q.name + "/" + q.version + "/terraform-provider-" + typ + "_" + q.version + "_SHA256SUMS"
	if signature {
		value += ".sig"
	}
	return value
}

func (h handler) providerBundle(r *http.Request, q providerOp) (providerBundle, error) {
	metadata, result, digest, err := h.upstreamProviderPackage(r, q)
	b := providerBundle{q: q, metadata: metadata, digest: digest, results: []source.Result{result}}
	if err != nil {
		return b, err
	}
	b.paths = []string{
		providerArchivePath(q),
		providerCompanionPath(q, false),
		providerCompanionPath(q, true),
	}
	for _, path := range b.paths {
		if !h.k.Can(r, path, types.ActionRead) {
			return b, source.ErrDenied
		}
	}
	fetch := func(index int, target, variant string, validate func(context.Context, io.ReadSeeker, int64) error) (source.Result, error) {
		resource := pkgroute.Resource(r, b.paths[index], "")
		resource.Method = http.MethodGet
		resource.Query = ""
		resource.Accept = "application/octet-stream"
		resource.ExternalURL = target
		resource.Variant = "terraform-provider-" + variant + ":url-policy-v2"
		resource.CheckURL = providerURLGuard(pkgroute.RequestFrom(r).Repo, target)
		resource.Validate = validate
		if index == 0 {
			resource.Checksums.SHA256 = metadata.Shasum
		} else {
			resource.Mutable = true
		}
		result, err := h.k.Read(r, resource)
		stages := []string{
			"archive could not be retrieved or validated; check its SHA-256, ZIP contents and allowed destination",
			"checksum document could not be retrieved or validated; check its archive entry, SHA-256 and allowed destination",
			"checksum signature could not be retrieved or verified against an advertised upstream key; check signer identity and allowed destination",
		}
		return result, providerReadError(err, stages[index])
	}
	sumsResult, err := fetch(
		1,
		metadata.SumsURL,
		"sums-v1:"+digest,
		func(_ context.Context, body io.ReadSeeker, size int64) error {
			if size > moduleMetadataLimit {
				return remoteProviderError("oversized checksum document")
			}
			data, e := io.ReadAll(io.LimitReader(body, moduleMetadataLimit+1))
			sum, ok := tf.Shasum(data, metadata.Filename)
			if e != nil || len(data) > moduleMetadataLimit || !ok ||
				!strings.EqualFold(sum, metadata.Shasum) {
				return remoteProviderError(
					"checksum document omits the archive or disagrees with package metadata",
				)
			}
			return nil
		},
	)
	if err != nil {
		return b, err
	}
	b.results = append(b.results, sumsResult)
	sums, err := h.providerBytes(r, sumsResult, moduleMetadataLimit)
	if err != nil {
		return b, err
	}
	sumDigest := sha256.Sum256(sums)
	armors := make([]string, 0, len(metadata.SigningKeys.GPG))
	for _, key := range metadata.SigningKeys.GPG {
		armors = append(armors, key.Armor)
	}
	signatureResult, err := fetch(
		2,
		metadata.SignatureURL,
		"signature-v1:"+digest+":"+hex.EncodeToString(sumDigest[:]),
		func(_ context.Context, body io.ReadSeeker, size int64) error {
			if size > moduleMetadataLimit {
				return remoteProviderError("oversized detached signature")
			}
			signature, e := io.ReadAll(io.LimitReader(body, moduleMetadataLimit+1))
			if e != nil || len(signature) > moduleMetadataLimit {
				return remoteProviderError("invalid detached signature")
			}
			_, e = tf.Check(
				tf.Release{
					Repo:          pkgroute.RequestFrom(r).Repo.Key,
					Archive:       metadata.Filename,
					ArchiveSHA256: metadata.Shasum,
					SumsName:      "SHA256SUMS",
					Sums:          sums,
					SignatureName: "SHA256SUMS.sig",
					Signature:     signature,
				},
				armors,
			)
			if e != nil {
				return remoteProviderError(
					"checksum signature does not verify against an advertised upstream key",
				)
			}
			return nil
		},
	)
	if err != nil {
		return b, err
	}
	b.results = append(b.results, signatureResult)
	artifact, err := fetch(
		0,
		metadata.DownloadURL,
		"archive-v1:"+strings.ToLower(metadata.Shasum),
		func(ctx context.Context, body io.ReadSeeker, size int64) error {
			return (moduleSource{Archive: "zip"}).validateArchive(ctx, body, size)
		},
	)
	if err != nil {
		return b, err
	}
	b.results = append(b.results, artifact)
	if q.isMirror {
		var reader io.ReadSeeker
		var size int64
		if artifact.Item != nil {
			opened, info, e := h.k.Blobs().Open(r.Context(), artifact.Item.Actual.SHA256)
			if e != nil {
				return b, fmt.Errorf("terraform provider archive: %w", e)
			}
			defer opened.Close()
			reader, size = opened, info.Size
		} else {
			var ok bool
			reader, ok = artifact.Stream.Body.(io.ReadSeeker)
			if !ok {
				return b, remoteProviderError("archive cannot be verified")
			}
			size = artifact.Stream.Size
		}
		hash, ok, e := tf.H1(&archiveReaderAt{ctx: r.Context(), r: archive.ReaderAt(reader)}, size)
		if e != nil || !ok {
			return b, remoteProviderError("archive content hash cannot be verified")
		}
		b.h1 = hash
		if _, e = reader.Seek(0, io.SeekStart); e != nil {
			return b, fmt.Errorf("terraform provider rewind: %w", e)
		}
	}
	return b, nil
}

func (h handler) remoteProvider(w http.ResponseWriter, r *http.Request, q providerOp) {
	if q.isMirror {
		registry, err := url.Parse(pkgroute.RequestFrom(r).Repo.TerraformRegistryURL)
		if err != nil {
			h.k.Error(w, r, remoteProviderError("invalid registry URL"))
			return
		}
		q.isUpstreamMirror = strings.Trim(registry.Path, "/") != ""
		if !q.isUpstreamMirror && !strings.EqualFold(q.host, registry.Host) &&
			!strings.EqualFold(q.host, registry.Hostname()) {
			h.k.Error(w, r, missing())
			return
		}
	}
	if !h.k.RequireFolder(w, r, providerFolder(q), types.ActionRead) {
		return
	}
	if q.isUpstreamMirror {
		h.remoteProviderMirrorUpstream(w, r, q)
		return
	}
	versions, result, err := h.upstreamProviderVersions(r, q)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if q.isList {
		h.remoteProviderVersions(w, r, q, versions, result)
		return
	}
	for _, version := range versions {
		if version.Version != q.version {
			continue
		}
		if q.isMirror {
			h.remoteProviderMirror(w, r, q, version, result)
			return
		}
		bundle, e := h.providerBundle(r, q)
		defer bundle.close()
		if e != nil {
			h.k.Error(w, r, e)
			return
		}
		doc := bundle.metadata.doc
		for index, key := range []string{"download_url", "shasums_url", "shasums_signature_url"} {
			target, e := h.remoteProviderURL(r, bundle, index)
			if e != nil {
				h.k.Error(w, r, e)
				return
			}
			doc[key], _ = json.Marshal(target)
		}
		results := append([]source.Result{result}, bundle.results...)
		h.serveProviderJSON(w, r, doc, results, bundle.paths)
		return
	}
	h.k.Error(w, r, missing())
}

// Probe known client platforms only when authoritative versions metadata omits
// platform hints; never advertise an unobserved candidate as an available platform.
var providerPlatforms = []remotePlatform{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"linux", "arm"},
	{"linux", "386"},
	{"darwin", "amd64"},
	{"darwin", "arm64"},
	{"windows", "amd64"},
	{"windows", "386"},
	{"freebsd", "amd64"},
	{"freebsd", "386"},
	{"freebsd", "arm"},
	{"openbsd", "amd64"},
	{"openbsd", "386"},
	{"solaris", "amd64"},
}

func (h handler) remoteProviderVersions(
	w http.ResponseWriter,
	r *http.Request,
	q providerOp,
	versions []remoteProviderVersion,
	result source.Result,
) {
	out := []remoteProviderVersion{}
	paths := []string{}
	results := []source.Result{result}
	lookups := 0
	for _, version := range versions {
		q.version = version.Version
		platforms, observed, visible, err := h.providerVersionVisibility(
			r,
			q,
			version.Platforms,
			&lookups,
		)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if len(visible) > 0 {
			version.Platforms = platforms
			out = append(out, version)
			paths = append(paths, visible...)
			results = append(results, observed...)
		}
	}
	if len(out) == 0 {
		h.k.Error(w, r, missing())
		return
	}
	var doc any = map[string]any{"versions": out}
	if q.isMirror {
		v := map[string]any{}
		for _, version := range out {
			v[version.Version] = struct{}{}
		}
		doc = map[string]any{"versions": v}
	}
	h.serveProviderJSON(w, r, doc, results, paths)
}

// Supplied platforms establish canonical archive identities without fetching
// every platform descriptor. Missing hints need one actual readable descriptor
// to disclose a version, while leaving its optional platform list unspecified.
func (h handler) providerVersionVisibility(
	r *http.Request, q providerOp, platforms []remotePlatform, lookups *int,
) ([]remotePlatform, []source.Result, []string, error) {
	readable := []remotePlatform{}
	paths := []string{}
	if len(platforms) > 0 {
		for _, platform := range platforms {
			q.os, q.arch = platform.OS, platform.Arch
			path := providerArchivePath(q)
			if h.k.Can(r, path, types.ActionRead) {
				readable = append(readable, platform)
				paths = append(paths, path)
			}
		}
		return readable, nil, paths, nil
	}
	for _, platform := range providerPlatforms {
		q.os, q.arch = platform.OS, platform.Arch
		path := providerArchivePath(q)
		if !h.k.Can(r, path, types.ActionRead) {
			continue
		}
		*lookups++
		if *lookups > moduleDiscoveryLimit {
			return nil, nil, nil, remoteProviderError(
				"versions without platform hints exceed the bounded package lookup limit",
			)
		}
		_, metadata, _, err := h.upstreamProviderPackage(r, q)
		if err != nil {
			var api *apierror.Error
			if errors.As(err, &api) && api.Status == 404 {
				continue
			}
			return nil, nil, nil, err
		}
		return nil, []source.Result{metadata}, []string{path}, nil
	}
	return nil, nil, nil, nil
}

func (h handler) remoteProviderMirror(
	w http.ResponseWriter,
	r *http.Request,
	q providerOp,
	version remoteProviderVersion,
	result source.Result,
) {
	platforms := version.Platforms
	if len(platforms) == 0 {
		platforms = providerPlatforms
	}
	archives := map[string]any{}
	paths := []string{}
	results := []source.Result{result}
	for _, platform := range platforms {
		q.os, q.arch = platform.OS, platform.Arch
		if !h.k.Can(r, providerArchivePath(q), types.ActionRead) {
			continue
		}
		bundle, err := h.providerBundle(r, q)
		defer bundle.close()
		if err != nil {
			var api *apierror.Error
			if errors.As(err, &api) && api.Status == 404 {
				continue
			}
			h.k.Error(w, r, err)
			return
		}
		target, err := h.remoteProviderURL(r, bundle, 0)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		archives[q.os+"_"+q.arch] = map[string]any{
			"url":    target,
			"hashes": []string{bundle.h1, "zh:" + strings.ToLower(bundle.metadata.Shasum)},
		}
		paths = append(paths, bundle.paths...)
		results = append(results, bundle.results...)
	}
	if len(archives) == 0 {
		h.k.Error(w, r, missing())
		return
	}
	h.serveProviderJSON(w, r, map[string]any{"archives": archives}, results, paths)
}

func (h handler) serveProviderJSON(
	w http.ResponseWriter,
	r *http.Request,
	doc any,
	results []source.Result,
	paths []string,
) {
	body, err := json.Marshal(doc)
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("terraform provider response: %w", err))
		return
	}
	digest := sha256.Sum256(body)
	response := source.Result{
		Stream: &source.Stream{
			Body:   &moduleBody{bytes.NewReader(body)},
			Status: 200,
			Representation: source.Representation{
				Size:      int64(len(body)),
				MediaType: "application/json",
				ETag:      `"` + hex.EncodeToString(digest[:]) + `"`,
			},
		},
		Revalidate: combineModuleFences(results, paths),
		Used:       combineModuleUsage(results),
	}
	h.k.ServeResult(w, r, response)
}
