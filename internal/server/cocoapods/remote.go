package cocoapods

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

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/authz"
	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxCDNMetadata = 16 << 20

func cdnURL(c repo.Config, path string) (*url.URL, error) {
	base := c.PodsCdnURL
	if base == "" {
		base = "https://cdn.cocoapods.org/"
	}
	u, err := url.Parse(base)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || u.Scheme != "https" && u.Scheme != "http" {
		return nil, source.ErrUnsupported
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + path
	u.RawPath = ""
	return u, nil
}

func cdnGuard(target *url.URL) func(context.Context, *url.URL) error {
	return func(_ context.Context, next *url.URL) error {
		if next.User != nil || next.Fragment != "" || next.RawQuery != "" {
			return source.ErrDenied
		}
		if next.Scheme == target.Scheme && strings.EqualFold(next.Host, target.Host) &&
			next.EscapedPath() == target.EscapedPath() {
			return nil
		}
		// The public CocoaPods CDN redirects Specs to jsDelivr's mirror of
		// the same path. No other cross-origin hop is a CDN source.
		if target.Scheme == "https" && target.Host == "cdn.cocoapods.org" &&
			strings.HasPrefix(target.Path, "/Specs/") && next.Scheme == "https" &&
			next.Host == "cdn.jsdelivr.net" &&
			next.EscapedPath() == "/cocoa/"+strings.TrimPrefix(target.EscapedPath(), "/") {
			return nil
		}
		return source.ErrDenied
	}
}

func validCDNPath(path string) bool {
	switch path {
	case "CocoaPods-version.yml", "all_pods.txt", "deprecated_podspecs.txt":
		return true
	}
	if strings.HasPrefix(path, "all_pods_versions_") && strings.HasSuffix(path, ".txt") {
		shard := strings.TrimSuffix(strings.TrimPrefix(path, "all_pods_versions_"), ".txt")
		if len(shard) != 5 || shard[1] != '_' || shard[3] != '_' {
			return false
		}
		for _, ch := range []byte{shard[0], shard[2], shard[4]} {
			if ch < '0' || ch > '9' && ch < 'a' || ch > 'f' {
				return false
			}
		}
		return true
	}
	_, _, ok := parseSpecPath(path)
	return ok
}

func validateCDN(
	path string,
) func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error {
	return func(_ context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
		if size < 0 || size > maxCDNMetadata {
			return source.ErrInvalidResult
		}
		data, err := io.ReadAll(io.LimitReader(body, maxCDNMetadata+1))
		if err != nil || int64(len(data)) != size {
			return source.ErrInvalidResult
		}
		if name, version, ok := parseSpecPath(path); ok {
			if _, _, _, e := sourceDetails(data, name, version); e != nil {
				return source.ErrInvalidResult
			}
			return nil
		}
		if path == "CocoaPods-version.yml" {
			var version struct {
				Min           string `yaml:"min"`
				Last          string `yaml:"last"`
				PrefixLengths []int  `yaml:"prefix_lengths"`
			}
			if yaml.Unmarshal(data, &version) != nil || !podfmt.ValidVersion(version.Min) ||
				!podfmt.ValidVersion(version.Last) || len(version.PrefixLengths) != 3 {
				return source.ErrInvalidResult
			}
			for _, n := range version.PrefixLengths {
				if n < 1 || n > 4 {
					return source.ErrInvalidResult
				}
			}
			return nil
		}
		if path == "all_pods.txt" {
			for _, row := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if row != "" && !podfmt.ValidName(row) {
					return source.ErrInvalidResult
				}
			}
			return nil
		}
		if strings.HasPrefix(path, "all_pods_versions_") {
			for _, row := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if row == "" {
					continue
				}
				fields := strings.Split(row, "/")
				if len(fields) < 2 || !podfmt.ValidName(fields[0]) ||
					podfmt.ShardFile(fields[0]) != path {
					return source.ErrInvalidResult
				}
				for _, version := range fields[1:] {
					if !podfmt.ValidVersion(version) {
						return source.ErrInvalidResult
					}
				}
			}
		}
		if strings.ContainsRune(string(data), 0) {
			return source.ErrInvalidResult
		}
		return nil
	}
}

