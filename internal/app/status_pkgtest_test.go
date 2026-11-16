package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// pkgtestProtocol is the test-only protocol that drives the router kit
// through the whole server: authentication, the item routes' download and
// the item service's deploy. It serves generic repositories, so the
// fixtures' libs-local and its grants are its repository. A virtual
// repository's file is served through its members (pkgroute.Virtual), and
// merged/<path> answers the keys of the members a merge of the document at
// path takes, in the walk's order, through pkgroute.Collect.
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
		Sources: map[repo.Class]pkgroute.Source{
			repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
			repo.Virtual: pkgroute.Virtual{},
		},
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
			r.Get("/merged/*", func(w http.ResponseWriter, r *http.Request) {
				path := strings.TrimPrefix(pkgroute.RequestFrom(r).Rest, "merged/")
				parts, err := pkgroute.Collect(k, r, path, func(mr *http.Request) (string, error) {
					_, err := k.ReadMetadata(mr, pkgroute.Resource(mr, path, path))
					return pkgroute.RequestFrom(mr).Repo.Key, err
				})
				if err == nil && len(parts) == 0 {
					err = apierror.New(http.StatusNotFound, "Could not find resource")
				}
				if err != nil {
					k.Error(w, r, pkgroute.SourceError(err, false))
					return
				}
				keys := make([]string, len(parts))
				for i, p := range parts {
					keys[i] = p.Doc
				}
				_ = json.NewEncoder(w).Encode(keys)
			})
		},
	}
}

