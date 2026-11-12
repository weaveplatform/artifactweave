package pypihttp

import (
	"errors"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	pypifmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pypi"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// metadataSuffix names a wheel's core metadata file (PEP 658).
const metadataSuffix = ".metadata"

// download answers a GET or HEAD of a file through the item routes'
// download, so Range (7.133.3), the ETag and X-Checksum-* headers,
// download counts, redirects and permissions behave as on every download
// (decision 0037). With pypi.core_metadata on, a path that names a wheel
// with core metadata, plus ".metadata", is that metadata instead. The
// 6.x link form under packages/ is not served: no file can be stored
// there (RTFACT-30372).
func (h handler) download(w http.ResponseWriter, r *http.Request, p pkgroute.Path) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		h.remoteDownload(w, r, p)
		return
	}
	if len(p.Segments) == 0 || p.Folder {
		// A folder is no file, as on the kit's own download.
		h.k.Error(w, r, errNoFile)
		return
	}
	path := p.String()
	wheel, ok := strings.CutSuffix(path, metadataSuffix)
	if ok && strings.HasSuffix(wheel, ".whl") {
		h.wheelMetadata(w, r, path, wheel)
		return
	}
	h.k.ServeItem(w, r, path)
}

// wheelMetadata answers path, the core metadata of the wheel at wheel
// (PEP 658): the index's, with pypi.core_metadata on, or else a file
// stored at path. Either way the wheel's download policy applies, not
// path's: decision 0032 checks a file's sidecar and an archive's entry at
// the file, so a blacked-out repository or a pattern rejecting the wheel
// refuses its metadata too, naming the wheel, and a pattern on .metadata
// paths alone refuses nothing. [I] 7.x's PEP 658 support is an
// undocumented system property, so the path it names is not known
// (RTFACT-26891, #216).
func (h handler) wheelMetadata(w http.ResponseWriter, r *http.Request, path, wheel string) {
	if h.format.CoreMetadata {
		text, found, err := h.coreMetadata(r, wheel)
		switch {
		case err != nil:
			h.k.Error(w, r, err)
			return
		case found:
			// Under the wheel's Read permission, whose metadata it is.
			if h.k.Require(w, r, wheel, api.ActionRead) && h.allowed(w, r, wheel) {
				writePage(w, r, "application/octet-stream", false, []byte(text))
			}
			return
		}
	}
	if h.k.Require(w, r, path, api.ActionRead) && h.allowed(w, r, wheel) {
		h.k.ServeMetadata(w, r, path)
	}
}

// allowed applies the download policy to path, having answered its
// refusal.
func (h handler) allowed(w http.ResponseWriter, r *http.Request, path string) bool {
	if err := h.k.CheckDownload(r, path); err != nil {
		h.k.Error(w, r, err)
		return false
	}
	return true
}

// coreMetadata is the core metadata the index keeps for the wheel at path,
// when it has whole METADATA there: computed, as npm's packuments are, so
// an item a client deployed at the .metadata path is not served while the
// wheel has metadata, and the bytes always match the hash the pages
// advertise (decision 0037).
func (h handler) coreMetadata(r *http.Request, path string) (string, bool, error) {
	f, err := h.k.StoredItem(r, path)
	switch {
	case errors.Is(err, kernel.ErrNotFound) || err == nil && f.Item.Folder:
		return "", false, nil
	case err != nil:
		return "", false, err //nolint:wrapcheck // the kit names the protocol
	}
	pv, linked, err := pkgindex.VersionOf(r.Context(), h.k.DB(), f.Item.ID)
	if err != nil || !linked {
		return "", false, err //nolint:wrapcheck // pkgindex wraps its own
	}
	m, err := pypifmt.DecodeMetadata(pv.Metadata)
	if err != nil {
		return "", false, err //nolint:wrapcheck // the format names the document
	}
	for _, file := range m.Files {
		if file.Path == path && file.Core {
			return m.Text(file), true, nil
		}
	}
	return "", false, nil
}

// errNoFile is a path that names no file to serve, answered as the kit
// answers one.
var errNoFile = apierror.New(http.StatusNotFound, "Could not find resource")
