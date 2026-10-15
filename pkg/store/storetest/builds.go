package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runBuilds covers build-info runs (decision 0014).
func runBuilds(t *testing.T, newStore Factory) {
	t.Run("Builds", func(t *testing.T) { testBuilds(t, newStore(t)) })
	t.Run("BuildRuns", func(t *testing.T) { testBuildRuns(t, newStore(t)) })
	t.Run("BuildDeletes", func(t *testing.T) { testBuildDeletes(t, newStore(t)) })
	t.Run("BuildRename", func(t *testing.T) { testBuildRename(t, newStore(t)) })
	t.Run("BuildPromotions", func(t *testing.T) { testBuildPromotions(t, newStore(t)) })
	t.Run("BuildFiles", func(t *testing.T) { testBuildFiles(t, newStore(t)) })
	t.Run("BuildRetention", func(t *testing.T) { testBuildRetentionPolicy(t, newStore(t)) })
}

// sum returns checksums unique to n.
func sum(n int) types.Checksums {
	return types.Checksums{
		SHA1:   repeatHex(n, 40),
		SHA256: repeatHex(n, 64),
		MD5:    repeatHex(n, 32),
	}
}

// repeatHex is n's last hex digit repeated to length.
func repeatHex(n, length int) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = "0123456789abcdef"[n%16]
	}
	return string(b)
}

// fxBuild builds a run of name and number that started at minute start,
// with two modules: one producing two artifacts and consuming a
// dependency, one carrying properties only.
func fxBuild(project, name, number string, start int) types.Build {
	return types.Build{
		ProjectKey: project,
		Name:       name,
		Number:     number,
		Started:    at(start),
		URL:        "https://ci.example/" + name + "/" + number,
		Created:    at(start + 1),
		CreatedBy:  "ci",
		Properties: types.Properties{
			"buildInfo.env.BRANCH": {"main"},
			"vcs.revision":         {"abc", "abc", "123"},
			"empty":                {},
		},
		Modules: []types.BuildModule{
			{
				ID:         "com.acme:app:" + number,
				Type:       "maven",
				Properties: types.Properties{"module.flag": {"on"}},
				Artifacts: []types.BuildFile{
					{
						Name:      "app.jar",
						Type:      "jar",
						Path:      "com/acme/app/" + number + "/app.jar",
						Checksums: sum(1),
					},
					{
						Name:      "app.pom",
						Type:      "pom",
						Path:      "com/acme/app/" + number + "/app.pom",
						Checksums: sum(2),
					},
				},
				Dependencies: []types.BuildFile{
					{
						ID:        "org.lib:core:2.0",
						Name:      "core.jar",
						Type:      "jar",
						Scopes:    []string{"compile", "runtime"},
						Checksums: sum(3),
					},
				},
			},
			{ID: "docs", Type: "generic", Properties: types.Properties{"docs": {"yes"}}},
		},
		Document: []byte(`{"name":"` + name + `","number":"` + number + `"}`),
	}
}

// sameBuild fails t when got differs from what the store keeps for v.
func sameBuild(t *testing.T, what string, v, got types.Build) {
	t.Helper()
	if diff := cmp.Diff(store.NormaliseBuild(v), got, opts); diff != "" {
		t.Fatalf("%s (-want +got):\n%s", what, diff)
	}
}

