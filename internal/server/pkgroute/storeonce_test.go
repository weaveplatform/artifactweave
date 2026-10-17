package pkgroute_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
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

// onceFormat stores what PUT /once/* carries through StoreOnce, refusing
// other content with 409, and answers through Created.
func onceFormat() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:    "testfmt",
		Types:   []repo.PackageType{repo.Generic},
		Surface: apierror.NPM,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			r.Put("/once/*", func(w http.ResponseWriter, r *http.Request) {
				p, err := pkgroute.ParsePath(
					strings.TrimPrefix(pkgroute.RequestFrom(r).Rest, "once/"),
				)
				if err != nil {
					k.Error(w, r, err)
					return
				}
				it, err := k.StoreOnce(r, p.String(), r.Body, p.Props,
					pkgroute.DeclaredChecksums(r.Header), func(stored types.Item) error {
						return apierror.New(
							http.StatusConflict,
							"other content at "+stored.FullPath(),
						)
					})
				if err != nil {
					k.Error(w, r, err)
					return
				}
				k.Created(w, r, it)
			})
		},
	}
}

// onceOptions are the kit's options over st, with an item error mapping
// that keeps what differs returns and the item service's refusals apart.
func onceOptions(f *kitFixture, st store.Store) pkgroute.Options {
	return pkgroute.Options{
		Store: st,
		Items: func() *item.Service { return f.items },
		ItemError: func(_ context.Context, _ string, err error) error {
			var typed *apierror.Error
			switch {
			case errors.As(err, &typed):
				return typed
			case errors.Is(err, item.ErrBadChecksum):
				return apierror.New(http.StatusBadRequest, err.Error())
			case errors.Is(err, item.ErrChecksumMismatch), errors.Is(err, item.ErrConflict):
				return apierror.New(http.StatusConflict, err.Error())
			}
			return err
		},
		Created: func(w http.ResponseWriter, _ *http.Request, it types.Item) {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, "created "+it.FullPath())
		},
		Fault: func(_ *http.Request, err error) { f.faults = append(f.faults, err) },
	}
}

