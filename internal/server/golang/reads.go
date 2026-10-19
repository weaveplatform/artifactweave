package golang

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	goformat "github.com/weaveplatform/artifactweave/internal/format/golang"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// reads answers the GOPROXY reads of decision 0035 from the package index.
// A version is visible once both its .mod and its .zip are stored, and
// until then every Go read of it answers 404: jf go-publish deploys the
// .zip first, and a version listed before its .mod arrived would answer
// 404 for it, which the go command takes for "not here".
type reads struct{ k *pkgroute.Kit }

// serve answers GET and HEAD; net/http drops the body of a HEAD.
func (h reads) serve(w http.ResponseWriter, r *http.Request) {
	q, err := parse(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	switch q.Kind {
	case goformat.KindList:
		h.list(w, r, q.Module)
	case goformat.KindLatest:
		h.latest(w, r, q.Module)
	case goformat.KindInfo:
		h.info(w, r, q.File())
	default:
		h.file(w, r, q.File())
	}
}

// version is a visible version of a module.
type version struct {
	types.PackageVersion
	// info reports a stored .info.
	info bool
}

// list answers @v/list: every visible version whose .mod the caller may
// read, pseudo-versions included, one per line in ascending order with
// the time goformat.ListTime gives it. A module with none answers 404, to
// a caller who may read nothing in it too, so the answer says nothing
// about what it may not read.
func (h reads) list(w http.ResponseWriter, r *http.Request, m string) {
	vs, err := h.readable(r, m)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	indexed := make([]types.PackageVersion, 0, len(vs))
	for _, v := range vs {
		indexed = append(indexed, v.PackageVersion)
	}
	var b strings.Builder
	for _, line := range goformat.Listed(indexed) {
		b.WriteString(line + "\n")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, b.String())
}

// latest answers @latest and @v/latest.info with the .info of the version
// goformat.Latest chooses among those list would consider, pseudo-versions
// included.
func (h reads) latest(w http.ResponseWriter, r *http.Request, m string) {
	vs, err := h.readable(r, m)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	// vs is never empty, so Latest always chooses one of them.
	latest, _ := goformat.Latest(names(vs))
	for _, v := range vs {
		if v.Version == latest {
			h.serveInfo(w, r, m, v)
		}
	}
}

// info answers a version's .info. A caller who may read neither the .info
// nor the .mod it is synthesised from is refused before anything is looked
// up.
func (h reads) info(w http.ResponseWriter, r *http.Request, f goformat.File) {
	mod := goformat.File{Module: f.Module, Version: f.Version, Ext: goformat.ExtMod}.Path()
	if !h.k.Can(r, mod, types.ActionRead) && !h.k.Require(w, r, f.Path(), types.ActionRead) {
		return
	}
	v, err := h.version(r, f.Module, f.Version)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	h.serveInfo(w, r, f.Module, v)
}

// serveInfo answers v's stored .info through the item download, which
// needs Read on it, or synthesises one from the package index, which needs
// Read on the .mod. A synthesised time is when the version's first file
// was stored: jf go-publish deploys .info last and skips it against an
// Artifactory older than 6.10 (research 26 section 3.1).
func (h reads) serveInfo(w http.ResponseWriter, r *http.Request, m string, v version) {
	if v.info {
		h.k.ServeItem(
			w,
			r,
			goformat.File{Module: m, Version: v.Version, Ext: goformat.ExtInfo}.Path(),
		)
		return
	}
	mod := goformat.File{Module: m, Version: v.Version, Ext: goformat.ExtMod}.Path()
	if !h.k.Require(w, r, mod, types.ActionRead) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).
		Encode(info{Version: v.Version, Time: v.Created.UTC().Format(time.RFC3339)})
}

// info is a version's .info as the GOPROXY protocol defines it.
type info struct {
	Version string `json:"Version"` //nolint:tagliatelle // the GOPROXY protocol's names
	Time    string `json:"Time"`    //nolint:tagliatelle // the GOPROXY protocol's names
}

