package pypi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type warehouseRemote struct {
	doc      remoteDoc
	urls     []remoteFile
	releases map[string][]remoteFile
}

func parseWarehouse(r io.Reader, base, name, version string) (warehouseRemote, error) {
	var out warehouseRemote
	if _, _, err := remoteLink(base, base); err != nil {
		return out, err
	}
	b, err := remoteBytes(r)
	if err != nil {
		return out, err
	}
	if json.Unmarshal(b, &out.doc) != nil || out.doc == nil {
		return out, remoteError()
	}
	var info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(out.doc["info"], &info) != nil || !pypifmt.ValidName(info.Name) ||
		pypifmt.Normalize(info.Name) != name ||
		!validRemoteVersion(info.Version) ||
		(version != "" && pypifmt.NormalizeVersion(info.Version) != pypifmt.NormalizeVersion(version)) {
		return out, remoteError()
	}
	out.urls, err = warehouseFiles(out.doc["urls"], base, name, info.Version)
	if err != nil {
		return out, err
	}
	if raw, ok := out.doc["releases"]; ok {
		var releases map[string]json.RawMessage
		if json.Unmarshal(raw, &releases) != nil || releases == nil ||
			len(releases) > remoteEntryLimit {
			return out, remoteError()
		}
		out.releases = map[string][]remoteFile{}
		for v, raw := range releases {
			if !validRemoteVersion(v) {
				return out, remoteError()
			}
			files, e := warehouseFiles(raw, base, name, v)
			if e != nil {
				return out, e
			}
			out.releases[v] = files
		}
	}
	return out, nil
}

func warehouseFiles(raw []byte, base, name, version string) ([]remoteFile, error) {
	var entries []remoteDoc
	if json.Unmarshal(raw, &entries) != nil || entries == nil || len(entries) > remoteEntryLimit {
		return nil, remoteError()
	}
	out := make([]remoteFile, 0, len(entries))
	for _, entry := range entries {
		if err := validateWarehouseAttributes(entry); err != nil {
			return nil, err
		}
		var filename, href string
		var hashes map[string]string
		if json.Unmarshal(entry["filename"], &filename) != nil ||
			json.Unmarshal(entry["url"], &href) != nil ||
			json.Unmarshal(entry["digests"], &hashes) != nil ||
			hashes == nil {
			return nil, remoteError()
		}
		f, err := newRemoteFile(base, name, filename, href, hashes)
		if err != nil || pypifmt.NormalizeVersion(f.version) != pypifmt.NormalizeVersion(version) {
			return nil, remoteError()
		}
		f.doc = entry
		if raw, ok := entry["core-metadata"]; ok {
			if err = f.setCore(raw); err != nil {
				return nil, err
			}
		}
		out = append(out, f)
	}
	return out, uniqueFiles(out)
}

func validateWarehouseAttributes(entry remoteDoc) error {
	for _, field := range []string{"requires_python", "yanked_reason"} {
		if raw, ok := entry[field]; ok && string(raw) != "null" {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return remoteError()
			}
		}
	}
	if raw, ok := entry["yanked"]; ok {
		var value bool
		if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
			return remoteError()
		}
	}
	return nil
}

func (h handler) remoteWarehouse(w http.ResponseWriter, r *http.Request, name, version string) {
	if !pypifmt.ValidName(name) {
		h.k.Error(w, r, notFound(r))
		return
	}
	name = pypifmt.Normalize(name)
	target := strings.TrimRight(
		pkgroute.RequestFrom(r).Repo.PyPIRegistryURL,
		"/",
	) + "/pypi/" + url.PathEscape(
		name,
	) + "/"
	if version != "" {
		target += url.PathEscape(version) + "/"
	}
	target += "json"
	resource := source.Resource{
		Kind:                source.Metadata,
		Path:                "pypi/" + name + "/json",
		Method:              http.MethodGet,
		Accept:              "application/json",
		ExternalURL:         target,
		ExpectedContentType: "application/json",
		PermissionFolder:    true,
		PermissionPath:      name,
		Variant:             "pypi-warehouse-v2",
	}
	if version != "" {
		resource.Path = "pypi/" + name + "/" + version + "/json"
	}
	resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, _ int64, metadata source.MetadataResponse) error {
		_, err := parseWarehouse(r, metadata.BaseURL, name, version)
		return err
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if result.Stream == nil {
		h.k.Error(w, r, remoteError())
		return
	}
	b, err := remoteBytes(result.Stream.Body)
	_ = result.Stream.Body.Close()
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	doc, err := parseWarehouse(bytes.NewReader(b), result.Metadata.BaseURL, name, version)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	paths := []string{}
	filter := func(files []remoteFile) []remoteDoc {
		out := []remoteDoc{}
		for _, f := range files {
			if !h.k.Can(r, f.path, types.ActionRead) {
				continue
			}
			paths = append(paths, f.path)
			f.doc["url"] = encode(repoBase(r) + "/" + escapePath(f.path) + hashFragment(f))
			out = append(out, f.doc)
		}
		return out
	}
	urls := filter(doc.urls)
	if len(urls) == 0 {
		h.k.Error(w, r, notFound(r))
		return
	}
	doc.doc["urls"] = encode(urls)
	if doc.releases != nil {
		releases := map[string][]remoteDoc{}
		for v, files := range doc.releases {
			visible := filter(files)
			if len(visible) > 0 {
				releases[v] = visible
			}
		}
		doc.doc["releases"] = encode(releases)
	}
	fenceRemote(&result, paths)
	h.serveRemote(w, r, result, "application/json", encode(doc.doc))
}
