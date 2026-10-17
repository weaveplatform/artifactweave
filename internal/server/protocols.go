package server

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// protocolOptions gives the router kit the item routes' download, deploy
// and error mapping, so a protocol's files are redirected, counted, hooked
// and refused as theirs are.
func (s artifactoryService) protocolOptions() pkgroute.Options {
	return pkgroute.Options{
		Store: s.cfg.Store,
		Items: func() *item.Service { return s.items(s.cfg.Store) },
		ServeItem: func(w http.ResponseWriter, r *http.Request, c repo.Config, it types.Item) {
			p, _ := auth.FromContext(r.Context())
			s.serveFile(w, r, s.cfg.Store, found{repo: c, item: it}, p)
		},
		ItemError: s.itemError,
		Created: func(w http.ResponseWriter, r *http.Request, it types.Item) {
			writeJSON(w, http.StatusCreated, itemCreated(r.Context(), it))
		},
		Fault: func(r *http.Request, err error) { noteCause(r.Context(), err) },
	}
}