func (h handler) remoteResult(r *http.Request, path string) (source.Result, error) {
	if !validCDNPath(path) {
		return source.Result{}, source.ErrInvalidRequest
	}
	c := pkgroute.RequestFrom(r).Repo
	target, err := cdnURL(c, path)
	if err != nil {
		return source.Result{}, err
	}
	permission := ""
	if name, _, ok := parseSpecPath(path); ok {
		permission = name
	}
	resource := source.Resource{
		Kind: source.ResourceKindMetadata, Path: "cdn/" + path,
		ExternalURL: target.String(), CheckURL: cdnGuard(target),
		Method: http.MethodGet, PermissionFolder: true, PermissionPath: permission,
		Variant: "cocoapods-cdn-v1", ValidateMetadata: validateCDN(path),
	}
	if path == "CocoaPods-version.yml" {
		// ServeContent and CDNs use both standard and legacy YAML labels. The
		// complete version document still has to pass validateCDN before cache.
		resource.ExpectedContentTypes = []string{
			"application/yaml", "application/x-yaml", "text/yaml",
			"text/plain", "application/octet-stream",
		}
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, fmt.Errorf("cocoapods: read CDN metadata: %w", err)
	}
	return result, nil
}

func (h handler) remoteRead(w http.ResponseWriter, r *http.Request, path string) {
	if name, version, ext, ok := parseSourcePath(path); ok {
		h.remoteArchive(w, r, name, version, ext)
		return
	}
	if !validCDNPath(path) {
		h.k.Error(w, r, notFound())
		return
	}
	result, err := h.remoteResult(r, path)
	if errors.Is(err, source.ErrInvalidRequest) {
		h.k.Error(w, r, notFound())
		return
	}
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	if result.Stream == nil {
		h.k.Error(w, r, source.ErrInvalidResult)
		return
	}
	if name, version, ok := parseSpecPath(path); ok {
		h.remoteSpec(w, r, r, result, name, version, pkgroute.RequestFrom(r).Repo.Key)
		return
	}
	if strings.HasPrefix(path, "all_pods_versions_") || path == "all_pods.txt" {
		h.remoteIndex(w, r, result, path)
		return
	}
	h.k.ServeResult(w, r, result)
}

func sourceExtension(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.User != nil || u.Host == "" ||
		(u.Scheme != "https" && u.Scheme != "http") || u.Fragment != "" {
		return ""
	}
	switch {
	case strings.HasSuffix(strings.ToLower(u.Path), ".tar.gz"):
		return ".tar.gz"
	case strings.HasSuffix(strings.ToLower(u.Path), ".tgz"):
		return ".tgz"
	case strings.HasSuffix(strings.ToLower(u.Path), ".zip"):
		return ".zip"
	default:
		return ""
	}
}

func sourceURLAllowed(c repo.Config, target *url.URL) error {
	if target == nil || target.User != nil || target.Fragment != "" ||
		(target.Scheme != "https" && target.Scheme != "http") ||
		c.ExternalDependenciesEnabled == nil || !*c.ExternalDependenciesEnabled {
		return source.ErrDenied
	}
	base, err := url.Parse(c.URL)
	if err != nil || base == nil || base.Host == "" ||
		base.Scheme == "https" && target.Scheme != "https" {
		return source.ErrDenied
	}
	// The remote transport may intentionally forward credentials to any HTTPS
	// host when these settings are enabled. Never let a metadata-selected pod
	// source expand that credential scope beyond the configured remote origin.
	if (c.AllowAnyHostAuth != nil && *c.AllowAnyHostAuth ||
		c.EnableCookieManagement != nil && *c.EnableCookieManagement) &&
		(target.Scheme != base.Scheme || !strings.EqualFold(target.Host, base.Host)) {
		return source.ErrDenied
	}
	for _, pattern := range c.ExternalDependenciesPatterns {
		if pattern != "" && authz.CompilePattern(pattern).Match(target.String()) {
			return nil
		}
	}
	return source.ErrDenied
}

