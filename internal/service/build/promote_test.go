package build

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// promotionWorld adds release-local, a federated repository, a virtual
// repository deploying to release-local, one without a default deployment
// repository, and a remote repository to newWorld's.
func promotionWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	for _, c := range []repo.Config{
		{Key: "release-local", Class: repo.Local},
		{Key: "fed", Class: repo.Federated},
		{
			Key: "release", Class: repo.Virtual, Repositories: []string{"release-local"},
			DefaultDeploymentRepo: "release-local",
		},
		{Key: "no-deploy", Class: repo.Virtual, Repositories: []string{"release-local"}},
		{Key: "up", Class: repo.Remote, URL: "https://up.example"},
	} {
		if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// put deploys content at path in a repository, or its cache.
func (w *world) put(key string, cached bool, path, content string, props types.Properties) {
	w.t.Helper()
	ctx := context.Background()
	c, err := w.db.Repos().Get(ctx, key)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := (&item.Service{Store: w.db, Blobs: w.blobs}).Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Cached: cached, Path: path},
		Body:   strings.NewReader(content), Properties: props,
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) props(key, path string) types.Properties {
	w.t.Helper()
	ctx := context.Background()
	it, err := w.db.Items().Get(ctx, key, false, path)
	if err != nil {
		w.t.Fatal(err)
	}
	p, err := w.db.Items().Properties(ctx, it.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) promotions(name, number string) []types.BuildPromotion {
	w.t.Helper()
	ctx := context.Background()
	b, err := w.db.Builds().Get(ctx, "", name, number, time.Time{})
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := w.db.Builds().Promotions(ctx, b.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// buildProps are the properties the JFrog CLI gives a build's upload.
func buildProps(name, number string) types.Properties {
	return types.Properties{"build.name": {name}, "build.number": {number}}
}

// fileModule is a module with artifacts and dependencies, each "name" or
// "name@scope" of content of the same name.
func fileModule(artifacts, dependencies []string) string {
	entry := func(s string) string {
		name, scope, _ := strings.Cut(s, "@")
		scopes := ""
		if scope != "" {
			scopes = fmt.Sprintf(`,"scopes":[%q]`, scope)
		}
		return fmt.Sprintf(`{"id":%q,"name":%q,"sha1":%q%s}`, name, name, sha1Hex(name), scopes)
	}
	var a, d []string
	for _, s := range artifacts {
		a = append(a, entry(s))
	}
	for _, s := range dependencies {
		d = append(d, entry(s))
	}
	return fmt.Sprintf(`{"id":"m","artifacts":[%s],"dependencies":[%s]}`,
		strings.Join(a, ","), strings.Join(d, ","))
}

func texts(msgs []item.Message) string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Level+" "+m.Text)
	}
	return strings.Join(out, "\n")
}

// TestPromoteMove moves a run's artifact, found by its build properties,
// with the dependency in the scope asked for, and records the promotion.
func TestPromoteMove(t *testing.T) {
	w := promotionWorld(t)
	ctx := context.Background()
	w.put("libs-local", false, "app/a.bin", "a.bin", buildProps("app", "1"))
	// The same content without the run's properties is not its artifact,
	// nor is it outside the source repository.
	w.put("libs-local", false, "elsewhere/a.bin", "a.bin", nil)
	w.put("fed", false, "app/a.bin", "a.bin", buildProps("app", "1"))
	w.put("libs-local", false, "deps/d.jar", "d.jar", nil)
	w.put("libs-local", false, "deps/t.jar", "t.jar", nil)
	w.publish(doc("app", "1", started0,
		fileModule([]string{"a.bin"}, []string{"d.jar@compile", "t.jar@test"})))
	when := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)

	res, err := w.svc.Promote(ctx, Promotion{
		Name: "app", Number: "1", Status: "released", Comment: "ok", Timestamp: when,
		SourceRepo: "libs-local", TargetRepo: "release", Artifacts: true, Dependencies: true,
		Scopes: []string{"compile"}, FailFast: true, By: "admin",
	}, everyone)
	if err != nil {
		t.Fatal(err)
	}
	if res.Aborted || !strings.Contains(texts(res.Messages),
		"INFO moving libs-local:app/a.bin to release-local:app/a.bin completed successfully") {
		t.Errorf("report:\n%s", texts(res.Messages))
	}
	for path, want := range map[string]bool{
		"app/a.bin": false, "elsewhere/a.bin": true, "deps/d.jar": false, "deps/t.jar": true,
	} {
		if w.has("libs-local", path) != want {
			t.Errorf("libs-local/%s present: %v, want %v", path, !want, want)
		}
	}
	if !w.has("release-local", "app/a.bin") || !w.has("release-local", "deps/d.jar") ||
		!w.has("fed", "app/a.bin") {
		t.Error("the files did not land in release-local, or fed's moved")
	}
	got := w.promotions("app", "1")
	if len(got) != 1 {
		t.Fatalf("promotions %+v", got)
	}
	p := got[0]
	if p.Status != "released" || p.Comment != "ok" || p.CIUser != "admin" ||
		p.CreatedBy != "admin" || p.Repo != "release-local" || !p.Created.Equal(when) {
		t.Errorf("promotion %+v", p)
	}
}

