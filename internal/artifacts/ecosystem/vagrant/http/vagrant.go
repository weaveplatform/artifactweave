package vagranthttp

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

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	boxfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/vagrant"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// protocolName is the protocol's name, and so its prefix:
// pkgroute.Mount serves it under /artifactory/api/vagrant/{repoKey}, where
// downloadURL points each box.
const protocolName = "vagrant"

// Protocol returns the native Vagrant routes for the router kit, under
// /artifactory/api/vagrant/{repoKey}. GET and HEAD of a box file's path,
// <path>.box, serve the stored box as the item download does. GET and HEAD
// of a box name, <name> or <namespace>/<name>, optionally with matrix
// parameters that filter the boxes by their properties, answer the box's
// catalog JSON. Only local and federated repositories have a source; a
// remote or virtual repository answers 501.
func Protocol() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:         protocolName,
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
	if err == nil && len(rel) <= 4096 && boxFile(p) {
		// 7.x serves box files on this route as well as catalogs: its
		// catalogs link /api/vagrant/<repo>/<path>.box (RTFACT-17993), and
		// clients download there (RTFACT-27264, RTFACT-13807). Telling a
		// file from a name by the path alone [I] lets ServeItem check read
		// permission before it looks anything up (DV-057), then the download
		// policy, so a refusal says nothing of what is stored. The query is
		// the item download's: 7.x served a box to a URL with one
		// (RTFACT-27264), and an access_token in it authenticates nothing
		// (DV-148). A Range answers 206; 7.x ignoring it is a bug.
		h.k.ServeItem(w, r, p.String())
		return
	}
	if err != nil || len(rel) > 4096 || p.Folder || !boxfmt.ValidName(p.String()) {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Vagrant box name"))
		return
	}
	if r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Vagrant metadata query"))
		return
	}
	if !h.k.RequireFolder(w, r, "", api.ActionRead) {
		return
	}
	var out catalog
	err = h.k.DB().Tx(r.Context(), func(ctx context.Context) error {
		var e error
		out, e = h.catalog(
			r.WithContext(ctx),
			p.String(),
			model.Matrix{Props: p.Props, Mandatory: p.Mandatory},
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

func (h handler) catalog(r *http.Request, name string, matrix model.Matrix) (catalog, error) {
	out := catalog{Name: name, Versions: []version{}}
	c := pkgroute.RequestFrom(r).Repo
	p, err := h.k.DB().Packages().Find(r.Context(), c.Key, name)
	if errors.Is(err, kernel.ErrNotFound) {
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
	// The blackout refuses the catalog with its 404, naming the box the
	// request names (R2a, R9, R10), after read permission as every policy
	// check is (DV-057): a caller who may read none of the box's entries,
	// or whose matrix parameters match none, still answers "Vagrant box not
	// found".
	if err := h.k.CheckBlackedOut(r, name); err != nil {
		return out, fmt.Errorf("vagrant catalog: %w", err)
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
	v model.PackageVersion,
	matrix model.Matrix,
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
	// Only the patterns leave a box out here. Leaving every box out under
	// the blackout would answer "Vagrant box not found" to a caller who
	// could see one, which catalog refuses with the blackout 404 instead.
	open := c
	open.BlackedOut = nil
	var items []model.Item
	for _, id := range ids {
		it, err := h.k.DB().Items().GetByID(r.Context(), id)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("vagrant box item: %w", err)
		}
		if it.Folder || it.Cached || it.RepoKey != c.Key ||
			!h.k.Can(r, it.FullPath(), api.ActionRead) ||
			generic.CheckDownload(open, it.FullPath(), false) != nil {
			continue
		}
		items = append(items, it)
	}
	// Several ordinary item paths may name one provider. Prefer the newest
	// readable deployment, with a stable path tie-break, without exposing others.
	slices.SortFunc(items, func(a, b model.Item) int {
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

// boxFile reports whether p names a box file rather than a box: a valid
// item path at which a box may be stored (boxfmt.IsBoxPath). Matrix
// parameters keep a path a catalog request, since they are the catalog's
// filter and no source shows them on a box file [I].
func boxFile(p pkgroute.Path) bool {
	if p.Folder || len(p.Props) > 0 || !boxfmt.IsBoxPath(p.String()) {
		return false
	}
	_, _, _, err := kernel.SplitItemPath(p.String())
	return err == nil
}

// downloadURL is the box file route's URL for it, where 7.x's catalogs
// point (RTFACT-17993, RTFACT-27264). It carries no token: Vagrant 2.4.9
// sends its token as a Bearer header to any URL on its server's host
// (add_downloader_authentication.rb), this one included.
func downloadURL(r *http.Request, it model.Item) string {
	parts := strings.Split(it.FullPath(), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	base := baseurl.From(r.Context()) + "/artifactory/api/" + protocolName + "/" +
		url.PathEscape(it.RepoKey)
	return base + "/" + strings.Join(parts, "/")
}
func notFound() error { return apierror.New(http.StatusNotFound, "Vagrant box not found") }
