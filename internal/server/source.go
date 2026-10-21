package server

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// getRemoteItem is reached only for a directly addressed remote repository.
// Recursive lookup, storage APIs and explicit cache aliases keep their existing
// cache-only behavior. P10 must supply an explicit composition policy before a
// virtual member can fetch; installing the P9 service cannot enable that path.
func (s artifactoryService) getRemoteItem(
	w http.ResponseWriter,
	r *http.Request,
	c repo.Config,
	req itemRequest,
	p auth.Principal,
	acc access,
) {
	escapedKey, _, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/"), "/")
	resource := pkgroute.Resource(r, req.path,
		pkgroute.EscapedResourcePath(r, "/artifactory/"+escapedKey+"/"))
	result, err := s.cfg.RemoteSources.Read(r.Context(), source.Request{
		Repo:     c,
		Subject:  p.Subject(),
		Access:   acc.d,
		Resource: resource,
		Matrix:   req.matrix(),
		Project:  c.ProjectKey,
	})
	if err != nil {
		writeErr(
			w,
			r,
			s.itemError(r.Context(), "read source", pkgroute.SourceError(err, p.Anonymous)),
		)
		return
	}
	if result.Stream != nil {
		if err = pkgroute.ServeStream(w, r, result.Stream); err != nil {
			noteCause(r.Context(), err)
			panic(http.ErrAbortHandler)
		}
		return
	}
	if ok, matchErr := matchesMatrix(r.Context(), s.cfg.Store, *result.Item, req); matchErr != nil {
		writeErr(w, r, s.internalError(r.Context(), "item properties", matchErr))
	} else if !ok {
		writeErr(w, r, itemNotFound())
	} else {
		s.serveFile(w, r, s.cfg.Store, found{repo: c, item: *result.Item}, p)
	}
}
