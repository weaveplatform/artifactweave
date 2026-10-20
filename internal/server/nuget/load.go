package nuget

import (
	"errors"
	"fmt"
	"net/http"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// version is one indexed version a caller may see: its index entry, whose
// Version is the normalised version lower-cased (nugetfmt.VersionKey), and
// the document the NuGet format keeps for it.
type version struct {
	pv   types.PackageVersion
	meta nugetfmt.Metadata
}

// pkg is a package as one caller sees it: its name in the index, the id
// lower-cased, and the versions it may read, ascending in NuGet order, as
// the index orders them.
type pkg struct {
	name     string
	versions []version
}

// only is p with the versions keep accepts.
func (p pkg) only(keep func(nugetfmt.Metadata) bool) pkg {
	out := pkg{name: p.name}
	for _, v := range p.versions {
		if keep(v.meta) {
			out.versions = append(out.versions, v)
		}
	}
	return out
}

// find is the version whose index key is key, the normalised version
// lower-cased.
func (p pkg) find(key string) (version, bool) {
	for _, v := range p.versions {
		if v.pv.Version == key {
			return v, true
		}
	}
	return version{}, false
}

// load reads the package whose id is id, in any case, from the index,
// keeping the versions with a .nupkg the caller may read: a version whose
// only file is a .snupkg pushed ahead of its package has nothing to serve
// yet (decision 0038). ok is false when no version is left, so a package
// the caller may read nothing of reads as one that does not exist.
func (h handler) load(r *http.Request, id string) (pkg, bool, error) {
	ctx := r.Context()
	st := h.k.DB()
	key := pkgroute.RequestFrom(r).Repo.Key
	name := nugetfmt.PackageName(id)
	found, err := st.Packages().Find(ctx, key, name)
	if errors.Is(err, store.ErrNotFound) {
		return pkg{}, false, nil
	}
	if err != nil {
		return pkg{}, false, fmt.Errorf("nuget: package %s: %w", name, err)
	}
	return h.versionsOf(r, found)
}

// versionsOf is load for a package the index already returned.
func (h handler) versionsOf(r *http.Request, found types.Package) (pkg, bool, error) {
	all, err := h.k.DB().Packages().Versions(r.Context(), found.ID)
	if err != nil {
		return pkg{}, false, fmt.Errorf("nuget: versions of %s: %w", found.Name, err)
	}
	p := pkg{name: found.Name}
	for _, pv := range all {
		m, err := nugetfmt.DecodeMetadata(pv.Metadata)
		if err != nil {
			return pkg{}, false, fmt.Errorf("nuget: %s %s: %w", found.Name, pv.Version, err)
		}
		if !m.HasPackage() || !h.k.Can(r, m.Path, types.ActionRead) {
			continue
		}
		p.versions = append(p.versions, version{pv: pv, meta: m})
	}
	return p, len(p.versions) > 0, nil
}

// indexed is version key of the package whose id is id, in any case, when
// a .nupkg backs it, whoever may read it. A download refuses a caller who
// may not read the .nupkg as the item routes refuse one, 401 to anonymous
// so that the client sends credentials, where a listing leaves the version
// out.
func (h handler) indexed(r *http.Request, id, key string) (version, bool, error) {
	ctx := r.Context()
	st := h.k.DB()
	repoKey := pkgroute.RequestFrom(r).Repo.Key
	name := nugetfmt.PackageName(id)
	found, err := st.Packages().Find(ctx, repoKey, name)
	if errors.Is(err, store.ErrNotFound) {
		return version{}, false, nil
	}
	if err != nil {
		return version{}, false, fmt.Errorf("nuget: package %s: %w", name, err)
	}
	pv, err := st.Packages().Version(ctx, found.ID, key)
	if errors.Is(err, store.ErrNotFound) {
		return version{}, false, nil
	}
	if err != nil {
		return version{}, false, fmt.Errorf("nuget: %s %s: %w", name, key, err)
	}
	m, err := nugetfmt.DecodeMetadata(pv.Metadata)
	if err != nil {
		return version{}, false, fmt.Errorf("nuget: %s %s: %w", name, key, err)
	}
	return version{pv: pv, meta: m}, m.HasPackage(), nil
}

// versionKey is the index's key for v, a version as a URL or a query
// writes it: normalised and lower-cased, so build metadata, missing or
// extra zeros and the case of a label all name the one version NuGet takes
// them for. ok is false for anything NuGet cannot parse.
func versionKey(v string) (string, bool) {
	parsed, ok := nugetfmt.ParseVersion(v)
	if !ok {
		return "", false
	}
	return nugetfmt.VersionKey(parsed), true
}
