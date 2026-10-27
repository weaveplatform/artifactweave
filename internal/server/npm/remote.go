package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const remoteMetadataLimit = 64 << 20

type remoteDocument map[string]json.RawMessage

func (h handler) remoteRead(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	name, rest, ok := splitName(p.Segments)
	switch {
	case !ok:
		h.k.Error(w, r, notFound(r))
	case slices.Contains(rest, "-"):
		h.remoteTarball(w, r, name, p.String())
	case len(rest) == 0:
		h.remotePackage(w, r, name, "", false)
	case len(rest) == 1:
		h.remotePackage(w, r, name, rest[0], false)
	default:
		h.k.Error(w, r, notFound(r))
	}
}

func (h handler) remotePackage(
	w http.ResponseWriter,
	r *http.Request,
	name, spec string,
	tagsOnly bool,
) {
	accept := jsonType
	if spec == "" && !tagsOnly && abbreviated(r) {
		accept = abbreviatedType
	}
	doc, result, err := h.upstreamPackage(r, name, accept)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	versions, err := h.rewriteVersions(r, name, doc)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	paths := make([]string, 0, len(versions))
	for version := range versions {
		paths = append(paths, npmfmt.CanonicalPath(name, version))
	}
	var output any = doc
	if tagsOnly {
		output = doc["dist-tags"]
	}
	if spec != "" {
		var tags map[string]string
		_ = json.Unmarshal(doc["dist-tags"], &tags)
		if tagged, ok := tags[spec]; ok {
			spec = tagged
		}
		selected, ok := versions[spec]
		if !ok {
			h.k.Error(w, r, notFound(r))
			return
		}
		output = selected
		paths = []string{npmfmt.CanonicalPath(name, spec)}
	}
	fenceDisclosures(&result, paths)
	h.serveRemoteJSON(w, r, result, accept, output)
}

func (h handler) upstreamPackage(
	r *http.Request,
	name, accept string,
) (remoteDocument, source.Result, error) {
	resource := source.Resource{
		Kind:                source.ResourceKindMetadata,
		Path:                name,
		EscapedPath:         url.PathEscape(name),
		Method:              http.MethodGet,
		Accept:              accept,
		PermissionFolder:    true,
		PermissionPath:      name,
		ExpectedContentType: jsonType,
		Variant:             "npm-packument-v2",
		Validate:            validatePackageMetadata(pkgroute.RequestFrom(r).Repo, name),
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return nil, result, fmt.Errorf("npm metadata: %w", err)
	}
	doc, err := readRemoteDocument(result)
	if err != nil {
		return nil, result, err
	}
	var got string
	if json.Unmarshal(doc["name"], &got) != nil || got != name {
		return nil, result, remoteProtocolError()
	}
	return doc, result, nil
}

func readRemoteDocument(result source.Result) (remoteDocument, error) {
	if result.Stream == nil {
		return nil, remoteProtocolError()
	}
	defer result.Stream.Body.Close()
	return decodeRemoteDocument(result.Stream.Body)
}

func (h handler) rewriteVersions(
	r *http.Request,
	name string,
	doc remoteDocument,
) (map[string]remoteDocument, error) {
	var versions map[string]remoteDocument
	if json.Unmarshal(doc["versions"], &versions) != nil || versions == nil {
		return nil, remoteProtocolError()
	}
	for v, manifest := range versions {
		path := npmfmt.CanonicalPath(name, v)
		if !h.k.Can(r, path, types.ActionRead) {
			delete(versions, v)
			continue
		}
		dist, err := remoteDist(pkgroute.RequestFrom(r).Repo, name, v, manifest)
		if err != nil {
			return nil, err
		}
		dist.fields["tarball"] = jsonString(tarballBase(r) + "/" + escapePath(path))
		manifest["dist"] = encode(dist.fields)
	}
	if len(versions) == 0 {
		return nil, notFound(r)
	}
	var tags map[string]string
	if json.Unmarshal(doc["dist-tags"], &tags) != nil {
		return nil, remoteProtocolError()
	}
	for tag, v := range tags {
		if _, ok := versions[v]; !ok {
			delete(tags, tag)
		}
	}
	doc["versions"], doc["dist-tags"] = encode(versions), encode(tags)
	// Version timestamps can reveal versions removed by authorization filtering.
	var times map[string]json.RawMessage
	if json.Unmarshal(doc["time"], &times) == nil {
		for key := range times {
			if key != "created" && key != "modified" {
				if _, ok := versions[key]; !ok {
					delete(times, key)
				}
			}
		}
		doc["time"] = encode(times)
	}
	return versions, nil
}

