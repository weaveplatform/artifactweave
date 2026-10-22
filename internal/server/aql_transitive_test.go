package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const transitiveEndpoint = "/artifactory/api/search/aql"

type transitiveWorld struct {
	*itemWorld
	calls  []string
	rows   map[string][]peerAQLRow
	before func(source.Request) error
	check  func(source.Request) error
}

func transitiveFixture(t *testing.T) *transitiveWorld {
	t.Helper()
	w := &transitiveWorld{rows: map[string][]peerAQLRow{"up": {transitiveRow("fresh.txt")}}}
	w.itemWorld = searchWorld(t, false, func(c *Config) {
		up, err := c.Store.Repos().Get(t.Context(), "up")
		if err != nil {
			t.Fatal(err)
		}
		up.URL = "https://peer.example/artifactory/source"
		up.ContentSynchronisation = &repo.ContentSynchronisation{Enabled: true}
		if err = c.Store.Repos().Update(t.Context(), up); err != nil {
			t.Fatal(err)
		}
		c.RemoteSources = &source.Service{
			Resolve: sourceIdentity,
			HTTP: sourceBackend(
				func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					w.calls = append(w.calls, q.Repo.Key)
					if w.before != nil {
						if err := w.before(q); err != nil {
							return source.Result{}, err
						}
					}
					parsed, err := aql.Parse(q.Resource.SmartAQL)
					if err != nil {
						t.Fatal(err)
					}
					if target, err := parsed.TransitiveTarget(); err != nil || target != "source" ||
						parsed.Transitive {
						t.Fatalf("invalid peer query %s %v", q.Resource.SmartAQL, err)
					}
					body := transitiveJSON(t, w.rows[q.Repo.Key])
					if err = q.Resource.ValidateMetadata(
						ctx,
						bytes.NewReader(body),
						int64(len(body)),
						source.MetadataResponse{},
					); err != nil {
						return source.Result{}, err
					}
					return source.Result{
						Stream: &source.Stream{
							Body:   io.NopCloser(bytes.NewReader(body)),
							Status: 200,
							Representation: source.Representation{
								MediaType: "application/json",
								Size:      int64(len(body)),
							},
						},
					}, nil
				},
			),
			Revalidate: func(ctx context.Context, q source.Request, _ source.Permit) error {
				if w.check != nil {
					if err := w.check(q); err != nil {
						return err
					}
				}
				p, ok := auth.FromContext(ctx)
				if !ok {
					return source.ErrDenied
				}
				p, err := c.Auth.Current(ctx, p, c.Store.Tokens())
				if err != nil {
					return source.ErrDenied
				}
				p, err = auth.Authorize(
					ctx,
					authz.Sources{Permissions: c.Store.Permissions(), Groups: c.Store.Groups()},
					p,
				)
				if err != nil {
					return err
				}
				path := q.Resource.Path
				allows := p.Access().Allows
				if q.Resource.PermissionFolder {
					path = q.Resource.PermissionPath
					allows = p.Access().AllowsFolder
				}
				if !allows(types.ResourceArtifact, target(q.Repo), path, types.ActionRead) {
					return source.ErrDenied
				}
				return nil
			},
		}
	})
	return w
}

func transitiveRow(name string) peerAQLRow {
	return peerAQLRow{
		Repo:       "source",
		Path:       ".",
		Name:       name,
		Type:       "file",
		Size:       7,
		ActualSHA1: sha1Hex("newdata"),
		ActualMD5:  md5Hex("newdata"),
		SHA256:     sha256Hex("newdata"),
	}
}

