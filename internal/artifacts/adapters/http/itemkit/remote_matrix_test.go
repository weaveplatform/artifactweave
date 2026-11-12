package itemkit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// copyStore is an in-memory store whose item lookups fail with getErr
// when it is set, and whose remote cache lists the pages exact returns for
// a path, in place of its own.
type copyStore struct {
	*inmem.Store
	getErr error
	exact  map[string]copyPage
}

// copyPage is one page of a remote cache listing: its entries, the token
// of the next page, and the error the listing fails with.
type copyPage struct {
	entries []model.RemoteCacheEntry
	next    string
	err     error
}

func (s copyStore) Items() port.ItemStore {
	return copyItems{ItemStore: s.Store.Items(), err: s.getErr}
}

func (s copyStore) RemoteCache() port.RemoteCacheStore {
	return copyCache{RemoteCacheStore: s.Store.RemoteCache(), pages: s.exact}
}

type copyItems struct {
	port.ItemStore
	err error
}

func (i copyItems) Get(ctx context.Context, key string, cached bool, path string) (model.Item, error) {
	if i.err != nil {
		return model.Item{}, i.err
	}
	return i.ItemStore.Get(ctx, key, cached, path) //nolint:wrapcheck // the fake passes it on
}

type copyCache struct {
	port.RemoteCacheStore
	pages map[string]copyPage
}

func (c copyCache) Exact(
	_ context.Context, _, _ string, p kernel.Page,
) ([]model.RemoteCacheEntry, string, error) {
	page := c.pages[p.After]
	return page.entries, page.next, page.err
}

// TestUnitRemoteCopyMatches (#1371): the copy of a path a remote's cache
// holds is matched against a request's matrix parameters before the
// upstream is asked, as 7.x's RemoteRepoBase.internalGetInfo answers from
// the cache and DbStoringRepoMixin.getInfo answers PROPERTY_MISMATCH for a
// cached copy whose properties do not match (7.161.26). A cached item is
// matched by its properties (PropertiesImpl.matchQuery: a mandatory key it
// lacks, or a key it carries with other values, does not match; a key it
// lacks does). A metadata document the cache keeps without an item carries
// no properties [I], so only a mandatory key refuses it; a negative or
// item-backed entry is no such document. With no copy the upstream decides,
// and with no parameters nothing is read. A store that cannot answer fails
// the match rather than serving a copy that may not match.
func TestUnitRemoteCopyMatches(t *testing.T) {
	c := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, URL: "https://up.example"})
	document := model.RemoteCacheEntry{Blob: blob.Blob{Checksums: kernel.Checksums{
		SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
	}}}
	negative := document
	negative.NegativeUntil = time.Now().Add(time.Hour)
	backed := document
	backed.ItemID = 7
	fault := errors.New("store fault")
	for _, tc := range []struct {
		name, params string
		item         *model.Properties
		folder       bool
		getErr       error
		pages        map[string]copyPage
		want         bool
		wantErr      bool
	}{
		{name: "no parameters read nothing", getErr: fault, want: true},
		{name: "no copy", params: ";x+=1", want: true},
		{
			name: "an item carrying a mandatory key", params: ";x+=1",
			item: &model.Properties{"x": {"1"}}, want: true,
		},
		{name: "an item lacking a mandatory key", params: ";x+=1", item: &model.Properties{}},
		{name: "an item lacking an optional key", params: ";x=1", item: &model.Properties{}, want: true},
		{
			name: "an item carrying another value", params: ";x=1",
			item: &model.Properties{"x": {"2"}},
		},
		{name: "a folder is no copy", params: ";x+=1", item: &model.Properties{}, folder: true, want: true},
		{name: "an item lookup fault", params: ";x=1", getErr: fault, wantErr: true},
		{
			name: "a document and a mandatory key", params: ";x+=1",
			pages: map[string]copyPage{"": {entries: []model.RemoteCacheEntry{document}}},
		},
		{
			name: "a document and an optional key", params: ";x=1",
			pages: map[string]copyPage{"": {entries: []model.RemoteCacheEntry{document}}},
			want:  true,
		},
		{
			name: "negative and item-backed entries", params: ";x+=1",
			pages: map[string]copyPage{"": {entries: []model.RemoteCacheEntry{negative, backed}}},
			want:  true,
		},
		{
			name: "a document on a later page", params: ";x+=1",
			pages: map[string]copyPage{
				"":      {entries: []model.RemoteCacheEntry{negative}, next: "later"},
				"later": {entries: []model.RemoteCacheEntry{document}},
			},
		},
		{
			name: "a listing fault", params: ";x+=1",
			pages: map[string]copyPage{"": {err: fault}}, wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := kittest.New(t, inmem.New())
			st := copyStore{Store: w.Store, exact: tc.pages}
			if tc.item != nil {
				it := w.Put(t, c, true, "a/file.bin", []byte("cached"))
				if tc.folder {
					it.Folder = true
					var err error
					if it, err = w.Store.Items().Put(t.Context(), it); err != nil {
						t.Fatal(err)
					}
				}
				if err := w.Store.Items().SetProperties(t.Context(), it.ID, *tc.item,
					time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			st.getErr = tc.getErr
			req, err := itemkit.ParseEscapedItemPath("remote/a/file.bin" + tc.params)
			if err != nil {
				t.Fatal(err)
			}
			got, err := itemkit.RemoteCopyMatches(t.Context(), st, c, req.Path, req)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Errorf("RemoteCopyMatches = %v, %v; want %v, error %v", got, err, tc.want,
					tc.wantErr)
			}
		})
	}
}
