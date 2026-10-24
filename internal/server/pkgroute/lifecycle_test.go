package pkgroute_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// changeFormat changes what it names through the kit: PATCH /props/*
// annotates the file with k=v, DELETE /files/* deletes it, and
// GET /folder/* reports whether the caller may write below a folder.
func changeFormat() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:    "testfmt",
		Types:   []repo.PackageType{repo.Generic},
		Surface: apierror.NPM,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			path := func(r *http.Request, prefix string) string {
				return strings.TrimPrefix(pkgroute.RequestFrom(r).Rest, prefix)
			}
			r.Patch("/props/*", func(w http.ResponseWriter, r *http.Request) {
				it, err := k.DB().Items().Get(r.Context(), local, false, path(r, "props/"))
				if err == nil {
					err = k.Annotate(r, it, types.Properties{"k": {"v"}}, nil)
				}
				if err != nil {
					k.Error(w, r, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
			r.Delete("/files/*", func(w http.ResponseWriter, r *http.Request) {
				if err := k.Delete(r, path(r, "files/")); err != nil {
					k.Error(w, r, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
			r.Get("/folder/*", func(w http.ResponseWriter, r *http.Request) {
				if k.CanFolder(r, path(r, "folder/"), types.ActionWrite) {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.WriteHeader(http.StatusForbidden)
			})
		},
	}
}

// Annotate and Delete change a file through the item service for a caller
// holding Annotate or Delete on it, refuse anyone else, answer 404 for a
// missing file or a folder, and pass the item service's failures through
// the server's mapping.
func TestAccAnnotateAndDelete(t *testing.T) {
	f := newKitFixture(t, nil)
	f.h = f.router(t, onceOptions(f, f.db), changeFormat())
	ctx := context.Background()
	for _, c := range []struct {
		method, path, user string
		want               int
	}{
		{http.MethodPatch, "props/a/b.txt", "admin-like", http.StatusForbidden},
		{http.MethodPatch, "props/a/b.txt", "", http.StatusUnauthorized},
		{http.MethodDelete, "files/a/b.txt", "reader", http.StatusForbidden},
		{http.MethodDelete, "files/work/absent.txt", "writer", http.StatusNotFound},
		{http.MethodDelete, "files/work", "writer", http.StatusNotFound},
		{http.MethodDelete, "files/work/old.txt", "writer", http.StatusNoContent},
		{http.MethodGet, "folder/work", "writer", http.StatusNoContent},
		{http.MethodGet, "folder/a", "writer", http.StatusForbidden},
		{http.MethodGet, "folder/a", "", http.StatusForbidden},
	} {
		rec := f.do(c.method, base+local+"/"+c.path, c.user, "")
		if rec.Code != c.want {
			t.Errorf("%s %s as %q: %d %s", c.method, c.path, c.user, rec.Code, rec.Body)
		}
	}
	if _, err := f.db.Items().Get(ctx, local, false, "work/old.txt"); !errors.Is(
		err, store.ErrNotFound) {
		t.Fatalf("not deleted: %v", err)
	}
	// Annotate needs its own grant.
	if err := f.db.Permissions().Create(ctx, types.Permission{
		Name: "annotator",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{
				Users: map[string]types.Actions{"annotator": types.ActionAnnotate},
			},
			Targets: map[string]types.Target{local: {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(http.MethodPatch, base+local+"/props/a/b.txt", "annotator", ""); rec.Code !=
		http.StatusNoContent {
		t.Fatalf("annotate: %d %s", rec.Code, rec.Body)
	}
	// The item service's failures, and a store that cannot say what the
	// path holds, are faults.
	f.items = &item.Service{Store: failingPatch{f.db}, Blobs: f.items.Blobs}
	if rec := f.do(http.MethodPatch, base+local+"/props/a/b.txt", "annotator", ""); rec.Code !=
		http.StatusInternalServerError {
		t.Fatalf("annotate failure: %d %s", rec.Code, rec.Body)
	}
	f.items = &item.Service{Store: failingTrash{f.db}, Blobs: f.items.Blobs}
	if rec := f.do(http.MethodDelete, base+local+"/files/work/new.txt", "writer", ""); rec.Code !=
		http.StatusNotFound {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body)
	}
	f.deploy(t, local, "work/new.txt", "new")
	if rec := f.do(http.MethodDelete, base+local+"/files/work/new.txt", "writer", ""); rec.Code !=
		http.StatusInternalServerError {
		t.Fatalf("delete failure: %d %s", rec.Code, rec.Body)
	}
	f.h = f.router(t, onceOptions(f, failingStore{f.db}), changeFormat())
	if rec := f.do(http.MethodDelete, base+local+"/files/work/new.txt", "writer", ""); rec.Code !=
		http.StatusInternalServerError {
		t.Fatalf("store failure: %d %s", rec.Code, rec.Body)
	}
}

// failingTrash fails putting anything in the trash can.
type failingTrash struct{ store.Store }

func (s failingTrash) Trash() store.TrashStore { return failingTrashStore{s.Store.Trash()} }

type failingTrashStore struct{ store.TrashStore }

func (failingTrashStore) Add(context.Context, types.TrashEntry) (types.TrashEntry, error) {
	return types.TrashEntry{}, errors.New("trash down")
}
