package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// buildsAQLWorld is a router over a store with runs app/1, app/2 and
// tool/1, each producing libs-local/app.jar, and a promotion of app/1. dev
// may read libs-local and the builds under app/**; tokens can be issued.
func buildsAQLWorld(t *testing.T, tune ...func(*Config)) (http.Handler, *token.Service) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Users().Create(ctx, types.User{Name: "dev", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	dev := types.Grants{Users: map[string]types.Actions{"dev": types.ActionRead}}
	if err := db.Permissions().Create(ctx, types.Permission{
		Name: "dev-app", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: dev,
				Targets: map[string]types.Target{"libs-local": {Include: []string{"**"}}},
			},
			types.ResourceBuild: {
				Actions: dev,
				Targets: map[string]types.Target{
					"artifactory-build-info": {Include: []string{"app/**"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := repo.Config{Key: "libs-local", Class: repo.Local}
	if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	if _, err := (&item.Service{Store: db, Blobs: blobs}).Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: "app.jar"}, Body: strings.NewReader("jar"),
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	jar := types.BuildFile{Name: "app.jar", Checksums: types.Checksums{SHA1: sha1Hex("jar")}}
	for n, run := range [][2]string{{"app", "1"}, {"app", "2"}, {"tool", "1"}} {
		b, err := db.Builds().Put(ctx, types.Build{
			Name: run[0], Number: run[1], Started: base.Add(time.Duration(n) * time.Minute),
			Created: base.Add(time.Hour), CreatedBy: "ci", URL: "https://ci/" + run[0],
			Modules: []types.BuildModule{{ID: run[0], Artifacts: []types.BuildFile{jar}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			if _, err := db.Builds().AddPromotion(ctx, types.BuildPromotion{
				BuildID: b.ID, Created: base.Add(2 * time.Hour), CreatedBy: "admin",
				Status: "released", Repo: "libs-release", CIUser: "jenkins",
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a, PasswordHash: testHash,
		Tokens: tokens,
	}
	for _, f := range tune {
		f(&cfg)
	}
	return New(cfg), tokens
}

// bearerFor issues dev a token with scope.
func bearerFor(t *testing.T, tokens *token.Service, scope string) map[string]string {
	t.Helper()
	sc, err := types.ParseScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := tokens.Issue(context.Background(), token.Request{Username: "dev", Scope: sc})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Authorization": "Bearer " + issued.AccessToken}
}

// buildRows runs query and returns each row as "<name>/<number>" and the
// range block.
func buildRows(
	t *testing.T,
	h http.Handler,
	as map[string]string,
	query string,
) ([]string, map[string]any) {
	t.Helper()
	rec := do(t, h, http.MethodPost, aqlURL, as, query)
	var res struct {
		Results []map[string]any `json:"results"`
		Range   map[string]any   `json:"range"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
	}
	out := make([]string, len(res.Results))
	for n, r := range res.Results {
		out[n] = r["build.name"].(string) + "/" + r["build.number"].(string)
	}
	return out, res.Range
}

func TestSearchAQLBuilds(t *testing.T) {
	h, tokens := buildsAQLWorld(t)
	admin := basic("admin", "pw")
	rec := do(t, h, http.MethodPost, aqlURL+"?compact=true", admin, `builds.find({"name":"tool"})`)
	want := `{"results":[{"build.created":"2026-01-01T01:00:00.000Z","build.created_by":"ci",` +
		`"build.name":"tool","build.number":"1","build.repo":"artifactory-build-info",` +
		`"build.started":"2026-01-01T00:02:00.000Z","build.url":"https://ci/tool"}],` +
		`"range":{"start_pos":0,"end_pos":1,"total":1}}`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("builds row %d\n%s\nwant\n%s", rec.Code, rec.Body, want)
	}
	rec = do(t, h, http.MethodPost, aqlURL+"?compact=true", admin, `build.promotions.find()`)
	want = `{"results":[{"build.promotion.created":"2026-01-01T02:00:00.000Z",` +
		`"build.promotion.created_by":"admin","build.promotion.repo":"libs-release",` +
		`"build.promotion.status":"released","build.promotion.user":"jenkins"}],` +
		`"range":{"start_pos":0,"end_pos":1,"total":1}}`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("promotions row %d\n%s\nwant\n%s", rec.Code, rec.Body, want)
	}
	// The JFrog CLI's build search: the item a run produced, by the run's
	// name and number together.
	rows, _ := aqlRows(t, &itemWorld{t: t, h: h}, "admin",
		`items.find({"$or":[{"$and":[{"artifact.module.build.name":"tool",`+
			`"artifact.module.build.number":"1"}]}]}).include("repo","path","name")`)
	if !slices.Equal(rows, []string{"libs-local/./app.jar"}) {
		t.Fatalf("build artifacts %q", rows)
	}

	// Without a build-scoped token a non-admin queries items only.
	const named = `builds.find().include("name","number","repo")`
	rec = do(t, h, http.MethodPost, aqlURL, basic("dev", "pw"), named)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "build:") {
		t.Fatalf("basic auth builds query %d %s", rec.Code, rec.Body)
	}
	scoped := bearerFor(t, tokens, "build:artifactory-build-info/**:r")
	// The defaults carry name, number and repo; an include must name them.
	wantStatus(t, do(t, h, http.MethodPost, aqlURL, scoped,
		`builds.find().include("name","number")`), http.StatusBadRequest)
	// Rows are the runs dev's build permission covers: app/**.
	if rows, _ := buildRows(t, h, scoped, named); !slices.Equal(rows, []string{"app/1", "app/2"}) {
		t.Fatalf("scoped builds %q", rows)
	}
	// The token's own pattern narrows that, on the document path.
	one := bearerFor(t, tokens, "build:artifactory-build-info/app/1-*:r")
	if rows, _ := buildRows(t, h, one, named); !slices.Equal(rows, []string{"app/1"}) {
		t.Fatalf("narrowed builds %q", rows)
	}
	rec = do(t, h, http.MethodPost, aqlURL, one, `build.promotions.find()`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"released"`) {
		t.Fatalf("scoped promotions %d %s", rec.Code, rec.Body)
	}
	two := bearerFor(t, tokens, "build:artifactory-build-info/app/2-*:r")
	rec = do(t, h, http.MethodPost, aqlURL, two, `build.promotions.find()`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"released"`) {
		t.Fatalf("a promotion of an unreadable run %d %s", rec.Code, rec.Body)
	}
	// A build scope reaches no artifact.
	rows, _ = aqlRows(t, &itemWorld{t: t, h: h}, "", `items.find().include("repo","path","name")`,
		"Authorization", scoped["Authorization"])
	if len(rows) != 0 {
		t.Fatalf("a build-scoped token read items %q", rows)
	}

	// The limit applies before permission filtering; a scan factor reads
	// ahead.
	desc := named + `.sort({"$desc":["name"]}).limit(1)`
	if rows, _ := buildRows(t, h, scoped, desc); len(rows) != 0 {
		t.Fatalf("limit before filtering %q", rows)
	}
	scan := map[string]string{"Authorization": scoped["Authorization"], "scan-factor": "3"}
	if rows, _ := buildRows(t, h, scan, desc); !slices.Equal(rows, []string{"app/1"}) {
		t.Fatalf("scan factor %q", rows)
	}
}

func TestSearchAQLBuildsCap(t *testing.T) {
	h, _ := buildsAQLWorld(t, func(c *Config) { c.AQL.MaxResults = 2 })
	rows, rng := buildRows(t, h, basic("admin", "pw"), `builds.find()`)
	if len(rows) != 2 || rng["notification"] != aqlTrimmed {
		t.Fatalf("capped builds %q %v", rows, rng)
	}
	rows, rng = buildRows(t, h, basic("admin", "pw"), `builds.find().limit(2)`)
	if len(rows) != 2 || rng["notification"] != nil {
		t.Fatalf("a limit within the cap %q %v", rows, rng)
	}
}
