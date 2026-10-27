package skills

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	skillsfmt "github.com/weaveplatform/artifactweave/internal/format/skills"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type skillVersion struct {
	record types.PackageVersion
	meta   skillsfmt.Metadata
}

type skill struct {
	slug     string
	versions []skillVersion // newest visible version first
}

func (s skill) latest() skillVersion { return s.versions[0] }

func (h handler) authenticated(w http.ResponseWriter, r *http.Request) bool {
	if _, err := auth.RequireAuthenticated(r.Context()); err != nil {
		h.k.Error(w, r, err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func badQuery(message string) error { return apierror.New(http.StatusBadRequest, message) }

func notFound() error { return apierror.New(http.StatusNotFound, "Could not find resource") }

func (h handler) discovery(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	base := scheme + "://" + r.Host
	writeJSON(w, map[string]string{
		"apiBase": base + "/artifactory/api/skills/" +
			url.PathEscape(pkgroute.RequestFrom(r).Repo.Key),
		"authBase": base,
		"registry": base + "/artifactory/api/skills/" +
			url.PathEscape(pkgroute.RequestFrom(r).Repo.Key),
		"minCliVersion": "0.1.0",
	})
}

// visible reads the durable package index, then filters every version by its
// canonical bundle path. A broad folder grant must never reveal a version
// whose artifact path has a narrower deny rule.
func (h handler) visible(r *http.Request, slug string) (skill, error) {
	key := pkgroute.RequestFrom(r).Repo.Key
	p, err := h.k.DB().Packages().Find(r.Context(), key, slug)
	if errors.Is(err, store.ErrNotFound) {
		return skill{}, notFound()
	}
	if err != nil {
		return skill{}, fmt.Errorf("skills: find %s: %w", slug, err)
	}
	versions, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
	if err != nil {
		return skill{}, fmt.Errorf("skills: versions %s: %w", slug, err)
	}
	s := skill{slug: slug}
	for i := len(versions) - 1; i >= 0; i-- {
		v := versions[i]
		if !h.k.Can(r, skillsfmt.Path(slug, v.Version), types.ActionRead) {
			continue
		}
		var meta skillsfmt.Metadata
		if err := json.Unmarshal(v.Metadata, &meta); err != nil {
			return skill{}, fmt.Errorf("skills: metadata %s/%s: %w", slug, v.Version, err)
		}
		s.versions = append(s.versions, skillVersion{record: v, meta: meta})
	}
	if len(s.versions) == 0 {
		return skill{}, notFound()
	}
	return s, nil
}

func (h handler) selected(
	w http.ResponseWriter,
	r *http.Request,
	slug, version string,
) (skillVersion, bool) {
	if !h.authenticated(w, r) {
		return skillVersion{}, false
	}
	if !skillsfmt.ValidSlug(slug) || version != "latest" && !skillsfmt.ValidVersion(version) {
		h.k.Error(w, r, badQuery("invalid skill slug or version"))
		return skillVersion{}, false
	}
	if !h.k.RequireFolder(w, r, slug, types.ActionRead) {
		return skillVersion{}, false
	}
	s, err := h.visible(r, slug)
	if err != nil {
		h.k.Error(w, r, err)
		return skillVersion{}, false
	}
	if version == "" || version == "latest" {
		return s.latest(), true
	}
	for _, v := range s.versions {
		if v.record.Version == version {
			return v, true
		}
	}
	h.k.Error(w, r, notFound())
	return skillVersion{}, false
}

type listItem struct {
	Slug          string `json:"slug"`
	DisplayName   string `json:"displayName"` //nolint:tagliatelle // ClawHub wire format
	Summary       string `json:"summary"`
	Updated       string `json:"updated"`
	Versions      int    `json:"versions"`
	LatestVersion struct {
		Version   string `json:"version"`
		CreatedAt int64  `json:"createdAt"` //nolint:tagliatelle // JFrog CLI wire format
	} `json:"latestVersion"` //nolint:tagliatelle // JFrog CLI wire format
}

func display(m skillsfmt.Metadata, slug string) string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	if m.Name != "" {
		return m.Name
	}
	return slug
}

func summary(m skillsfmt.Metadata) string {
	if m.Summary != "" {
		return m.Summary
	}
	return m.Description
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (h handler) allVisible(r *http.Request) ([]skill, error) {
	var out []skill
	page := store.Page{Limit: store.MaxLimit}
	key := pkgroute.RequestFrom(r).Repo.Key
	for {
		all, next, err := h.k.DB().Packages().List(r.Context(), key, page)
		if err != nil {
			return nil, fmt.Errorf("skills: list packages: %w", err)
		}
		for _, p := range all {
			s, err := h.visible(r, p.Name)
			if errors.Is(err, store.ErrNotFound) || is404(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		if next == "" {
			break
		}
		page.After = next
	}
	return out, nil
}

func is404(err error) bool {
	var ae *apierror.Error
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

func boundInt(raw string, fallback, min, max int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, badQuery("invalid pagination parameter")
	}
	return n, nil
}

type cursor struct {
	Offset int `json:"offset"`
}

func decodeCursor(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	if len(raw) > 128 {
		return 0, badQuery("invalid cursor")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, badQuery("invalid cursor")
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil || c.Offset < 0 || c.Offset > 1_000_000 {
		return 0, badQuery("invalid cursor")
	}
	return c.Offset, nil
}

func encodeCursor(offset int) string {
	b, _ := json.Marshal(cursor{Offset: offset})
	return base64.RawURLEncoding.EncodeToString(b)
}

func (h handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.authenticated(w, r) || !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	limit, err := boundInt(r.URL.Query().Get("limit"), 200, 1, 200)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	offset, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	sortBy := r.URL.Query().Get("sort")
	if sortBy != "" && sortBy != "updated" && sortBy != "name" && sortBy != "downloads" {
		h.k.Error(w, r, badQuery("invalid sort"))
		return
	}
	all, err := h.allVisible(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if sortBy == "name" || sortBy == "downloads" {
		slices.SortFunc(all, func(a, b skill) int { return strings.Compare(a.slug, b.slug) })
	} else {
		slices.SortFunc(all, func(a, b skill) int {
			if c := b.latest().record.Created.Compare(a.latest().record.Created); c != 0 {
				return c
			}
			return strings.Compare(a.slug, b.slug)
		})
	}
	if offset > len(all) {
		offset = len(all)
	}
	end := min(offset+limit, len(all))
	items := make([]listItem, 0, end-offset)
	for _, s := range all[offset:end] {
		v := s.latest()
		li := listItem{
			Slug: s.slug, DisplayName: display(v.meta, s.slug),
			Summary: summary(v.meta), Updated: stamp(v.record.Created),
			Versions: len(s.versions),
		}
		li.LatestVersion.Version = v.record.Version
		li.LatestVersion.CreatedAt = v.record.Created.Unix()
		items = append(items, li)
	}
	next := ""
	if end < len(all) {
		next = encodeCursor(end)
	}
	writeJSON(w, map[string]any{"items": items, "nextCursor": next, "total": len(all)})
}

func (h handler) detail(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if !h.authenticated(w, r) {
		return
	}
	if !skillsfmt.ValidSlug(slug) {
		h.k.Error(w, r, badQuery("invalid skill slug"))
		return
	}
	if !h.k.RequireFolder(w, r, slug, types.ActionRead) {
		return
	}
	s, err := h.visible(r, slug)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	v := s.latest()
	tags := v.meta.Tags
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, map[string]any{
		"slug": slug, "displayName": display(v.meta, slug),
		"summary": summary(v.meta), "tags": tags, "versions": len(s.versions),
		"latestVersion": v.record.Version, "updated": stamp(v.record.Created),
	})
}

func (h handler) versions(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if !h.authenticated(w, r) {
		return
	}
	if !skillsfmt.ValidSlug(slug) {
		h.k.Error(w, r, badQuery("invalid skill slug"))
		return
	}
	if !h.k.RequireFolder(w, r, slug, types.ActionRead) {
		return
	}
	s, err := h.visible(r, slug)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	limit, err := boundInt(r.URL.Query().Get("limit"), 200, 1, 200)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	offset, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if offset > len(s.versions) {
		offset = len(s.versions)
	}
	end := min(offset+limit, len(s.versions))
	results := make([]map[string]any, 0, end-offset)
	for _, v := range s.versions[offset:end] {
		results = append(results, map[string]any{
			"version": v.record.Version, "fingerprint": v.meta.Fingerprint,
			"published": stamp(v.record.Created), "createdAt": v.record.Created.Unix(),
			"changelog": v.meta.Changelog,
		})
	}
	next := ""
	if end < len(s.versions) {
		next = encodeCursor(end)
	}
	writeJSON(w, map[string]any{
		"slug": slug, "items": results, "versions": results, "nextCursor": next,
	})
}

func (h handler) version(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	v, ok := h.selected(w, r, slug, chi.URLParam(r, "version"))
	if !ok {
		return
	}
	base := "/artifactory/api/skills/" + url.PathEscape(pkgroute.RequestFrom(r).Repo.Key)
	writeJSON(w, map[string]string{
		"slug": slug, "version": v.record.Version,
		"displayName": display(v.meta, slug), "summary": summary(v.meta),
		"fingerprint": v.meta.Fingerprint, "changelog": v.meta.Changelog,
		"published": stamp(v.record.Created), "downloadUrl": base +
			"/api/v1/download?slug=" + url.QueryEscape(slug) + "&version=" +
			url.QueryEscape(v.record.Version),
	})
}

func (h handler) search(w http.ResponseWriter, r *http.Request) {
	if !h.authenticated(w, r) || !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if len(q) == 0 || len(q) > 256 {
		h.k.Error(w, r, badQuery("invalid search query"))
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			h.k.Error(w, r, badQuery("invalid search limit"))
			return
		}
		limit = max(1, min(n, 100))
	}
	var err error
	offset, err := boundInt(r.URL.Query().Get("offset"), 0, 0, 1_000_000)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	all, err := h.allVisible(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	type result struct {
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		Version     string `json:"version"`
		DisplayName string `json:"displayName"` //nolint:tagliatelle // ClawHub wire format
		Summary     string `json:"summary"`
		Description string `json:"description"`
		Match       string `json:"match"`
	}
	results := make([]result, 0)
	for _, s := range all {
		v := s.latest()
		match := ""
		switch {
		case strings.Contains(s.slug, q):
			match = "slug"
		case strings.Contains(strings.ToLower(display(v.meta, s.slug)), q):
			match = "displayName"
		case strings.Contains(strings.ToLower(summary(v.meta)), q):
			match = "summary"
		default:
			for _, tag := range v.meta.Tags {
				if strings.Contains(strings.ToLower(tag), q) {
					match = "tags"
					break
				}
			}
		}
		if match != "" {
			results = append(results, result{
				Slug: s.slug, Name: s.slug, Version: v.record.Version,
				DisplayName: display(v.meta, s.slug), Summary: summary(v.meta),
				Description: summary(v.meta), Match: match,
			})
		}
	}
	total := len(results)
	if offset > total {
		offset = total
	}
	results = results[offset:min(offset+limit, total)]
	writeJSON(w, map[string]any{
		"results": results, "total": total, "limit": limit, "offset": offset,
	})
}

func (h handler) resolve(w http.ResponseWriter, r *http.Request) {
	if !h.authenticated(w, r) {
		return
	}
	slug, hash := r.URL.Query().Get("slug"), r.URL.Query().Get("hash")
	if len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
		h.k.Error(w, r, badQuery("invalid SHA-256 fingerprint"))
		return
	}
	if !skillsfmt.ValidSlug(slug) {
		h.k.Error(w, r, badQuery("invalid skill slug"))
		return
	}
	if !h.k.RequireFolder(w, r, slug, types.ActionRead) {
		return
	}
	s, err := h.visible(r, slug)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, v := range s.versions {
		if v.meta.Fingerprint == hash {
			writeJSON(w, map[string]string{
				"slug": slug, "version": v.record.Version, "fingerprint": hash,
			})
			return
		}
	}
	h.k.Error(w, r, notFound())
}

func (h handler) download(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	v, ok := h.selected(w, r, slug, r.URL.Query().Get("version"))
	if !ok {
		return
	}
	h.k.ServeItem(w, r, skillsfmt.Path(slug, v.record.Version))
}

func (h handler) singleFile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	v, ok := h.selected(w, r, slug, r.URL.Query().Get("version"))
	if !ok {
		return
	}
	name := r.URL.Query().Get("path")
	if !safeFileName(name) || strings.HasSuffix(name, "/") {
		h.k.Error(w, r, badQuery("invalid skill file path"))
		return
	}
	path := skillsfmt.Path(slug, v.record.Version)
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	f, err := h.k.Item(r, path)
	if errors.Is(err, store.ErrNotFound) {
		h.k.Error(w, r, notFound())
		return
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	rc, info, err := h.k.Blobs().Open(r.Context(), f.Item.Actual.SHA256)
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("skills: open bundle: %w", err))
		return
	}
	defer rc.Close()
	if info.Size < 0 || info.Size > skillsfmt.MaxArchiveBytes {
		h.k.Error(w, r, apierror.New(http.StatusInternalServerError, "invalid stored skill bundle"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(rc, skillsfmt.MaxArchiveBytes+1))
	if err != nil || int64(len(data)) != info.Size {
		h.k.Error(w, r, fmt.Errorf("skills: read bundle: %w", err))
		return
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("skills: stored zip: %w", err))
		return
	}
	for _, entry := range z.File {
		if entry.Name != name || !entry.Mode().IsRegular() ||
			entry.UncompressedSize64 > skillsfmt.MaxArchiveBytes {
			continue
		}
		part, err := entry.Open()
		if err != nil {
			h.k.Error(w, r, fmt.Errorf("skills: open file: %w", err))
			return
		}
		content, readErr := io.ReadAll(io.LimitReader(part, skillsfmt.MaxArchiveBytes+1))
		closeErr := part.Close()
		if readErr != nil || closeErr != nil || uint64(len(content)) != entry.UncompressedSize64 {
			h.k.Error(
				w,
				r,
				apierror.New(http.StatusInternalServerError, "invalid stored skill file"),
			)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(content))
		_, _ = w.Write(content)
		return
	}
	h.k.Error(w, r, notFound())
}

func (h handler) xrayStatus(w http.ResponseWriter, r *http.Request) {
	if !h.authenticated(w, r) {
		return
	}
	path := r.URL.Query().Get("path")
	if _, _, ok := skillsfmt.ParsePath(path); !ok {
		h.k.Error(w, r, badQuery("invalid skill artifact path"))
		return
	}
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	f, err := h.k.Item(r, path)
	if errors.Is(err, store.ErrNotFound) || err == nil && f.Item.Folder {
		h.k.Error(w, r, notFound())
		return
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, map[string]string{
		"status": "NOT_IN_ENTITLEMENT", "repoKey": pkgroute.RequestFrom(r).Repo.Key,
		"path": path,
	})
}
