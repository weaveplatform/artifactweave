package pypi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// project is a project as one caller sees it: its versions with a file the
// caller may read, oldest first by PEP 440, as the index orders them.
type project struct {
	// name is the project's normalised name, the index's key for it.
	name     string
	releases []release
}

// release is one version of a project with the files the caller may
// read, ordered by file name, then path, as the format keeps them.
type release struct {
	version string
	files   []pypifmt.File
	// meta is the version's document, whose Texts hold the files' core
	// metadata.
	meta pypifmt.Metadata
}

// Reads tells whether the caller may read the file at a path of the
// repository a request names.
type Reads func(path string) bool

// load reads the project called name, normalised, in the repository key
// from the package index, keeping the files reads allows; ok is false when
// the index has no such project or the caller may read none of its files.
func load(
	ctx context.Context,
	st store.Store,
	key, name string,
	reads Reads,
) (project, bool, error) {
	found, err := st.Packages().Find(ctx, key, name)
	if errors.Is(err, store.ErrNotFound) {
		return project{}, false, nil
	}
	if err != nil {
		return project{}, false, fmt.Errorf("pypi: project %s: %w", name, err)
	}
	p, err := readable(ctx, st, found, reads, false)
	return p, err == nil && len(p.releases) > 0, err
}

// readable reads the versions of pkg with the files reads allows, or, when
// probe is set, only enough of them to tell whether there is one.
func readable(
	ctx context.Context,
	st store.Store,
	pkg types.Package,
	reads Reads,
	probe bool,
) (project, error) {
	versions, err := st.Packages().Versions(ctx, pkg.ID)
	if err != nil {
		return project{}, fmt.Errorf("pypi: versions of %s: %w", pkg.Name, err)
	}
	p := project{name: pkg.Name}
	for _, v := range versions {
		m, err := pypifmt.DecodeMetadata(v.Metadata)
		if err != nil {
			return project{}, fmt.Errorf("pypi: %s %s: %w", pkg.Name, v.Version, err)
		}
		rel := release{version: v.Version, meta: m}
		for _, f := range m.Files {
			if reads(f.Path) {
				rel.files = append(rel.files, f)
			}
		}
		if len(rel.files) == 0 {
			continue
		}
		p.releases = append(p.releases, rel)
		if probe {
			break
		}
	}
	return p, nil
}

// projects are the normalised names of the projects in the repository key
// with a file reads allows, in the order the index lists them, by name.
func projects(ctx context.Context, st store.Store, key string, reads Reads) ([]string, error) {
	var names []string
	page := store.Page{Limit: store.MaxLimit}
	for {
		pkgs, next, err := st.Packages().List(ctx, key, page)
		if err != nil {
			return nil, fmt.Errorf("pypi: projects of %s: %w", key, err)
		}
		for _, pkg := range pkgs {
			p, err := readable(ctx, st, pkg, reads, true)
			if err != nil {
				return nil, err
			}
			if len(p.releases) > 0 {
				names = append(names, pkg.Name)
			}
		}
		if next == "" {
			return names, nil
		}
		page.After = next
	}
}

// reads is what the request's caller may read in its repository.
func (h handler) reads(r *http.Request) Reads {
	return func(path string) bool { return h.k.Can(r, path, types.ActionRead) }
}

// repoBase is the request's repository's URL under /api/pypi/, from the
// base URL resolver.
func repoBase(r *http.Request) string {
	return RepoURL(baseurl.From(r.Context()), pkgroute.RequestFrom(r).Repo.Key)
}

// RepoURL is the URL of the repository key under /api/pypi/ at base, the
// server's base URL.
func RepoURL(base, key string) string {
	return base + "/artifactory/api/pypi/" + url.PathEscape(key)
}

// escapePath escapes each segment of a repository path for a URL.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
