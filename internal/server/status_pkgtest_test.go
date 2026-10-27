package server

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// pkgtestProtocol is the test-only protocol that drives the router kit
// through the whole server: authentication, the item routes' download and
// the item service's deploy. It serves generic repositories, so the
// fixtures' libs-local and its grants are its repository.
func pkgtestProtocol() pkgroute.Protocol {
	files := func(k *pkgroute.Kit, w http.ResponseWriter, r *http.Request) (pkgroute.Path, bool) {
		p, err := pkgroute.ParsePath(strings.TrimPrefix(pkgroute.RequestFrom(r).Rest, "files/"))
		if err != nil {
			k.Error(w, r, err)
			return p, false
		}
		return p, true
	}
	return pkgroute.Protocol{
		Name:         "pkgtest",
		Types:        []repo.PackageType{repo.Generic},
		ErrorProfile: apierror.ProfileArtifactory,
		Reserved:     []string{"{repoKey}/reindex"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			serve := func(w http.ResponseWriter, r *http.Request) {
				if p, ok := files(k, w, r); ok {
					k.ServeItem(w, r, p.String())
				}
			}
			r.Get("/files/*", serve)
			r.Head("/files/*", serve)
			r.Put("/files/*", func(w http.ResponseWriter, r *http.Request) {
				if p, ok := files(k, w, r); ok {
					k.Deploy(w, r, p.String(), p.Props)
				}
			})
		},
	}
}

const (
	pkgtestBase = "/artifactory/api/pkgtest/"
	pkgtestGet  = "GET " + pkgtestBase + "{repoKey}/files/*"
	pkgtestHead = "HEAD " + pkgtestBase + "{repoKey}/files/*"
	pkgtestPut  = "PUT " + pkgtestBase + "{repoKey}/files/*"
)

// pkgtestStatusPart is the test-only protocol's share of the status table.
var pkgtestStatusPart = statusPart{
	handwritten: map[string]bool{pkgtestGet: true, pkgtestHead: true, pkgtestPut: true},
	rows: func() []statusCase {
		file := pkgtestBase + "libs-local/files/fixture/get.txt"
		put := pkgtestBase + "libs-local/files/pkgtest/put.txt"
		return []statusCase{
			{pkgtestGet, file, "admin", "", 200},
			{pkgtestGet, file, "user", "", 403},
			{pkgtestGet, file, "anonymous", "", 401},
			{pkgtestGet, file, "", "", 401},
			{pkgtestGet, pkgtestBase + "libs-local/files/fixture/absent", "admin", "", 404},
			{pkgtestGet, pkgtestBase + "missing/files/x", "admin", "", 404},
			// Another package type's repository is not the protocol's.
			{pkgtestGet, pkgtestBase + npmRepos[0].Key + "/files/x", "admin", "", 404},
			// The class gate: remote and virtual repositories are P9's and
			// P10's.
			{pkgtestGet, pkgtestBase + "remote-private/files/x", "admin", "", 501},
			{pkgtestGet, pkgtestBase + "virt/files/x", "admin", "", 501},
			{pkgtestGet, pkgtestBase + "libs-local/elsewhere", "admin", "", 404},
			{pkgtestGet, pkgtestBase + "libs-local/reindex", "admin", "", 405},
			{pkgtestHead, file, "admin", "", 200},
			{pkgtestHead, file, "user", "", 403},
			{pkgtestPut, put + ";k=v", "admin", "content", 201},
			{pkgtestPut, put, "user", "content", 403},
			{pkgtestPut, put, "", "content", 401},
			{pkgtestPut, pkgtestBase + "libs-local/files/pkgtest/bad;=v", "admin", "content", 400},
		}
	},
	tune: func(c *Config) { c.Protocols = append(c.Protocols, pkgtestProtocol()) },
}
