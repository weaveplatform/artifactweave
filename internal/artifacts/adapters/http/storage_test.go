package artifactshttp

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The permissions view renders what Grants answers in the legacy letters,
// leaving out a name that holds no action. It asks about the item's full
// path through the item's repository, then ANY, then the ANY LOCAL or ANY
// REMOTE target that the repository's class adds, and passes a Grants error
// up as it came, since ItemGrants names the target it was reading.
func TestUnitStoragePermissionsRenderGrants(t *testing.T) {
	var asked []string
	grants := &accessapitest.Grants{ItemGrantsFunc: func(
		_ context.Context,
		targets []string,
		path string,
		folder bool,
	) (map[string]accessapi.Actions, map[string]accessapi.Actions, error) {
		if path != "dir/a.txt" || folder {
			t.Errorf("ItemGrants asked about %q (folder %t), want the file dir/a.txt", path, folder)
		}
		asked = targets
		users := map[string]accessapi.Actions{
			"dev":  accessapi.ActionRead | accessapi.ActionWrite,
			"none": 0,
		}
		return users, map[string]accessapi.Actions{"readers": accessapi.ActionRead}, nil
	}}
	v := storageView{
		s:   newArtifactoryAPI(Config{Access: accessapi.API{Grants: grants}}),
		key: "libs",
	}
	item := model.Item{RepoKey: "libs", Path: "dir", Name: "a.txt"}
	want := map[string]any{
		"uri": "/artifactory/api/storage/libs/dir/a.txt",
		"principals": map[string]any{
			"users":  map[string][]string{"dev": {"r", "w"}},
			"groups": map[string][]string{"readers": {"r"}},
		},
	}
	for class, targets := range map[repo.Class][]string{
		repo.Local:     {"libs", accessapi.TargetAny, accessapi.TargetAnyLocal},
		repo.Federated: {"libs", accessapi.TargetAny, accessapi.TargetAnyLocal},
		repo.Remote:    {"libs", accessapi.TargetAny, accessapi.TargetAnyRemote},
		repo.Virtual:   {"libs", accessapi.TargetAny},
	} {
		f := itemkit.Found{Repo: repo.Config{Key: "libs", Class: class}, Item: item}
		media, body, err := v.permissions(t.Context(), f)
		if err != nil || media != "application/json" || !reflect.DeepEqual(body, want) {
			t.Errorf("%s: %q, %v, %v; want %v", class, media, body, err, want)
		}
		if !slices.Equal(asked, targets) {
			t.Errorf("%s: asked about %q, want %q", class, asked, targets)
		}
	}
	grants.ItemGrantsFunc = func(
		context.Context, []string, string, bool,
	) (map[string]accessapi.Actions, map[string]accessapi.Actions, error) {
		return nil, nil, errBoom
	}
	f := itemkit.Found{Repo: repo.Config{Key: "libs", Class: repo.Local}, Item: item}
	if _, _, err := v.permissions(t.Context(), f); !errors.Is(err, errBoom) ||
		err.Error() != errBoom.Error() {
		t.Errorf("a Grants error: %v, want it as Grants gave it", err)
	}
}

// A member the store cannot read fails a virtual repository's folder view
// with the store's error, which answers 500, rather than a list that leaves
// the member's items out.
func TestUnitStorageTreesMemberReadFails(t *testing.T) {
	db := inmem.New()
	virt := *repo.Defaults(&repo.Config{
		Key: "virt", Class: repo.Virtual, Repositories: []string{"libs-local"},
	})
	if err := db.Repos().Create(t.Context(), virt); err != nil {
		t.Fatal(err)
	}
	v := storageView{
		st:   faultyStore{Store: db, fail: map[string]bool{"get:libs-local": true}},
		repo: virt,
	}
	if _, err := v.trees(t.Context()); !errors.Is(err, errBoom) {
		t.Fatalf("trees: %v, want the store's error", err)
	}
}

