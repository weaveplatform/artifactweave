// Package cargo serves the HTTP sparse registry and Cargo web API.
package cargo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/format/cargo"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxPublishMetadata = 1 << 20

func Protocol(sources *source.Service) pkgroute.Protocol {
	classes := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
	}
	if sources != nil {
		classes[repo.Remote] = pkgroute.Remote{Service: sources}
	}
	return pkgroute.Protocol{
		Name: "cargo", Types: []repo.PackageType{repo.Cargo}, Surface: apierror.Artifactory,
		Sources: classes, Reserved: []string{"{repoKey}/reindex"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Get("/index/config.json", h.config)
			r.Head("/index/config.json", h.config)
			r.Get("/index/*", h.index)
			r.Head("/index/*", h.index)
			r.Put("/api/v1/crates/new", h.publish)
			r.Get("/api/v1/crates/{name}/{version}/download", h.download)
			r.Head("/api/v1/crates/{name}/{version}/download", h.download)
			r.Delete("/api/v1/crates/{name}/{version}/yank", h.yank)
			r.Put("/api/v1/crates/{name}/{version}/unyank", h.yank)
			r.Get("/api/v1/crates/{name}", h.versions)
		},
	}
}

type handler struct{ k *pkgroute.Kit }

func failure(status int, message string) error { return apierror.New(status, message) }

func (h handler) replyError(w http.ResponseWriter, r *http.Request, err error) {
	var api *apierror.Error
	if errors.As(err, &api) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(api.Status)
		_ = json.NewEncoder(w).
			Encode(map[string]any{"errors": []map[string]string{{"detail": api.Message}}})
		return
	}
	h.k.Error(w, r, err)
}

func configURL(r *http.Request) string {
	return baseurl.From(r.Context()) + "/artifactory/api/cargo/" + pkgroute.RequestFrom(r).Repo.Key
}

func (h handler) config(w http.ResponseWriter, r *http.Request) {
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		_, result, err := h.remoteDocument(r, "index/config.json", "")
		if err != nil {
			h.replyError(w, r, err)
			return
		}
		if result.Revalidate != nil {
			if err := result.Revalidate(r.Context()); err != nil {
				h.replyError(w, r, err)
				return
			}
		}
		if result.Used != nil {
			_ = result.Used(r.Context())
		}
	}
	c := pkgroute.RequestFrom(r).Repo
	authRequired := c.CargoAnonymousAccess == nil || !*c.CargoAnonymousAccess
	base := configURL(r)
	respond(
		w,
		r,
		map[string]any{"dl": base + "/api/v1/crates", "api": base, "auth-required": authRequired},
	)
}

func respond(w http.ResponseWriter, r *http.Request, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

func recordFor(ctx context.Context, st store.Store, it types.Item) (cargo.Record, error) {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return cargo.Record{}, err
	}
	if len(props[cargo.RecordProperty]) != 1 {
		return cargo.Record{}, cargo.ErrInvalid
	}
	var rec cargo.Record
	if err = json.Unmarshal(
		[]byte(props[cargo.RecordProperty][0]),
		&rec,
	); err != nil || !rec.Valid() ||
		rec.Checksum != it.Actual.SHA256 {
		return cargo.Record{}, cargo.ErrInvalid
	}
	return rec, nil
}

