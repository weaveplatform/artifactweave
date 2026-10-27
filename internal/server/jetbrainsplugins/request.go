package jetbrainsplugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

const maxQueryLength = 4096

func invalidRequest() error {
	return apierror.New(http.StatusBadRequest, "invalid JetBrains Plugins request")
}

func invalidCompatibleRequest(reason string) error {
	return apierror.New(http.StatusBadRequest, "invalid JetBrains compatible request: "+reason)
}

func missingRoute() error {
	return apierror.New(http.StatusNotFound, "JetBrains Plugins resource not found")
}

//nolint:gocyclo // Each case is a distinct Marketplace route grammar.
func parseResource(
	c repo.Config,
	raw, rawQuery string,
) (source.Resource, string, error) {
	if len(raw) > 256 || len(rawQuery) > maxQueryLength || raw == "" ||
		strings.ContainsAny(raw, "%\\?#") || strings.Contains(raw, "//") {
		return source.Resource{}, "", invalidRequest()
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil || len(values) > 16 {
		return source.Resource{}, "", invalidRequest()
	}
	for key, entries := range values {
		if !safeQueryToken(key) || len(entries) != 1 || len(entries[0]) > 256 ||
			strings.ContainsAny(entries[0], "\x00\r\n") {
			return source.Resource{}, "", invalidRequest()
		}
	}
	if _, err := upstreamURL(c, raw, values); err != nil {
		return source.Resource{}, "", err
	}
	parts := strings.Split(raw, "/")
	switch {
	case raw == "api/search/updates/compatible":
		allowed := map[string]bool{
			"build":       true,
			"os":          true,
			"pluginXmlId": true,
			"channel":     true,
			"module":      true,
			"arch":        true,
		}
		for key := range values {
			if !allowed[key] {
				return source.Resource{}, "", invalidCompatibleRequest(
					"unsupported query key " + key,
				)
			}
		}
		if !safeBuild(values.Get("build")) {
			return source.Resource{}, "", invalidCompatibleRequest("invalid build")
		}
		if !safePluginID(values.Get("pluginXmlId")) {
			return source.Resource{}, "", invalidCompatibleRequest("invalid plugin ID")
		}
		if len(values.Get("os")) > 128 {
			return source.Resource{}, "", invalidCompatibleRequest("OS value too long")
		}
		// Current IDEs select compatible updates by CPU architecture as well
		// as build. Keep it in the upstream query and the cache identity.
		if arch := values.Get("arch"); arch != "" && !safeQueryToken(arch) {
			return source.Resource{}, "", invalidCompatibleRequest("invalid architecture")
		}
		id := values.Get("pluginXmlId")
		key := pluginCacheKey(id)
		return metadataResource(c, raw, values,
			"plugins/"+key+"/compatible-"+digest(values.Encode())+".json",
			"plugins/"+key, validateCompatible(id)), "", nil
	case raw == "api/search/plugins":
		if !safeBuild(values.Get("build")) || values.Get("search") == "" ||
			len(values.Get("search")) > 256 {
			return source.Resource{}, "", invalidRequest()
		}
		return metadataResource(c, raw, values,
			"catalog/search/"+digest(values.Encode())+".json", "", validateSearch), "", nil
	case raw == "files/pluginsXMLIds.json" || raw == "files/jbPluginsXMLIds.json":
		if len(values) != 0 {
			return source.Resource{}, "", invalidRequest()
		}
		return metadataResource(c, raw, values, "catalog/"+parts[1], "", validatePluginIDs), "", nil
	case len(parts) == 4 && parts[0] == "files" && parts[3] == "meta.json" &&
		safeNumericID(parts[1]) && safeNumericID(parts[2]):
		if len(values) != 0 {
			return source.Resource{}, "", invalidRequest()
		}
		return metadataResource(c, raw, values,
			"catalog/files/"+parts[1]+"/"+parts[2]+".json", "",
			validateUpdateMetadata(parts[2])), "", nil
	case raw == "pluginManager":
		if !onlyKeys(values, "id", "build", "uuid", "updatedFrom", "channel", "action") ||
			!safePluginID(values.Get("id")) || !safeBuild(values.Get("build")) ||
			values.Get("action") != "" && values.Get("action") != "download" ||
			len(values.Get("updatedFrom")) > 128 || len(values.Get("channel")) > 64 {
			return source.Resource{}, "", invalidRequest()
		}
		id := values.Get("id")
		// UUID is client telemetry, not package identity or upstream authority.
		values.Del("uuid")
		return archiveResource(c, raw, values, id), id, nil
	default:
		return source.Resource{}, "", missingRoute()
	}
}

func metadataResource(
	c repo.Config, upstream string, query url.Values, path, permission string,
	validate func(context.Context, io.ReadSeeker, int64) error,
) source.Resource {
	target, _ := upstreamURL(c, upstream, query) // parseResource checked the base URL.
	r := source.Resource{
		Kind: source.ResourceKindMetadata, Path: path, ExternalURL: target.String(),
		CheckURL: redirectGuard(target, false), Variant: "jetbrains-metadata-v1",
		ExpectedContentTypes: []string{"application/json", "application/graphql+json"},
		Validate:             validate,
	}
	if permission == "" {
		r.PermissionRoot = true
	} else {
		r.PermissionFolder = true
		r.PermissionPath = permission
	}
	return r
}

func archiveResource(c repo.Config, upstream string, query url.Values, id string) source.Resource {
	target, _ := upstreamURL(c, upstream, query) // parseResource checked the base URL.
	return source.Resource{
		Kind: source.ResourceKindArtifact, Path: "plugins/" + pluginCacheKey(id) + "/" +
			digest(query.Encode()) + ".archive",
		ExternalURL: target.String(), CheckURL: redirectGuard(target, true),
		Variant: "jetbrains-archive-v1", Validate: validateArchive(id),
	}
}

func upstreamURL(c repo.Config, path string, query url.Values) (*url.URL, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || u.Scheme != "https" && u.Scheme != "http" {
		return nil, source.ErrInvalidRequest
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + path
	u.RawPath = ""
	u.RawQuery = query.Encode()
	return u, nil
}

func redirectGuard(target *url.URL, archive bool) func(context.Context, *url.URL) error {
	return func(_ context.Context, next *url.URL) error {
		if next == nil || next.User != nil || next.Fragment != "" ||
			next.Scheme != "https" && next.Scheme != "http" {
			return source.ErrDenied
		}
		if next.String() == target.String() {
			return nil
		}
		if !safeMarketplaceFile(next.Path, archive) {
			return source.ErrDenied
		}
		if next.Scheme == target.Scheme && strings.EqualFold(next.Host, target.Host) {
			return nil
		}
		if target.Scheme == "https" && strings.EqualFold(target.Host, "plugins.jetbrains.com") &&
			next.Scheme == "https" && strings.EqualFold(next.Host, "downloads.marketplace.jetbrains.com") {
			return nil
		}
		return source.ErrDenied
	}
}

func safeMarketplaceFile(path string, archive bool) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 4 && parts[0] == "files" && safeNumericID(parts[1]) &&
		safeNumericID(parts[2]) {
		if !archive {
			return parts[3] == "meta.json"
		}
		return safeFilename(parts[3]) && (strings.HasSuffix(parts[3], ".zip") ||
			strings.HasSuffix(parts[3], ".jar"))
	}
	return false
}

func safeFilename(name string) bool {
	if name == "" || len(name) > 200 || strings.HasPrefix(name, ".") ||
		strings.Contains(name, "..") {
		return false
	}
	for _, ch := range name {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' ||
			ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

func safePluginID(id string) bool {
	if id == "" || len(id) > 200 || !utf8.ValidString(id) {
		return false
	}
	for _, ch := range id {
		if unicode.IsControl(ch) {
			return false
		}
	}
	return true
}

func pluginCacheKey(id string) string {
	if safeFilename(id) {
		return id
	}
	return "opaque-" + digest(id)
}

func safeNumericID(id string) bool {
	if len(id) == 0 || len(id) > 12 || id[0] == '0' {
		return false
	}
	for _, ch := range id {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func safeBuild(build string) bool {
	if build == "" || len(build) > 64 {
		return false
	}
	for _, ch := range build {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' ||
			ch >= '0' && ch <= '9' || ch == '-' || ch == '.' || ch == '_' {
			continue
		}
		return false
	}
	return true
}

func safeQueryToken(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, ch := range s {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' ||
			ch >= '0' && ch <= '9' || ch == '-' || ch == '_' {
			continue
		}
		return false
	}
	return true
}

func onlyKeys(values url.Values, keys ...string) bool {
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range values {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}