func transitiveJSON(t *testing.T, rows []peerAQLRow) []byte {
	t.Helper()
	if rows == nil {
		rows = []peerAQLRow{}
	}
	b, err := json.Marshal(
		map[string]any{
			"results": rows,
			"range":   map[string]int{"start_pos": 0, "end_pos": len(rows), "total": len(rows)},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (w *transitiveWorld) search(t *testing.T, query, user string) []map[string]any {
	t.Helper()
	return hitsOf(t, w.do("POST", transitiveEndpoint, user, query))
}

func hitNames(hits []map[string]any) []string {
	out := make([]string, 0, len(hits))
	for _, hit := range hits {
		out = append(out, fmt.Sprint(hit["repo"])+"/"+fmt.Sprint(hit["name"]))
	}
	return out
}

func TestTransitiveAQLColdCacheAndDuplicatePrecedence(t *testing.T) {
	w := transitiveFixture(t)
	q := `items.find({"repo":"up"})`
	if got := hitNames(
		w.search(t, q, "reader"),
	); !reflect.DeepEqual(got, []string{"up-cache/cached.txt"}) ||
		len(w.calls) != 0 {
		t.Fatal("ordinary query fetched", got, w.calls)
	}
	conflict := transitiveRow("cached.txt")
	conflict.Size = 999
	w.rows["up"] = append(w.rows["up"], conflict, transitiveRow("fresh.txt"))
	rec := w.do("POST", transitiveEndpoint, "reader", q+`.transitive()`)
	hits := hitsOf(t, rec)
	if got := hitNames(
		hits,
	); !reflect.DeepEqual(
		got,
		[]string{"up-cache/cached.txt", "up/fresh.txt"},
	) {
		t.Fatal(got)
	}
	if hits[0]["size"] != float64(1) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cache precedence", hits, rec.Header())
	}
	if _, err := w.db.Items().
		Get(t.Context(), "up", true, "fresh.txt"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("search persisted cache item", err)
	}
	wantStatus(t, w.do("GET", "/artifactory/up-cache/fresh.txt", "reader", ""), 404)
	if len(w.calls) != 1 {
		t.Fatal("cache alias fetched", w.calls)
	}
}

func TestTransitiveAQLRejectsUnsupportedShapesBeforeNetwork(t *testing.T) {
	w := transitiveFixture(t)
	for _, q := range []string{
		`items.find({}).transitive()`, `items.find({"repo":"up-cache"}).transitive()`, `items.find({"repo":"libs-local"}).transitive()`,
		`items.find({"repo":"up"}).sort({"$asc":["name"]}).transitive()`, `items.find({"repo":"up"}).offset(0).transitive()`,
		`items.find({"repo":"up"}).include("stat").transitive()`, `items.find({"repo":"up"}).include("name").transitive()`,
		`items.find({"$or":[{"repo":"up"},{"name":"fresh.txt"}]}).transitive()`, `builds.find({"repo":"up"}).transitive()`,
	} {
		wantStatus(t, w.do("POST", transitiveEndpoint, "reader", q), 400)
	}
	if len(w.calls) != 0 {
		t.Fatal(w.calls)
	}
	c, err := w.db.Repos().Get(t.Context(), "up")
	if err != nil {
		t.Fatal(err)
	}
	c.ContentSynchronisation = nil
	if err = w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	wantStatus(
		t,
		w.do("POST", transitiveEndpoint, "admin", `items.find({"repo":"up"}).transitive()`),
		400,
	)
	if len(w.calls) != 0 {
		t.Fatal(w.calls)
	}
}

func TestTransitiveAQLVirtualOrderLimitPatternsAndPermissions(t *testing.T) {
	w := transitiveFixture(t)
	keys := []string{"r3", "r1", "r6", "r2", "r5", "r4"}
	for _, key := range keys {
		c := *repo.Defaults(&repo.Config{Key: key, Class: repo.Remote, URL: "https://peer.example/artifactory/source", ContentSynchronisation: &repo.ContentSynchronisation{Enabled: true}})
		if err := w.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		w.rows[key] = []peerAQLRow{transitiveRow("fresh.txt")}
	}
	for _, c := range []repo.Config{{Key: "inner", Class: repo.Virtual, Repositories: keys[:3]}, {Key: "outer", Class: repo.Virtual, Repositories: []string{"inner", "r3", "r2", "r5", "r4", "libs-local"}}} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	hits := w.search(t, `items.find({"repo":"outer","name":"fresh.txt"}).transitive()`, "reader")
	if len(hits) != 5 || !reflect.DeepEqual(w.calls, keys[:5]) {
		t.Fatal("member order/limit", hits, w.calls)
	}
	w.calls = nil
	c, err := w.db.Repos().Get(t.Context(), "outer")
	if err != nil {
		t.Fatal(err)
	}
	c.ExcludesPattern = new("fresh.txt")
	if err = w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if hits = w.search(t, `items.find({"repo":"outer"}).transitive()`, "reader"); len(hits) != 2 {
		t.Fatal("virtual patterns failed", hits)
	} // two local files under libs-local
	w.calls = nil
	if hits = w.search(
		t,
		`items.find({"repo":"outer","name":"fresh.txt"}).transitive()`,
		"user",
	); len(hits) != 0 ||
		len(w.calls) != 0 {
		t.Fatal("unauthorized discovery", hits, w.calls)
	}
	w.calls = nil
	if hits = w.search(
		t,
		`items.find({"repo":"outer","name":"fresh.txt"}).transitive()`,
		"admin",
	); len(
		hits,
	) != 5 {
		t.Fatal("admin pattern exemption", hits)
	}
	for _, q := range []string{`items.find({"repo":"loop"}).transitive()`, `items.find({"repo":"virt"}).transitive()`} {
		wantStatus(t, w.do("POST", transitiveEndpoint, "admin", q), 400)
	}
}

func TestTransitiveAQLBoundsFailuresAndCurrentFences(t *testing.T) {
	for _, failure := range []string{"upstream", "credentials", "permission", "source", "timeout"} {
		t.Run(failure, func(t *testing.T) {
			w := transitiveFixture(t)
			w.before = func(q source.Request) error {
				switch failure {
				case "upstream":
					return errors.New("https://secret:password@peer.example/private")
				case "timeout":
					return context.DeadlineExceeded
				case "credentials":
					u, err := w.db.Users().Get(t.Context(), "reader")
					if err != nil {
						t.Fatal(err)
					}
					u.PasswordHash = []byte("changed")
					return w.db.Users().Update(t.Context(), u)
				case "permission":
					return w.db.Groups().Delete(t.Context(), auth.ReadersGroup)
				case "source":
					c := q.Repo
					c.URL = "https://changed.example/artifactory/source"
					return w.db.Repos().Update(t.Context(), c)
				}
				return nil
			}
			rec := w.do(
				"POST",
				transitiveEndpoint,
				"reader",
				`items.find({"repo":"up"}).transitive()`,
			)
			if rec.Code == 200 || strings.Contains(rec.Body.String(), "peer.example") ||
				strings.Contains(rec.Body.String(), "fresh.txt") {
				t.Fatal(rec.Code, rec.Body)
			}
		})
	}
	w := transitiveFixture(t)
	hits := w.search(t, `items.find({"repo":"up"}).limit(1).transitive()`, "reader")
	if len(hits) != 1 {
		t.Fatal(hits)
	}
	w.calls = nil
	if hits = w.search(
		t,
		`items.find({"repo":"up"}).limit(0).transitive()`,
		"reader",
	); len(hits) != 0 ||
		len(w.calls) != 0 {
		t.Fatal(hits, w.calls)
	}
}

func TestTransitiveCachedNonmatchSuppressesUpstreamMetadata(t *testing.T) {
	w := transitiveFixture(t)
	conflict := transitiveRow("cached.txt")
	conflict.Properties = append(conflict.Properties, struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}{"stage", "upstream"})
	fresh := transitiveRow("fresh.txt")
	fresh.Properties = conflict.Properties
	w.rows["up"] = []peerAQLRow{conflict, fresh}
	hits := w.search(t, `items.find({"repo":"up","@stage":"upstream"}).transitive()`, "reader")
	if got := hitNames(hits); !reflect.DeepEqual(got, []string{"up/fresh.txt"}) {
		t.Fatal("peer metadata replaced actual cached identity", got)
	}
}

func TestTransitiveAQLFinalExactPathFence(t *testing.T) {
	w := transitiveFixture(t)
	permission := types.Permission{
		Name: "only-paths",
		Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Targets: map[string]types.Target{"up": {Include: []string{"allowed/**"}}},
				Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			},
		},
	}
	if err := w.db.Permissions().Create(t.Context(), permission); err != nil {
		t.Fatal(err)
	}
	first, second, hidden := transitiveRow(
		"a.bin",
	), transitiveRow(
		"b.bin",
	), transitiveRow(
		"secret.bin",
	)
	first.Path, second.Path, hidden.Path = "allowed", "allowed", "private"
	w.rows["up"] = []peerAQLRow{first, second, hidden}
	hits := w.search(t, `items.find({"repo":"up"}).transitive()`, "user")
	if len(hits) != 2 {
		t.Fatal("narrow descendant grant or hidden file filter", hits)
	}
	changed := false
	w.check = func(q source.Request) error {
		if !changed && q.Resource.Kind == source.Artifact && q.Resource.Path == "allowed/b.bin" {
			changed = true
			resource := permission.Resources[types.ResourceArtifact]
			resource.Targets["up"] = types.Target{Include: []string{"allowed/b.bin"}}
			permission.Resources[types.ResourceArtifact] = resource
			return w.db.Permissions().Update(t.Context(), permission)
		}
		return nil
	}
	rec := w.do("POST", transitiveEndpoint, "user", `items.find({"repo":"up"}).transitive()`)
	wantStatus(t, rec, 403)
	if !changed || strings.Contains(rec.Body.String(), "a.bin") {
		t.Fatal("earlier selected path escaped final fence", rec.Body)
	}
}