func testBuilds(t *testing.T, s store.Store) {
	ctx := context.Background()
	bs := s.Builds()

	first := get[types.Build](t)(bs.Put(ctx, fxBuild("", "app", "1", 0)))
	if first.ID == 0 {
		t.Fatal("Put assigned no ID")
	}
	want := fxBuild("", "app", "1", 0)
	want.ID = first.ID
	sameBuild(t, "Put", want, first)
	if got := first.Properties["vcs.revision"]; !cmp.Equal(got, []string{"123", "abc"}) {
		t.Fatalf("property values = %v, want sorted and unique", got)
	}
	if _, ok := first.Properties["empty"]; ok {
		t.Fatal("a key without values was kept")
	}
	sameBuild(t, "Get by started", want, get[types.Build](t)(bs.Get(ctx, "", "app", "1", at(0))))
	sameBuild(t, "ByID", want, get[types.Build](t)(bs.ByID(ctx, first.ID)))

	// A second publish of the same number is a second run.
	again := get[types.Build](t)(bs.Put(ctx, fxBuild("", "app", "1", 5)))
	if again.ID == first.ID {
		t.Fatal("second run reused the first run's ID")
	}
	if got := get[types.Build](t)(bs.Get(ctx, "", "app", "1", time.Time{})); got.ID != again.ID {
		t.Fatalf("Get without started = run %d, want the latest, %d", got.ID, again.ID)
	}
	if got := get[types.Build](t)(bs.Latest(ctx, "", "app")); got.ID != again.ID {
		t.Fatalf("Latest = run %d, want %d", got.ID, again.ID)
	}

	// The same run in another project is a different run.
	other := get[types.Build](t)(bs.Put(ctx, fxBuild("proj", "app", "1", 0)))
	if got := get[types.Build](t)(bs.Latest(ctx, "proj", "app")); got.ID != other.ID {
		t.Fatalf("project Latest = run %d, want %d", got.ID, other.ID)
	}

	_, err := bs.Put(ctx, fxBuild("", "app", "1", 0))
	wantErr(t, err, store.ErrConflict)
	for name, b := range map[string]types.Build{
		"no name":    {Number: "1", Started: at(0)},
		"no number":  {Name: "app", Started: at(0)},
		"no started": {Name: "app", Number: "1"},
	} {
		_, err := bs.Put(ctx, b)
		if !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("Put %s: err = %v, want ErrInvalid", name, err)
		}
	}
	_, err = bs.Get(ctx, "", "app", "9", time.Time{})
	wantErr(t, err, store.ErrNotFound)
	_, err = bs.Get(ctx, "", "app", "1", at(3))
	wantErr(t, err, store.ErrNotFound)
	_, err = bs.ByID(ctx, 1<<40)
	wantErr(t, err, store.ErrNotFound)
	_, err = bs.Latest(ctx, "", "missing")
	wantErr(t, err, store.ErrNotFound)

	// Replace changes the content and keeps the identity.
	repl := fxBuild("other", "renamed", "99", 30)
	repl.ID = first.ID
	repl.URL = "https://ci.example/append"
	repl.Modified, repl.ModifiedBy = at(40), "appender"
	repl.Properties = types.Properties{"appended": {"true"}}
	repl.Modules = append(repl.Modules, types.BuildModule{ID: "other/7", Type: "build"})
	repl.Document = []byte(`{"appended":true}`)
	must(t, bs.Replace(ctx, repl))
	wantRepl := want
	wantRepl.URL, wantRepl.Modified, wantRepl.ModifiedBy = repl.URL, repl.Modified, repl.ModifiedBy
	wantRepl.Properties, wantRepl.Modules, wantRepl.Document = repl.Properties, repl.Modules, repl.Document
	sameBuild(t, "Replace", wantRepl, get[types.Build](t)(bs.ByID(ctx, first.ID)))
	repl.ID = 1 << 40
	wantErr(t, bs.Replace(ctx, repl), store.ErrNotFound)

	// The stored identity keeps the document item path exact.
	odd := fxBuild("", "team/app", "1 / 2", 0)
	odd.Started = time.Date(2026, 10, 15, 9, 30, 0, 123_000_000, time.UTC)
	stored := get[types.Build](t)(bs.Put(ctx, odd))
	repo, full := get[types.Build](t)(bs.ByID(ctx, stored.ID)).DocumentItem()
	if repo != "artifactory-build-info" || full != "team%2Fapp/1%20%2F%202-1792056600123.json" {
		t.Fatalf("DocumentItem = %q, %q", repo, full)
	}
}

