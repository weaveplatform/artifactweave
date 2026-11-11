package pypihttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func remoteIndexURL(c repo.Config, name string) (string, error) {
	base := strings.TrimRight(c.PyPIRegistryURL, "/") + "/"
	suffix := strings.Trim(c.PyPIRepositorySuffix, "/")
	if strings.ContainsAny(suffix, "?#\\\r\n\x00") {
		return "", remoteError()
	}
	if suffix == "" {
		suffix = repo.DefaultPyPIRepositorySuffix
	}
	for _, part := range strings.Split(suffix, "/") {
		if part == "." || part == ".." || part == "" {
			return "", remoteError()
		}
	}
	target, fragment, err := remoteLink(base, suffix+"/")
	if err != nil || fragment != "" {
		return "", remoteError()
	}
	if name != "" {
		target += url.PathEscape(name) + "/"
	}
	return target, nil
}

// itemPath reports whether path can be a read's policy path: a request or
// upstream path that is no item path, which no stored file could have,
// leaves the policy at the read's own path.
func itemPath(path string) bool {
	_, _, _, err := kernel.SplitItemPath(path)
	return err == nil && path == strings.Trim(path, "/") && !strings.ContainsAny(path, "\r\n")
}

// upstreamIndex reads the remote's page of project name, or its project
// list for "". Read permission is checked at the project's folder, or the
// root, and the remote's download policy at the page's 7.x cache path
// (pagePath), so a pattern on a project's folder, which 7.x's hash paths
// for files never match, leaves the page served (JFrog on RTFACT-17256).
// lookup, when set, names what a download or another page reads the page
// for: that read is refused only by the blackout, naming lookup, so the
// patterns of the file or page it serves decide.
func (h handler) upstreamIndex(
	r *http.Request,
	name, lookup string,
) (remoteIndex, source.Result, error) {
	target, err := remoteIndexURL(pkgroute.RequestFrom(r).Repo, name)
	if err != nil {
		return remoteIndex{}, source.Result{}, err
	}
	accept := htmlType
	if h.json(r) {
		accept = jsonType + ", text/html;q=0.1"
	}
	resource := source.Resource{
		Kind:             source.ResourceKindMetadata,
		Path:             "simple",
		Method:           http.MethodGet,
		ExternalURL:      target,
		Accept:           accept,
		PermissionFolder: true,
		PermissionPath:   name,
		Variant:          "pypi-simple-v3",
	}
	if itemPath(pagePath(name)) {
		resource.PolicyPath = pagePath(name)
	}
	if lookup != "" {
		resource.BlackoutOnly = true
		if itemPath(lookup) {
			resource.PolicyPath = lookup
		}
	}
	if name != "" {
		resource.Path += "/" + name
	}
	resource.ExpectedContentTypes = []string{
		htmlType,
		htmlV1Type,
		htmlLatestType,
		jsonType,
		jsonLatest,
		"application/json",
	}
	resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, _ int64, metadata source.MetadataResponse) error {
		_, e := parseRemoteIndex(r, metadata.BaseURL, name)
		return e
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return remoteIndex{}, result, fmt.Errorf("pypi index: %w", err)
	}
	if result.Stream == nil {
		return remoteIndex{}, result, remoteError()
	}
	defer result.Stream.Body.Close()
	index, err := parseRemoteIndex(result.Stream.Body, result.Metadata.BaseURL, name)
	return index, result, err
}

