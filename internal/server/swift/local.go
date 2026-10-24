package swift

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	swiftfmt "github.com/weaveplatform/artifactweave/internal/format/swift"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type release struct {
	URL string `json:"url,omitempty"`
}

type releases struct {
	Releases map[string]release `json:"releases"`
}

type resource struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Checksum string `json:"checksum"`
}

type releaseInfo struct {
	ID          string         `json:"id"`
	Version     string         `json:"version"`
	Resources   []resource     `json:"resources"`
	Metadata    map[string]any `json:"metadata"`
	PublishedAt string         `json:"publishedAt,omitempty"`
}

func (h handler) localRead(w http.ResponseWriter, r *http.Request, q request) {
	if q.kind == "releases" {
		h.localReleases(w, r, q.packageID)
		return
	}
	p := q.packageID
	path := p.ArchivePath()
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	f, err := h.k.Item(r, path)
	switch {
	case errors.Is(err, store.ErrNotFound), err == nil && f.Item.Folder:
		h.k.Error(w, r, bad(http.StatusNotFound, "Swift release not found"))
		return
	case err != nil:
		h.k.Error(w, r, err)
		return
	}
	switch q.kind {
	case "release":
		h.localReleaseInfo(w, r, p, f.Item)
	case "manifest":
		h.localManifest(w, r, p, f.Item)
	case "archive":
		archiveHeaders(w, p, f.Item)
		h.k.ServeFound(w, r, f)
	default:
		h.k.Error(w, r, bad(http.StatusNotFound, "Swift resource not found"))
	}
}

func (h handler) localReleases(w http.ResponseWriter, r *http.Request, p swiftfmt.Package) {
	folder := p.Scope + "/" + p.Name
	if !h.k.RequireFolder(w, r, folder, types.ActionRead) {
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	pkg, err := h.k.DB().Packages().Find(r.Context(), c.Key, p.ID())
	if errors.Is(err, store.ErrNotFound) {
		h.k.Error(w, r, bad(http.StatusNotFound, "Swift package not found"))
		return
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	versions, err := h.k.DB().Packages().Versions(r.Context(), pkg.ID)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	out := releases{Releases: map[string]release{}}
	for _, v := range versions {
		p.Version = v.Version
		if !swiftfmt.ValidVersion(v.Version) || !h.k.Can(r, p.ArchivePath(), types.ActionRead) {
			continue
		}
		it, err := h.k.DB().Items().Get(r.Context(), c.Key, false, p.ArchivePath())
		if errors.Is(err, store.ErrNotFound) || err == nil && it.Folder {
			continue
		}
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		out.Releases[v.Version] = release{URL: releaseURL(r, p)}
	}
	if len(out.Releases) == 0 {
		h.k.Error(w, r, bad(http.StatusNotFound, "Swift package not found"))
		return
	}
	writeJSON(w, r, out)
}

func (h handler) localReleaseInfo(
	w http.ResponseWriter,
	r *http.Request,
	p swiftfmt.Package,
	it types.Item,
) {
	metadata := map[string]any{}
	props, err := h.k.DB().Items().Properties(r.Context(), it.ID)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if value := props["swift.metadata"]; len(value) == 1 {
		if err := json.Unmarshal([]byte(value[0]), &metadata); err != nil || metadata == nil {
			h.k.Error(w, r, bad(http.StatusInternalServerError, "invalid stored Swift metadata"))
			return
		}
	}
	writeJSON(w, r, releaseInfo{
		ID:      p.ID(),
		Version: p.Version,
		Resources: []resource{
			{Name: "source-archive", Type: "application/zip", Checksum: it.Actual.SHA256},
		},
		Metadata:    metadata,
		PublishedAt: it.Created.UTC().Format(time.RFC3339Nano),
	})
}

func (h handler) localManifest(
	w http.ResponseWriter,
	r *http.Request,
	p swiftfmt.Package,
	it types.Item,
) {
	wanted := "Package.swift"
	if values, ok := r.URL.Query()["swift-version"]; ok && len(values) == 1 {
		if !validToolsVersion(values[0]) {
			h.k.Error(w, r, bad(http.StatusBadRequest, "invalid swift-version"))
			return
		}
		wanted = "Package@swift-" + values[0] + ".swift"
	} else if r.URL.RawQuery != "" {
		h.k.Error(w, r, bad(http.StatusBadRequest, "invalid Swift manifest query"))
		return
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), it.Actual.SHA256)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	a, inspectErr := swiftfmt.InspectArchive(r.Context(), rc, it.Size)
	_ = rc.Close()
	if inspectErr != nil {
		h.k.Error(w, r, fmt.Errorf("swift: stored archive: %w", inspectErr))
		return
	}
	data := a.Manifests[wanted]
	if data == nil && wanted != "Package.swift" {
		w.Header().Set("Content-Version", "1")
		w.Header().Set("Location", releaseURL(r, p)+"/Package.swift")
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	base := releaseURL(r, p) + "/Package.swift"
	var links []string
	for name, version := range a.Tools {
		if name == "Package.swift" {
			continue
		}
		requested := strings.TrimSuffix(strings.TrimPrefix(name, "Package@swift-"), ".swift")
		links = append(
			links,
			fmt.Sprintf(
				`<%s?swift-version=%s>; rel="alternate"; filename="%s"; swift-tools-version="%s"`,
				base,
				requested,
				name,
				version,
			),
		)
	}
	sort.Strings(links)
	if len(links) > 0 {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
	w.Header().Set("Content-Version", "1")
	w.Header().Set("Content-Type", "text/x-swift")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, wanted))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, wanted, it.Created, bytes.NewReader(data))
}

func validToolsVersion(s string) bool {
	if len(s) == 0 || len(s) > 16 {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func archiveHeaders(w http.ResponseWriter, p swiftfmt.Package, it types.Item) {
	w.Header().Set("Content-Version", "1")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().
		Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.zip"`, p.Name, p.Version))
	w.Header().Set("Cache-Control", "private, no-store")
	if sha, err := hex.DecodeString(it.Actual.SHA256); err == nil {
		w.Header().Set("Digest", "sha-256="+base64.StdEncoding.EncodeToString(sha))
	}
}

func releaseURL(r *http.Request, p swiftfmt.Package) string {
	path := "/artifactory/api/swift/" + responseKey(
		r,
	) + "/" + p.Scope + "/" + p.Name + "/" + p.Version
	return baseurl.From(r.Context()) + path
}

func responseKey(r *http.Request) string {
	rel := strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/api/swift/")
	key, _, _ := strings.Cut(rel, "/")
	return key
}

func writeJSON(w http.ResponseWriter, r *http.Request, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Version", "1")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
