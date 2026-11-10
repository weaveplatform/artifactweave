package server

import (
	"net/http"
	"net/url"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// protocolOptions gives the router kit the item routes' download, deploy
// and error mapping, so a protocol's files are redirected, counted, hooked
// and refused as theirs are.
func (s artifactoryService) protocolOptions() pkgroute.Options {
	return pkgroute.Options{
		Store: s.cfg.Store,
		Items: func() *item.Service { return s.Items(s.cfg.Store) },
		ServeItem: func(w http.ResponseWriter, r *http.Request, c repo.Config, it types.Item) {
			p, _ := kernel.FromContext(r.Context())
			s.ServeFile(w, r, s.cfg.Store, Found{Repo: c, Item: it}, p)
		},
		DeployItem: func(w http.ResponseWriter, r *http.Request, c repo.Config, rest string) {
			s.deployEscapedItem(w, r, url.PathEscape(c.Key)+"/"+rest)
		},
		ItemError: s.ItemError,
		Created: func(w http.ResponseWriter, r *http.Request, it types.Item) {
			httpkit.WriteJSON(w, http.StatusCreated, ItemCreated(r.Context(), it))
		},
		Fault: func(r *http.Request, err error) { httpkit.NoteCause(r.Context(), err) },
	}
}