func (h handler) remoteProject(w http.ResponseWriter, r *http.Request, name string) {
	index, result, err := h.upstreamIndex(r, name, "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	var files []remoteFile
	var paths []string
	for _, f := range index.files {
		if h.k.Can(r, f.path, api.ActionRead) {
			files = append(files, f)
			paths = append(paths, f.path)
		}
	}
	if len(files) == 0 && len(index.files) > 0 {
		h.k.Error(w, r, notFound(r))
		return
	}
	fenceRemote(&result, paths)
	if h.json(r) {
		h.serveRemote(
			w,
			r,
			result,
			jsonType,
			remoteProjectJSON(index, files, name, repoBase(r), h.format.CoreMetadata),
		)
		return
	}
	h.serveRemote(w, r, result, htmlType, remoteHTML(files, repoBase(r), h.format.CoreMetadata))
}

func remoteProjectJSON(index remoteIndex, files []remoteFile, name, base string, core bool) []byte {
	doc := index.doc
	if len(doc) == 0 {
		doc = remoteDoc{
			"meta": encode(map[string]string{"api-version": "1.1"}),
			"name": encode(name),
		}
	}
	out := make([]remoteDoc, 0, len(files))
	versions := []string{}
	seen := map[string]bool{}
	for _, f := range files {
		d := f.doc
		if d == nil {
			d = remoteDoc{}
		}
		d["filename"], d["url"], d["hashes"] = encode(
			f.name,
		), encode(
			base+"/"+escapePath(f.path)+hashFragment(f),
		), encode(
			f.hashes,
		)
		if v, ok := f.attrs["data-requires-python"]; ok {
			d["requires-python"] = encode(v)
		}
		if v, ok := f.attrs["data-yanked"]; ok {
			if v == "" {
				d["yanked"] = encode(true)
			} else {
				d["yanked"] = encode(v)
			}
		}
		if v, ok := f.attrs["data-gpg-sig"]; ok {
			d["gpg-sig"] = encode(v == "true")
		}
		delete(d, "provenance")
		if core && f.hasCoreMetadata {
			var v any = true
			if len(f.metadata) > 0 {
				v = f.metadata
			}
			d["core-metadata"], d["dist-info-metadata"] = encode(v), encode(v)
		} else {
			delete(d, "core-metadata")
			delete(d, "dist-info-metadata")
		}
		out = append(out, d)
		if !seen[f.version] {
			versions = append(versions, f.version)
			seen[f.version] = true
		}
	}
	doc["files"], doc["versions"] = encode(out), encode(versions)
	return encode(doc)
}

func (h handler) remoteList(w http.ResponseWriter, r *http.Request) {
	index, result, err := h.upstreamIndex(r, "", "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	names := []string{}
	paths := []string{}
	lookups := 0
	for _, name := range index.projects {
		if !h.k.CanFolder(r, name, api.ActionRead) {
			continue
		}
		// A folder or invented marker can match permission rules while every real
		// distribution is excluded. Prove a readable file before project disclosure.
		// 7.x answers the list from its stored copy, which a pattern on one
		// project's page does not change, so this read checks the blackout
		// alone [I].
		lookups++
		if lookups > 256 {
			h.k.Error(w, r, remoteError())
			return
		}
		child, _, e := h.upstreamIndex(r, name, pagePath(name))
		if e != nil {
			h.k.Error(w, r, e)
			return
		}
		path := ""
		for _, f := range child.files {
			if h.k.Can(r, f.path, api.ActionRead) {
				path = f.path
				break
			}
		}
		if path == "" {
			continue
		}
		names = append(names, name)
		paths = append(paths, path)
	}
	fenceRemote(&result, paths)
	if h.json(r) {
		h.serveRemote(w, r, result, jsonType, listJSON(names))
	} else {
		h.serveRemote(w, r, result, htmlType, listHTML(names))
	}
}

// fenceRemote checks again, before delivery, that the caller may read each
// file the answer discloses. It checks Read permission alone
// (CheckPermission): a release whose file the remote's patterns reject
// stays listed, as 7.x lists it (RTFACT-24511, RTFACT-29715), and only the
// file's own read is refused.
func fenceRemote(result *source.Result, paths []string) {
	prior := result.Revalidate
	check := result.CheckPermission
	result.Revalidate = func(ctx context.Context) error {
		if prior != nil {
			if err := prior(ctx); err != nil {
				return err
			}
		}
		for _, path := range paths {
			if check != nil {
				if err := check(ctx, path); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

type remoteBody struct{ *bytes.Reader }

func (*remoteBody) Close() error { return nil }

func (h handler) serveRemote(
	w http.ResponseWriter,
	r *http.Request,
	result source.Result,
	media string,
	body []byte,
) {
	sum := sha256.Sum256(body)
	result.Stream = &source.Stream{
		Body:   &remoteBody{bytes.NewReader(body)},
		Status: http.StatusOK,
		Representation: source.Representation{
			Size:      int64(len(body)),
			MediaType: media,
			ETag:      `"` + hex.EncodeToString(sum[:]) + `"`,
		},
	}
	w.Header().Set("Vary", "Accept")
	h.k.ServeResult(w, r, result)
}

func (h handler) remoteDownload(w http.ResponseWriter, r *http.Request, p pkgroute.Path) {
	if len(p.Segments) != 3 || p.Folder {
		h.k.Error(w, r, notFound(r))
		return
	}
	path := p.String()
	parent := strings.TrimSuffix(strings.TrimSuffix(path, metadataSuffix), ".asc")
	suffix := strings.TrimPrefix(path, parent)
	isMetadata := parent != path
	if !h.k.Require(w, r, parent, api.ActionRead) {
		return
	}
	index, _, err := h.upstreamIndex(r, p.Segments[0], parent)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, f := range index.files {
		if f.path != parent {
			continue
		}
		resource, hashes, e := h.remoteFileResource(r, f, path, suffix)
		if e != nil {
			h.k.Error(w, r, e)
			return
		}
		result, e := h.k.Read(r, resource)
		if e != nil {
			h.k.Error(w, r, e)
			return
		}
		if e = h.checkRemoteHashes(r.Context(), result, hashes); e != nil {
			h.k.Error(w, r, e)
			return
		}
		if isMetadata {
			fenceRemote(&result, []string{parent})
		}
		h.k.ServeResult(w, r, result)
		return
	}
	h.k.Error(w, r, notFound(r))
}

// remoteFileResource is the read of path, f or, after suffix, its core
// metadata or signature. The remote's download policy is checked at the
// path 7.x caches f at, its upstream URL's path, which for PyPI.org is
// packages/<a>/<b>/<hash>/<file> (RTFACT-31147), rather than at path:
// JFrog's support answer quoted on RTFACT-17256 has a project's name in a
// pattern not stop retrieval, as files are stored by hash. The metadata
// and signature take f's check, as decision 0032 checks a sidecar at its
// file. [I] The path 7.x keeps a file from another upstream at is not
// known (#216).
func (h handler) remoteFileResource(
	r *http.Request,
	f remoteFile,
	path, suffix string,
) (source.Resource, map[string]string, error) {
	resource := pkgroute.Resource(r, path, "")
	resource.ExternalURL = f.target
	resource.Query = ""
	resource.Accept = "application/octet-stream"
	resource.Variant = "pypi-dist-v2:" + string(encode(f.hashes))
	resource.Checksums = checksumPins(f.hashes)
	resource.Validate = f.validateArtifact
	if u, err := url.Parse(f.target); err == nil && itemPath(strings.Trim(u.Path, "/")) {
		resource.PolicyPath = strings.Trim(u.Path, "/")
	}
	if suffix == "" {
		return resource, f.hashes, nil
	}
	u, _ := url.Parse(f.target)
	u.Path += suffix
	u.RawPath = ""
	resource.ExternalURL = u.String()
	resource.Kind = source.ResourceKindMetadata
	resource.Method = http.MethodGet
	resource.PermissionFolder = true
	resource.PermissionPath = strings.Split(f.path, "/")[0]
	resource.Checksums = kernel.Checksums{}
	if suffix == ".asc" {
		if f.attrs["data-gpg-sig"] != "true" {
			return resource, nil, notFound(r)
		}
		resource.Variant = "pypi-signature-v1"
		resource.Validate = validateRemoteSignature
		return resource, nil, nil
	}
	if !h.format.CoreMetadata || !f.hasCoreMetadata {
		return resource, nil, notFound(r)
	}
	resource.Variant = "pypi-core-v1:" + string(encode(f.metadata))
	resource.Checksums = checksumPins(f.metadata)
	resource.Validate = func(_ context.Context, body io.ReadSeeker, _ int64) error { return f.validateCore(body) }
	return resource, f.metadata, nil
}
