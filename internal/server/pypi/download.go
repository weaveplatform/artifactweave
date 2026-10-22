package pypi

import (
	"errors"
	"net/http"
	"strings"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
	if wheel, ok := strings.CutSuffix(path, metadataSuffix); ok && h.format.CoreMetadata &&
		strings.HasSuffix(wheel, ".whl") {
		text, found, err := h.coreMetadata(r, wheel)
		switch {
		case err != nil:
			h.k.Error(w, r, err)
			return
		case found:
			// Under the wheel's Read permission, whose metadata it is.
			if h.k.Require(w, r, wheel, types.ActionRead) {
				writePage(w, r, "application/octet-stream", false, []byte(text))
			}
			return
		}
	}
	h.k.ServeItem(w, r, path)
}

// coreMetadata is the core metadata the index keeps for the wheel at path,
// when it has whole METADATA there: computed, as npm's packuments are, so
// an item a client deployed at the .metadata path is not served while the
// wheel has metadata, and the bytes always match the hash the pages
// advertise (decision 0037).
func (h handler) coreMetadata(r *http.Request, path string) (string, bool, error) {
	f, err := h.k.Item(r, path)
	switch {
	case errors.Is(err, store.ErrNotFound) || err == nil && f.Item.Folder:
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
