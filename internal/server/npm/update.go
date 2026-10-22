package npm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// okBody is the body of a packument update and of each unpublish step. npm
// reads none of them.
var okBody = map[string]bool{"ok": true}

// update applies a packument update, as npm deprecate sends it: the
// packument it read with ?write=true, each matching version's deprecated
// set to the message, or to "" to undeprecate (R25 2.2). Only deprecated
// is applied, as the npm.deprecated property of the version's tarball,
// which needs Annotate; versions the body leaves out, or the index lacks,
// are left alone, as is every other field (decision 0034). A message that
// is no string, or an empty one, is no deprecation.
func (h handler) update(w http.ResponseWriter, r *http.Request, name string, b publishBody) {
	sent, _, err := bodyVersions(b)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	versions, found := h.indexedOr404(w, r, name)
	if !found {
		return
	}
	var changes []annotation
	for _, v := range versions {
		raw, inBody := sent[v.number]
		if !inBody {
			continue
		}
		var doc struct {
			Deprecated any `json:"deprecated"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			h.k.Error(w, r, badRequest(fmt.Sprintf("the version %s is not an object", v.number)))
			return
		}
		message, _ := doc.Deprecated.(string)
		if message == v.deprecated {
			continue
		}
		it, err := h.tarball(r, name, v.number)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if !h.k.Require(w, r, it.FullPath(), types.ActionAnnotate) {
			return
		}
		changes = append(changes, annotation{it: it, key: npmfmt.PropDeprecated, values: []string{
			message,
		}})
	}
	if err := h.annotate(r, changes); err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jsonType, okBody)
}

// annotation sets the property key of it to values, or removes it when
// they are all empty.
type annotation struct {
	it     types.Item
	key    string
	values []string
}

// annotate makes the changes in one transaction through the kit, so each
// tarball's format hook follows its change; Annotate on each is checked as
// it is made, so a caller checks them all first to change nothing when
// one would be refused.
func (h handler) annotate(r *http.Request, changes []annotation) error {
	err := h.k.DB().Tx(r.Context(), func(ctx context.Context) error {
		r := r.WithContext(ctx)
		for _, c := range changes {
			set, remove := types.Properties{c.key: c.values}, []string(nil)
			if !slices.ContainsFunc(c.values, func(v string) bool { return v != "" }) {
				set, remove = nil, []string{c.key}
			}
			if err := h.k.Annotate(r, c.it, set, remove); err != nil {
				return err //nolint:wrapcheck // the kit's own answer
			}
		}
		return nil
	})
	return err //nolint:wrapcheck // the transaction returns the kit's answers
}

// bodyVersions is a packument body's versions by the version the index
// would clean each key to; a key that is no version is dropped. sent is
// whether the body has versions at all.
func bodyVersions(b publishBody) (versions map[string]json.RawMessage, sent bool, err error) {
	raw, sent := b.fields["versions"]
	if !sent {
		return nil, false, nil
	}
	if json.Unmarshal(raw, &versions) != nil || versions == nil {
		return nil, true, badRequest("the body's versions is not an object")
	}
	out := make(map[string]json.RawMessage, len(versions))
	for k, v := range versions {
		if clean, valid := npmfmt.CleanVersion(k); valid {
			out[clean] = v
		}
	}
	return out, true, nil
}

// unpublishVersions answers the first step of npm unpublish <name>@<v>:
// PUT /<name>/-rev/<rev> with the packument less the versions to remove
// (R25 2.2). Each version the caller can see but the body leaves out is
// removed, through the trash can, which needs Delete on its tarball; the
// rest of the body, its dist-tags included, is ignored, since the server
// settles latest itself. <rev> is compared with nothing: every packument's
// _rev is 1-0, as 7.x answers it, so it cannot show a stale view (#290).
func (h handler) unpublishVersions(w http.ResponseWriter, r *http.Request, name string) {
	if !h.k.RequireFolder(w, r, name, types.ActionDelete) {
		return
	}
	if r.ContentLength > h.maxPublish {
		h.k.Error(w, r, tooLarge(h.maxPublish))
		return
	}
	b, err := readPublish(limited{http.MaxBytesReader(w, r.Body, h.maxPublish)},
		func(io.Reader) (blob.Info, error) {
			return blob.Info{}, badRequest("an unpublish carries no tarball")
		})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	keep, sent, err := bodyVersions(b)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
		return
	case !sent:
		// Read as keeping nothing, a body without versions would remove
		// every version.
		h.k.Error(w, r, badRequest("the body names no versions to keep"))
		return
	}
	p, ok := h.load(w, r, name)
	if !ok {
		return
	}
	var gone []string
	for _, v := range p.versions {
		if _, kept := keep[v.number]; !kept {
			gone = append(gone, npmfmt.CanonicalPath(name, v.number))
		}
	}
	h.remove(w, r, gone)
}

// unpublish answers npm unpublish's DELETEs (R25 2.2):
// DELETE /<name>/-rev/<rev> removes every version, and
// DELETE /<tarball path>/-rev/<rev> the tarball at that path, which npm
// sends after the PUT that removed its version, so it finds it gone and
// takes the 404 as done. <rev> is compared with nothing (unpublishVersions).
func (h handler) unpublish(w http.ResponseWriter, r *http.Request) {
	if h.remoteReadOnly(w, r) {
		return
	}
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	segs := p.Segments
	n := len(segs)
	if n < 3 || segs[n-2] != "-rev" {
		h.k.Error(w, r, apierror.New(http.StatusMethodNotAllowed,
			"method not allowed: "+r.Method+" "+r.URL.Path))
		return
	}
	name, rest, named := splitName(segs[:n-2])
	switch {
	case named && len(rest) == 0:
		h.unpublishPackage(w, r, name)
	case named && slices.Contains(rest, "-"):
		// Segments joined with slashes, as a tarball's read joins them.
		h.remove(w, r, []string{strings.Join(segs[:n-2], "/")})
	default:
		h.k.Error(w, r, notFound(r))
	}
}

// unpublishPackage removes every version of the package called name, the
// whole package as npm unpublish --force asks: each tarball needs Delete,
// those the caller cannot read included. [I] Whether 7.x also removes the
// package's folder and files off its canonical paths is unknown (#216);
// only the versions' tarballs go.
func (h handler) unpublishPackage(w http.ResponseWriter, r *http.Request, name string) {
	if !h.k.RequireFolder(w, r, name, types.ActionDelete) {
		return
	}
	versions, found := h.indexedOr404(w, r, name)
	if !found {
		return
	}
	paths := make([]string, 0, len(versions))
	for _, v := range versions {
		paths = append(paths, npmfmt.CanonicalPath(name, v.number))
	}
	h.remove(w, r, paths)
}

// remove deletes the files at paths through the trash can, in one
// transaction, once the caller is known to hold Delete on each.
func (h handler) remove(w http.ResponseWriter, r *http.Request, paths []string) {
	for _, p := range paths {
		if !h.k.Require(w, r, p, types.ActionDelete) {
			return
		}
	}
	err := h.k.DB().Tx(r.Context(), func(ctx context.Context) error {
		r := r.WithContext(ctx)
		for _, p := range paths {
			if err := h.k.Delete(r, p); err != nil {
				return err //nolint:wrapcheck // the kit's own answer
			}
		}
		return nil
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jsonType, okBody)
}

// indexed is every version of the package called name in the request's
// repository, whatever the caller may read; ok is false when there is
// none.
func (h handler) indexed(r *http.Request, name string) ([]types.PackageVersion, bool, error) {
	ctx := r.Context()
	st := h.k.DB()
	found, err := st.Packages().Find(ctx, pkgroute.RequestFrom(r).Repo.Key, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("npm: package %s: %w", name, err)
	}
	versions, err := st.Packages().Versions(ctx, found.ID)
	if err != nil {
		return nil, false, fmt.Errorf("npm: versions of %s: %w", name, err)
	}
	return versions, len(versions) > 0, nil
}

// tarball is the file at the canonical path of name at version, the one
// file the index links the version to.
func (h handler) tarball(r *http.Request, name, version string) (types.Item, error) {
	p := npmfmt.CanonicalPath(name, version)
	it, err := h.k.DB().Items().Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, p)
	if err != nil {
		return it, fmt.Errorf("npm: tarball %s: %w", p, err)
	}
	return it, nil
}
