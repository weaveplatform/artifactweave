package swifthttp

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	swiftfmt "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/swift"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// Options configures the Swift protocol. RemoteSources serves remote
// repositories and fetches through them; nil leaves the remote class
// without a source, so a remote repository answers 501.
type Options struct{ RemoteSources *source.Service }

// Protocol returns the Swift Package Registry API for the router kit, under
// /artifactory/api/swift/{repoKey}: GET and HEAD of the identifier lookup
// (identifiers?url=), a package's release list ({scope}/{name}), a
// release's metadata ({scope}/{name}/{version}, with or without .json),
// its manifest ({scope}/{name}/{version}/Package.swift, optionally with
// swift-version) and its source archive ({scope}/{name}/{version}.zip),
// and PUT of a release ({scope}/{name}/{version}). Local and federated
// repositories are served from their items and package index, virtual ones
// from their members in order, and remote ones through o.RemoteSources
// when it is set; a class without a source answers 501. The virtual class's
// source supports no single-item lookup, because the protocol reads a
// virtual repository's members itself. {repoKey}/reindex is reserved for
// JFrog's reindex operation, which the server serves beside the protocol.
func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
		repo.Virtual:   virtualSource{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Name: "swift", Types: []repo.PackageType{repo.Swift},
		ErrorProfile: apierror.ProfileSwiftRegistry, Sources: sources,
		Reserved: []string{"{repoKey}/reindex"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
			r.Put("/*", h.publish)
		},
	}
}

type virtualSource struct{}

func (virtualSource) Item(
	context.Context,
	pkgroute.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

type handler struct{ k *pkgroute.Kit }

type request struct {
	packageID swiftfmt.Package
	kind      string
	manifest  string
}

func parse(raw string) (request, error) {
	parts := strings.Split(raw, "/")
	for _, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded != part || strings.ContainsAny(part, ";\\\x00") {
			return request{}, bad(http.StatusBadRequest, "invalid Swift registry path")
		}
	}
	if len(parts) == 1 && parts[0] == "identifiers" {
		return request{kind: "identifiers"}, nil
	}
	if len(parts) < 2 || !swiftfmt.ValidScope(parts[0]) || !swiftfmt.ValidName(parts[1]) {
		return request{}, bad(http.StatusNotFound, "Swift package not found")
	}
	p := swiftfmt.Package{Scope: parts[0], Name: parts[1]}
	switch len(parts) {
	case 2:
		return request{packageID: p, kind: "releases"}, nil
	case 3:
		if version, ok := strings.CutSuffix(parts[2], ".zip"); ok &&
			swiftfmt.ValidVersion(version) {
			p.Version = version
			return request{packageID: p, kind: "archive"}, nil
		}
		if version, ok := strings.CutSuffix(parts[2], ".json"); ok {
			p.Version = version
		} else {
			p.Version = parts[2]
		}
		if swiftfmt.ValidVersion(p.Version) {
			return request{packageID: p, kind: "release"}, nil
		}
	case 4:
		p.Version = parts[2]
		if swiftfmt.ValidVersion(p.Version) && parts[3] == "Package.swift" {
			return request{packageID: p, kind: "manifest", manifest: parts[3]}, nil
		}
	}
	return request{}, bad(http.StatusNotFound, "Swift package resource not found")
}

func bad(status int, detail string) error { return apierror.New(status, detail) }

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	q, err := parse(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if q.kind != "manifest" && r.URL.RawQuery != "" && q.kind != "identifiers" {
		h.k.Error(w, r, bad(http.StatusBadRequest, "unsupported Swift registry query"))
		return
	}
	if q.kind == "manifest" && r.URL.RawQuery != "" {
		values := r.URL.Query()
		versions := values["swift-version"]
		if len(values) != 1 || len(versions) != 1 || !validToolsVersion(versions[0]) {
			h.k.Error(w, r, bad(http.StatusBadRequest, "invalid Swift manifest query"))
			return
		}
	}
	if q.kind == "identifiers" {
		values, err := url.ParseQuery(r.URL.RawQuery)
		urls := values["url"]
		if err != nil || len(values) != 1 || len(urls) != 1 || strings.TrimSpace(urls[0]) == "" {
			h.k.Error(w, r, bad(http.StatusBadRequest, "Swift identifier lookup requires one URL"))
			return
		}
		if !h.k.RequireFolder(w, r, "", api.ActionRead) {
			return
		}
		h.k.Error(w, r, bad(http.StatusNotFound, "Swift package identifier mapping not found"))
		return
	}
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Local, repo.Federated:
		h.localRead(w, r, q)
	case repo.Remote:
		h.remoteRead(w, r, q)
	case repo.Virtual:
		h.virtualRead(w, r, q)
	default:
		h.k.Error(w, r, source.ErrUnsupported)
	}
}

func (h handler) publish(w http.ResponseWriter, r *http.Request) {
	q, err := parse(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if q.kind != "release" || r.URL.RawQuery != "" {
		h.k.Error(
			w,
			r,
			bad(
				http.StatusMethodNotAllowed,
				"Swift release publication uses PUT /{scope}/{name}/{version}",
			),
		)
		return
	}
	if class := pkgroute.RequestFrom(r).Repo.Class; class != repo.Local && class != repo.Federated {
		h.k.Error(
			w,
			r,
			bad(
				http.StatusMethodNotAllowed,
				"Swift releases can be published only to local or federated repositories",
			),
		)
		return
	}
	h.localPublish(w, r, q.packageID)
}
