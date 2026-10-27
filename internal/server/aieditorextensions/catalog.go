package aieditorextensions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	format "github.com/weaveplatform/artifactweave/internal/format/aieditorextensions"
)

const maxCatalog = 8 << 20

var errCatalog = errors.New("aieditorextensions: invalid extension gallery metadata")

type galleryCatalog struct {
	Results []galleryResult `json:"results"`
}

//nolint:tagliatelle // Microsoft's gallery response uses these camelCase names.
type galleryResult struct {
	Extensions     []galleryExtension `json:"extensions"`
	ResultMetadata []resultMetadata   `json:"resultMetadata,omitempty"`
}

//nolint:tagliatelle // Microsoft's gallery response uses these camelCase names.
type resultMetadata struct {
	MetadataType  string         `json:"metadataType"`
	MetadataItems []metadataItem `json:"metadataItems"`
}

type metadataItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

//nolint:tagliatelle // Microsoft's gallery response uses these camelCase names.
type galleryExtension struct {
	Publisher     galleryPublisher `json:"publisher"`
	ExtensionID   string           `json:"extensionId"`
	ExtensionName string           `json:"extensionName"`
	DisplayName   string           `json:"displayName,omitempty"`
	Flags         string           `json:"flags,omitempty"`
	Versions      []galleryVersion `json:"versions"`
}

//nolint:tagliatelle // Microsoft's gallery response uses these camelCase names.
type galleryPublisher struct {
	PublisherName string `json:"publisherName"`
	DisplayName   string `json:"displayName,omitempty"`
}

//nolint:tagliatelle // Microsoft's gallery response uses these camelCase names.
type galleryVersion struct {
	Version          string            `json:"version"`
	Files            []galleryFile     `json:"files"`
	Properties       []galleryProperty `json:"properties,omitempty"`
	AssetURI         string            `json:"assetUri"`
	FallbackAssetURI string            `json:"fallbackAssetUri"`
}

//nolint:tagliatelle // Microsoft's gallery response uses these camelCase names.
type galleryFile struct {
	AssetType string `json:"assetType"`
	Source    string `json:"source"`
}

type galleryProperty struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

//nolint:gocyclo // Each branch rejects an untrusted Marketplace field before cache publication.
func readCatalog(body io.Reader, size int64, base string, q parsedQuery) (galleryCatalog, error) {
	if size < 0 || size > maxCatalog {
		return galleryCatalog{}, errCatalog
	}
	data, err := io.ReadAll(io.LimitReader(body, maxCatalog+1))
	if err != nil || len(data) > maxCatalog || int64(len(data)) != size {
		return galleryCatalog{}, errCatalog
	}
	var catalog galleryCatalog
	dec := json.NewDecoder(bytes.NewReader(data))
	if dec.Decode(&catalog) != nil || !errors.Is(dec.Decode(new(any)), io.EOF) ||
		len(catalog.Results) != 1 || len(catalog.Results[0].Extensions) > 50 {
		return galleryCatalog{}, errCatalog
	}
	seen := make(map[string]bool)
	for i := range catalog.Results[0].Extensions {
		e := &catalog.Results[0].Extensions[i]
		publisher, name, ok := format.Identity(e.Publisher.PublisherName + "." + e.ExtensionName)
		if !ok || q.Publisher != "" && (publisher != q.Publisher || name != q.Name) ||
			len(e.ExtensionID) == 0 || len(e.ExtensionID) > 64 ||
			len(e.DisplayName) > 256 || len(e.Publisher.DisplayName) > 256 ||
			len(e.Flags) > 128 || len(e.Versions) == 0 || len(e.Versions) > 16 ||
			seen[publisher+"."+name] {
			return galleryCatalog{}, errCatalog
		}
		seen[publisher+"."+name] = true
		e.Publisher.PublisherName, e.ExtensionName = publisher, name
		for vi := range e.Versions {
			v := &e.Versions[vi]
			if !format.Version(v.Version) || len(v.Files) < 2 || len(v.Files) > 32 ||
				len(
					v.Properties,
				) > 64 || !safeAssetBase(base, v.AssetURI, publisher, name, v.Version) ||
				!safeAssetBase(base, v.FallbackAssetURI, publisher, name, v.Version) {
				return galleryCatalog{}, errCatalog
			}
			files := make(map[string]bool)
			for _, file := range v.Files {
				if _, ok := format.AssetCachePath(
					publisher,
					name,
					v.Version,
					file.AssetType,
				); !ok ||
					files[file.AssetType] ||
					!safeAssetSource(
						base,
						file.Source,
						publisher,
						name,
						v.Version,
						file.AssetType,
					) {
					return galleryCatalog{}, errCatalog
				}
				files[file.AssetType] = true
			}
			if !files["Microsoft.VisualStudio.Code.Manifest"] ||
				!files["Microsoft.VisualStudio.Services.VSIXPackage"] {
				return galleryCatalog{}, errCatalog
			}
			checksumSeen := false
			for _, property := range v.Properties {
				if len(property.Key) == 0 || len(property.Key) > 128 || len(property.Value) > 4096 {
					return galleryCatalog{}, errCatalog
				}
				if property.Key == "Microsoft.VisualStudio.Services.VsixSha256" {
					if checksumSeen || !sha256Value(property.Value) {
						return galleryCatalog{}, errCatalog
					}
					checksumSeen = true
				}
			}
			if !checksumSeen {
				return galleryCatalog{}, errCatalog
			}
		}
	}
	return catalog, nil
}