func testBuildRuns(t *testing.T, s store.Store) {
	ctx := context.Background()
	bs := s.Builds()
	var all []types.BuildRun
	for i, number := range []string{"1", "2", "2", "3", "10"} {
		r := get[types.Build](t)(bs.Put(ctx, fxBuild("", "app", number, i)))
		all = append(all, r.Run())
	}
	// Two runs that started at the same instant order by ID.
	tie := get[types.Build](t)(bs.Put(ctx, fxBuild("", "app", "11", 4)))
	all = append(all, tie.Run())
	get[types.Build](t)(bs.Put(ctx, fxBuild("", "zeta", "1", 9)))
	get[types.Build](t)(bs.Put(ctx, fxBuild("", "beta", "1", 2)))
	get[types.Build](t)(bs.Put(ctx, fxBuild("proj", "gamma", "1", 1)))

	got := pages(t, 2, func(p store.Page) ([]types.BuildRun, string, error) {
		return bs.Runs(ctx, "", "app", p)
	})
	wantRuns := []types.BuildRun{all[5], all[4], all[3], all[2], all[1], all[0]}
	same(t, "Runs newest first", wantRuns, got)
	if runs, next, err := bs.Runs(
		ctx,
		"",
		"missing",
		store.Page{},
	); err != nil || len(runs) != 0 ||
		next != "" {
		t.Fatalf("Runs of an unknown name = %v, %q, %v", runs, next, err)
	}
	_, _, err := bs.Runs(ctx, "", "app", store.Page{After: store.Cursor("x")})
	wantErr(t, err, store.ErrInvalidCursor)

	names := pages(t, 1, func(p store.Page) ([]types.BuildName, string, error) {
		return bs.Names(ctx, "", p)
	})
	same(t, "Names", []types.BuildName{
		{Name: "app", LastStarted: at(4)},
		{Name: "beta", LastStarted: at(2)},
		{Name: "zeta", LastStarted: at(9)},
	}, names)
	project := get[[]types.BuildName](t)(func() ([]types.BuildName, error) {
		v, _, err := bs.Names(ctx, "proj", store.Page{})
		return v, err
	}())
	same(
		t,
		"project Names",
		[]types.BuildName{{ProjectKey: "proj", Name: "gamma", LastStarted: at(1)}},
		project,
	)
	_, _, err = bs.Names(ctx, "", store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)
}

func testBuildDeletes(t *testing.T, s store.Store) {
	ctx := context.Background()
	bs := s.Builds()
	var runs []types.Build
	for i, number := range []string{"1", "1", "1", "2"} {
		runs = append(runs, get[types.Build](t)(bs.Put(ctx, fxBuild("", "app", number, i))))
	}
	get[types.BuildPromotion](
		t,
	)(
		bs.AddPromotion(ctx, types.BuildPromotion{BuildID: runs[0].ID, Status: "staged"}),
	)
	must(t, bs.PutRetention(ctx, types.BuildRetention{Name: "app", Count: 5}))

	// One run per occurrence, the earliest first; an occurrence with no run
	// left is skipped.
	deleted := get[[]types.BuildRun](t)(bs.DeleteNumbers(ctx, "", "app", []string{"1", "1", "3"}))
	same(t, "DeleteNumbers", []types.BuildRun{runs[0].Run(), runs[1].Run()}, deleted)
	_, err := bs.Promotions(ctx, runs[0].ID)
	wantErr(t, err, store.ErrNotFound)
	left := get[[]types.BuildRun](t)(func() ([]types.BuildRun, error) {
		v, _, err := bs.Runs(ctx, "", "app", store.Page{})
		return v, err
	}())
	same(t, "after DeleteNumbers", []types.BuildRun{runs[3].Run(), runs[2].Run()}, left)
	if got := get[[]types.BuildRun](t)(bs.DeleteNumbers(ctx, "", "app", nil)); len(got) != 0 {
		t.Fatalf("DeleteNumbers(nil) deleted %v", got)
	}

	all := get[[]types.BuildRun](t)(bs.DeleteAll(ctx, "", "app"))
	same(t, "DeleteAll oldest first", []types.BuildRun{runs[2].Run(), runs[3].Run()}, all)
	_, err = bs.Latest(ctx, "", "app")
	wantErr(t, err, store.ErrNotFound)
	_, err = bs.Retention(ctx, "", "app")
	wantErr(t, err, store.ErrNotFound)
	if got := get[[]types.BuildRun](t)(bs.DeleteAll(ctx, "", "app")); len(got) != 0 {
		t.Fatalf("second DeleteAll deleted %v", got)
	}
}

