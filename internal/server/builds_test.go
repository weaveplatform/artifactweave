package server

import (
	"context"
	"crypto/md5" //nolint:gosec // wire checksum
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/build"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const vendorJSON = "application/vnd.org.jfrog.artifactory+json"

func md5Hex(s string) string {
	b := md5.Sum([]byte(s)) //nolint:gosec // wire checksum
	return hex.EncodeToString(b[:])
}

// startedAt is testStarted moved on by n seconds.
func startedAt(n int) string {
	t, err := build.ParseStarted(testStarted)
	if err != nil {
		panic(err)
	}
	return build.FormatStarted(t.Add(time.Duration(n) * time.Second))
}

// artifactDoc is a build-info document whose one module produced content,
// with fields the spec does not list, which must survive.
func artifactDoc(name, number, started, content string) string {
	return fmt.Sprintf(`{"name":%q,"number":%q,"started":%q,`+
		`"agent":{"name":"jfrog-cli-go","version":"2.80.0"},"vcs":[{"revision":"abc"}],`+
		`"properties":{"buildInfo.env.CI":"true","count":3},`+
		`"modules":[{"id":"m1","type":"generic","artifacts":[{"name":"a.bin","path":"app/a.bin",`+
		`"sha1":%q,"sha256":%q,"md5":%q,"localPath":"/tmp/a.bin"}]}]}`,
		name, number, started, sha1Hex(content), sha256Hex(content), md5Hex(content))
}

// documentPath is where a run's document item is in artifactory-build-info.
func documentPath(name, number, started string) string {
	t, err := build.ParseStarted(started)
	if err != nil {
		panic(err)
	}
	_, p := types.Build{Name: name, Number: number, Started: t}.DocumentItem()
	return p
}

func (w *itemWorld) publish(doc string) {
	w.t.Helper()
	wantStatus(w.t, w.do(http.MethodPut, buildsPath, "admin", doc, "Content-Type", vendorJSON), 204)
}

func (w *itemWorld) hasItem(key, p string) bool {
	w.t.Helper()
	_, err := w.db.Items().Get(context.Background(), key, false, p)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		w.t.Fatal(err)
	}
	return err == nil
}

