// Package golang serves Go module repositories at
// /artifactory/api/go/{repoKey}/ through the router kit (decisions 0032 and
// 0035): the GOPROXY reads the go command makes, and the deploys of
// jf go-publish. Paths, version selection and the package index are
// internal/format/golang's; this package answers HTTP with them.
package golang

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	goformat "github.com/weaveplatform/artifactweave/internal/format/golang"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Protocol is the Go module protocol. Its errors are plain text: the go
// command shows an error's body only when it is (research 26 section 1.1).
func Protocol() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:    "go",
		Types:   []repo.PackageType{repo.Go},
		Surface: apierror.Text,
		// The first element of a module path contains a dot, so "sumdb"
		// never names a module.
		Absent: []string{"sumdb/"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			reads := reads{k: k}
			r.Get("/*", reads.serve)
			r.Head("/*", reads.serve)
			r.Put("/*", publisher{k}.publish)
			// A path that names no Go operation answers 404 to every
			// method, and only one that does answers 405 to a method no
			// route serves.
			r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
				if _, err := operation(r); errors.Is(err, goformat.ErrNoOperation) {
					k.Error(w, r, noOperation(r))
					return
				}
				k.Error(w, r, apierror.New(http.StatusMethodNotAllowed,
					fmt.Sprintf("Method %s is not allowed on %s.", r.Method, r.URL.Path)))
			})
		},
	}
}

// operation reads the GOPROXY operation the request's path names, with the
// format's errors as they are.
func operation(r *http.Request) (goformat.Request, error) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		return goformat.Request{}, err //nolint:wrapcheck // the kit's 400, answered as it is
	}
	return goformat.ParseRequest(p.String()) //nolint:wrapcheck // read by errors.Is and errors.As
}

// parse is operation with its errors as a read answers them: 404 for a
// path that names no operation or fails its checks, and the kit's 400 for
// a malformed escape.
func parse(r *http.Request) (goformat.Request, error) {
	q, err := operation(r)
	var invalid *goformat.InvalidPathError
	switch {
	case errors.Is(err, goformat.ErrNoOperation):
		return q, noOperation(r)
	case errors.As(err, &invalid):
		return q, apierror.New(http.StatusNotFound, invalid.Message)
	}
	return q, err
}

func noOperation(r *http.Request) error {
	return apierror.New(http.StatusNotFound,
		fmt.Sprintf("%s names no Go module operation.", r.URL.Path))
}
