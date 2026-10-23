// Package skills serves the local ClawHub-compatible Skills publish API.
package skills

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	skillsfmt "github.com/weaveplatform/artifactweave/internal/format/skills"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxPayloadBytes = 64 << 10

func Protocol() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name: "skills", Types: []repo.PackageType{repo.Skills},
		Surface: apierror.Artifactory,
		Sources: map[repo.Class]pkgroute.Source{repo.Local: pkgroute.Local{}},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k}
			r.Get("/.well-known/clawhub.json", h.discovery)
			r.Get("/api/v1/skills", h.list)
			r.Get("/api/v1/skills/{slug}", h.detail)
			r.Get("/api/v1/skills/{slug}/versions", h.versions)
			r.Get("/api/v1/skills/{slug}/versions/{version}", h.version)
			r.Get("/api/v1/skills/{slug}/file", h.singleFile)
			r.Get("/api/v1/search", h.search)
			r.Get("/api/v1/resolve", h.resolve)
			r.Get("/api/v1/download", h.download)
			r.Get("/xrayStatus", h.xrayStatus)
			r.Post("/api/v1/skills", h.publish)
		},
	}
}

type handler struct{ k *pkgroute.Kit }

type payload struct {
	Slug        string   `json:"slug"`
	Version     string   `json:"version"`
	DisplayName string   `json:"displayName"` //nolint:tagliatelle // JFrog's publish field
	Summary     string   `json:"summary"`
	Changelog   string   `json:"changelog"`
	Tags        []string `json:"tags"`
}

type file struct {
	name string
	data []byte
}

func (h handler) publish(w http.ResponseWriter, r *http.Request) {
	if !h.k.RequireFolder(w, r, "", types.ActionWrite) {
		return
	}
	p, files, err := readPublish(w, r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	target := skillsfmt.Path(p.Slug, p.Version)
	body, err := bundle(files)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	props := types.Properties{}
	for key, value := range map[string]string{
		"skill.displayName": p.DisplayName,
		"skill.summary":     p.Summary,
		"skill.changelog":   p.Changelog,
	} {
		if value != "" {
			props[key] = []string{types.TruncatePropertyValue(value, types.PropertyValueMax)}
		}
	}
	if len(p.Tags) > 0 {
		props["skill.tags"] = p.Tags
	}
	it, err := h.k.Store(r, target, bytes.NewReader(body), props, types.Checksums{})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"slug": p.Slug, "version": p.Version, "fingerprint": it.Actual.SHA256,
	})
}

func readPublish(w http.ResponseWriter, r *http.Request) (payload, []file, error) {
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" || params["boundary"] == "" {
		return payload{}, nil, apierror.New(
			http.StatusUnsupportedMediaType,
			"multipart/form-data required",
		)
	}
	r.Body = http.MaxBytesReader(w, r.Body, skillsfmt.MaxArchiveBytes+maxPayloadBytes+(1<<20))
	mr := multipart.NewReader(r.Body, params["boundary"])
	var p payload
	var files []file
	seen := map[string]bool{}
	havePayload := false
	total := 0
	for parts := 0; ; parts++ {
		if parts > skillsfmt.MaxFiles+1 {
			return payload{}, nil, apierror.New(
				http.StatusRequestEntityTooLarge,
				"too many skill files",
			)
		}
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return payload{}, nil, formError(err)
		}
		switch part.FormName() {
		case "payload":
			if havePayload || part.FileName() != "" {
				return payload{}, nil, apierror.New(http.StatusBadRequest, "invalid payload part")
			}
			havePayload = true
			data, err := io.ReadAll(io.LimitReader(part, maxPayloadBytes+1))
			if err != nil {
				return payload{}, nil, formError(err)
			}
			if len(data) > maxPayloadBytes {
				return payload{}, nil, apierror.New(
					http.StatusRequestEntityTooLarge,
					"payload too large",
				)
			}
			if err := json.Unmarshal(data, &p); err != nil {
				return payload{}, nil, apierror.New(http.StatusBadRequest, "invalid payload JSON")
			}
		case "files[]":
			_, disp, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if err != nil {
				return payload{}, nil, apierror.New(
					http.StatusBadRequest,
					"invalid file disposition",
				)
			}
			name := disp["filename"]
			if name == "" || path.Base(name) == "." || !safeFileName(name) || seen[name] {
				return payload{}, nil, apierror.New(
					http.StatusBadRequest,
					"invalid skill file name",
				)
			}
			seen[name] = true
			data, err := io.ReadAll(io.LimitReader(part, int64(skillsfmt.MaxArchiveBytes-total+1)))
			if err != nil {
				return payload{}, nil, formError(err)
			}
			total += len(data)
			if total > skillsfmt.MaxArchiveBytes {
				return payload{}, nil, apierror.New(
					http.StatusRequestEntityTooLarge,
					"skill bundle too large",
				)
			}
			files = append(files, file{name: name, data: data})
		default:
			return payload{}, nil, apierror.New(http.StatusBadRequest, "unknown multipart part")
		}
	}
	if !havePayload || len(files) == 0 || !skillsfmt.ValidSlug(p.Slug) ||
		!skillsfmt.ValidVersion(p.Version) || !validPayload(p) {
		return payload{}, nil, apierror.New(
			http.StatusBadRequest,
			"invalid skill slug, version or files",
		)
	}
	return p, files, nil
}

func validPayload(p payload) bool {
	if len(p.DisplayName) > 4096 || len(p.Summary) > 4096 ||
		len(p.Changelog) > 4096 || len(p.Tags) > 64 {
		return false
	}
	for _, tag := range p.Tags {
		if len(tag) > 256 {
			return false
		}
	}
	return true
}

func formError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return apierror.New(http.StatusRequestEntityTooLarge, "skill upload too large")
	}
	return apierror.New(http.StatusBadRequest, "malformed multipart request")
}

func safeFileName(name string) bool {
	if name == "" || len(name) > 1024 || name[0] == '/' || path.Clean(name) != name {
		return false
	}
	for _, ch := range name {
		if ch == '\\' || ch == 0 {
			return false
		}
	}
	for part := range slices.Values(bytes.Split([]byte(name), []byte("/"))) {
		if len(part) == 0 || string(part) == "." || string(part) == ".." {
			return false
		}
	}
	return true
}

func bundle(files []file) ([]byte, error) {
	slices.SortFunc(files, func(a, b file) int {
		switch {
		case a.name < b.name:
			return -1
		case a.name > b.name:
			return 1
		default:
			return 0
		}
	})
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Store}
		h.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
		h.SetMode(0o644)
		writer, err := zw.CreateHeader(h)
		if err != nil {
			return nil, apierror.New(http.StatusBadRequest, "invalid skill file")
		}
		if _, err := writer.Write(f.data); err != nil {
			return nil, apierror.New(http.StatusBadRequest, "invalid skill file")
		}
	}
	if err := zw.Close(); err != nil || out.Len() > skillsfmt.MaxArchiveBytes {
		return nil, apierror.New(http.StatusRequestEntityTooLarge, "skill bundle too large")
	}
	return out.Bytes(), nil
}