func (h handler) serveRemoteJSON(
	w http.ResponseWriter,
	r *http.Request,
	result source.Result,
	contentType string,
	doc any,
) {
	body := append(encode(doc), '\n')
	// Validators identify the filtered, request-specific bytes, never the raw cache.
	result.Stream = &source.Stream{
		Body:   &remoteJSONBody{Reader: bytes.NewReader(body)},
		Status: http.StatusOK,
		Representation: source.Representation{
			Size:      int64(len(body)),
			MediaType: contentType,
			ETag:      remoteETag(body),
		},
	}
	h.k.ServeResult(w, r, result)
}

type remoteJSONBody struct{ *bytes.Reader }

func (*remoteJSONBody) Close() error { return nil }

func (h handler) remoteTarball(w http.ResponseWriter, r *http.Request, name, path string) {
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	doc, _, err := h.upstreamPackage(r, name, jsonType)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	var versions map[string]remoteDocument
	if json.Unmarshal(doc["versions"], &versions) != nil {
		h.k.Error(w, r, remoteProtocolError())
		return
	}
	for v, manifest := range versions {
		if npmfmt.CanonicalPath(name, v) != path {
			continue
		}
		dist, distErr := remoteDist(pkgroute.RequestFrom(r).Repo, name, v, manifest)
		if distErr != nil {
			h.k.Error(w, r, distErr)
			return
		}
		resource := pkgroute.Resource(r, path, escapePath(path))
		resource.Accept = "application/octet-stream"
		resource.ExternalURL = dist.target
		resource.Checksums = dist.checksums
		resource.Variant = dist.variant
		if dist.integrity.algorithm != "" {
			resource.Validate = dist.integrity.validate
		}
		resource.Query = ""
		resource.EscapedPath = ""
		result, readErr := h.k.Read(r, resource)
		if readErr != nil {
			h.k.Error(w, r, readErr)
			return
		}
		if err = dist.integrity.checkResult(r.Context(), h.k, result); err != nil {
			h.k.Error(w, r, err)
			return
		}
		h.k.ServeResult(w, r, result)
		return
	}
	h.k.Error(w, r, notFound(r))
}

func (h handler) remoteSearch(w http.ResponseWriter, r *http.Request) {
	c := pkgroute.RequestFrom(r).Repo
	base, err := url.Parse(strings.TrimRight(c.URL, "/") + "/")
	if err != nil {
		h.k.Error(w, r, remoteProtocolError())
		return
	}
	target := base.ResolveReference(&url.URL{Path: "-/v1/search", RawQuery: r.URL.Query().Encode()})
	resource := source.Resource{
		Kind:                source.ResourceKindMetadata,
		Path:                "-/v1/search",
		Method:              http.MethodGet,
		Accept:              jsonType,
		ExternalURL:         target.String(),
		PermissionFolder:    true,
		ExpectedContentType: jsonType,
		Variant:             "npm-search-v2",
		Validate:            validateSearchMetadata,
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	doc, err := readRemoteDocument(result)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	var objects []remoteDocument
	if json.Unmarshal(doc["objects"], &objects) != nil {
		h.k.Error(w, r, remoteProtocolError())
		return
	}
	filtered := make([]remoteDocument, 0, len(objects))
	paths := make([]string, 0, len(objects))
	for _, entry := range objects {
		var pkg struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if json.Unmarshal(entry["package"], &pkg) != nil || pkg.Name == "" || pkg.Version == "" {
			continue
		}
		if h.k.Can(r, npmfmt.CanonicalPath(pkg.Name, pkg.Version), types.ActionRead) {
			filtered = append(filtered, entry)
			paths = append(paths, npmfmt.CanonicalPath(pkg.Name, pkg.Version))
		}
	}
	doc["objects"] = encode(filtered)
	// Upstream totals disclose packages the caller is not allowed to discover.
	delete(doc, "total")
	fenceDisclosures(&result, paths)
	h.serveRemoteJSON(w, r, result, jsonType, doc)
}

// Remote caches remain read-only through npm's publication and tag APIs.
// Authentication endpoints still issue local credentials for read clients.
func (h handler) remoteReadOnly(w http.ResponseWriter, r *http.Request) bool {
	if pkgroute.RequestFrom(r).Repo.Class != repo.Remote {
		return false
	}
	h.k.Error(w, r, apierror.New(http.StatusMethodNotAllowed, "remote npm repository is read-only"))
	return true
}

func fenceDisclosures(result *source.Result, paths []string) {
	current, check := result.Revalidate, result.CheckRead
	result.Revalidate = func(ctx context.Context) error {
		if current != nil {
			if err := current(ctx); err != nil {
				return err
			}
		}
		if check != nil {
			for _, path := range paths {
				if err := check(ctx, path); err != nil {
					return err
				}
			}
		}
		return nil
	}
}