// TestPromoteCopyProperties copies, sets the properties on the copies
// only, and records the CI user named.
func TestPromoteCopyProperties(t *testing.T) {
	w := promotionWorld(t)
	ctx := context.Background()
	w.put("libs-local", false, "app/a.bin", "a.bin", buildProps("app", "1"))
	w.publish(doc("app", "1", started0, fileModule([]string{"a.bin"}, nil)))
	_, err := w.svc.Promote(ctx, Promotion{
		Name: "app", Number: "1", Status: "staged", CIUser: "jenkins", TargetRepo: "fed",
		Copy: true, Artifacts: true, Properties: types.Properties{"stage": {"qa", "prod"}},
		FailFast: true, By: "admin",
	}, everyone)
	if err != nil {
		t.Fatal(err)
	}
	if !w.has("libs-local", "app/a.bin") {
		t.Error("a copy removed its source")
	}
	if got := w.props("fed", "app/a.bin"); !slices.Equal(got["stage"], []string{"prod", "qa"}) ||
		!got.Has("build.name", "app") {
		t.Errorf("copy properties %v", got)
	}
	if got := w.props("libs-local", "app/a.bin"); got.Has("stage", "qa") {
		t.Errorf("source properties %v", got)
	}
	if got := w.promotions("app", "1"); len(got) != 1 || got[0].CIUser != "jenkins" {
		t.Errorf("promotions %+v", got)
	}
}

// TestPromoteWithoutTarget changes only properties and status, and a
// promotion without a status records nothing.
func TestPromoteWithoutTarget(t *testing.T) {
	w := promotionWorld(t)
	ctx := context.Background()
	w.put("libs-local", false, "app/a.bin", "a.bin", buildProps("app", "1"))
	w.publish(doc("app", "1", started0, fileModule([]string{"a.bin"}, nil)))
	res, err := w.svc.Promote(ctx, Promotion{
		Name: "app", Number: "1", Artifacts: true,
		Properties: types.Properties{"stage": {"qa"}}, FailFast: true, By: "admin",
	}, everyone)
	if err != nil {
		t.Fatal(err)
	}
	want := "INFO Skipping build item relocation: no target repository selected.\n" +
		"INFO Skipping promotion status update: no status received."
	if got := texts(res.Messages); got != want {
		t.Errorf("report:\n%s\nwant\n%s", got, want)
	}
	if !w.props("libs-local", "app/a.bin").Has("stage", "qa") {
		t.Error("the properties were not set where the file is")
	}
	if got := w.promotions("app", "1"); len(got) != 0 {
		t.Errorf("promotions %+v", got)
	}
	if _, err := w.svc.Promote(ctx, Promotion{
		Name: "app", Number: "1", Status: "seen", By: "admin",
	}, everyone); err != nil {
		t.Fatal(err)
	}
	if got := w.promotions("app", "1"); len(got) != 1 || got[0].Repo != "" {
		t.Errorf("promotions %+v", got)
	}
}

// TestPromoteDryRun reports what a promotion would do and does nothing.
func TestPromoteDryRun(t *testing.T) {
	w := promotionWorld(t)
	ctx := context.Background()
	w.put("libs-local", false, "app/a.bin", "a.bin", buildProps("app", "1"))
	w.publish(doc("app", "1", started0, fileModule([]string{"a.bin"}, nil)))
	res, err := w.svc.Promote(ctx, Promotion{
		Name: "app", Number: "1", Status: "released", TargetRepo: "release-local",
		Artifacts: true, Properties: types.Properties{"k": {"v"}}, DryRun: true,
		FailFast: true, By: "admin",
	}, everyone)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(texts(res.Messages), "INFO Dry run for moving libs-local:app/a.bin") {
		t.Errorf("report:\n%s", texts(res.Messages))
	}
	if !w.has("libs-local", "app/a.bin") || w.has("release-local", "app/a.bin") ||
		w.props("libs-local", "app/a.bin").Has("k", "v") || len(w.promotions("app", "1")) != 0 {
		t.Error("a dry run changed something")
	}
}

