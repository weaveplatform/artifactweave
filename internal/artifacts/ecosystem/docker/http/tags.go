package dockerhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
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
// specification's order, a page of them with n and last. A blacked-out
// repository refuses it before the image is looked for (imageBlackedOut).
func (g *Registry) listTags(w http.ResponseWriter, r *http.Request, q Request) {
	if err := q.Require(r, q.Name+"/", api.ActionRead); err != nil {
		Write(w, err)
		return
	}
	if imageBlackedOut(w, q) {
		return
	}
	pages := g.store.Items().DockerPages()
	var first *layout.TagPages
	open := func(last string, batch int) names {
		src := layout.TagsAfter(pages, q.Repo.Key, q.Name, last, batch)
		if first == nil {
			first = src
		}
		return src
	}
	page, err := readPage(r, open)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	if first == nil {
		// n=0 reads no page.
		first = layout.TagsAfter(pages, q.Repo.Key, q.Name, "", 1)
	}
	// Whether the image exists does not depend on the page asked for.
	found, err := first.Found(r.Context())
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	if !found {
		Write(w, nameUnknown(q))
		return
	}
	name, _ := json.Marshal(q.addressed())
	g.writePage(w, r, `{"name":`+string(name)+`,"tags":`, page)
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
	pages := g.store.Items().DockerPages()
	var readable []repo.Config
	for _, c := range repos {
		if g.readsAnything(r, c) {
			readable = append(readable, c)
		}
	}
	open := func(last string, batch int) names {
		sources := make([]catalogSource, 0, len(readable))
		for _, c := range readable {
			// An image whose folder its repository's include and exclude
			// patterns reject is left out, before the page is cut, as 7.x
			// filters its catalog (RTFACT-26524, fixed). An image the
			// patterns reject only some tags of stays, as its tags list
			// keeps those tags (RTFACT-27234).
			patterns := repo.PatternsOf(c)
			accept := func(name string) bool { return ValidName(name) && patterns.Accepts(name, true) }
			prefix := ""
			if q.RepoKey == "" {
				prefix = c.Key + "/"
			}
			sources = append(sources, catalogSource{
				prefix: prefix,
				images: layout.ImagesAfter(pages, c.Key, prefix, last, accept, batch),
			})
		}
		return &catalogMerge{sources: sources}
	}
	page, err := readPage(r, open)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	g.writePage(w, r, `{"repositories":`, page)
}

// catalogSource is one repository's images, named as the catalog names
// them, with the next one read ahead.
type catalogSource struct {
	prefix string
	images *layout.ImagePages
	head   string
	ready  bool
	done   bool
}

// catalogMerge merges the repositories' images, each source in Compare's
// order, into one listing in that order: a repository's images are its
// sorted page, and the merge reads one more from a source only when it has
// listed the one before.
type catalogMerge struct{ sources []catalogSource }

func (m *catalogMerge) Next(ctx context.Context) (string, bool, error) {
	best := -1
	for n := range m.sources {
		s := &m.sources[n]
		if !s.ready && !s.done {
			image, ok, err := s.images.Next(ctx)
			if err != nil {
				return "", false, err //nolint:wrapcheck // the layout names itself
			}
			s.head, s.ready, s.done = s.prefix+image, ok, !ok
		}
		if s.ready && (best < 0 || layout.Compare(s.head, m.sources[best].head) < 0) {
			best = n
		}
	}
	if best < 0 {
		return "", false, nil
	}
	m.sources[best].ready = false
	return m.sources[best].head, true, nil
}

// served lists the repositories the registry-wide catalog covers: the
// Docker, OCI and Helm OCI repositories that hold their own images. Remote
// and virtual repositories are left out until the registry serves them.
func (g *Registry) served(r *http.Request) ([]repo.Config, error) {
	all, err := g.store.Repos().List(r.Context(), port.RepoFilter{})
	if err != nil {
		return nil, err //nolint:wrapcheck // store errors name themselves
	}
	return slices.DeleteFunc(all, func(c repo.Config) bool {
		return !Serves(c.PackageType) || c.Class == repo.Remote || c.Class == repo.Virtual
	}), nil
}

// nextLink is the Link to the page of n names after last.
func nextLink(r *http.Request, n int, last string) string {
	next := url.Values{"n": {strconv.Itoa(n)}, "last": {last}}
	return "<" + r.URL.Path + "?" + next.Encode() + `>; rel="next"`
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
