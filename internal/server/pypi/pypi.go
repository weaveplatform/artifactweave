// Package pypi serves PyPI repositories at /artifactory/api/pypi/{repoKey}/
// through the router kit (decisions 0032 and 0037): the simple repository
// API that pip, uv and Poetry install from, in HTML (PEP 503) and, when
// pypi.simple_json is on, JSON (PEP 691), with the wheels' core metadata
// files (PEP 658) when pypi.core_metadata is on, and the files themselves
// through the item routes' download, so redirects, Range and download
// counts apply to them as to any file. The pages are built on each request
// from what the PyPI format keeps in the package index; nothing is stored
// for them.
package pypi

import (
	"crypto/sha1" //nolint:gosec // the pages' ETag is a SHA-1, as npm's packuments'
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Options are the PyPI protocol's settings.
type Options struct {
	// Format are the PyPI format's settings, whose SimpleJSON and
	// CoreMetadata decide what the simple index answers.
	Format pypifmt.Settings
	// RemoteSources opts the native protocol into remote metadata and artifact reads.
	RemoteSources *source.Service
}

// Protocol is the PyPI protocol. Its errors are in Artifactory's envelope,
// as every 7.x answer under /api/pypi/ is (decision 0037); pip, uv and
// twine read only the status. The reindex is not reserved: a GET of
// "reindex" names a file at the repository's root, while the operation's
// own POST reaches the generated server ahead of the kit.
func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Sources: sources,
		Name:    "pypi",
		Types:   []repo.PackageType{repo.PyPI},
		Surface: apierror.Artifactory,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			routes(r, handler{k: k, format: o.Format})
		},
	}
}

func routes(r chi.Router, h handler) {
	// Generated JSON GET routes take precedence in the full server. The kit
	// also serves JSON so HEAD and standalone protocol mounts share its behavior.
	r.Get("/*", h.read)
	r.Head("/*", h.read)
	r.Post("/", h.upload)
	r.Post("/*", h.upload)
	r.Put("/*", h.put)
}

type handler struct {
	k      *pkgroute.Kit
	format pypifmt.Settings
}

// read dispatches JSON metadata, simple index pages, files and wheel metadata.
func (h handler) read(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !p.Folder && len(p.Segments) >= 3 && p.Segments[0] == "pypi" &&
		p.Segments[len(p.Segments)-1] == "json" {
		switch len(p.Segments) {
		case 3:
			h.metadataJSON(w, r, p.Segments[1], "")
			return
		case 4:
			h.metadataJSON(w, r, p.Segments[1], p.Segments[2])
			return
		}
	}
	if len(p.Segments) > 0 && p.Segments[0] == "simple" {
		h.simple(w, r, p)
		return
	}
	h.download(w, r, p)
}

// simple answers the simple index: /simple and /simple/<name>, without
// the slash every page URL ends in (PEP 503), and a name that is not
// normalised, redirect to the page's own URL; /simple/ is the project
// list and /simple/<normalised>/ a project's page.
func (h handler) simple(w http.ResponseWriter, r *http.Request, p pkgroute.Path) {
	switch len(p.Segments) {
	case 1:
		if !p.Folder {
			h.redirect(w, r, "")
			return
		}
		h.list(w, r)
	case 2:
		name := p.Segments[1]
		switch {
		case !pypifmt.ValidName(name):
			h.k.Error(w, r, notFound(r))
		case !p.Folder || pypifmt.Normalize(name) != name:
			h.redirect(w, r, pypifmt.Normalize(name))
		default:
			h.project(w, r, name)
		}
	default:
		h.k.Error(w, r, notFound(r))
	}
}

// redirect answers 302 to the simple index's page for project, or to the
// project list for "". The Location is absolute, from the base URL
// resolver, which honours X-JFrog-Override-Base-Url (RTFACT-21395) and
// then the configured base URL, as JFrog intends (RTFACT-31494).
func (h handler) redirect(w http.ResponseWriter, r *http.Request, project string) {
	to := repoBase(r) + "/simple/"
	if project != "" {
		to += url.PathEscape(project) + "/"
	}
	// The override base URL intentionally selects the redirect host (decision 0037).
	//nolint:gosec // G710: the shared base URL resolver supplies the destination
	http.Redirect(w, r, to, http.StatusFound)
}

func notFound(r *http.Request) error {
	return apierror.New(http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
}

// writePage answers a computed document, a page of the simple index or a
// core metadata file, with Cache-Control: no-cache, so no caching proxy
// serves a page from before the last upload (RTFACT-29209), and
// X-Checksum-Sha1 with an ETag equal to it, unquoted, as npm's packuments
// carry them (decision 0034); a matching If-None-Match answers 304
// (7.98.7, RTFACT-30198). The checksum is of the body sent: a page differs
// by caller and form, so its bytes are what identify it. vary adds Vary:
// Accept for a page, whose form Accept chooses. [I] The ETag's form and
// the Cache-Control 7.x sends are unknown (#216).
func writePage(w http.ResponseWriter, r *http.Request, contentType string, vary bool, body []byte) {
	sum := sha1.Sum(body) //nolint:gosec // an ETag, not a security check
	tag := hex.EncodeToString(sum[:])
	if vary {
		w.Header().Set("Vary", "Accept")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Checksum-Sha1", tag)
	w.Header().Set("ETag", tag)
	if noneMatch(r.Header.Get("If-None-Match"), tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		// HTML builders escape each value; JSON and metadata use their own content types.
		//nolint:gosec // G705: body is escaped HTML, encoded JSON, or text/plain metadata
		_, _ = w.Write(body)
	}
}

// noneMatch reports whether the If-None-Match header h names tag or is
// "*". Each entry is compared with any weak prefix and quotes taken off,
// since the ETag is sent unquoted and a client or proxy may send it back
// either way.
func noneMatch(h, tag string) bool {
	for entry := range strings.SplitSeq(h, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "*" || strings.Trim(strings.TrimPrefix(entry, "W/"), `"`) == tag {
			return true
		}
	}
	return false
}