func sourceRedirectGuard(c repo.Config) func(context.Context, *url.URL) error {
	return func(_ context.Context, next *url.URL) error {
		// The source fetcher also applies its DNS and address policy at each hop.
		// An archive may move to an expressly approved origin, but never gains
		// unapproved network or credential scope through a redirect.
		return sourceURLAllowed(c, next)
	}
}

func sourcePath(name, version, ext string) string {
	// Keep the cache item below the pod name: the CDN spec's folder permit
	// can then revalidate this exact disclosed descendant at delivery.
	return name + "/" + version + "/source" + ext
}

func parseSourcePath(p string) (name, version, ext string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 4 || parts[0] != "sources" || !podfmt.ValidName(parts[1]) ||
		!podfmt.ValidVersion(parts[2]) {
		return "", "", "", false
	}
	ext = strings.TrimPrefix(parts[3], "source")
	if (ext != ".tar.gz" && ext != ".tgz" && ext != ".zip") ||
		p != "sources/"+sourcePath(parts[1], parts[2], ext) {
		return "", "", "", false
	}
	return parts[1], parts[2], ext, true
}

func sourceDetails(
	data []byte,
	name, version string,
) (map[string]json.RawMessage, string, string, error) {
	var fields map[string]json.RawMessage
	var declaredName, declaredVersion string
	if json.Unmarshal(data, &fields) != nil ||
		json.Unmarshal(fields["name"], &declaredName) != nil ||
		json.Unmarshal(fields["version"], &declaredVersion) != nil ||
		declaredName != name || declaredVersion != version {
		return nil, "", "", source.ErrInvalidResult
	}
	var origin map[string]json.RawMessage
	if json.Unmarshal(fields["source"], &origin) != nil {
		return nil, "", "", source.ErrInvalidResult
	}
	var target, checksum string
	if _, exists := origin["http"]; !exists {
		// Git-source content needs the dedicated Git mirror in NAT-07. Do not
		// return the upstream URL and let a client bypass the repository.
		return nil, "", "", source.ErrUnsupported
	}
	if json.Unmarshal(origin["http"], &target) != nil || sourceExtension(target) == "" {
		return nil, "", "", source.ErrInvalidResult
	}
	if raw, exists := origin["sha256"]; exists {
		if json.Unmarshal(raw, &checksum) != nil || len(checksum) != 64 {
			return nil, "", "", source.ErrInvalidResult
		}
		if _, err := hex.DecodeString(checksum); err != nil {
			return nil, "", "", source.ErrInvalidResult
		}
	}
	return fields, target, checksum, nil
}

func (h handler) remoteSpec(
	w http.ResponseWriter,
	r, visible *http.Request,
	result source.Result,
	name, version, publicKey string,
) {
	defer result.Stream.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Stream.Body, maxCDNMetadata+1))
	if err != nil || len(data) > maxCDNMetadata {
		h.k.Error(w, r, source.ErrInvalidResult)
		return
	}
	fields, target, checksum, err := sourceDetails(data, name, version)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	if target != "" {
		u, parseErr := url.Parse(target)
		if parseErr != nil || sourceURLAllowed(pkgroute.RequestFrom(r).Repo, u) != nil {
			h.k.Error(w, r, pkgroute.SourceError(source.ErrDenied, false))
			return
		}
		ext := sourceExtension(target)
		if !h.k.Can(r, sourcePath(name, version, ext), types.ActionRead) ||
			!h.k.Can(visible, sourcePath(name, version, ext), types.ActionRead) {
			h.k.Error(w, r, notFound())
			return
		}
		location := baseurl.From(
			r.Context(),
		) + "/artifactory/api/pods/" + url.PathEscape(
			publicKey,
		) + "/" +
			url.PathEscape(
				"sources",
			) + "/" + url.PathEscape(
			name,
		) + "/" + url.PathEscape(
			version,
		) + "/source" + ext
		origin := map[string]string{"http": location}
		if checksum != "" {
			origin["sha256"] = checksum
		}
		fields["source"], _ = json.Marshal(origin)
		data, err = json.Marshal(fields)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
	}
	h.serveRewritten(w, r, result, "application/json", data)
}