func sha256Value(raw string) bool {
	if len(raw) != 64 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func safeAssetBase(base, raw, publisher, name, version string) bool {
	if raw == "" {
		return false
	}
	if safeAssetURL(base, raw, publisher, name, version, "") {
		return true
	}
	// Marketplace's fallbackAssetUri uses the documented assetbyname route
	// rather than the CDN's extensions/{publisher}/{name}/{version} layout.
	origin, err := url.Parse(base)
	if err != nil || origin.Host == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	sameOrigin := u.Scheme == origin.Scheme && strings.EqualFold(u.Host, origin.Host)
	marketplaceOrigin := origin.Scheme == "https" &&
		origin.Hostname() == "marketplace.visualstudio.com"
	microsoftFallback := marketplaceOrigin &&
		u.Scheme == "https" &&
		strings.EqualFold(u.Hostname(), publisher+".gallery.vsassets.io") &&
		u.Port() == ""
	return (sameOrigin || microsoftFallback) &&
		u.EscapedPath() == "/_apis/public/gallery/publisher/"+publisher+"/extension/"+name+"/"+version+"/assetbyname"
}

func safeAssetSource(base, raw, publisher, name, version, assetType string) bool {
	return safeAssetURL(base, raw, publisher, name, version, assetType)
}

func safeAssetURL(base, raw, publisher, name, version, assetType string) bool {
	origin, err := url.Parse(base)
	if err != nil || origin.Host == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" ||
		u.RawQuery != "" || u.Opaque != "" || u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	sameOrigin := u.Scheme == origin.Scheme && strings.EqualFold(u.Host, origin.Host)
	microsoftCDN := origin.Scheme == "https" &&
		origin.Hostname() == "marketplace.visualstudio.com" &&
		u.Scheme == "https" &&
		microsoftAssetHost(u.Hostname(), publisher)
	if !sameOrigin && !microsoftCDN {
		return false
	}
	path := strings.ToLower(u.EscapedPath())
	identity := "/" + publisher + "/" + name + "/" + strings.ToLower(version)
	if strings.Contains(path, "%") ||
		!strings.HasSuffix(path, identity) && !strings.Contains(path, identity+"/") {
		return false
	}
	if assetType != "" && !strings.HasSuffix(path, "/"+strings.ToLower(assetType)) {
		return false
	}
	return true
}

func microsoftAssetHost(host, publisher string) bool {
	host = strings.ToLower(host)
	return host == publisher+".gallerycdn.vsassets.io" ||
		host == publisher+".gallery.vsassets.io"
}

func projectCatalog(
	ctx context.Context,
	c galleryCatalog,
	assetBase string,
	allow func(context.Context, string) bool,
) ([]byte, []string, error) {
	out := galleryCatalog{Results: []galleryResult{{Extensions: make([]galleryExtension, 0)}}}
	var disclosed []string
	for _, e := range c.Results[0].Extensions {
		path := format.ExtensionFolder(e.Publisher.PublisherName, e.ExtensionName)
		if !allow(ctx, path) {
			continue
		}
		disclosed = append(disclosed, path)
		for i := range e.Versions {
			v := &e.Versions[i]
			base := fmt.Sprintf("%s/assets/%s/%s/%s", assetBase,
				e.Publisher.PublisherName, e.ExtensionName, v.Version)
			v.AssetURI, v.FallbackAssetURI = base, base
			files := make([]galleryFile, 0, 3)
			for _, file := range v.Files {
				// Only published asset routes may appear in the rewritten catalog.
				// Presentation files need their own MIME and content validators.
				switch file.AssetType {
				case manifestAsset, packageAsset, signatureAsset:
					file.Source = base + "/" + file.AssetType
					files = append(files, file)
				}
			}
			v.Files = files
			v.Properties = projectedProperties(v.Properties)
		}
		out.Results[0].Extensions = append(out.Results[0].Extensions, e)
	}
	out.Results[0].ResultMetadata = []resultMetadata{{
		MetadataType:  "ResultCount",
		MetadataItems: []metadataItem{{Name: "TotalCount", Count: len(disclosed)}},
	}}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal gallery projection: %w", err)
	}
	return b, disclosed, nil
}

func projectedProperties(in []galleryProperty) []galleryProperty {
	out := make([]galleryProperty, 0, len(in))
	for _, property := range in {
		switch property.Key {
		case "Microsoft.VisualStudio.Code.Engine",
			"Microsoft.VisualStudio.Services.VsixSha256",
			"Microsoft.VisualStudio.Code.ExtensionDependencies",
			"Microsoft.VisualStudio.Code.ExtensionPack":
			out = append(out, property)
		}
	}
	return out
}

func versionProperty(v galleryVersion, key string) string {
	for _, property := range v.Properties {
		if property.Key == key {
			return property.Value
		}
	}
	return ""
}

func fileSource(v galleryVersion, assetType string) string {
	for _, file := range v.Files {
		if file.AssetType == assetType {
			return file.Source
		}
	}
	return ""
}

func projectedETag(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
