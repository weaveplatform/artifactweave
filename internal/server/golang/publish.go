package golang

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	goformat "github.com/weaveplatform/artifactweave/internal/format/golang"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// publisher answers the deploys jf go-publish sends: one PUT per file of a
// version, .zip, then .mod, then .info (research 26 section 3.1).
type publisher struct{ k *pkgroute.Kit }

// publish deploys PUT <module>/@v/<version>.<ext>[;key=value...]. The path
// checks come first, so a malformed path is 400 whoever sends it. A .zip or
// .mod is stored once: the same content again only merges its properties
// and needs Deploy alone, which lets a pipeline re-run jf go-publish, and
// other content is 409 even for a caller who may delete, since go.sum pins
// it. A .info is checked, then stored as the item routes store a file, so
// replacing one needs Delete.
func (p publisher) publish(w http.ResponseWriter, r *http.Request) {
	k := p.k
	path, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		k.Error(w, r, err)
		return
	}
	f, err := file(r, path)
	if err != nil {
		k.Error(w, r, err)
		return
	}
	declared := pkgroute.DeclaredChecksums(r.Header)
	var it types.Item
	if f.Ext == goformat.ExtInfo {
		// Checked before the body is read, so a refused caller's never is.
		if !k.Require(w, r, f.Path(), types.ActionWrite) {
			return
		}
		it, err = p.storeInfo(r, f, path.Props, declared)
	} else {
		it, err = k.StoreOnce(r, f.Path(), r.Body, path.Props, declared,
			func(types.Item) error { return goformat.Republished(f) })
	}
	if err != nil {
		k.Error(w, r, err)
		return
	}
	k.Created(w, r, it)
}

// file is the version file a PUT names, or the answer to one that names
// none, as the reads answer other methods: 404 for a path that is no Go
// operation and 405 for @v/list and @latest, which are read only; and 400
// for a version file whose path fails the checks.
func file(r *http.Request, p pkgroute.Path) (goformat.File, error) {
	req, err := goformat.ParseRequest(p.String())
	var invalid *goformat.InvalidPathError
	switch {
	case errors.As(err, &invalid):
		return goformat.File{}, apierror.New(http.StatusBadRequest, invalid.Message)
	case err != nil:
		return goformat.File{}, noOperation(r)
	case req.Kind == goformat.KindList || req.Kind == goformat.KindLatest:
		return goformat.File{}, apierror.New(http.StatusMethodNotAllowed,
			fmt.Sprintf("Method %s is not allowed on %s.", r.Method, r.URL.Path))
	}
	return req.File(), nil
}

// storeInfo checks a .info's body and stores it.
func (p publisher) storeInfo(
	r *http.Request,
	f goformat.File,
	props types.Properties,
	declared types.Checksums,
) (types.Item, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, goformat.MaxInfo+1))
	if err != nil {
		return types.Item{}, apierror.New(http.StatusBadRequest,
			fmt.Sprintf("Reading the .info of version %s of module %s failed: %v.",
				f.Version, f.Module, err))
	}
	if err := goformat.CheckInfo(f, body); err != nil {
		return types.Item{}, apierror.New(http.StatusBadRequest, err.Error())
	}
	return p.k.Store( //nolint:wrapcheck // the kit's answer, given as it is
		r, f.Path(), bytes.NewReader(body), props, declared)
}