// A member the store cannot read fails the read gate of a virtual
// repository's root, which asks the members (Admits; #1533), with 500
// rather than a refusal.
func TestUnitStorageVirtualRootGateFails(t *testing.T) {
	db := inmem.New()
	virt := *repo.Defaults(&repo.Config{
		Key: "virt", Class: repo.Virtual, Repositories: []string{"libs-local"},
	})
	if err := db.Repos().Create(t.Context(), virt); err != nil {
		t.Fatal(err)
	}
	v := storageView{
		s:    newArtifactoryAPI(Config{}),
		st:   faultyStore{Store: db, fail: map[string]bool{"get:libs-local": true}},
		key:  "virt",
		repo: virt,
		acc:  itemkit.Access{Decider: &accessapitest.Decider{}},
	}
	var e *apierror.Error
	if err := v.admit(t.Context(), "", kernel.Principal{Name: "user"}); !errors.As(err, &e) ||
		e.Status != http.StatusInternalServerError {
		t.Fatalf("admit: %v, want 500", err)
	}
}

// itemFaults fails the item store's read named by fail with errBoom: any
// item's ("get"), any root's ("root") or any item's properties
// ("properties").
type itemFaults struct {
	*inmem.Store
	fail string
}

func (f itemFaults) Items() port.ItemStore { return faultyItemReads{f.Store.Items(), f.fail} }

type faultyItemReads struct {
	port.ItemStore
	fail string
}

func (f faultyItemReads) Get(
	ctx context.Context,
	key string,
	cached bool,
	fullPath string,
) (model.Item, error) {
	if f.fail == "get" {
		return model.Item{}, errBoom
	}
	return f.ItemStore.Get(ctx, key, cached, fullPath) //nolint:wrapcheck // test double
}

func (f faultyItemReads) Root(ctx context.Context, key string, cached bool) (model.Item, error) {
	if f.fail == "root" {
		return model.Item{}, errBoom
	}
	return f.ItemStore.Root(ctx, key, cached) //nolint:wrapcheck // test double
}

func (f faultyItemReads) Properties(ctx context.Context, id int64) (model.Properties, error) {
	if f.fail == "properties" {
		return nil, errBoom
	}
	return f.ItemStore.Properties(ctx, id) //nolint:wrapcheck // test double
}

// propertiesWorld is a store with the locals a-local and b-local, the
// remote nostore, which keeps no cache, the virtual repository virt over
// nostore, a-local and b-local, and the items p.bin (none in a-local, k=v
// in b-local), q.bin (m=1 in a-local, k=v in b-local) and n.bin, k=v in
// nostore's cache, which it never fills.
func propertiesWorld(t *testing.T) (*inmem.Store, repo.Config) {
	t.Helper()
	ctx := t.Context()
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: "a-local", Class: repo.Local},
		{Key: "b-local", Class: repo.Local},
		{
			Key: "nostore", Class: repo.Remote, URL: "https://up.example",
			StoreArtifactsLocally: new(bool),
		},
		{
			Key: "virt", Class: repo.Virtual,
			Repositories: []string{"nostore", "a-local", "b-local"},
		},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, it := range []struct {
		key    string
		cached bool
		name   string
		props  model.Properties
	}{
		{"a-local", false, "p.bin", nil},
		{"b-local", false, "p.bin", model.Properties{"k": {"v"}}},
		{"a-local", false, "q.bin", model.Properties{"m": {"1"}}},
		{"b-local", false, "q.bin", model.Properties{"k": {"v"}}},
		{"nostore", true, "n.bin", model.Properties{"k": {"v"}}},
	} {
		stored, err := db.Items().Put(ctx, model.Item{RepoKey: it.key, Cached: it.cached, Name: it.name})
		if err != nil {
			t.Fatal(err)
		}
		if it.props != nil {
			if err := db.Items().SetProperties(ctx, stored.ID, it.props, stored.Modified); err != nil {
				t.Fatal(err)
			}
		}
	}
	virt, err := db.Repos().Get(ctx, "virt")
	if err != nil {
		t.Fatal(err)
	}
	return db, virt
}

