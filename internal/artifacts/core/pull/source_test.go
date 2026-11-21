package pull_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pull"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type peer struct {
	binding         string
	entries         []pull.Entry
	next            string
	props           model.Properties
	currentErr      error
	denied          map[string]bool
	filesErr        error
	propertyErr     error
	inspectErr      error
	inspected       *pull.Entry
	seenPage        kernel.Page
	filesCalls      int
	propertyCalls   int
	afterProperties func()
}

func (p *peer) Binding() string                  { return p.binding }
func (p *peer) Revalidate(context.Context) error { return p.currentErr }
func (p *peer) CheckRead(_ context.Context, path string) error {
	if p.denied[path] {
		return source.ErrDenied
	}
	return p.currentErr
}

func (p *peer) Files(_ context.Context, page kernel.Page) ([]pull.Entry, string, error) {
	p.seenPage, p.filesCalls = page, p.filesCalls+1
	return p.entries, p.next, p.filesErr
}

func (p *peer) Inspect(_ context.Context, path string) (pull.Entry, error) {
	if p.inspected != nil {
		return *p.inspected, p.inspectErr
	}
	for _, entry := range p.entries {
		if entry.Path == path {
			return entry, p.inspectErr
		}
	}
	return pull.Entry{}, kernel.ErrNotFound
}

func (p *peer) Properties(context.Context, string) (model.Properties, error) {
	p.propertyCalls++
	if p.afterProperties != nil {
		p.afterProperties()
	}
	return p.props, p.propertyErr
}

type world struct {
	db      *inmem.Store
	source  pull.Source
	config  repo.Config
	peer    *peer
	openErr error
	root    string
	scope   remote.DiscoveryScope
}

func fixture(t *testing.T) *world {
	t.Helper()
	w := &world{db: inmem.New(), peer: &peer{binding: "private-options-hash", denied: map[string]bool{}}}
	w.config = *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Generic, URL: "https://peer.example/artifactory/source"})
	if err := w.db.Repos().Create(t.Context(), w.config); err != nil {
		t.Fatal(err)
	}
	service := &source.Service{Resolve: func(ctx context.Context, c repo.Config) (model.SourceIdentity, error) {
		return w.db.RemoteCache().Source(ctx, c.Key)
	}}
	w.source = pull.Source{
		Content: &item.Service{Store: w.db}, InTx: w.db.InTx,
		Open: func(context.Context, string, string) (*source.Service, source.Request, error) {
			return service, source.Request{Repo: w.config, Subject: accessapi.Subject{Name: "current"}, Access: &accessapitest.Decider{Admin: true}}, w.openErr
		},
		Peer: func(_ context.Context, _ *source.Service, _ source.Request, root string, scope remote.DiscoveryScope) (pull.Peer, error) {
			w.root, w.scope = root, scope
			return w.peer, nil
		},
	}
	return w
}

func file(path string) pull.Entry {
	return pull.Entry{Path: path, Size: 4, Checksums: kernel.Checksums{SHA256: strings.Repeat("a", 64)}, Modified: time.Unix(10, 0), Updated: time.Unix(20, 0)}
}

