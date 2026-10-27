// Package vagrant serves the native Vagrant box catalog at Artifactory's prefix.
package vagrant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/format/generic"
	boxfmt "github.com/weaveplatform/artifactweave/internal/format/vagrant"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func Protocol() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:         "vagrant",
		Types:        []repo.PackageType{repo.Vagrant},
		ErrorProfile: apierror.ProfileArtifactory,
		Sources: map[repo.Class]pkgroute.Source{
			repo.Local:     pkgroute.Local{},
			repo.Federated: pkgroute.Local{},
		},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
		},
	}
}

type handler struct{ k *pkgroute.Kit }

type catalog struct {
	Name     string    `json:"name"`
	Versions []version `json:"versions"`
}

type version struct {
	Version   string     `json:"version"`
	Providers []provider `json:"providers"`
}

type provider struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	ChecksumType string `json:"checksum_type"`
	Checksum     string `json:"checksum"`
	Architecture string `json:"architecture,omitempty"`
}

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	rel := pkgroute.RequestFrom(r).Rest
	p, err := pkgroute.ParsePath(rel)
	if err != nil || len(rel) > 4096 || p.Folder || !boxfmt.ValidName(p.String()) {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Vagrant box name"))
		return
	}
	if r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Vagrant metadata query"))
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	var out catalog
	err = h.k.DB().Tx(r.Context(), func(ctx context.Context) error {
		var e error
		out, e = h.catalog(
			r.WithContext(ctx),
			p.String(),
			types.Matrix{Props: p.Props, Mandatory: p.Mandatory},
		)
		return e
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	data, err := json.Marshal(out)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (h handler) catalog(r *http.Request, name string, matrix types.Matrix) (catalog, error) {
	out := catalog{Name: name, Versions: []version{}}
	c := pkgroute.RequestFrom(r).Repo
	p, err := h.k.DB().Packages().Find(r.Context(), c.Key, name)
	if errors.Is(err, store.ErrNotFound) {
		return out, notFound()
	}
	if err != nil {
		return out, fmt.Errorf("vagrant package: %w", err)
	}
	versions, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
	if err != nil {
		return out, fmt.Errorf("vagrant versions: %w", err)
	}
	if len(versions) > 10000 {
		return out, apierror.New(http.StatusServiceUnavailable, "Vagrant catalog exceeds limit")
	}
	remaining := 10000
	for _, v := range versions {
		entries, err := h.providers(r, name, v, matrix, &remaining)
		if err != nil {
			return out, err
		}
		if len(entries) > 0 {
			out.Versions = append(out.Versions, version{Version: v.Version, Providers: entries})
		}
	}
	if len(out.Versions) == 0 {
		return out, notFound()
	}
	slices.SortFunc(
		out.Versions,
		func(a, b version) int { return strings.Compare(a.Version, b.Version) },
	)
	return out, nil
}

func (h handler) providers(
	r *http.Request,
	name string,
	v types.PackageVersion,
	matrix types.Matrix,
	remaining *int,
) ([]provider, error) {
	ids, err := h.k.DB().Packages().Files(r.Context(), v.ID)
	if err != nil {
		return nil, fmt.Errorf("vagrant version files: %w", err)
	}
	if len(ids) > *remaining {
		return nil, apierror.New(http.StatusServiceUnavailable, "Vagrant providers exceed limit")
	}
	*remaining -= len(ids)
	c := pkgroute.RequestFrom(r).Repo
	var items []types.Item
	for _, id := range ids {
		it, err := h.k.DB().Items().GetByID(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("vagrant box item: %w", err)
		}
		if it.Folder || it.Cached || it.RepoKey != c.Key ||
			!h.k.Can(r, it.FullPath(), types.ActionRead) ||
			generic.CheckDownload(c, it.FullPath(), false) != nil {
			continue
		}
		items = append(items, it)
	}
	// Several ordinary item paths may name one provider. Prefer the newest
	// readable deployment, with a stable path tie-break, without exposing others.
	slices.SortFunc(items, func(a, b types.Item) int {
		if n := b.Updated.Compare(a.Updated); n != 0 {
			return n
		}
		return strings.Compare(a.FullPath(), b.FullPath())
	})
	seen := map[string]bool{}
	out := make([]provider, 0, len(items))
	for _, it := range items {
		props, err := h.k.DB().Items().Properties(r.Context(), it.ID)
		if err != nil {
			return nil, fmt.Errorf("vagrant box properties: %w", err)
		}
		id, box, ok := boxfmt.Verified(props, it.Actual.SHA256)
		if !ok || id.Name != name || id.Version != v.Version || !matrix.Matches(props) {
			continue
		}
		key := id.Provider + "\x00" + box.Architecture
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(
			out,
			provider{
				Name:         id.Provider,
				URL:          downloadURL(r, it),
				ChecksumType: "sha256",
				Checksum:     it.Actual.SHA256,
				Architecture: box.Architecture,
			},
		)
	}
	slices.SortFunc(out, func(a, b provider) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return strings.Compare(a.Architecture, b.Architecture)
	})
	return out, nil
}

func downloadURL(r *http.Request, it types.Item) string {
	parts := strings.Split(it.FullPath(), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	base := baseurl.From(r.Context()) + "/artifactory/" + url.PathEscape(it.RepoKey)
	return base + "/" + strings.Join(parts, "/")
}
func notFound() error { return apierror.New(http.StatusNotFound, "Vagrant box not found") }