// A file stored once takes the same content again from a caller without
// Delete, which only merges its properties, and refuses other content even
// to a caller with Delete.
func TestStoreOnce(t *testing.T) {
	f := newKitFixture(t, nil)
	f.h = f.router(t, onceOptions(f, f.db), onceFormat())
	ctx := context.Background()
	rec := f.do(http.MethodPut, base+local+"/once/m/v1.zip;k=a", "updater", "v1")
	if rec.Code != http.StatusCreated || rec.Body.String() != "created m/v1.zip" ||
		rec.Header().Get("X-Checksum-Sha256") == "" {
		t.Fatalf("first store: %d %s %v", rec.Code, rec.Body, rec.Header())
	}
	first, err := f.db.Items().Get(ctx, local, false, "m/v1.zip")
	if err != nil {
		t.Fatal(err)
	}
	// updater may not delete, and needs no Delete to repeat itself.
	rec = f.do(http.MethodPut, base+local+"/once/m/v1.zip;k=b;j=x", "updater", "v1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("same content: %d %s", rec.Code, rec.Body)
	}
	again, err := f.db.Items().Get(ctx, local, false, "m/v1.zip")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || !again.Modified.Equal(first.Modified) {
		t.Fatalf("the same content rewrote the file: %+v, then %+v", first, again)
	}
	props, err := f.db.Items().Properties(ctx, again.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(props["k"], []string{"b"}) || !slices.Equal(props["j"], []string{"x"}) {
		t.Fatalf("properties %v", props)
	}
	// Without properties, the same content changes nothing.
	if rec := f.do(
		http.MethodPut,
		base+local+"/once/m/v1.zip",
		"updater",
		"v1",
	); rec.Code != http.StatusCreated {
		t.Fatalf("same content, no properties: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		path, user, body string
		headers          []string
		want             int
		says             string
	}{
		{"/once/m/v1.zip", "updater", "v2", nil, http.StatusConflict, "other content at m/v1.zip"},
		// Delete does not let other content replace it.
		{"/once/work/old.txt", "writer", "new", nil, http.StatusConflict, "other content at work/old.txt"},
		{"/once/m/v2.zip", "reader", "v2", nil, http.StatusForbidden, ""},
		{"/once/m/v2.zip", "", "v2", nil, http.StatusUnauthorized, ""},
		// The item service's refusals keep their status: a bad and a wrong
		// declared checksum, and a file over a folder.
		{"/once/m/v2.zip", "updater", "v2", []string{"X-Checksum-Sha1", "zz"}, http.StatusBadRequest, ""},
		{
			"/once/m/v2.zip", "updater", "v2",
			[]string{"X-Checksum-Sha1", strings.Repeat("0", 40)},
			http.StatusConflict, "",
		},
		{"/once/dir", "updater", "x", nil, http.StatusConflict, ""},
	} {
		rec := f.do(http.MethodPut, base+local+c.path, c.user, c.body, c.headers...)
		if rec.Code != c.want || c.says != "" && npmError(t, rec) != c.says {
			t.Errorf("%s as %q: %d %s, want %d", c.path, c.user, rec.Code, rec.Body, c.want)
		}
	}
	got, err := f.db.Items().Get(ctx, local, false, "m/v1.zip")
	if err != nil || got.Actual.SHA256 != first.Actual.SHA256 {
		t.Fatalf("refused content was stored: %+v %v", got, err)
	}
	if _, err := f.db.Items().
		Get(ctx, local, false, "m/v2.zip"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("a refused first store left a file: %v", err)
	}
}

// A store that cannot say what the path holds is a fault.
func TestStoreOnceStoreFailure(t *testing.T) {
	f := newKitFixture(t, nil)
	f.h = f.router(t, onceOptions(f, failingStore{f.db}), onceFormat())
	rec := f.do(http.MethodPut, base+local+"/once/m/v1.zip", "updater", "v1")
	if rec.Code != http.StatusInternalServerError || len(f.faults) != 1 {
		t.Fatalf("store failure: %d %s, faults %v", rec.Code, rec.Body, f.faults)
	}
}

// Merging the properties can fail like any write; the item service's
// error is answered as the server maps it.
func TestStoreOnceAnnotateFailure(t *testing.T) {
	f := newKitFixture(t, nil)
	f.h = f.router(t, onceOptions(f, f.db), onceFormat())
	if rec := f.do(
		http.MethodPut,
		base+local+"/once/m/v1.zip",
		"updater",
		"v1",
	); rec.Code != http.StatusCreated {
		t.Fatalf("first store: %d %s", rec.Code, rec.Body)
	}
	f.items = &item.Service{Store: failingPatch{f.db}, Blobs: f.items.Blobs}
	rec := f.do(http.MethodPut, base+local+"/once/m/v1.zip;k=v", "updater", "v1")
	if rec.Code != http.StatusInternalServerError || len(f.faults) != 1 {
		t.Fatalf("annotate failure: %d %s, faults %v", rec.Code, rec.Body, f.faults)
	}
}

// Without the server's deploy body, Created answers 201 with the checksum
// headers alone, as Deploy always has.
func TestCreatedWithoutBody(t *testing.T) {
	f := newKitFixture(t, nil)
	o := onceOptions(f, f.db)
	o.Created = nil
	f.h = f.router(t, o, onceFormat())
	rec := f.do(http.MethodPut, base+local+"/once/m/v1.zip", "updater", "v1")
	if rec.Code != http.StatusCreated || rec.Body.Len() != 0 ||
		rec.Header().Get("X-Checksum-Sha1") == "" {
		t.Fatalf("%d %q %v", rec.Code, rec.Body, rec.Header())
	}
}

// failingPatch fails writing properties.
type failingPatch struct{ store.Store }

func (s failingPatch) Items() store.ItemStore { return failingPatchItems{s.Store.Items()} }

type failingPatchItems struct{ store.ItemStore }

func (failingPatchItems) PatchProperties(context.Context, int64, types.Properties, []string) error {
	return errors.New("properties down")
}