func (h handler) remoteIndex(
	w http.ResponseWriter,
	r *http.Request,
	result source.Result,
	p string,
) {
	visible, err := h.remoteIndexRows(r, result, p)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	h.serveRewritten(w, r, result, "text/plain; charset=utf-8", lines(visible))
}

func (h handler) remoteIndexRows(
	r *http.Request,
	result source.Result,
	p string,
) ([]string, error) {
	defer result.Stream.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Stream.Body, maxCDNMetadata+1))
	if err != nil || len(data) > maxCDNMetadata {
		return nil, source.ErrInvalidResult
	}
	var visible []string
	for _, row := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if row == "" {
			continue
		}
		if p == "all_pods.txt" {
			if podfmt.ValidName(row) && h.k.CanFolder(r, row, types.ActionRead) {
				visible = append(visible, row)
			}
			continue
		}
		parts := strings.Split(row, "/")
		if len(parts) < 2 || !podfmt.ValidName(parts[0]) || podfmt.ShardFile(parts[0]) != p {
			continue
		}
		kept := []string{parts[0]}
		for _, v := range parts[1:] {
			if podfmt.ValidVersion(v) && h.k.CanFolder(r, parts[0]+"/"+v, types.ActionRead) {
				kept = append(kept, v)
			}
		}
		if len(kept) > 1 {
			visible = append(visible, strings.Join(kept, "/"))
		}
	}
	return visible, nil
}

func (h handler) serveRewritten(
	w http.ResponseWriter,
	r *http.Request,
	result source.Result,
	media string,
	data []byte,
) {
	sum := sha256.Sum256(data)
	result.Stream = &source.Stream{
		Body: io.NopCloser(bytes.NewReader(data)), Status: http.StatusOK,
		Representation: source.Representation{
			MediaType: media, Size: int64(len(data)),
			ETag:      `"` + hex.EncodeToString(sum[:]) + `"`,
			Checksums: types.Checksums{SHA256: hex.EncodeToString(sum[:])},
		},
	}
	h.k.ServeResult(w, r, result)
}

func (h handler) remoteArchive(w http.ResponseWriter, r *http.Request, name, version, ext string) {
	specResult, err := h.remoteResult(r, podfmt.SpecPath(name, version))
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if specResult.Stream == nil {
		h.k.Error(w, r, pkgroute.SourceError(source.ErrInvalidResult, false))
		return
	}
	data, err := io.ReadAll(io.LimitReader(specResult.Stream.Body, maxCDNMetadata+1))
	_ = specResult.Stream.Body.Close()
	if err != nil || len(data) > maxCDNMetadata {
		h.k.Error(w, r, pkgroute.SourceError(source.ErrInvalidResult, false))
		return
	}
	_, target, checksum, err := sourceDetails(data, name, version)
	if err != nil || target == "" || sourceExtension(target) != ext {
		h.k.Error(w, r, notFound())
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	u, _ := url.Parse(target)
	if err := sourceURLAllowed(c, u); err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	resource := pkgroute.Resource(r, sourcePath(name, version, ext), "")
	resource.ExternalURL = target
	resource.Query = ""
	targetHash := sha256.Sum256([]byte(target))
	resource.Variant = "cocoapods-source-http-v1:" + hex.EncodeToString(
		targetHash[:],
	) + ":" + checksum
	resource.CheckURL = sourceRedirectGuard(c)
	resource.Checksums.SHA256 = checksum
	resource.Validate = func(_ context.Context, body io.ReadSeeker, size int64) error {
		return podfmt.ValidateSourceArchive(body, size, ext)
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	prior := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if prior != nil {
			if e := prior(ctx); e != nil {
				return e
			}
		}
		if specResult.CheckRead != nil {
			return specResult.CheckRead(ctx, sourcePath(name, version, ext))
		}
		return nil
	}
	h.k.ServeResult(w, r, result)
}
