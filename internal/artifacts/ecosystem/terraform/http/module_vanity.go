package terraformhttp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

const moduleVanityLimit = 10

func parseModuleVanity(r io.Reader, base string, c repo.Config) (moduleSource, string, error) {
	// A 16 MiB HTML document becomes base64 in its private JSON control.
	const controlLimit = (source.MaxModuleVanityDocumentBytes+2)/3*4 + 16384
	data, err := io.ReadAll(io.LimitReader(r, controlLimit+1))
	if err != nil || len(data) > controlLimit {
		return moduleSource{}, "", remoteModuleError()
	}
	var control source.ModuleVanityResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&control) != nil || decoder.Decode(new(any)) != io.EOF ||
		len(control.Identity) != 64 || control.Source != "" && len(control.HTML) != 0 ||
		len(control.HTML) > source.MaxModuleVanityDocumentBytes || !utf8.Valid(control.HTML) {
		return moduleSource{}, "", remoteModuleError()
	}
	if _, err := hex.DecodeString(control.Identity); err != nil {
		return moduleSource{}, "", remoteModuleError()
	}
	raw := control.Source
	if raw == "" {
		raw, err = moduleHTMLSource(control.HTML)
		if err != nil {
			return moduleSource{}, "", err
		}
	}
	next, err := parseModuleSource(raw, base, c)
	return next, control.Identity, err
}

func moduleHTMLSource(body []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoder.Strict = false
	decoder.AutoClose, decoder.Entity = xml.HTMLAutoClose, xml.HTMLEntity
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", remoteModuleError()
		}
		switch element := token.(type) {
		case xml.StartElement:
			if strings.EqualFold(element.Name.Local, "body") {
				return "", remoteModuleError()
			}
			if !strings.EqualFold(element.Name.Local, "meta") {
				continue
			}
			attrs := make(map[string]string, 2)
			for _, attr := range element.Attr {
				name := strings.ToLower(attr.Name.Local)
				if name != "name" && name != "content" {
					continue
				}
				if _, duplicate := attrs[name]; duplicate {
					return "", remoteModuleError()
				}
				attrs[name] = attr.Value
			}
			if attrs["name"] == "terraform-get" && attrs["content"] != "" {
				return attrs["content"], nil
			}
		case xml.EndElement:
			if strings.EqualFold(element.Name.Local, "head") {
				return "", remoteModuleError()
			}
		}
	}
}

func (h handler) resolveModuleVanity(
	r *http.Request,
	m tf.Module,
	location moduleSource,
	metadata source.Result,
) (moduleSource, source.Result, error) {
	results := []source.Result{metadata}
	seen := make(map[string]bool)
	var selectors []string
	pins := kernel.Checksums{}
	for hops := 0; location.Vanity; hops++ {
		identity, err := source.ModuleVanityIdentity(location.Target)
		if err != nil || hops >= moduleVanityLimit || seen[identity] {
			return moduleSource{}, metadata, remoteModuleError()
		}
		seen[identity] = true
		if location.Subdir != "" {
			selectors = append(selectors, location.Subdir)
		}
		if pins, err = combineModuleChecksums(pins, location.Checksums); err != nil {
			return moduleSource{}, metadata, err
		}
		u, _ := url.Parse(location.Target)
		query := u.Query()
		query.Set("terraform-get", "1")
		u.RawQuery = query.Encode()
		target := u.String()
		c := pkgroute.RequestFrom(r).Repo
		resource := source.Resource{
			Kind: source.ResourceKindMetadata, Method: http.MethodGet,
			// The source cache keys ExternalURL with its HMAC. Keep the
			// document/visited identity out of observable metadata paths.
			Path:        "module-metadata/vanity",
			ExternalURL: target, CheckURL: moduleURLGuard(c, target),
			PermissionFolder: true, PermissionPath: m.Package(), BlackoutOnly: true,
			Variant: "terraform-module-vanity-v1:url-policy-v2",
			Accept:  "text/html, application/xhtml+xml", ExpectedContentType: "application/json",
			ModuleVanityDocument: true,
		}
		resource.ValidateMetadata = func(_ context.Context, body io.ReadSeeker, _ int64, actual source.MetadataResponse) error {
			_, _, err := parseModuleVanity(body, actual.BaseURL, c)
			return err
		}
		result, err := h.k.Read(r, resource)
		if err != nil {
			return moduleSource{}, metadata, fmt.Errorf("terraform module vanity: %w", err)
		}
		if result.Stream == nil {
			return moduleSource{}, metadata, remoteModuleError()
		}
		next, effective, err := parseModuleVanity(result.Stream.Body, result.Metadata.BaseURL, c)
		closeModuleResult(result)
		if err != nil || effective != identity && seen[effective] {
			return moduleSource{}, metadata, remoteModuleError()
		}
		seen[effective] = true
		results = append(results, result)
		location = next
	}
	var err error
	if location.Checksums, err = combineModuleChecksums(pins, location.Checksums); err != nil {
		return moduleSource{}, metadata, err
	}
	// Each outer selector applies inside the directory selected by the next
	// getter, matching nested client acquisition while retaining the full package.
	for i := len(selectors) - 1; i >= 0; i-- {
		if location.Subdir != "" {
			location.Subdir += "/"
		}
		location.Subdir += selectors[i]
	}
	if location.Subdir != "" && (len(location.Subdir) > 8192 || !validModuleSubdir(location.Subdir)) {
		return moduleSource{}, metadata, remoteModuleError()
	}
	metadata.Revalidate = combineModuleFences(results, nil, nil)
	metadata.Used = combineModuleUsage(results)
	return location, metadata, nil
}

func combineModuleChecksums(a, b kernel.Checksums) (kernel.Checksums, error) {
	for _, pair := range [][2]*string{{&a.MD5, &b.MD5}, {&a.SHA1, &b.SHA1}, {&a.SHA256, &b.SHA256}, {&a.SHA512, &b.SHA512}} {
		if *pair[0] != "" && *pair[1] != "" && *pair[0] != *pair[1] {
			return kernel.Checksums{}, remoteModuleError()
		}
		if *pair[0] == "" {
			*pair[0] = *pair[1]
		}
	}
	return a, nil
}
