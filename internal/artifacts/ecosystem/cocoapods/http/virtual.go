package cocoapodshttp

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	podfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// A virtual repository's CDN is built from its members through the package
// handlers' walk (pkgroute.MergeMembers and pkgroute.Collect): nested
// virtual repositories flattened, cycles and duplicates skipped, each
// member in 7.x's effective order. A member that is blacked out or whose
// patterns reject the CDN document, that the caller may read nothing in,
// whose read misses or is refused, or whose upstream fails is passed over
// (decision 0008, R1 to R3), a fault among them with a warning in the log
// (pkgroute.Kit.WarnPassedOver) [I]. Each member decides Read on the pods and
// versions it serves: a virtual repository has no permission target of its
// own (R10). A caller who may read no member gets the CDN's not-found for a
// pod's document or archive, and is refused a document of the whole
// repository (virtualRootGate).

func (h handler) virtualRead(w http.ResponseWriter, r *http.Request, path string) {
	if !validCDNPath(path) {
		if _, _, _, ok := parseSourcePath(path); !ok {
			if _, _, ok := podfmt.ParsePath(strings.TrimPrefix(path, "packages/")); !ok {
				h.k.Error(w, r, notFound())
				return
			}
		}
	}
	switch {
	case path == "CocoaPods-version.yml" || path == "deprecated_podspecs.txt":
		if !h.virtualRootGate(w, r, path) {
			return
		}
		if path == "deprecated_podspecs.txt" {
			serveBytes(w, r, "text/plain; charset=utf-8", nil)
			return
		}
		serveBytes(
			w,
			r,
			"application/x-yaml",
			[]byte("---\nmin: 1.0.0\nlast: 1.17.0\nprefix_lengths:\n- 1\n- 1\n- 1\n"),
		)
	case path == "all_pods.txt" || strings.HasPrefix(path, "all_pods_versions_"):
		h.virtualIndex(w, r, path)
	case strings.HasPrefix(path, "Specs/"):
		name, version, ok := parseSpecPath(path)
		if !ok {
			h.k.Error(w, r, notFound())
			return
		}
		h.virtualSpec(w, r, path, name, version)
	case strings.HasPrefix(path, "packages/"):
		h.virtualLocalArchive(w, r, strings.TrimPrefix(path, "packages/"))
	default:
		name, version, ext, ok := parseSourcePath(path)
		if !ok {
			h.k.Error(w, r, notFound())
			return
		}
		h.virtualSourceArchive(w, r, name, version, ext)
	}
}

// virtualRootGate asks the gates of a CDN document of the whole
// repository, all_pods.txt, a shard of it, CocoaPods-version.yml or
// deprecated_podspecs.txt, and reports whether the caller passed. The
// virtual repository's own patterns refuse the document first, at its 7.x
// path ([I], R5), for every caller. Then its root gate
// (pkgroute.Kit.RequireRoot) refuses a caller who may read the root of no
// member, as gems' indexes and the LuaRocks manifest are (decision 0008,
// R3).
func (h handler) virtualRootGate(w http.ResponseWriter, r *http.Request, path string) bool {
	if err := h.k.CheckDocument(r, cdnDocument(path)); err != nil {
		h.k.Error(w, r, err)
		return false
	}
	return h.k.RequireRoot(w, r)
}

// virtualIndex merges the members' CDN index at path, all_pods.txt or one
// shard, with podfmt.Index. A remote member contributes the rows of its
// upstream's index, a hosted member those of its package index; each keeps
// only the pods and versions the caller may read in that member.
func (h handler) virtualIndex(w http.ResponseWriter, r *http.Request, path string) {
	if !h.virtualRootGate(w, r, path) {
		return
	}
	parts, err := pkgroute.Collect(h.k, r, cdnDocument(path), func(mr *http.Request) (podfmt.Pods, error) {
		pods, err := h.memberIndex(mr, path)
		h.k.WarnPassedOver(mr, err)
		return pods, err
	})
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	merged, _ := podfmt.Index{}.Merge(r.Context(), parts)
	shard := path != "all_pods.txt"
	rows := merged.Lines(shard)
	if shard && len(rows) == 0 {
		h.k.Error(w, r, notFound())
		return
	}
	serveBytes(w, r, "text/plain; charset=utf-8", lines(rows))
}