// TestUnitStorageVirtualPropertiesPick (#1537): a virtual repository's
// properties are those of the first member's item, locals then caches,
// that the caller may read and that has any, and only then are the keys
// the request names kept (ArtifactResource.resolveProperties,
// getPropertiesResponse); a remote member that keeps no cache is never
// read (getResolvedLocalFederatedAndCachedRepos).
func TestUnitStorageVirtualPropertiesPick(t *testing.T) {
	db, virt := propertiesWorld(t)
	admin := itemkit.Access{Decider: &accessapitest.Decider{Admin: true}}
	notA := func(_ accessapi.ResourceType, r accessapi.Repo, _ string, _ accessapi.Actions) bool {
		return r.Key != "a-local"
	}
	noA := itemkit.Access{Decider: &accessapitest.Decider{AllowsFunc: notA, AllowsFolderFunc: notA}}
	for _, c := range []struct {
		path, names string
		acc         itemkit.Access
		want        model.Properties
	}{
		{"p.bin", "", admin, model.Properties{"k": {"v"}}},
		{"q.bin", "", admin, model.Properties{"m": {"1"}}},
		{"q.bin", "k", admin, nil},
		{"q.bin", "k", noA, model.Properties{"k": {"v"}}},
		{"n.bin", "", admin, nil},
		{"", "", admin, nil},
	} {
		v := storageView{st: db, key: "virt", repo: virt, path: c.path, acc: c.acc}
		_, body, err := v.virtualProperties(t.Context(), c.names)
		var e *apierror.Error
		switch {
		case c.want == nil:
			if !errors.As(err, &e) || e.Status != http.StatusNotFound {
				t.Errorf("%s?properties=%s: %v %v, want 404", c.path, c.names, body, err)
			}
		case err != nil ||
			!reflect.DeepEqual(body.(map[string]any)["properties"], c.want):
			t.Errorf("%s?properties=%s: %v %v, want %v", c.path, c.names, body, err, c.want)
		}
	}
}

// A store failure while a virtual repository's members are read fails
// its item info and properties with the store's error, which answers 500,
// rather than a miss.
func TestUnitStorageVirtualItemFaults(t *testing.T) {
	db, virt := propertiesWorld(t)
	admin := itemkit.Access{Decider: &accessapitest.Decider{Admin: true}}
	for _, c := range []struct {
		st   Store
		path string
		view func(storageView) error
	}{
		{
			faultyStore{Store: db, fail: map[string]bool{"get:a-local": true}},
			"p.bin",
			func(v storageView) error { _, err := v.virtualItem(t.Context()); return err },
		},
		{
			itemFaults{db, "get"},
			"p.bin",
			func(v storageView) error { _, err := v.virtualItem(t.Context()); return err },
		},
		{
			itemFaults{db, "root"},
			"",
			func(v storageView) error { _, err := v.virtualItem(t.Context()); return err },
		},
		{
			itemFaults{db, "properties"},
			"q.bin",
			func(v storageView) error { _, _, err := v.virtualProperties(t.Context(), ""); return err },
		},
	} {
		v := storageView{st: c.st, key: "virt", repo: virt, path: c.path, acc: admin}
		if err := c.view(v); !errors.Is(err, errBoom) {
			t.Errorf("%T %s: %v, want the store's error", c.st, c.path, err)
		}
	}
}

func TestUnitParseProperties(t *testing.T) {
	for in, want := range map[string]model.Properties{
		`a=1`:          {"a": {"1"}},
		`a=1,2|b=`:     {"a": {"1", "2"}, "b": {""}},
		`a=x=y;a=z`:    {"a": {"x=y", "z"}},
		`a=p\;q\|r`:    {"a": {"p;q|r"}},
		`a\b=1`:        {"ab": {"1"}},
		`;;a=1;`:       {"a": {"1"}},
		`a=trailing\`:  {"a": {`trailing\`}},
		`=1`:           nil,
		`a=1;missingv`: nil,
	} {
		got, err := parseProperties(in)
		if want == nil {
			if err == nil {
				t.Errorf("%q: parsed %v, want an error", in, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, %v, want %v", in, got, err, want)
		}
	}
}