func testBuildRename(t *testing.T, s store.Store) {
	ctx := context.Background()
	bs := s.Builds()
	a1 := get[types.Build](t)(bs.Put(ctx, fxBuild("", "a", "1", 0)))
	get[types.Build](t)(bs.Put(ctx, fxBuild("", "a", "2", 1)))
	get[types.Build](t)(bs.Put(ctx, fxBuild("", "c", "1", 2)))
	must(t, bs.PutRetention(ctx, types.BuildRetention{Name: "a", Count: 3}))
	must(t, bs.PutRetention(ctx, types.BuildRetention{Name: "b", Count: 9}))

	must(t, bs.Rename(ctx, "", "a", "b"))
	got := get[types.Build](t)(bs.ByID(ctx, a1.ID))
	if got.Name != "b" || got.Number != "1" {
		t.Fatalf("renamed run = %s/%s", got.Name, got.Number)
	}
	if _, err := bs.Latest(ctx, "", "a"); err == nil {
		t.Fatal("old name still has runs")
	}
	same(t, "moved retention", types.BuildRetention{Name: "b", Count: 3},
		get[types.BuildRetention](t)(bs.Retention(ctx, "", "b")))
	_, err := bs.Retention(ctx, "", "a")
	wantErr(t, err, store.ErrNotFound)

	// A retention left on a name without runs gives way to the renamed
	// name's, here none.
	must(t, bs.PutRetention(ctx, types.BuildRetention{Name: "orphan", Count: 1}))
	must(t, bs.Rename(ctx, "", "c", "orphan"))
	_, err = bs.Retention(ctx, "", "orphan")
	wantErr(t, err, store.ErrNotFound)

	wantErr(t, bs.Rename(ctx, "", "missing", "x"), store.ErrNotFound)
	get[types.Build](t)(bs.Put(ctx, fxBuild("", "c", "1", 2)))
	wantErr(t, bs.Rename(ctx, "", "b", "c"), store.ErrConflict)
	must(t, bs.Rename(ctx, "", "b", "b"))
	wantErr(t, bs.Rename(ctx, "proj", "b", "z"), store.ErrNotFound)
}

func testBuildPromotions(t *testing.T, s store.Store) {
	ctx := context.Background()
	bs := s.Builds()
	run := get[types.Build](t)(bs.Put(ctx, fxBuild("", "app", "1", 0)))
	late := types.BuildPromotion{
		BuildID: run.ID, Created: at(5), CreatedBy: "admin", Status: "released", Comment: "ship it",
		CIUser: "jenkins", SourceRepo: "libs-staging", Repo: "libs-release",
	}
	early := types.BuildPromotion{
		BuildID: run.ID,
		Created: at(2),
		Status:  "staged",
		Repo:    "libs-staging",
	}
	tie := types.BuildPromotion{BuildID: run.ID, Created: at(2), Status: "tested"}
	var added []types.BuildPromotion
	for _, p := range []types.BuildPromotion{late, early, tie} {
		got := get[types.BuildPromotion](t)(bs.AddPromotion(ctx, p))
		if got.ID == 0 {
			t.Fatal("AddPromotion assigned no ID")
		}
		p.ID = got.ID
		same(t, "AddPromotion", p, got)
		added = append(added, p)
	}
	same(t, "Promotions", []types.BuildPromotion{added[1], added[2], added[0]},
		get[[]types.BuildPromotion](t)(bs.Promotions(ctx, run.ID)))

	_, err := bs.AddPromotion(ctx, types.BuildPromotion{BuildID: 1 << 40})
	wantErr(t, err, store.ErrNotFound)
	_, err = bs.Promotions(ctx, 1<<40)
	wantErr(t, err, store.ErrNotFound)
}

