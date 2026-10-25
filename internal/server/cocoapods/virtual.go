package cocoapods

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The source adapter renders confirmed upstream absence as a protocol 404.
// Virtual lookup still needs to try the next member after that mapping.
func missingMember(err error) bool {
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	var apiErr *apierror.Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

func (h handler) virtualMembers(r *http.Request) ([]*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	seen := map[string]bool{c.Key: true}
	members := make([]*http.Request, 0, len(c.Repositories))
	for _, key := range c.Repositories {
		if seen[key] {
			return nil, apierror.New(http.StatusBadRequest, "invalid CocoaPods virtual member")
		}
		seen[key] = true
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if missingMember(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cocoapods: virtual member: %w", err)
		}
		if member.PackageType != repo.CocoaPods ||
			(member.Class != repo.Local && member.Class != repo.Federated && member.Class != repo.Remote) {
			return nil, apierror.New(http.StatusBadRequest, "unsupported CocoaPods virtual member")
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("cocoapods: virtual member request: %w", err)
		}
		members = append(members, mr)
	}
	return members, nil
}

func (h handler) virtualRead(w http.ResponseWriter, r *http.Request, path string) {
	if !validCDNPath(path) {
		if _, _, _, ok := parseSourcePath(path); !ok {
			if _, _, ok := podfmt.ParsePath(strings.TrimPrefix(path, "packages/")); !ok {
				h.k.Error(w, r, notFound())
				return
			}
		}
	}
	members, err := h.virtualMembers(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if path == "CocoaPods-version.yml" {
		serveBytes(
			w,
			r,
			"application/x-yaml",
			[]byte("---\nmin: 1.0.0\nlast: 1.17.0\nprefix_lengths:\n- 1\n- 1\n- 1\n"),
		)
		return
	}
	if path == "deprecated_podspecs.txt" {
		serveBytes(w, r, "text/plain; charset=utf-8", nil)
		return
	}
	if path == "all_pods.txt" || strings.HasPrefix(path, "all_pods_versions_") {
		h.virtualIndex(w, r, members, path)
		return
	}
	if name, version, ok := parseSpecPath(path); ok {
		h.virtualSpec(w, r, members, path, name, version)
		return
	}
	if name, version, ext, ok := parseSourcePath(path); ok {
		h.virtualSourceArchive(w, r, members, name, version, ext)
		return
	}
	if strings.HasPrefix(path, "packages/") {
		h.virtualLocalArchive(w, r, members, strings.TrimPrefix(path, "packages/"))
		return
	}
	h.k.Error(w, r, notFound())
}

func (h handler) virtualIndex(
	w http.ResponseWriter,
	r *http.Request,
	members []*http.Request,
	path string,
) {
	names := map[string]bool{}
	versions := map[string]map[string]bool{}
	for _, mr := range members {
		c := pkgroute.RequestFrom(mr).Repo
		if c.Class == repo.Remote {
			result, err := h.remoteResult(mr, path)
			if missingMember(err) {
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
			rows, err := h.remoteIndexRows(mr, result, path)
			if err != nil {
				h.k.Error(w, r, pkgroute.SourceError(err, false))
				return
			}
			for _, row := range rows {
				if path == "all_pods.txt" {
					if h.k.CanFolder(r, row, types.ActionRead) {
						names[row] = true
					}
					continue
				}
				parts := strings.Split(row, "/")
				for _, v := range parts[1:] {
					if h.k.CanFolder(r, parts[0]+"/"+v, types.ActionRead) {
						if versions[parts[0]] == nil {
							versions[parts[0]] = map[string]bool{}
						}
						versions[parts[0]][v] = true
					}
				}
			}
			continue
		}
		packages, err := h.localPackages(mr)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		for name, entries := range packages {
			if path == "all_pods.txt" {
				if h.k.CanFolder(r, name, types.ActionRead) {
					names[name] = true
				}
				continue
			}
			if podfmt.ShardFile(name) != path {
				continue
			}
			for v := range entries {
				if h.k.Can(r, podfmt.Path(name, v), types.ActionRead) {
					if versions[name] == nil {
						versions[name] = map[string]bool{}
					}
					versions[name][v] = true
				}
			}
		}
	}
	rows := make([]string, 0, len(names)+len(versions))
	if path == "all_pods.txt" {
		for name := range names {
			rows = append(rows, name)
		}
	} else {
		for name, entries := range versions {
			v := make([]string, 0, len(entries))
			for one := range entries {
				v = append(v, one)
			}
			slices.Sort(v)
			rows = append(rows, name+"/"+strings.Join(v, "/"))
		}
		if len(rows) == 0 {
			h.k.Error(w, r, notFound())
			return
		}
	}
	slices.Sort(rows)
	serveBytes(w, r, "text/plain; charset=utf-8", lines(rows))
}

func (h handler) virtualSpec(
	w http.ResponseWriter,
	r *http.Request,
	members []*http.Request,
	path, name, version string,
) {
	key := pkgroute.RequestFrom(r).Repo.Key
	for _, mr := range members {
		c := pkgroute.RequestFrom(mr).Repo
		if c.Class == repo.Remote {
			result, err := h.remoteResult(mr, path)
			if missingMember(err) {
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
			h.remoteSpec(w, mr, r, result, name, version, key)
			return
		}
		packages, err := h.localPackages(mr)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		v, ok := packages[name][version]
		if !ok || !h.k.Can(r, podfmt.Path(name, version), types.ActionRead) {
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

func (h handler) virtualLocalArchive(
	w http.ResponseWriter,
	r *http.Request,
	members []*http.Request,
	path string,
) {
	name, version, ok := podfmt.ParsePath(path)
	if !ok || !h.k.Can(r, path, types.ActionRead) {
		h.k.Error(w, r, notFound())
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
		if v, found := packages[name][version]; found {
			h.k.ServeItem(w, v.request, path)
			return
		}
	}
	h.k.Error(w, r, notFound())
}

func (h handler) virtualSourceArchive(
	w http.ResponseWriter,
	r *http.Request,
	members []*http.Request,
	name, version, ext string,
) {
	if !h.k.Can(r, sourcePath(name, version, ext), types.ActionRead) {
		h.k.Error(w, r, notFound())
		return
	}
	for _, mr := range members {
		if pkgroute.RequestFrom(mr).Repo.Class != repo.Remote {
			continue
		}
		result, err := h.remoteResult(mr, podfmt.SpecPath(name, version))
		if missingMember(err) {
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
		data, err := io.ReadAll(io.LimitReader(result.Stream.Body, maxCDNMetadata+1))
		_ = result.Stream.Body.Close()
		if err != nil || len(data) > maxCDNMetadata {
			h.k.Error(w, r, pkgroute.SourceError(source.ErrInvalidResult, false))
			return
		}
		_, target, _, err := sourceDetails(data, name, version)
		if err != nil {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		}
		if target != "" && sourceExtension(target) == ext {
			h.remoteArchive(w, mr, name, version, ext)
			return
		}
	}
	h.k.Error(w, r, notFound())
}