// TestPromoteFailFast rolls a promotion that meets an error back whole and
// keeps only the lines about what went wrong; without fail-fast the rest
// goes ahead and the status is recorded.
func TestPromoteFailFast(t *testing.T) {
	setup := func(t *testing.T) *world {
		w := promotionWorld(t)
		w.put("libs-local", false, "app/a.bin", "a.bin", buildProps("app", "1"))
		// b.bin's content is nowhere with the run's properties.
		w.put("libs-local", false, "app/b.bin", "b.bin", nil)
		w.publish(doc("app", "1", started0,
			fileModule([]string{"a.bin", "b.bin"}, []string{"gone.jar"})))
		return w
	}
	ctx := context.Background()
	promote := func(w *world, failFast bool) PromotionResult {
		res, err := w.svc.Promote(ctx, Promotion{
			Name: "app", Number: "1", Status: "released", TargetRepo: "release-local",
			Artifacts: true, Dependencies: true, FailFast: failFast, By: "admin",
		}, everyone)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	w := setup(t)
	res := promote(w, true)
	want := "ERROR Unable to find artifact 'b.bin' of build 'app' #1\n" +
		"INFO Promotion aborted: nothing was moved, copied or changed."
	if got := texts(res.Messages); !res.Aborted || got != want {
		t.Errorf("aborted %v, report:\n%s\nwant\n%s", res.Aborted, got, want)
	}

	w = setup(t)
	res = promote(w, false)
	got := texts(res.Messages)
	if res.Aborted || !strings.Contains(got, "ERROR Unable to find artifact 'b.bin'") ||
		!strings.Contains(got, "ERROR Unable to find dependency 'gone.jar'") ||
		!w.has("release-local", "app/a.bin") || len(w.promotions("app", "1")) != 1 {
		t.Errorf("aborted %v, report:\n%s", res.Aborted, got)
	}

	// A missing dependency aborts as a missing artifact does.
	w = promotionWorld(t)
	w.publish(doc("app", "1", started0, fileModule(nil, []string{"gone.jar"})))
	res = promote(w, true)
	if !res.Aborted || !strings.Contains(texts(res.Messages), "dependency 'gone.jar'") {
		t.Errorf("aborted %v, report:\n%s", res.Aborted, texts(res.Messages))
	}

	// An error after a file moved undoes the move.
	w = promotionWorld(t)
	w.put("libs-local", false, "app/a.bin", "a.bin", buildProps("app", "1"))
	w.put("libs-local", false, "app/c.bin", "c.bin", buildProps("app", "1"))
	w.put("release-local", false, "app/c.bin", "taken", nil)
	w.publish(doc("app", "1", started0, fileModule([]string{"a.bin", "c.bin"}, nil)))
	res, err := w.svc.Promote(ctx, Promotion{
		Name: "app", Number: "1", TargetRepo: "release-local", Artifacts: true, FailFast: true,
		By: "deployer",
	}, perms{item: func(c repo.Config, _ string, want types.Actions) bool {
		return c.Key == "libs-local" || want != types.ActionDelete
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Aborted || !w.has("libs-local", "app/a.bin") || w.has("release-local", "app/a.bin") {
		t.Errorf("aborted %v, report:\n%s", res.Aborted, texts(res.Messages))
	}
	if got := texts(res.Messages); !strings.Contains(got, "no permission to overwrite") ||
		strings.Contains(got, "completed successfully") {
		t.Errorf("report:\n%s", got)
	}
}

// TestPromoteRefusals reports the files a caller may not promote, one by
// one, and refuses a caller without deploy on the build outright.
func TestPromoteRefusals(t *testing.T) {
	w := promotionWorld(t)
	ctx := context.Background()
	for _, n := range []string{"r", "d", "w", "x"} {
		w.put("libs-local", false, n+"/"+n+".bin", n+".bin", buildProps("app", "1"))
	}
	w.put("up", true, "c/c.bin", "c.bin", buildProps("app", "1"))
	w.put("release-local", false, "in/in.bin", "in.bin", buildProps("app", "1"))
	// A file where x.bin's folder would go.
	w.put("release-local", false, "x", "file", nil)
	w.publish(doc("app", "1", started0,
		fileModule([]string{"r.bin", "d.bin", "w.bin", "c.bin", "in.bin", "x.bin"}, nil)))
	a := perms{item: func(c repo.Config, path string, want types.Actions) bool {
		switch {
		case strings.HasPrefix(path, "r/"):
			return want != types.ActionRead
		case strings.HasPrefix(path, "d/"):
			return want != types.ActionDelete
		case strings.HasPrefix(path, "w/") && c.Key == "release-local":
			return want != types.ActionWrite
		case strings.HasPrefix(path, "in/"):
			return want != types.ActionAnnotate
		}
		return true
	}}
	res, err := w.svc.Promote(ctx, Promotion{
		Name: "app", Number: "1", TargetRepo: "release-local", Artifacts: true,
		Properties: types.Properties{"k": {"v"}}, By: "deployer",
	}, a)
	if err != nil {
		t.Fatal(err)
	}
	got := texts(res.Messages)
	for _, want := range []string{
		"ERROR Cannot move libs-local:r/r.bin: no permission to move it",
		"ERROR Cannot move libs-local:d/d.bin: no permission to move it",
		"ERROR Cannot move libs-local:w/w.bin: no permission to deploy to release-local:w/w.bin",
		"ERROR Cannot move up-cache:c/c.bin: files cannot be moved out of a remote repository's cache",
		"INFO release-local:in/in.bin is already in release-local",
		"ERROR Cannot move libs-local:x/x.bin: the target x/x.bin is below the file x",
		"WARNING User doesn't have permissions to annotate 'release-local:in/in.bin'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	if res.Aborted {
		t.Error("a promotion without fail-fast aborted")
	}

	if _, err := w.svc.Promote(ctx, Promotion{Name: "app", Number: "1"},
		only(types.ActionRead)); !errors.Is(err, ErrForbidden) {
		t.Errorf("without deploy on the build: %v", err)
	}
	// A warning aborts a fail-fast promotion as an error does.
	w.put("release-local", false, "in/solo.bin", "solo.bin", buildProps("solo", "1"))
	w.publish(doc("solo", "1", started0, fileModule([]string{"solo.bin"}, nil)))
	res, err = w.svc.Promote(ctx, Promotion{
		Name: "solo", Number: "1", Properties: types.Properties{"k": {"v"}}, Artifacts: true,
		SourceRepo: "release-local", FailFast: true, By: "deployer",
	}, a)
	if err != nil || !res.Aborted || !strings.Contains(texts(res.Messages), "WARNING") {
		t.Errorf("%v: aborted %v, report:\n%s", err, res.Aborted, texts(res.Messages))
	}
}

// TestPromoteCopiesFromCache copies a dependency out of a remote
// repository's cache, which a move may not do.
func TestPromoteCopiesFromCache(t *testing.T) {
	w := promotionWorld(t)
	w.put("up", true, "lib/d.jar", "d.jar", nil)
	w.publish(doc("app", "1", started0, fileModule(nil, []string{"d.jar"})))
	res, err := w.svc.Promote(context.Background(), Promotion{
		Name: "app", Number: "1", TargetRepo: "release-local", Copy: true, Dependencies: true,
		Properties: types.Properties{"k": {"v"}}, FailFast: true, By: "admin",
	}, everyone)
	if err != nil || res.Aborted {
		t.Fatalf("%v: report:\n%s", err, texts(res.Messages))
	}
	if !w.props("release-local", "lib/d.jar").Has("k", "v") {
		t.Error("the copy out of the cache is missing or untagged")
	}
}

// TestPromoteTargets resolves a virtual target to its default deployment
// repository and refuses targets that take no files.
func TestPromoteTargets(t *testing.T) {
	w := promotionWorld(t)
	ctx := context.Background()
	w.publish(doc("app", "1", started0))
	for target, want := range map[string]error{
		"absent": ErrNoTarget, "no-deploy": ErrInvalid, "up": ErrInvalid,
	} {
		if _, err := w.svc.Promote(ctx, Promotion{
			Name: "app", Number: "1", TargetRepo: target,
		}, everyone); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", target, err, want)
		}
	}
	if _, err := w.svc.Promote(ctx, Promotion{Name: "app", Number: "2"}, everyone); !errors.Is(
		err, ErrNotFound) {
		t.Errorf("unknown number: %v", err)
	}
}
