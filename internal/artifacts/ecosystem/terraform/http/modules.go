package terraformhttp

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type handler struct {
	k *pkgroute.Kit
	o Options
}

type operation struct {
	module tf.Module
	isList bool
}

func parse(rest string) (operation, bool) {
	path, err := pkgroute.ParsePath(rest)
	if err != nil || path.Folder || len(path.Props) > 0 {
		return operation{}, false
	}
	s := path.Segments
	if len(s) < 6 || s[0] != "v1" || s[1] != "modules" {
		return operation{}, false
	}
	version, list := "0.0.0", len(s) == 6 && s[5] == "versions"
	if !list {
		if len(s) != 7 || s[6] != "download" {
			return operation{}, false
		}
		version = s[5]
	}
	m, ok := tf.ParseModule(strings.Join(s[2:5], "/") + "/" + version + ".zip")
	return operation{module: m, isList: list}, ok
}

func (h handler) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("sig") {
		h.signed(w, r)
		return
	}
	if q, ok := parseProvider(pkgroute.RequestFrom(r).Rest); ok {
		h.provider(w, r, q)
		return
	}
	q, ok := parse(pkgroute.RequestFrom(r).Rest)
	if !ok {
		h.k.Error(w, r, missing())
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.k.Error(w, r, apierror.New(http.StatusMethodNotAllowed, "method not allowed"))
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class != repo.Remote &&
		pkgroute.RequestFrom(r).Repo.TerraformType != repo.TerraformModule {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusNotFound, "Repository is not a MODULE Terraform registry"),
		)
		return
	}
	if !h.k.RequireFolder(w, r, q.module.Package(), api.ActionRead) {
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		h.remoteModule(w, r, q)
		return
	}
	// A blacked-out repository serves no resolution metadata (R9,
	// R2a), so it refuses the version list before the index is read. 7.x
	// keeps a module.json per module (RTFACT-31372) at a path not shown, so
	// the refusal names the request's own path and patterns do not refuse
	// the list: a version whose archive they reject stays listed (R3).
	if q.isList {
		if err := h.k.CheckBlackedOut(r, pkgroute.RequestFrom(r).Rest); err != nil {
			h.k.Error(w, r, err)
			return
		}
	}
	vs, err := h.versions(r, q.module.Package())
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if q.isList {
		versions := make([]map[string]string, 0, len(vs))
		for _, v := range vs {
			if h.k.Can(r, v.item.FullPath(), api.ActionRead) {
				versions = append(versions, map[string]string{"version": v.version})
			}
		}
		if len(versions) == 0 {
			h.k.Error(w, r, missing())
			return
		}
		writeJSON(w, r, map[string]any{"modules": []any{map[string]any{"versions": versions}}})
		return
	}
	for _, v := range vs {
		if v.version != q.module.Version {
			continue
		}
		if !h.k.Require(w, r, v.item.FullPath(), api.ActionRead) {
			return
		}
		// The 204 answers as the GET of its URL would (R9): no URL
		// for an archive the download policy refuses.
		if err := h.k.CheckDownload(r, v.item.FullPath()); err != nil {
			h.k.Error(w, r, err)
			return
		}
		u, err := h.downloadURL(r, v.item)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		w.Header().Set("X-Terraform-Get", u)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.k.Error(w, r, missing())
}

type version struct {
	version string
	item    model.Item
}

func (h handler) versions(r *http.Request, name string) ([]version, error) {
	db, ctx := h.k.DB(), r.Context()
	p, err := db.Packages().Find(ctx, pkgroute.RequestFrom(r).Repo.Key, name)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil, missing()
	}
	if err != nil {
		return nil, fmt.Errorf("terraform: package: %w", err)
	}
	vs, err := db.Packages().Versions(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("terraform: versions: %w", err)
	}
	out := make([]version, 0, len(vs))
	for _, v := range vs {
		ids, err := db.Packages().Files(ctx, v.ID)
		if err != nil {
			return nil, fmt.Errorf("terraform: files: %w", err)
		}
		for _, id := range ids {
			it, err := db.Items().GetByID(ctx, id)
			if errors.Is(err, kernel.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("terraform: file: %w", err)
			}
			if !it.Folder {
				out = append(out, version{v.Version, it})
				break
			}
		}
	}
	return out, nil
}
