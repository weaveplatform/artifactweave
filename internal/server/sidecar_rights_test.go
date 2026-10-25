package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// sidecarGetStore fails reading the item at one path.
type sidecarGetStore struct {
	store.Store
	path *string
}

func (s sidecarGetStore) Items() store.ItemStore { return sidecarGets{s.Store.Items(), s.path} }

type sidecarGets struct {
	store.ItemStore
	path *string
}

//nolint:wrapcheck // test double
func (s sidecarGets) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (types.Item, error) {
	if p == *s.path {
		return types.Item{}, errBoom
	}
	return s.ItemStore.Get(ctx, key, cached, p)
}

// sidecarWorld is an item world with a Helm repository, charts, a chart
// with its .prov at a/web-1.0.0.tgz, which deployer may read and deploy
// but not delete, and a store that fails reading the item at *fail.
func sidecarWorld(t *testing.T) (*itemWorld, *string) {
	t.Helper()
	fail := new(string)
	w := newItemWorld(t, false, func(c *Config) { c.Store = sidecarGetStore{c.Store, fail} })
	ctx := context.Background()
	c := *repo.Defaults(&repo.Config{Key: "charts", Class: repo.Local, PackageType: repo.Helm})
	if err := w.db.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "deploy-charts",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"deployer": types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{"charts": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: w.db, Blobs: w.blobs}
	for p, body := range map[string][]byte{
		"a/web-1.0.0.tgz": helmChart("web", "1.0.0"), "a/web-1.0.0.tgz.prov": []byte("signed"),
	} {
		if _, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: c, Path: p}, Body: bytes.NewReader(body),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return w, fail
}

// A copy takes a chart's .prov along under the rights the chart needs: a
// caller who may deploy where it lands copies both, and one who may not
// delete a .prov already there replaces nothing, so the chart stays too.
func TestAccTransferRightsOnSidecars(t *testing.T) {
	w, _ := sidecarWorld(t)
	rec := w.do(
		http.MethodPost,
		"/artifactory/api/copy/charts/a/web-1.0.0.tgz?to=/charts/b/web-1.0.0.tgz",
		"deployer",
		"",
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	w.item("charts", false, "b/web-1.0.0.tgz.prov")
	rec = w.do(
		http.MethodPost,
		"/artifactory/api/copy/charts/a/web-1.0.0.tgz?to=/charts/c/web-1.0.0.tgz",
		"admin",
		"",
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// Only the .prov is left where the next copy lands.
	if _, err := w.db.Items().
		Delete(context.Background(), "charts", false, "c/web-1.0.0.tgz"); err != nil {
		t.Fatal(err)
	}
	rec = w.do(
		http.MethodPost,
		"/artifactory/api/copy/charts/a/web-1.0.0.tgz?to=/charts/c/web-1.0.0.tgz",
		"deployer",
		"",
	)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(),
		"User doesn't have permissions to override 'charts:c/web-1.0.0.tgz.prov'") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, err := w.db.Items().
		Get(context.Background(), "charts", false, "c/web-1.0.0.tgz"); err == nil {
		t.Fatal("the chart went without its .prov")
	}
}

// A store failing while the sidecars are looked up fails the request.
func TestAccSidecarRightsStoreFaults(t *testing.T) {
	for _, c := range []struct {
		fail, method, path string
	}{
		{"a/web-1.0.0.tgz.prov", http.MethodDelete, "/artifactory/charts/a/web-1.0.0.tgz"},
		{"a/web-1.0.0.tgz.prov", http.MethodPost, "/artifactory/api/copy/charts/a/web-1.0.0.tgz?to=/charts/b/web-1.0.0.tgz"},
		{"b/web-1.0.0.tgz.prov", http.MethodPost, "/artifactory/api/copy/charts/a/web-1.0.0.tgz?to=/charts/b/web-1.0.0.tgz"},
	} {
		w, fail := sidecarWorld(t)
		*fail = c.fail
		as := "deployer"
		if c.method == http.MethodDelete {
			as = "admin"
		}
		if rec := w.do(c.method, c.path, as, ""); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s, %s failing: %d %s", c.method, c.path, c.fail, rec.Code, rec.Body)
		}
	}
}