// memberIndex is the part of one member of a virtual repository in the
// CDN index at path.
func (h handler) memberIndex(mr *http.Request, path string) (podfmt.Pods, error) {
	out := podfmt.Pods{}
	if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
		result, err := h.remoteResult(mr, path, false)
		if err != nil {
			return nil, err
		}
		if result.Stream == nil {
			return nil, source.ErrInvalidResult
		}
		rows, err := h.remoteIndexRows(mr, result, path)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			parts := strings.Split(row, "/")
			out[parts[0]] = append(out[parts[0]], parts[1:]...)
		}
		return out, nil
	}
	packages, err := h.localPackages(mr)
	if err != nil {
		return nil, err
	}
	for name, entries := range packages {
		switch {
		case path == "all_pods.txt":
			if h.k.CanFolder(mr, name, api.ActionRead) {
				out[name] = nil
			}
		case podfmt.ShardFile(name) == path:
			for v := range entries {
				out[name] = append(out[name], v)
			}
		}
	}
	return out, nil
}

// virtualSpec serves the podspec at path from the first member in the walk
// that has it, rewritten to fetch its archive through the virtual
// repository.
func (h handler) virtualSpec(w http.ResponseWriter, r *http.Request, path, name, version string) {
	members, err := h.k.MergeMembers(r, cdnDocument(path))
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	key := pkgroute.RequestFrom(r).Repo.Key
	for _, mr := range members {
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			result, err := h.remoteResult(mr, path, false)
			h.k.WarnPassedOver(mr, err)
			if pkgroute.PassedOver(err) {
				continue
			}
			if err != nil {
				h.k.Error(w, r, err)
				return
			}
			if result.Stream == nil {
				h.k.Error(w, r, pkgroute.SourceError(source.ErrInvalidResult, false))
				return
			}
			h.remoteSpec(w, mr, result, name, version, key)
			return
		}
		packages, err := h.localPackages(mr)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		v, ok := packages[name][version]
		if !ok {
			continue
		}
		data, err := specFor(baseurl.From(r.Context()), key, v.archive)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		serveBytes(w, r, "application/json", data)
		return
	}
	h.k.Error(w, r, notFound())
}

// virtualLocalArchive serves a hosted pod's archive from the first hosted
// member that holds it and lets the caller read it. The virtual
// repository's own patterns refusing the archive leave it no member to
// search (6.20 VirtualRepoDownloadStrategy), and a member that refuses it
// is passed over for the next one that lists it (R5).
func (h handler) virtualLocalArchive(w http.ResponseWriter, r *http.Request, path string) {
	name, version, _ := podfmt.ParsePath(path)
	members, err := h.k.MergeMembers(r, path)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			continue
		}
		packages, err := h.localPackages(mr)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if _, found := packages[name][version]; found {
			h.k.ServeItem(w, mr, path)
			return
		}
	}
	h.k.Error(w, r, notFound())
}

// virtualSourceArchive serves a remote pod's source archive from the first
// remote member whose spec names it with this extension and that lets the
// caller read it. A remote member whose own policy refuses the archive is
// passed over, as a hosted member is (R5).
func (h handler) virtualSourceArchive(
	w http.ResponseWriter,
	r *http.Request,
	name, version, ext string,
) {
	p := sourcePath(name, version, ext)
	members, err := h.k.MergeMembers(r, p)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if pkgroute.RequestFrom(mr).Repo.Class != repo.Remote || !h.k.Can(mr, p, api.ActionRead) {
			continue
		}
		result, err := h.remoteResult(mr, podfmt.SpecPath(name, version), true)
		h.k.WarnPassedOver(mr, err)
		if pkgroute.PassedOver(err) {
			continue
		}
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if result.Stream == nil {
			h.k.Error(w, r, pkgroute.SourceError(source.ErrInvalidResult, false))
			return
		}
		data, err := pkgroute.ReadServed(
			r.Context(),
			result.Stream.Body,
			maxCDNMetadata,
			source.ErrInvalidResult,
		)
		_ = result.Stream.Body.Close()
		if err == nil && len(data) > maxCDNMetadata {
			err = source.ErrInvalidResult
		}
		if err != nil {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		}
		fields, target, _, err := sourceDetails(data, name, version)
		if err != nil {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		}
		if target != "" && archiveExtension(fields, target) == ext {
			h.remoteArchive(w, mr, name, version, ext)
			return
		}
	}
	h.k.Error(w, r, notFound())
}