func TestTransitiveVirtualTraversalBounds(t *testing.T) {
	for _, depth := range []int{9, 260} {
		configs := map[string]repo.Config{"remote": {Key: "remote", Class: repo.Remote}}
		if depth == 9 {
			next := "remote"
			for i := depth; i >= 0; i-- {
				key := fmt.Sprintf("v%d", i)
				configs[key] = repo.Config{
					Key:          key,
					Class:        repo.Virtual,
					Repositories: []string{next},
				}
				next = key
			}
		} else {
			members := make([]string, depth)
			for i := range members {
				members[i] = "remote"
			}
			configs["v0"] = repo.Config{Key: "v0", Class: repo.Virtual, Repositories: members}
		}
		if _, err := transitiveMembers(configs, "v0"); err == nil {
			t.Fatal("unbounded virtual traversal", depth)
		}
	}
}

func TestTransitiveAQLCacheArrivalBeforeDelivery(t *testing.T) {
	w := transitiveFixture(t)
	w.rows["up"] = []peerAQLRow{transitiveRow("first.txt"), transitiveRow("second.txt")}
	changed := false
	w.check = func(q source.Request) error {
		if !changed && q.Resource.Kind == source.Artifact && q.Resource.Path == "second.txt" {
			changed = true
			_, err := w.db.Items().
				Put(t.Context(), types.Item{RepoKey: "up", Cached: true, Path: ".", Name: "first.txt", Size: 999})
			return err
		}
		return nil
	}
	rec := w.do("POST", transitiveEndpoint, "reader", `items.find({"repo":"up"}).transitive()`)
	wantStatus(t, rec, 502)
	if !changed || strings.Contains(rec.Body.String(), "first.txt") {
		t.Fatal("late cache content escaped precedence fence", rec.Body)
	}
}

func TestTransitiveAQLTrimmingSurvivesFilteringAndDuplicates(t *testing.T) {
	w := transitiveFixture(t)
	w.rows["up"] = []peerAQLRow{transitiveRow("fresh.txt"), transitiveRow("fresh.txt")}
	c, err := w.db.Repos().Get(t.Context(), "up")
	if err != nil {
		t.Fatal(err)
	}
	c.ExcludesPattern = new("fresh.txt")
	if err = w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	rec := w.do(
		"POST",
		transitiveEndpoint,
		"reader",
		`items.find({"repo":"up","name":"fresh.txt"}).limit(1).transitive()`,
	)
	if hits := hitsOf(t, rec); len(hits) != 0 {
		t.Fatal(hits)
	}
	if !strings.Contains(rec.Body.String(), "notification") {
		t.Fatal("candidate bound hidden after filtering/deduplication", rec.Body)
	}
}
