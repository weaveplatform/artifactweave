// Package nuget serves NuGet's protocols on NuGet repositories (decision
// 0038), mounted through the router kit (decision 0032): the v3 API under
// /artifactory/api/nuget/v3/{repoKey}/ (V3) and, under
// /artifactory/api/nuget/{repoKey}/, the push, delete and relist the v3
// index's PackagePublish names (V2). Every feed is built on each request
// from what the NuGet format keeps in the package index, for the caller
// and the request's base URL, so nothing derived is stored and a version is
// served once its deploy commits. Downloads go through the item routes'
// download, so the download policy and counts apply to them as to any
// file, and a push stores through the item service, whose NuGet format
// checks and indexes it as it does a REST deploy.
package nuget

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// V3 is NuGet's v3 protocol: the service index, the flat container, the
// registration hives and the symbols push. Its
// name has two segments, so chi routes /api/nuget/v3/... here ahead of
// V2's {repoKey}: a repository keyed v3 is reached through this tree only
// (decision 0038). What no route serves, the symbol-server reads and the v2
// queries JFrog's support once suggested under this prefix among them,
// answers 404.
func V3() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:      "nuget/v3",
		Types:     []repo.PackageType{repo.NuGet},
		Surface:   apierror.Artifactory,
		Challenge: forced,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k}
			r.Get("/", h.serviceIndex)
			r.Head("/", h.serviceIndex)
			r.Get("/index.json", h.serviceIndex)
			r.Head("/index.json", h.serviceIndex)
			r.Get("/flatcontainer/*", h.flat)
			r.Head("/flatcontainer/*", h.flat)
			for _, hive := range hives {
				r.Get("/"+hive.path+"/*", h.registration(hive))
				r.Head("/"+hive.path+"/*", h.registration(hive))
			}
			r.Put("/symbols", h.symbols)
			r.Put("/symbols/", h.symbols)
		},
	}
}

// V2 is the repository's v2 root, /api/nuget/{repoKey}/, which the v3
// index names as PackagePublish and LegacyGallery and where JFrog
// documents pushes and deletes: a PUT is a push into the folder its path
// names, a DELETE of {id}/{version} a delete, and a POST of it a relist.
func V2() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:      "nuget",
		Types:     []repo.PackageType{repo.NuGet},
		Surface:   apierror.Artifactory,
		Challenge: forced,
		// The JFrog operation beside the feed, whose POST reaches its own
		// handler ahead of the kit; its other methods answer 405.
		Reserved: []string{"{repoKey}/reindex"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k}
			r.Put("/", h.push)
			r.Put("/*", h.push)
			r.Delete("/*", h.remove)
			r.Post("/*", h.relist)
		},
	}
}

// forced reports a repository with forceNugetAuthentication on, whose
// anonymous requests the kit answers 401 with the Basic challenge before
// they are routed, whatever the anonymous user may read (decision 0038,
// "Authentication"). The NuGet client sends credentials only after a 401,
// so a feed that answered an anonymous caller what it may read would never
// receive them. [I] The item routes are not affected.
func forced(c repo.Config) bool {
	return c.ForceNugetAuthentication != nil && *c.ForceNugetAuthentication
}

type handler struct {
	k *pkgroute.Kit
}

// segments are the decoded segments of the request's path after prefix,
// the route's own part of it, such as "flatcontainer/". A path that does
// not parse answers 400 through the caller.
func segments(r *http.Request, prefix string) ([]string, error) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit's *apierror.Error, answered as it is
	}
	segs := p.Segments
	if len(segs) > 0 && segs[0] == prefix {
		segs = segs[1:]
	}
	return segs, nil
}

// v3Base is the repository's v3 root under the request's base URL, and
// v2Base its v2 root, from which every URL a response carries is built
// for the request, never stored, so a client behind any proxy can follow
// them.
func v3Base(r *http.Request) string {
	return baseurl.From(r.Context()) + "/artifactory/api/nuget/v3/" +
		url.PathEscape(pkgroute.RequestFrom(r).Repo.Key)
}

func v2Base(r *http.Request) string {
	return baseurl.From(r.Context()) + "/artifactory/api/nuget/" +
		url.PathEscape(pkgroute.RequestFrom(r).Repo.Key)
}

func notFound(r *http.Request) error {
	return apierror.New(http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
}

const jsonType = "application/json"

// writeJSON answers 200 with v; net/http drops the body of a HEAD.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", jsonType)
	_, _ = w.Write(append(encode(v), '\n'))
}

// encode is v as JSON with HTML characters as written, so a description
// keeps its author's text. Every value given is built from strings,
// numbers and booleans, which always encode.
func encode(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