// file answers a visible version's .mod or .zip through the item download,
// so the download redirect policy, Range and the download counts apply. A
// caller who may not read the file is refused before the version is looked
// up.
func (h reads) file(w http.ResponseWriter, r *http.Request, f goformat.File) {
	if !h.k.Require(w, r, f.Path(), types.ActionRead) {
		return
	}
	if _, err := h.version(r, f.Module, f.Version); err != nil {
		h.k.Error(w, r, err)
		return
	}
	h.k.ServeItem(w, r, f.Path())
}

// version returns version v of module m when it is visible, or the 404.
func (h reads) version(r *http.Request, m, v string) (version, error) {
	ctx := r.Context()
	key := pkgroute.RequestFrom(r).Repo.Key
	missing := apierror.New(http.StatusNotFound,
		fmt.Sprintf("Version %s of module %s is not in %s.", v, m, key))
	db := h.k.DB()
	p, err := db.Packages().Find(ctx, key, m)
	if errors.Is(err, store.ErrNotFound) {
		return version{}, missing
	} else if err != nil {
		return version{}, fmt.Errorf("golang: module %s in %s: %w", m, key, err)
	}
	pv, err := db.Packages().Version(ctx, p.ID, v)
	if errors.Is(err, store.ErrNotFound) {
		return version{}, missing
	} else if err != nil {
		return version{}, fmt.Errorf("golang: version %s of %s in %s: %w", v, m, key, err)
	}
	stored := map[string]bool{}
	for _, ext := range []string{goformat.ExtZip, goformat.ExtMod, goformat.ExtInfo} {
		path := goformat.File{Module: m, Version: v, Ext: ext}.Path()
		f, err := h.k.Item(r, path)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return version{}, err //nolint:wrapcheck // the kit wraps its own
		default:
			stored[ext] = !f.Item.Folder
		}
	}
	if !stored[goformat.ExtZip] || !stored[goformat.ExtMod] {
		return version{}, missing
	}
	return version{PackageVersion: pv, info: stored[goformat.ExtInfo]}, nil
}

// readable returns m's visible versions whose .mod the caller may read, or
// the 404 when there are none.
func (h reads) readable(r *http.Request, m string) ([]version, error) {
	ctx := r.Context()
	key := pkgroute.RequestFrom(r).Repo.Key
	missing := apierror.New(http.StatusNotFound,
		fmt.Sprintf("Module %s has no version in %s.", m, key))
	db := h.k.DB()
	p, err := db.Packages().Find(ctx, key, m)
	if errors.Is(err, store.ErrNotFound) {
		return nil, missing
	} else if err != nil {
		return nil, fmt.Errorf("golang: module %s in %s: %w", m, key, err)
	}
	all, err := db.Packages().Versions(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("golang: versions of %s in %s: %w", m, key, err)
	}
	stored, err := files(ctx, db, key, m+"/@v")
	if err != nil {
		return nil, err
	}
	var out []version
	for _, v := range all {
		mod := goformat.File{Module: m, Version: v.Version, Ext: goformat.ExtMod}
		if !stored[v.Version+"."+goformat.ExtZip] || !stored[v.Version+"."+goformat.ExtMod] ||
			!h.k.Can(r, mod.Path(), types.ActionRead) {
			continue
		}
		out = append(out, version{PackageVersion: v, info: stored[v.Version+"."+goformat.ExtInfo]})
	}
	if len(out) == 0 {
		return nil, missing
	}
	return out, nil
}

// files is the names of the files directly in folder, which for a module's
// @v folder says which of every version's files are stored in one listing.
func files(ctx context.Context, db store.Store, key, folder string) (map[string]bool, error) {
	out := map[string]bool{}
	page := store.Page{Limit: store.MaxLimit}
	for {
		items, next, err := db.Items().Children(ctx, key, false, folder, page)
		if err != nil {
			return nil, fmt.Errorf("golang: list %s/%s: %w", key, folder, err)
		}
		for _, it := range items {
			if !it.Folder {
				out[it.Name] = true
			}
		}
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

func names(vs []version) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Version)
	}
	return out
}