func (h handler) localRecords(r *http.Request, name string) ([]cargo.Record, error) {
	c := pkgroute.RequestFrom(r).Repo
	if !h.k.CanFolder(r, "crates/"+name, types.ActionRead) {
		return nil, failure(http.StatusForbidden, "Cargo crate read denied")
	}
	var out []cargo.Record
	after := ""
	for {
		items, next, err := h.k.DB().
			Items().
			Descendants(r.Context(), c.Key, false, "crates/"+name, store.Page{After: after, Limit: 100})
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.Folder || !h.k.Can(r, it.FullPath(), types.ActionRead) {
				continue
			}
			n, v, ok := cargo.ParseCratePath(it.FullPath())
			if !ok || !strings.EqualFold(n, name) {
				continue
			}
			rec, err := recordFor(r.Context(), h.k.DB(), it)
			if err != nil || rec.Name != n || rec.Version != v {
				return nil, failure(http.StatusBadGateway, "invalid stored Cargo metadata")
			}
			out = append(out, rec)
			if len(out) > 10000 {
				return nil, failure(http.StatusBadGateway, "Cargo index exceeds limit")
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	return out, nil
}

func (h handler) index(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(pkgroute.RequestFrom(r).Rest, "index/")
	parts := strings.Split(path, "/")
	name := parts[len(parts)-1]
	if !cargo.ValidName(name) || path != cargo.IndexPath(name) || strings.ToLower(name) != name {
		h.replyError(w, r, failure(http.StatusNotFound, "Cargo index path not found"))
		return
	}
	if !h.k.RequireFolder(w, r, "crates/"+name, types.ActionRead) {
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		data, result, err := h.remoteDocument(r, "index/"+path, name)
		if err != nil {
			h.replyError(w, r, err)
			return
		}
		h.serveRemoteBytes(w, r, data, result, "text/plain; charset=utf-8")
		return
	}
	records, err := h.localRecords(r, name)
	if err != nil {
		h.replyError(w, r, err)
		return
	}
	if len(records) == 0 {
		h.replyError(w, r, failure(http.StatusNotFound, "Cargo crate not found"))
		return
	}
	var out bytes.Buffer
	for _, rec := range records {
		line, _ := cargo.Encode(rec)
		out.WriteString(line)
		out.WriteByte('\n')
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, name, types.Item{}.Updated, bytes.NewReader(out.Bytes()))
}

type publishDependency struct {
	Name            string   `json:"name"`
	VersionReq      string   `json:"version_req"`
	Features        []string `json:"features"`
	Optional        bool     `json:"optional"`
	DefaultFeatures bool     `json:"default_features"`
	Target          *string  `json:"target"`
	Kind            string   `json:"kind"`
	Registry        *string  `json:"registry"`
	ExplicitName    *string  `json:"explicit_name_in_toml"`
}
type publishMetadata struct {
	Name        string              `json:"name"`
	Version     string              `json:"vers"`
	Deps        []publishDependency `json:"deps"`
	Features    map[string][]string `json:"features"`
	Links       *string             `json:"links"`
	RustVersion *string             `json:"rust_version"`
}

func decodePublish(body io.Reader) (publishMetadata, []byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(body, header[:]); err != nil {
		return publishMetadata{}, nil, cargo.ErrInvalid
	}
	n := binary.LittleEndian.Uint32(header[:])
	if n == 0 || n > maxPublishMetadata {
		return publishMetadata{}, nil, cargo.ErrInvalid
	}
	metaBytes := make([]byte, n)
	if _, err := io.ReadFull(body, metaBytes); err != nil {
		return publishMetadata{}, nil, cargo.ErrInvalid
	}
	var meta publishMetadata
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return meta, nil, cargo.ErrInvalid
	}
	if _, err := io.ReadFull(body, header[:]); err != nil {
		return meta, nil, cargo.ErrInvalid
	}
	n = binary.LittleEndian.Uint32(header[:])
	if n == 0 || n > cargo.MaxCrate {
		return meta, nil, cargo.ErrInvalid
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(body, data); err != nil {
		return meta, nil, cargo.ErrInvalid
	}
	var trailing [1]byte
	if got, _ := body.Read(trailing[:]); got != 0 {
		return meta, nil, cargo.ErrInvalid
	}
	return meta, data, nil
}

func (h handler) publish(w http.ResponseWriter, r *http.Request) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		h.replyError(
			w,
			r,
			failure(http.StatusMethodNotAllowed, "remote Cargo repository is read-only"),
		)
		return
	}
	meta, data, err := decodePublish(io.LimitReader(r.Body, cargo.MaxCrate+maxPublishMetadata+16))
	if err != nil || !cargo.ValidName(meta.Name) || !cargo.ValidVersion(meta.Version) {
		h.replyError(w, r, failure(http.StatusBadRequest, "invalid Cargo publish body"))
		return
	}
	path := cargo.CratePath(meta.Name, meta.Version)
	if _, _, err = cargo.Inspect(
		r.Context(),
		bytes.NewReader(data),
		int64(len(data)),
		path,
	); err != nil {
		h.replyError(
			w,
			r,
			failure(http.StatusBadRequest, "crate manifest does not match publish metadata"),
		)
		return
	}
	if !h.k.Require(w, r, path, types.ActionWrite) {
		return
	}
	digest := sha256.Sum256(data)
	rec := cargo.Record{
		Name:        meta.Name,
		Version:     meta.Version,
		Deps:        []cargo.Dependency{},
		Checksum:    hex.EncodeToString(digest[:]),
		Features:    meta.Features,
		Links:       meta.Links,
		RustVersion: meta.RustVersion,
	}
	if rec.Features == nil {
		rec.Features = map[string][]string{}
	}
	for _, d := range meta.Deps {
		name := d.Name
		var actual *string
		if d.ExplicitName != nil {
			name = *d.ExplicitName
			actual = &d.Name
		}
		if d.Features == nil {
			d.Features = []string{}
		}
		kind := d.Kind
		if kind == "" {
			kind = "normal"
		}
		rec.Deps = append(
			rec.Deps,
			cargo.Dependency{
				Name:            name,
				Req:             d.VersionReq,
				Features:        d.Features,
				Optional:        d.Optional,
				DefaultFeatures: d.DefaultFeatures,
				Target:          d.Target,
				Kind:            kind,
				Registry:        d.Registry,
				Package:         actual,
			},
		)
	}
	line, err := cargo.Encode(rec)
	if err != nil {
		h.replyError(w, r, failure(http.StatusBadRequest, "invalid Cargo metadata"))
		return
	}
	info, err := h.k.Upload(r, bytes.NewReader(data))
	if err != nil {
		h.replyError(w, r, err)
		return
	}
	_, err = h.k.StoreNew(
		r,
		path,
		info,
		types.Properties{cargo.RecordProperty: {line}},
		types.Checksums{},
		func() error { return failure(http.StatusConflict, "Cargo version already exists") },
	)
	if err != nil {
		h.replyError(w, r, err)
		return
	}
	respond(
		w,
		r,
		map[string]any{
			"warnings": map[string]any{
				"invalid_categories": []string{},
				"invalid_badges":     []string{},
				"other":              []string{},
			},
		},
	)
}

