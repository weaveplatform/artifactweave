package nuget

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// push answers a push to PackagePublish, PUT /api/nuget/{repoKey}[/] or
// /api/nuget/{repoKey}/{folder...}/, which stores the package in that
// folder (decision 0038, "Push").
func (h handler) push(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	h.store(w, r, p.String(), false)
}

// symbols answers a push to SymbolPackagePublish, PUT
// /api/nuget/v3/{repoKey}/symbols[/], which stores a symbols package at the
// repository root, "flat and unrenamed" as 7.x stores it, whether or not
// its package is there (decision 0038, "Symbols").
func (h handler) symbols(w http.ResponseWriter, r *http.Request) {
	h.store(w, r, "", true)
}

// store makes decision 0038's checks of a push in its order, then stores
// the package at its canonical name in folder:
//  1. the caller, which the server's authentication found, from the
//     X-NuGet-ApiKey header or the request's credentials;
//  2. Deploy somewhere in folder, before the body is read, as npm's
//     publish is checked: 401 to anonymous, with the challenge, 403 to
//     anyone else;
//  3. a valid package, else 400, Microsoft's status for an invalid one; a
//     symbols package to the push route, or anything else to the symbols
//     route, is refused the same way;
//  4. Deploy on the canonical path;
//  5. a version the repository already holds, at any path, answers 409 to
//     every caller, administrators included: it is what --skip-duplicate
//     accepts (PackageUpdateResource) and what Enforce Layout answers;
//  6. the item routes' own refusals, through the NuGet format's Admit.
//
// It answers 201 with no body, the status 7.x answers; [I] the body is
// inferred.
func (h handler) store(w http.ResponseWriter, r *http.Request, folder string, symbols bool) {
	if !h.k.RequireFolder(w, r, folder, types.ActionWrite) {
		return
	}
	// The first file part is the package and the rest are ignored, as
	// Microsoft specifies; the client names it "package".
	form, err := h.k.ReadForm(r, "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	n, err := nugetfmt.ReadStored(r.Context(), h.k.Blobs(), form.File.Digests.SHA256)
	switch {
	case errors.Is(err, nugetfmt.ErrNotPackage):
		h.k.Error(w, r, apierror.New(http.StatusBadRequest,
			"The pushed file is "+strings.TrimPrefix(err.Error(), "nuget: ")+"."))
		return
	case err != nil:
		h.k.Error(w, r, err)
		return
	case n.IsSymbols() && !symbols:
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, fmt.Sprintf(
			"%s %s is a symbols package; push it to %s/symbols",
			n.ID, n.Version.Normalized(), v3Base(r))))
		return
	case !n.IsSymbols() && symbols:
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, fmt.Sprintf(
			"%s %s is no symbols package: it declares no %s package type",
			n.ID, n.Version.Normalized(), nugetfmt.SymbolsPackageType)))
		return
	}
	name, what := nugetfmt.FileName(n.ID, n.Version), ""
	if symbols {
		name, what = nugetfmt.SymbolsFileName(n.ID, n.Version), "the symbols of "
	}
	_, err = h.k.StoreNew(r, path.Join(folder, name), form.File, nil, types.Checksums{},
		func() error {
			// The canonical path holds the version already. Elsewhere in the
			// repository, Admit refuses it with the same words.
			return apierror.New(http.StatusConflict, fmt.Sprintf(
				"The repository '%s' already contains %s%s %s",
				pkgroute.RequestFrom(r).Repo.Key, what, n.ID, n.Version.Normalized()))
		})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