// runsOf returns the numbers and started timestamps GET /api/build/{name}
// lists.
func (w *itemWorld) runsOf(escaped string) []string {
	w.t.Helper()
	rec := w.do(http.MethodGet, buildsPath+"/"+escaped, "admin", "")
	wantStatus(w.t, rec, 200)
	var body struct {
		BuildsNumbers []struct{ URI, Started string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, r := range body.BuildsNumbers {
		out = append(out, r.URI+"@"+r.Started)
	}
	return out
}

// TestAccBuildPublishAndRead publishes two runs of one number, as a CI system
// re-running a build does, and reads them back.
func TestAccBuildPublishAndRead(t *testing.T) {
	w := newItemWorld(t, false)
	wantStatus(t, w.do(
		http.MethodPut,
		"/artifactory/libs-local/app/a.bin;build.name=team%2Fapp;build.number=1",
		"admin",
		"artifact",
	), 201)
	wantStatus(
		t,
		w.do(http.MethodPut, "/artifactory/other-local/copy/a.bin", "admin", "artifact"),
		201,
	)

	first := artifactDoc("team/app", "1", testStarted, "artifact")
	second := artifactDoc("team/app", "1", startedAt(60), "artifact")
	rec := w.do(http.MethodPut, buildsPath, "admin", first, "Content-Type", vendorJSON)
	wantStatus(t, rec, 204)
	if got := rec.Header().Get("X-Checksum-Sha256"); got != sha256Hex(first) {
		t.Errorf("X-Checksum-Sha256 %q, want the document's %q", got, sha256Hex(first))
	}
	w.publish(second)

	// Each run's document is an item at the CLI's encoded path.
	for doc, started := range map[string]string{first: testStarted, second: startedAt(60)} {
		p := documentPath("team/app", "1", started)
		if !strings.HasPrefix(p, "team%2Fapp/1-") {
			t.Fatalf("document path %s", p)
		}
		if it := w.item(types.BuildInfoRepository, false, p); it.Actual.SHA256 != sha256Hex(doc) {
			t.Errorf("item %s holds %s, want the document", p, it.Actual.SHA256)
		}
	}

	rec = w.do(http.MethodGet, buildsPath, "admin", "")
	wantStatus(t, rec, 200)
	want := `{"builds":[{"lastStarted":"` + startedAt(60) + `","uri":"/team/app"}],` +
		`"uri":"https://reg.example/artifactory/api/build"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("all builds %s, want %s", got, want)
	}

	runs := w.runsOf("team%2Fapp")
	if want := []string{
		"/1@" + startedAt(60),
		"/1@" + testStarted,
	}; fmt.Sprint(
		runs,
	) != fmt.Sprint(
		want,
	) {
		t.Errorf("runs %v, want %v", runs, want)
	}

	uri := `{"uri":"https://reg.example/artifactory/api/build/team%2Fapp/1","buildInfo":`
	rec = w.do(http.MethodGet, buildsPath+"/team%2Fapp/1", "admin", "")
	wantStatus(t, rec, 200)
	if got := rec.Body.String(); got != uri+second+"}" {
		t.Errorf("latest run %s, want the second document as published", got)
	}
	rec = w.do(
		http.MethodGet,
		buildsPath+"/team%2Fapp/1?started="+strings.ReplaceAll(
			testStarted,
			"+",
			"%2B",
		),
		"admin",
		"",
	)
	wantStatus(t, rec, 200)
	if got := rec.Body.String(); got != uri+first+"}" {
		t.Errorf("first run %s, want the first document", got)
	}
	// An unencoded + in the query arrives as a space.
	wantStatus(
		t,
		w.do(http.MethodGet, buildsPath+"/team%2Fapp/1?started="+testStarted, "admin", ""),
		200,
	)

	rec = w.do(http.MethodGet,
		"/artifactory/api/builds/buildArtifacts/team%2Fapp/1/artifactory-build-info", "admin", "")
	wantStatus(t, rec, 200)
	want = `[{"name":"a.bin","path":"app","repo":"libs-local"},` +
		`{"name":"a.bin","path":"copy","repo":"other-local"}]`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("build artifacts %s, want %s", got, want)
	}
	// reader reads every artifact but holds no build permission.
	wantStatus(t, w.do(http.MethodGet, buildsPath+"/team%2Fapp", "reader", ""), 403)
}

// TestAccBuildOverwrite is jf rt bp --overwrite: it lists the runs, sends a
// number once per run of it, then publishes again.
func TestAccBuildOverwrite(t *testing.T) {
	w := newItemWorld(t, false)
	for i, number := range []string{"1", "1", "2", "1"} {
		w.publish(buildDoc("app", number, startedAt(i)))
	}
	wantStatus(t, w.do(http.MethodPost, buildsPath+"/delete", "admin",
		`{"buildName":"app","buildNumbers":["1"]}`), 200)
	// One occurrence deletes the earliest run of the number.
	want := []string{"/1@" + startedAt(3), "/2@" + startedAt(2), "/1@" + startedAt(1)}
	if runs := w.runsOf("app"); fmt.Sprint(runs) != fmt.Sprint(want) {
		t.Errorf("runs %v, want %v", runs, want)
	}
	if w.hasItem(types.BuildInfoRepository, documentPath("app", "1", startedAt(0))) {
		t.Error("the deleted run's document item is still there")
	}

	rec := w.do(http.MethodPost, buildsPath+"/delete", "admin",
		`{"buildName":"app","buildNumbers":["1","1"],"project":"default"}`)
	wantStatus(t, rec, 200)
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("Content-Type %q", ct)
	}
	// Artifactory's text (deletebuilds), one entry per deleted run.
	if got := rec.Body.String(); got !=
		"The following builds has been deleted successfully: 'app#1', 'app#1'." {
		t.Errorf("message %q", got)
	}
	if runs := w.runsOf("app"); fmt.Sprint(runs) != fmt.Sprint([]string{"/2@" + startedAt(2)}) {
		t.Errorf("runs %v, want run 2 only", runs)
	}
	w.publish(buildDoc("app", "1", startedAt(9)))

	rec = w.do(http.MethodDelete, buildsPath+"/app?deleteAll=1", "admin", "")
	wantStatus(t, rec, 200)
	if got := rec.Body.String(); got !=
		"The following builds has been deleted successfully: 'app#2', 'app#1'." {
		t.Errorf("message %q", got)
	}
	wantStatus(t, w.do(http.MethodGet, buildsPath+"/app", "admin", ""), 404)
	if w.hasItem(types.BuildInfoRepository, "app") {
		t.Error("the build's folder outlived its last document")
	}
}

// TestAccBuildDuplicateEntries answers 400 to a build-info publish whose
// module lists one artifact twice, as Artifactory 7.x does by default, and
// accepts it when build.block_duplicate_entries is false.
func TestAccBuildDuplicateEntries(t *testing.T) {
	a := fmt.Sprintf(`{"name":"a.bin","path":"app/a.bin","sha1":%q}`, sha1Hex("artifact"))
	doc := fmt.Sprintf(`{"name":"app","number":"1","started":%q,`+
		`"modules":[{"id":"m1","artifacts":[%s,%s]}]}`, testStarted, a, a)

	w := newItemWorld(t, false)
	rec := w.do(http.MethodPut, buildsPath, "admin", doc, "Content-Type", vendorJSON)
	wantStatus(t, rec, 400)
	if !strings.Contains(rec.Body.String(),
		`"message":"the build-info module 'm1' lists the artifact 'a.bin' more than once"`) {
		t.Errorf("body %s", rec.Body)
	}
	wantStatus(t, w.do(http.MethodGet, buildsPath+"/app", "admin", ""), 404)

	w = newItemWorld(t, false, func(c *Config) { c.AllowDuplicateBuildEntries = true })
	w.publish(doc)
}

// TestAccBuildAppend adds modules to a run's document and its item.
func TestAccBuildAppend(t *testing.T) {
	w := newItemWorld(t, false)
	w.publish(artifactDoc("app", "1", testStarted, "x"))
	w.publish(buildDoc("app", "1", startedAt(5)))

	wantStatus(t, w.do(http.MethodPost,
		buildsPath+"/append/app/1?buildRepo=artifactory-build-info&started="+
			strings.ReplaceAll(testStarted, "+", "%2B"),
		"admin", `[{"id":"m2","type":"generic","extra":"kept"}]`), 204)

	rec := w.do(
		http.MethodGet,
		buildsPath+"/app/1?started="+strings.ReplaceAll(testStarted, "+", "%2B"),
		"admin",
		"",
	)
	wantStatus(t, rec, 200)
	var got struct {
		BuildInfo json.RawMessage `json:"buildInfo"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	doc := string(got.BuildInfo)
	if !strings.HasPrefix(doc, `{"name":"app",`) ||
		!strings.Contains(doc, `"localPath":"/tmp/a.bin"}]},`+
			`{"id":"m2","type":"generic","extra":"kept"}]`) {
		t.Errorf("appended document %s", doc)
	}
	it := w.item(types.BuildInfoRepository, false, documentPath("app", "1", testStarted))
	if it.Actual.SHA256 != sha256Hex(doc) {
		t.Error("the document item was not rewritten")
	}
	// The latest run is untouched.
	rec = w.do(http.MethodGet, buildsPath+"/app/1", "admin", "")
	if !strings.Contains(rec.Body.String(), `"buildInfo":`+buildDoc("app", "1", startedAt(5))) {
		t.Errorf("latest run changed: %s", rec.Body)
	}
}

// TestAccBuildRename moves the runs, their documents' name and their items.
func TestAccBuildRename(t *testing.T) {
	w := newItemWorld(t, false)
	w.publish(buildDoc("old", "1", testStarted))
	w.publish(buildDoc("old", "2", startedAt(1)))

	rec := w.do(http.MethodPost, buildsPath+"/rename/old?to=team%2Fnew", "admin", "")
	wantStatus(t, rec, 200)
	if got := rec.Body.String(); got != "Build renaming of 'old' to 'team/new' was successfully started." {
		t.Errorf("message %q", got)
	}
	wantStatus(t, w.do(http.MethodGet, buildsPath+"/old", "admin", ""), 404)
	rec = w.do(http.MethodGet, buildsPath+"/team%2Fnew/2", "admin", "")
	wantStatus(t, rec, 200)
	moved := `{"name":"team/new","number":"2","started":"` + startedAt(1) + `"}`
	if !strings.Contains(rec.Body.String(), `"buildInfo":`+moved) {
		t.Errorf("renamed run %s", rec.Body)
	}
	if !w.hasItem(types.BuildInfoRepository, documentPath("team/new", "2", startedAt(1))) ||
		w.hasItem(types.BuildInfoRepository, "old") {
		t.Error("the document items did not move")
	}
	// A rename to the same name changes nothing.
	wantStatus(
		t,
		w.do(http.MethodPost, buildsPath+"/rename/team%2Fnew?to=team%2Fnew", "admin", ""),
		200,
	)
	if runs := w.runsOf("team%2Fnew"); len(runs) != 2 {
		t.Errorf("runs %v", runs)
	}
}

// TestAccBuildDeleteArtifacts deletes, with a run, the items of its artifacts
// that carry its build properties, and no other copy.
func TestAccBuildDeleteArtifacts(t *testing.T) {
	w := newItemWorld(t, false)
	wantStatus(t, w.do(
		http.MethodPut,
		"/artifactory/libs-local/app/a.bin;build.name=app;build.number=1",
		"admin",
		"artifact",
	), 201)
	wantStatus(
		t,
		w.do(http.MethodPut, "/artifactory/other-local/copy/a.bin", "admin", "artifact"),
		201,
	)
	w.publish(artifactDoc("app", "1", testStarted, "artifact"))
	w.publish(artifactDoc("app", "2", startedAt(1), "artifact"))

	wantStatus(
		t,
		w.do(http.MethodDelete, buildsPath+"/app?buildNumbers=1&artifacts=1", "admin", ""),
		200,
	)
	if w.hasItem("libs-local", "app/a.bin") {
		t.Error("the run's artifact is still there")
	}
	if !w.hasItem("other-local", "copy/a.bin") {
		t.Error("a copy without the build's properties was deleted")
	}
	wantStatus(t, w.do(http.MethodPost, buildsPath+"/delete", "admin",
		`{"buildName":"app","buildNumbers":["2"],"deleteArtifacts":true}`), 200)
	if !w.hasItem("other-local", "copy/a.bin") {
		t.Error("a copy without the build's properties was deleted")
	}
}

// TestAccBuildPermissions holds the build resource to decision 0014's table
// on a pattern of build names.
func TestAccBuildPermissions(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	grant := func(name, user string, a types.Actions, include string) {
		t.Helper()
		if err := w.db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{types.ResourceBuild: {
				Actions: types.Grants{Users: map[string]types.Actions{user: a}},
				Targets: map[string]types.Target{
					types.BuildInfoRepository: {Include: []string{include}},
				},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	grant("deploy-app", "deployer", types.ActionRead|types.ActionWrite, "app/**")
	grant("delete-builds", "user", types.ActionRead|types.ActionDelete, "**")
	put := func(as, doc string) int {
		return w.do(http.MethodPut, buildsPath, as, doc, "Content-Type", "application/json").Code
	}

	if got := put("deployer", buildDoc("app", "1", testStarted)); got != 204 {
		t.Errorf("deploy: %d", got)
	}
	// Replacing a run deletes its document: deploy alone is not enough.
	if got := put("deployer", buildDoc("app", "1", testStarted)); got != 403 {
		t.Errorf("replace without delete: %d", got)
	}
	if got := put("deployer", buildDoc("secret", "1", testStarted)); got != 403 {
		t.Errorf("deploy outside the pattern: %d", got)
	}
	w.publish(buildDoc("secret", "1", testStarted))

	rec := w.do(http.MethodGet, buildsPath, "deployer", "")
	wantStatus(t, rec, 200)
	if strings.Contains(rec.Body.String(), "secret") ||
		!strings.Contains(rec.Body.String(), `"/app"`) {
		t.Errorf("deployer lists %s", rec.Body)
	}
	wantStatus(t, w.do(http.MethodGet, buildsPath+"/secret/1", "deployer", ""), 403)
	wantStatus(t, w.do(http.MethodPost, buildsPath+"/rename/app?to=app2", "deployer", ""), 403)
	wantStatus(t, w.do(http.MethodPost, buildsPath+"/append/app/1", "deployer", "[]"), 403)

	// user may delete builds but not the artifacts they name: the whole
	// delete is refused and the run stays.
	wantStatus(t, w.do(
		http.MethodPut,
		"/artifactory/libs-local/app/a.bin;build.name=app;build.number=2",
		"admin",
		"artifact",
	), 201)
	w.publish(artifactDoc("app", "2", startedAt(1), "artifact"))
	wantStatus(
		t,
		w.do(http.MethodDelete, buildsPath+"/app?buildNumbers=2&artifacts=1", "user", ""),
		403,
	)
	if runs := w.runsOf("app"); len(runs) != 2 {
		t.Errorf("runs after a refused delete: %v", runs)
	}
	wantStatus(t, w.do(http.MethodDelete, buildsPath+"/app?buildNumbers=2", "user", ""), 200)
}

// TestAccBuildInfoWithoutDocument answers a run stored without its document,
// as the store allows, with an empty one.
func TestAccBuildInfoWithoutDocument(t *testing.T) {
	w := newItemWorld(t, false)
	started, err := build.ParseStarted(testStarted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Builds().Put(context.Background(), types.Build{
		Name: "bare", Number: "1", Started: started,
	}); err != nil {
		t.Fatal(err)
	}
	rec := w.do(http.MethodGet, buildsPath+"/bare/1", "admin", "")
	wantStatus(t, rec, 200)
	if !strings.HasSuffix(rec.Body.String(), `"buildInfo":{}}`) {
		t.Errorf("body %s", rec.Body)
	}
}