// seedPkgtestVirtual adds the virtual repositories the walk's rows read:
// pkgtest-virt lists pkgtest-nested, which lists pkgtest-b and
// pkgtest-virt again, and then pkgtest-a; pkgtest-strict's own patterns
// exclude doc/. Both locals hold doc/index.txt, pkgtest-a alone
// only-a.txt, and pkgtest-b excluded/file.txt, which its patterns exclude.
// "user" may read pkgtest-b alone.
func seedPkgtestVirtual(t *testing.T, db *inmem.Store, blobs blob.Store) {
	t.Helper()
	ctx := context.Background()
	for _, c := range []repo.Config{
		{Key: "pkgtest-a", Class: repo.Local},
		{Key: "pkgtest-b", Class: repo.Local},
		{Key: "pkgtest-virt", Class: repo.Virtual, Repositories: []string{"pkgtest-nested", "pkgtest-a"}},
		{Key: "pkgtest-nested", Class: repo.Virtual, Repositories: []string{"pkgtest-b", "pkgtest-virt"}},
		{
			Key: "pkgtest-strict", Class: repo.Virtual, Repositories: []string{"pkgtest-a"},
			ExcludesPattern: new("doc/**"),
		},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	items := &item.Service{Store: db, Blobs: blobs, Formats: ecosystem.For}
	for key, paths := range map[string][]string{
		"pkgtest-a": {"doc/index.txt", "only-a.txt"},
		"pkgtest-b": {"doc/index.txt", "excluded/file.txt"},
	} {
		c, err := db.Repos().Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			if _, err := items.Deploy(ctx, item.Deploy{
				Target: item.Target{Repo: c, Path: p}, Body: strings.NewReader(key),
			}); err != nil {
				t.Fatal(err)
			}
		}
		if key == "pkgtest-b" {
			c.ExcludesPattern = new("excluded/**")
			if err := db.Repos().Update(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Permissions().Create(ctx, accesstest.Permission{
		Name: "pkgtest-read-b",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"user": api.ActionRead}},
			Targets: map[string]accesstest.Target{"pkgtest-b": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

const (
	pkgtestBase = "/artifactory/api/pkgtest/"
	pkgtestGet  = "GET " + pkgtestBase + "{repoKey}/files/*"
	pkgtestHead = "HEAD " + pkgtestBase + "{repoKey}/files/*"
	pkgtestPut  = "PUT " + pkgtestBase + "{repoKey}/files/*"
	// pkgtestMerged is a merged document's route (pkgroute.Collect).
	pkgtestMerged = "GET " + pkgtestBase + "{repoKey}/merged/*"
)

// pkgtestStatusPart is the test-only protocol's share of the status table.
var pkgtestStatusPart = statusPart{
	handwritten: map[string]bool{
		pkgtestGet: true, pkgtestHead: true, pkgtestPut: true, pkgtestMerged: true,
	},
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
			// The class gate: a remote repository is P9's, for a protocol
			// that registers no remote source.
			{pkgtestGet, pkgtestBase + "remote-private/files/x", "admin", "", 501},
			// A virtual repository's file comes from the first member in
			// the package handlers' walk that holds it and that the caller
			// may read; nested virtual repositories are flattened, the
			// cycle back skipped (decision 0008, R1, R2). A member the
			// caller may not read is not found, never 403 (R3), and a
			// member's or the virtual repository's patterns rejecting the
			// path pass over or refuse as R5 has it.
			{pkgtestGet, pkgtestBase + "virt/files/fixture/get.txt", "admin", "", 200},
			{pkgtestGet, pkgtestBase + "virt/files/fixture/get.txt", "user", "", 404},
			{pkgtestGet, pkgtestBase + "virt/files/x", "admin", "", 404},
			{pkgtestHead, pkgtestBase + "virt/files/fixture/get.txt", "admin", "", 200},
			{pkgtestGet, pkgtestBase + "pkgtest-virt/files/doc/index.txt", "admin", "", 200},
			{pkgtestGet, pkgtestBase + "pkgtest-virt/files/doc/index.txt", "user", "", 200},
			{pkgtestGet, pkgtestBase + "pkgtest-virt/files/only-a.txt", "admin", "", 200},
			{pkgtestGet, pkgtestBase + "pkgtest-virt/files/only-a.txt", "user", "", 404},
			{pkgtestGet, pkgtestBase + "pkgtest-virt/files/only-a.txt", "anonymous", "", 404},
			{pkgtestGet, pkgtestBase + "pkgtest-virt/files/only-a.txt", "", "", 401},
			{pkgtestGet, pkgtestBase + "pkgtest-nested/files/only-a.txt", "admin", "", 200},
			{pkgtestGet, pkgtestBase + "pkgtest-virt/files/excluded/file.txt", "admin", "", 404},
			{pkgtestGet, pkgtestBase + "pkgtest-strict/files/doc/index.txt", "admin", "", 404},
			{pkgtestMerged, pkgtestBase + "libs-local/merged/fixture/get.txt", "admin", "", 200},
			{pkgtestMerged, pkgtestBase + "libs-local/merged/fixture/get.txt", "user", "", 403},
			{pkgtestMerged, pkgtestBase + "libs-local/merged/fixture/get.txt", "", "", 401},
			{pkgtestMerged, pkgtestBase + "libs-local/merged/fixture/absent", "admin", "", 404},
			{pkgtestMerged, pkgtestBase + "pkgtest-virt/merged/doc/index.txt", "admin", "", 200},
			{pkgtestMerged, pkgtestBase + "pkgtest-virt/merged/doc/index.txt", "user", "", 200},
			{pkgtestMerged, pkgtestBase + "pkgtest-nested/merged/doc/index.txt", "admin", "", 200},
			{pkgtestMerged, pkgtestBase + "virt/merged/fixture/get.txt", "user", "", 404},
			{pkgtestMerged, pkgtestBase + "pkgtest-virt/merged/excluded/file.txt", "admin", "", 404},
			{pkgtestMerged, pkgtestBase + "pkgtest-strict/merged/doc/index.txt", "admin", "", 404},
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
	seed: seedPkgtestVirtual,
	tune: func(c *RouterConfig) { c.Protocols = append(c.Protocols, pkgtestProtocol()) },
}
