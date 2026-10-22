package pypi

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// JSONMetadata builds the Warehouse-shaped document from the caller's visible
// index. Filtering precedes version and metadata selection, so neither a hidden
// newer release nor the metadata of a hidden earlier file can leak.
func JSONMetadata(
	ctx context.Context,
	st store.Store,
	key, name, version, base string,
	reads Reads,
) (map[string]any, error) {
	p, ok, err := load(ctx, st, key, pypifmt.Normalize(name), reads)
	if err != nil {
		return nil, err
	}
	rel, found := jsonRelease(p, version)
	if !ok || !found {
		return nil, apierror.New(http.StatusNotFound, "Project or version was not found")
	}
	first := slices.MinFunc(rel.files, func(a, b pypifmt.File) int {
		return cmp.Or(a.Uploaded.Compare(b.Uploaded), cmp.Compare(a.Path, b.Path))
	})
	info := jsonInfo(rel.meta.Text(first), p.name, rel.version, first.RequiresPython)
	anyAvailable := slices.ContainsFunc(rel.files, func(f pypifmt.File) bool { return !f.Yanked })
	info["yanked"] = !anyAvailable
	info["yanked_reason"] = nil
	if !anyAvailable {
		info["yanked_reason"] = optional(first.YankedReason)
	}
	urls := make([]map[string]any, 0, len(rel.files))
	for _, f := range rel.files {
		urls = append(urls, distributionJSON(f, base))
	}
	return map[string]any{"info": info, "urls": urls}, nil
}

func jsonRelease(p project, version string) (release, bool) {
	var latest, stable release
	for _, rel := range p.releases {
		if version != "" {
			if types.ComparePEP440(version, rel.version) == 0 {
				return rel, true
			}
			continue
		}
		if latest.version == "" || types.ComparePEP440(rel.version, latest.version) > 0 {
			latest = rel
		}
		if !pypifmt.IsPrerelease(rel.version) &&
			slices.ContainsFunc(rel.files, func(f pypifmt.File) bool { return !f.Yanked }) &&
			(stable.version == "" || types.ComparePEP440(rel.version, stable.version) > 0) {
			stable = rel
		}
	}
	if stable.version != "" {
		return stable, true
	}
	return latest, latest.version != ""
}

func jsonInfo(text, name, version, requires string) map[string]any {
	fields := pypifmt.ParseFields(text)
	info := map[string]any{}
	for _, key := range []string{
		"author", "author_email", "description_content_type", "download_url", "home_page",
		"keywords", "license", "license_expression", "maintainer", "maintainer_email",
		"name", "platform", "requires_python", "summary", "version",
	} {
		info[key] = optional(fields.Get(strings.ReplaceAll(key, "_", "-")))
	}
	for key, field := range map[string]string{
		"classifiers": "classifier", "dynamic": "dynamic", "license_files": "license-file",
		"provides_extra": "provides-extra", "requires_dist": "requires-dist",
	} {
		values := slices.Clone(fields[field])
		for i, value := range values {
			if key == "dynamic" {
				value = strings.ReplaceAll(strings.ToLower(value), "-", "_")
			}
			values[i] = value
		}
		info[key] = values
	}
	var links map[string]string
	for _, value := range fields["project-url"] {
		if label, target, ok := strings.Cut(value, ","); ok {
			if links == nil {
				links = map[string]string{}
			}
			links[strings.TrimSpace(label)] = strings.TrimSpace(target)
		}
	}
	info["project_urls"] = links
	info["description"] = optional(pypifmt.Description(text))
	if info["name"] == nil {
		info["name"] = name
	}
	if info["version"] == nil {
		info["version"] = version
	}
	if info["requires_python"] == nil {
		info["requires_python"] = optional(requires)
	}
	return info
}

func optional(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func distributionJSON(f pypifmt.File, base string) map[string]any {
	kind, python := "sdist", "source"
	switch {
	case strings.HasSuffix(f.Name, ".whl"):
		kind = "bdist_wheel"
		parts := strings.Split(strings.TrimSuffix(f.Name, ".whl"), "-")
		if len(parts) >= 5 {
			python = parts[len(parts)-3]
		}
	case strings.HasSuffix(f.Name, ".egg"):
		kind = "bdist_egg"
		python = ""
		for _, part := range strings.Split(strings.TrimSuffix(f.Name, ".egg"), "-") {
			if strings.HasPrefix(part, "py") {
				python = strings.TrimPrefix(part, "py")
				break
			}
		}
	}
	return map[string]any{
		"comment_text": "", "digests": map[string]string{"md5": f.MD5, "sha256": f.SHA256},
		"filename": f.Name, "md5_digest": f.MD5, "packagetype": kind, "python_version": python,
		"requires_python": optional(f.RequiresPython), "size": f.Size,
		"upload_time":          f.Uploaded.UTC().Format("2006-01-02T15:04:05"),
		"upload_time_iso_8601": f.Uploaded.UTC().Format("2006-01-02T15:04:05.000000Z"),
		"url":                  base + "/" + escapePath(f.Path), "yanked": f.Yanked,
		"yanked_reason": optional(f.YankedReason),
	}
}

func (h handler) metadataJSON(w http.ResponseWriter, r *http.Request, name, version string) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		h.remoteWarehouse(w, r, name, version)
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	doc, err := JSONMetadata(r.Context(), h.k.DB(), pkgroute.RequestFrom(r).Repo.Key,
		name, version, repoBase(r), h.reads(r))
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	_ = WriteMetadata(w, doc, r.Method == http.MethodHead)
}

// WriteMetadata gives generated GET and kit HEAD the same representation
// headers, including its byte length even when no body is sent.
func WriteMetadata(w http.ResponseWriter, doc map[string]any, head bool) error {
	body := encode(doc)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if !head {
		if _, err := w.Write(body); err != nil {
			return fmt.Errorf("pypi: write JSON metadata: %w", err)
		}
	}
	return nil
}
