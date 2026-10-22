package docker

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// registerListings registers the tags list and the catalogs (decision
// 0031). They read the layout alone, so they need no blob store.
func (g *Registry) registerListings() {
	if g.store == nil {
		return
	}
	g.handle(KindTags, http.MethodGet, g.listTags)
	g.handle(KindCatalog, http.MethodGet, g.catalog)
}

// listTags serves GET .../<name>/tags/list: the image's tags in the
// specification's order, a page of them with n and last.
func (g *Registry) listTags(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", types.ActionRead); err != nil {
		Write(w, err)
		return
	}
	tags, found, err := layout.Tags(r.Context(), g.store.Items(), q.Repo.Key, q.Name)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	if !found {
		Write(w, nameUnknown(q))
		return
	}
	tags = paginate(w, r, tags)
	writeJSON(w, map[string]any{"name": q.addressed(), "tags": tags})
}

// catalog serves _catalog: the images of one repository by name, or with
// no repository those of every repository the registry serves, each as
// <repo>/<image> (decision 0003). A repository the caller can read nothing
// in is left out rather than refused, and so are folders whose names no
// client could pull. One the caller can read anything in lists every image,
// those outside the caller's paths included: 7.x treats the names as the
// repository's metadata, which "users whose access is scoped to a specific
// path" may view (docker-repositories, "Repository Metadata and
// Permissions"). The images themselves still need Read to pull.
func (g *Registry) catalog(w http.ResponseWriter, r *http.Request, q Request) {
	if !GrantsCatalog(r) {
		// A registry token lists only with registry:catalog:*, which the
		// challenge asks for; other credentials are filtered per image below.
		Write(w, Errorf(http.StatusUnauthorized, CodeUnauthorized,
			"the token does not grant registry:catalog:*"))
		return
	}
	repos := []repo.Config{q.Repo}
	if q.RepoKey == "" {
		var err error
		if repos, err = g.served(r); err != nil {
			g.writeErr(w, r, err)
			return
		}
	}
	keys := make([]string, 0, len(repos))
	for _, c := range repos {
		if readsAnything(r, c) {
			keys = append(keys, c.Key)
		}
	}
	images, err := layout.Images(r.Context(), g.store.Items(), keys)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	names := []string{}
	for _, im := range images {
		if !ValidName(im.Name) {
			continue
		}
		if q.RepoKey == "" {
			names = append(names, im.Repo+"/"+im.Name)
		} else {
			names = append(names, im.Name)
		}
	}
	slices.SortFunc(names, layout.Compare)
	writeJSON(w, map[string]any{"repositories": paginate(w, r, names)})
}

// served lists the repositories the registry-wide catalog covers: the
// Docker, OCI and Helm OCI repositories that hold their own images. Remote
// and virtual repositories are left out until the registry serves them.
func (g *Registry) served(r *http.Request) ([]repo.Config, error) {
	all, err := g.store.Repos().List(r.Context(), store.RepoFilter{})
	if err != nil {
		return nil, err //nolint:wrapcheck // store errors name themselves
	}
	return slices.DeleteFunc(all, func(c repo.Config) bool {
		return !Serves(c.PackageType) || c.Class == repo.Remote || c.Class == repo.Virtual
	}), nil
}

// paginate returns the page of names, ordered as layout.Compare orders
// them, that the request's n and last select: the names after last, at
// most n of them. When more follow it sets the Link header to the next
// page, in the form the client addressed. n of zero gives an empty page
// without a Link (end-8b). An n that is no count is ignored, as if absent:
// the specification's error codes have none for it.
func paginate(w http.ResponseWriter, r *http.Request, names []string) []string {
	return paginateHeaders(w.Header(), r, names)
}

func paginateHeaders(header http.Header, r *http.Request, names []string) []string {
	if names == nil {
		// An empty list is [] in JSON, never null.
		names = []string{}
	}
	values := r.URL.Query()
	if last := values.Get("last"); last != "" {
		i, _ := slices.BinarySearchFunc(names, last, layout.Compare)
		if i < len(names) && names[i] == last {
			i++
		}
		names = names[i:]
	}
	n, err := strconv.Atoi(values.Get("n"))
	if err != nil || n < 0 || n >= len(names) {
		return names
	}
	page := names[:n]
	if n > 0 {
		next := url.Values{"n": {strconv.Itoa(n)}, "last": {page[n-1]}}
		header.Set("Link", "<"+r.URL.Path+"?"+next.Encode()+`>; rel="next"`)
	}
	return page
}

// writeJSON writes body with 200.
func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// addressed is the image's name as the client addressed it: on the
// repository path the repository key comes first, as in every reference
// the client pulls; under the prefix and a subdomain the image alone.
func (q Request) addressed() string {
	if key, ok := strings.CutPrefix(q.base, "/v2/"); ok {
		return key + "/" + q.Name
	}
	return q.Name
}
