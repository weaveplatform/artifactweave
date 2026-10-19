// Package npm serves the npm registry of npm repositories at
// /artifactory/api/npm/{repoKey}/ (decision 0034), mounted through the
// router kit (decision 0032). Packuments and version manifests are built on
// each request from what the npm format keeps in the package index, and
// tarballs download through the item routes' download, so redirects and
// download counts apply to them as to any file. A publish stores the
// tarball through the item service, whose npm format indexes it as it
// indexes a REST deploy.
package npm

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Options are the npm registry's settings.
type Options struct {
	// Accounts verifies the passwords npm login sends and keeps the tokens
	// it issues.
	Accounts Accounts
	// MaxPublishSize is the largest publish request accepted
	// (npm.max_publish_size); zero means DefaultMaxPublishSize.
	MaxPublishSize int64
	// Format are the npm format's settings, which a read applies when it
	// resolves latest itself, as the format would settle it.
	Format npmfmt.Settings
}

// DefaultMaxPublishSize is decision 0034's default npm.max_publish_size.
const DefaultMaxPublishSize = 1 << 30

// Protocol is npm's registry protocol.
func Protocol(o Options) pkgroute.Protocol {
	if o.MaxPublishSize <= 0 {
		o.MaxPublishSize = DefaultMaxPublishSize
	}
	if o.Accounts.TokenExpiry <= 0 {
		o.Accounts.TokenExpiry = DefaultLoginTokenExpiry
	}
	return pkgroute.Protocol{
		Name:    "npm",
		Types:   []repo.PackageType{repo.NPM},
		Surface: apierror.NPM,
		// The JFrog operations beside the registry that the generated
		// servers answer. A repository's reindex is not reserved: "reindex"
		// is a package name too, whose packument a GET asks for, while the
		// operation's own POST reaches the generated server ahead of the kit.
		Reserved: []string{"auth", "{repoKey}/auth/{scope}"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			routes(r, handler{
				accounts: o.Accounts, k: k, maxPublish: o.MaxPublishSize, format: o.Format,
			})
		},
	}
}

func routes(r chi.Router, h handler) {
	r.Get("/-/ping", h.ping)
	r.Get("/-/whoami", h.whoami)
	r.Get("/-/v1/search", h.search)
	r.Put("/-/user/"+couchUser+"{user}", h.login)
	r.Delete("/-/user/token/{token}", h.logout)
	// Every other GET names a package: its packument, one version's
	// manifest, or a tarball. npm's own operations under /-/ that no route
	// claims answer 404.
	r.Get("/*", h.read)
	r.Head("/*", h.read)
	r.Put("/*", h.write)
	// npm dist-tag (#289) and npm unpublish's DELETEs (#290).
	r.Get("/-/package/*", h.listTags)
	r.Put("/-/package/*", h.addTag)
	r.Delete("/-/package/*", h.removeTag)
	r.Delete("/*", h.unpublish)
}

type handler struct {
	accounts   Accounts
	k          *pkgroute.Kit
	maxPublish int64
	format     npmfmt.Settings
}

// ping answers every caller the server lets through: an authenticated
// one, or anonymous while anonymous access is on (decision 0034). npm
// accepts any 2xx.
func (handler) ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jsonType, json.RawMessage(`{}`))
}

// whoami names the caller. npm asks only when it holds a token; an
// anonymous caller is told to authenticate.
func (h handler) whoami(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok || p.Anonymous {
		h.k.Error(w, r, auth.ErrUnauthenticated)
		return
	}
	writeJSON(w, http.StatusOK, jsonType, map[string]string{"username": p.Name})
}

// read answers a GET or HEAD by what the path names after the package's
// name (decision 0034): nothing is the packument, one segment the manifest
// of a version or dist-tag, and a path with a "-" segment a tarball, served
// as the item at that path.
func (h handler) read(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	name, rest, ok := splitName(p.Segments)
	switch {
	case !ok:
		h.k.Error(w, r, notFound(r))
	case slices.Contains(rest, "-"):
		// Segments joined with slashes: a scoped name's encoded slash is a
		// folder in the tree, as the canonical path has it.
		h.k.ServeItem(w, r, p.String())
	case len(rest) == 0:
		h.packument(w, r, name)
	case len(rest) == 1:
		h.manifest(w, r, name, rest[0])
	default:
		h.k.Error(w, r, notFound(r))
	}
}

// splitName takes a package's name off the front of a path's segments.
// npm sends a scoped name as one segment, "@scope%2fname", whose encoded
// slash ParsePath keeps inside it; a scoped name whose slash arrived
// unencoded is two. A first segment "-" is one of npm's own operations, no
// package.
func splitName(segs []string) (name string, rest []string, ok bool) {
	switch {
	case len(segs) == 0 || segs[0] == "-":
		return "", nil, false
	case strings.HasPrefix(segs[0], "@") && !strings.Contains(segs[0], "/"):
		if len(segs) < 2 {
			return "", nil, false
		}
		return segs[0] + "/" + segs[1], segs[2:], true
	default:
		return segs[0], segs[1:], true
	}
}

func notFound(r *http.Request) error {
	return apierror.New(http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
}

const (
	jsonType = "application/json"
	// abbreviatedType is the abbreviated packument's media type, which
	// pacote lists first in Accept when it installs.
	abbreviatedType = "application/vnd.npm.install-v1+json"
)

// writeJSON answers status with v.
func writeJSON(w http.ResponseWriter, status int, contentType string, v any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write(append(encode(v), '\n'))
}

func jsonString(s string) json.RawMessage { return encode(s) }

// encode is v as JSON with HTML characters as written, so a manifest keeps
// its author's text. Every value given is built from JSON already decoded,
// strings and numbers, which always encode.
func encode(v any) json.RawMessage {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