func testBuildFiles(t *testing.T, s store.Store) {
	ctx := context.Background()
	bs := s.Builds()
	one := get[types.Build](t)(bs.Put(ctx, fxBuild("", "app", "1", 0)))
	// Another project's run produces the same jar in its second module.
	two := fxBuild("proj", "lib", "7", 3)
	two.Modules[1].Artifacts = []types.BuildFile{{Name: "copy.jar", Type: "jar", Checksums: sum(1)}}
	two = get[types.Build](t)(bs.Put(ctx, two))

	jar := one.Modules[0].Artifacts[0]
	got := get[[]types.BuildFileRef](t)(bs.FindArtifacts(ctx, types.Checksums{SHA1: sum(1).SHA1}))
	same(t, "FindArtifacts by sha1", []types.BuildFileRef{
		{Run: one.Run(), Module: one.Modules[0].ID, File: jar},
		{Run: two.Run(), Module: two.Modules[0].ID, File: two.Modules[0].Artifacts[0]},
		{Run: two.Run(), Module: "docs", File: two.Modules[1].Artifacts[0]},
	}, got)
	same(t, "FindArtifacts by every checksum", got[:2],
		get[[]types.BuildFileRef](t)(bs.FindArtifacts(ctx, sum(1)))[:2])
	if got := get[[]types.BuildFileRef](t)(bs.FindArtifacts(ctx, types.Checksums{
		SHA1: sum(1).SHA1, MD5: sum(9).MD5,
	})); len(got) != 0 {
		t.Fatalf("a mismatched md5 still matched %v", got)
	}

	dep := one.Modules[0].Dependencies[0]
	deps := get[[]types.BuildFileRef](
		t,
	)(
		bs.FindDependencies(ctx, types.Checksums{SHA256: sum(3).SHA256}),
	)
	same(t, "FindDependencies by sha256", []types.BuildFileRef{
		{Run: one.Run(), Module: one.Modules[0].ID, File: dep},
		{Run: two.Run(), Module: two.Modules[0].ID, File: two.Modules[0].Dependencies[0]},
	}, deps)
	if got := get[[]types.BuildFileRef](t)(bs.FindDependencies(ctx, sum(1))); len(got) != 0 {
		t.Fatalf("an artifact checksum matched dependencies %v", got)
	}

	_, err := bs.FindArtifacts(ctx, types.Checksums{})
	wantErr(t, err, store.ErrInvalid)
	_, err = bs.FindDependencies(ctx, types.Checksums{})
	wantErr(t, err, store.ErrInvalid)
}

func testBuildRetentionPolicy(t *testing.T, s store.Store) {
	ctx := context.Background()
	bs := s.Builds()
	r := types.BuildRetention{
		ProjectKey: "proj", Name: "app", Count: 10, MinimumBuildDate: at(-60),
		DeleteArtifacts: true, KeepNumbers: []string{"1.0", "2.0"},
	}
	must(t, bs.PutRetention(ctx, r))
	same(t, "Retention", r, get[types.BuildRetention](t)(bs.Retention(ctx, "proj", "app")))
	r.Count, r.DeleteArtifacts, r.KeepNumbers, r.MinimumBuildDate = 2, false, nil, time.Time{}
	must(t, bs.PutRetention(ctx, r))
	same(t, "replaced Retention", r, get[types.BuildRetention](t)(bs.Retention(ctx, "proj", "app")))
	_, err := bs.Retention(ctx, "", "app")
	wantErr(t, err, store.ErrNotFound)
}