func binding(t *testing.T, w *world, selection api.PullSelection) api.PullRepository {
	t.Helper()
	r, err := w.source.Repository(t.Context(), "remote", selection)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUnitPullCatalogBindsCurrentSourceAndReturnsOnlySelectedReadableFiles(t *testing.T) {
	w := fixture(t)
	selection := api.PullSelection{Root: "selected", Include: "selected/**", Exclude: "**/excluded*"}
	w.peer.entries = []pull.Entry{file("selected/a.bin"), file("selected/excluded.bin"), file("other/file.bin"), file("selected/denied.bin")}
	w.peer.denied["selected/denied.bin"] = true
	r := binding(t, w, selection)
	if w.peer.filesCalls != 0 || r.Repository.Incarnation == "" || r.Repository.Generation == "" || r.Revision == "" || strings.Contains(r.Revision, "private-options") {
		t.Fatal("binding fetched content or exposed raw options")
	}
	items, next, err := w.source.Items(t.Context(), r, selection, kernel.Page{})
	if err != nil || next != "" || len(items) != 1 || items[0].Path != "selected/a.bin" || items[0].Version == "" || items[0].Repository != r {
		t.Fatalf("selected page %+v next=%q err=%v", items, next, err)
	}
	if w.root != "selected" || w.scope.Path != "selected" || w.scope.Root || w.peer.propertyCalls != 0 {
		t.Fatal("catalog widened traversal or selected unrequested metadata")
	}
	w.peer.binding = "new-options-hash"
	if _, _, err = w.source.Items(t.Context(), r, selection, kernel.Page{}); !errors.Is(err, source.ErrSuperseded) || w.peer.filesCalls != 1 {
		t.Fatal("saved binding reused superseded transport credentials", err)
	}
}

func TestUnitPullCatalogKeepsContinuationForFilteredEmptyPagesAndRejectsForeignScope(t *testing.T) {
	w := fixture(t)
	selection := api.PullSelection{Exclude: "**"}
	w.peer.entries, w.peer.next = []pull.Entry{file("a")}, kernel.Cursor("next-upstream-path")
	r := binding(t, w, selection)
	items, next, err := w.source.Items(t.Context(), r, selection, kernel.Page{Limit: 2})
	if err != nil || len(items) != 0 || next == "" {
		t.Fatalf("filtered page %+v %q %v", items, next, err)
	}
	w.peer.entries, w.peer.next = nil, ""
	_, final, err := w.source.Items(t.Context(), r, selection, kernel.Page{Limit: 2, After: next})
	if err != nil || final != "" || w.peer.seenPage.After != kernel.Cursor("next-upstream-path") {
		t.Fatal("lost upstream continuation", err)
	}
	other := api.PullSelection{Include: "a/**"}
	otherBinding := binding(t, w, other)
	before := w.peer.filesCalls
	if _, _, err = w.source.Items(t.Context(), otherBinding, other, kernel.Page{After: next}); !errors.Is(err, kernel.ErrInvalidCursor) || w.peer.filesCalls != before {
		t.Fatal("foreign selection cursor reached discovery", err)
	}
}

func TestUnitPullCatalogPropertiesAreStableAndCannotBorrowChangedFileFacts(t *testing.T) {
	w := fixture(t)
	selection := api.PullSelection{Properties: true}
	w.peer.entries, w.peer.props = []pull.Entry{file("a")}, model.Properties{"build": {"2", "1", "1"}}
	r := binding(t, w, selection)
	items, _, err := w.source.Items(t.Context(), r, selection, kernel.Page{})
	if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0].Properties, map[string][]string{"build": {"1", "2"}}) {
		t.Fatal("property selection", err)
	}
	w.peer.props = model.Properties{"build": {"1", "2"}}
	again, _, err := w.source.Items(t.Context(), r, selection, kernel.Page{})
	if err != nil || len(again) != 1 || again[0].Version != items[0].Version {
		t.Fatal("equivalent properties changed immutable snapshot version", err)
	}
	changed := file("a")
	changed.Updated = changed.Updated.Add(time.Second)
	w.peer.inspected = &changed
	if items, next, err := w.source.Items(t.Context(), r, selection, kernel.Page{}); !errors.Is(err, source.ErrSuperseded) || len(items) != 0 || next != "" {
		t.Fatal("properties were bound to an independently changed file", err)
	}
}

func TestUnitPullCatalogRefusesTransactionsAndCurrentAuthorityFailures(t *testing.T) {
	w := fixture(t)
	r := binding(t, w, api.PullSelection{})
	if err := w.db.Tx(t.Context(), func(ctx context.Context) error {
		_, _, err := w.source.Items(ctx, r, api.PullSelection{}, kernel.Page{})
		return err
	}); !errors.Is(err, kernel.ErrInvalid) || w.peer.filesCalls != 0 {
		t.Fatal("metadata transaction reached peer", err)
	}
	w.peer.currentErr = source.ErrDenied
	if _, _, err := w.source.Items(t.Context(), r, api.PullSelection{}, kernel.Page{}); !errors.Is(err, source.ErrDenied) || w.peer.filesCalls != 0 {
		t.Fatal("revoked caller reused a saved binding", err)
	}
	w.peer.currentErr = nil
	w.peer.entries = []pull.Entry{file("a"), file("b")}
	if _, _, err := w.source.Items(t.Context(), r, api.PullSelection{}, kernel.Page{Limit: 1}); !errors.Is(err, source.ErrInvalidResult) {
		t.Fatal("unbounded port response escaped", err)
	}
}