func (h handler) download(w http.ResponseWriter, r *http.Request) {
	name, version := chi.URLParam(r, "name"), chi.URLParam(r, "version")
	if !cargo.ValidName(name) || !cargo.ValidVersion(version) {
		h.replyError(w, r, failure(http.StatusNotFound, "Cargo crate not found"))
		return
	}
	path := cargo.CratePath(name, version)
	if pkgroute.RequestFrom(r).Repo.Class != repo.Remote {
		h.k.ServeItem(w, r, path)
		return
	}
	h.remoteCrate(w, r, name, version, path)
}

func (h handler) yank(w http.ResponseWriter, r *http.Request) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		h.replyError(
			w,
			r,
			failure(http.StatusMethodNotAllowed, "remote Cargo repository is read-only"),
		)
		return
	}
	name, version := chi.URLParam(r, "name"), chi.URLParam(r, "version")
	if !cargo.ValidName(name) || !cargo.ValidVersion(version) {
		h.replyError(w, r, failure(http.StatusBadRequest, "invalid Cargo crate identity"))
		return
	}
	path := cargo.CratePath(name, version)
	if !h.k.Require(w, r, path, types.ActionAnnotate) {
		return
	}
	it, err := h.k.DB().Items().Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, path)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			err = failure(http.StatusNotFound, "Cargo version not found")
		}
		h.replyError(w, r, err)
		return
	}
	rec, err := recordFor(r.Context(), h.k.DB(), it)
	if err != nil {
		h.replyError(w, r, failure(http.StatusBadGateway, "invalid stored Cargo metadata"))
		return
	}
	rec.Yanked = r.Method == http.MethodDelete
	line, _ := cargo.Encode(rec)
	if err = h.k.Annotate(
		r,
		it,
		types.Properties{
			cargo.RecordProperty: {line},
			cargo.YankedProperty: {fmt.Sprint(rec.Yanked)},
		},
		nil,
	); err != nil {
		h.replyError(w, r, err)
		return
	}
	respond(w, r, map[string]bool{"ok": true})
}

func (h handler) versions(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !cargo.ValidName(name) {
		h.replyError(w, r, failure(http.StatusNotFound, "Cargo crate not found"))
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		h.replyError(
			w,
			r,
			failure(http.StatusMethodNotAllowed, "remote Cargo versions API unavailable"),
		)
		return
	}
	records, err := h.localRecords(r, name)
	if err != nil {
		h.replyError(w, r, err)
		return
	}
	if len(records) == 0 {
		h.replyError(w, r, failure(http.StatusNotFound, "Cargo crate not found"))
		return
	}
	respond(w, r, map[string]any{"crate": map[string]any{"name": name}, "versions": records})
}
