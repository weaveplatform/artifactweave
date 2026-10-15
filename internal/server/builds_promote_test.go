package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

const promotionType = "application/vnd.org.jfrog.artifactory.build.PromotionRequest+json"

// promotionReport reads a promotion's messages as "LEVEL text" lines.
func promotionLines(t *testing.T, body []byte) []string {
	t.Helper()
	var r struct {
		Messages []struct{ Level, Message string }
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range r.Messages {
		out = append(out, m.Level+" "+m.Message)
	}
	return out
}

// TestBuildPromote is jf rt bpr's request: the vendor media type, a copy
// with properties, then a move into a virtual repository's default
// deployment repository, recorded where AQL finds it.
func TestBuildPromote(t *testing.T) {
	w := newItemWorld(t, false)
	wantStatus(t, w.do(
		http.MethodPut,
		"/artifactory/libs-local/app/a.bin;build.name=app;build.number=1",
		"admin",
		"artifact",
	), 201)
	w.publish(artifactDoc("app", "1", testStarted, "artifact"))

	rec := w.do(http.MethodPost, buildsPath+"/promote/app/1", "admin",
		`{"targetRepo":"other-local","copy":true,"dependencies":false,"failFast":true,`+
			`"dryRun":false,"properties":{"stage":["qa","prod"],"n":3,"ok":true,"none":null}}`,
		"Content-Type", promotionType)
	wantStatus(t, rec, 200)
	lines := promotionLines(t, rec.Body.Bytes())
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "INFO copying libs-local:app/a.bin") ||
		lines[1] != "INFO Skipping promotion status update: no status received." {
		t.Errorf("report %q", lines)
	}
	props := w.props("other-local", "app/a.bin")
	if !props.Has("stage", "prod") || !props.Has("n", "3") || !props.Has("ok", "true") ||
		props.Has("none", "") {
		t.Errorf("copy properties %v", props)
	}

	rec = w.do(http.MethodPost, buildsPath+"/promote/app/1?project=default", "admin",
		`{"status":"released","ciUser":"jenkins","timestamp":"2026-10-16T08:00:00.000+0000",`+
			`"sourceRepo":"other-local","targetRepo":"virt","properties":{"stage":"live"}}`,
		"Content-Type", "application/json")
	wantStatus(t, rec, 200)
	// virt deploys to libs-local, where the source of the copy already is:
	// the copy moves back over it.
	if !w.hasItem("libs-local", "app/a.bin") || w.hasItem("other-local", "app/a.bin") ||
		!w.props("libs-local", "app/a.bin").Has("stage", "live") {
		t.Errorf("after the move: %q", promotionLines(t, rec.Body.Bytes()))
	}
	rec = w.do(http.MethodPost, aqlURL+"?compact=true", "admin", `build.promotions.find()`)
	want := `{"results":[{"build.promotion.created":"2026-10-16T08:00:00.000Z",` +
		`"build.promotion.created_by":"admin","build.promotion.repo":"libs-local",` +
		`"build.promotion.status":"released","build.promotion.user":"jenkins"}],` +
		`"range":{"start_pos":0,"end_pos":1,"total":1}}`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Errorf("promotions %d\n%s\nwant\n%s", rec.Code, rec.Body, want)
	}
}

// TestBuildPromoteRefusals answers 400 with a report for what cannot be
// promoted at all, and for a fail-fast promotion that met an error.
func TestBuildPromoteRefusals(t *testing.T) {
	w := newItemWorld(t, false)
	w.publish(artifactDoc("app", "1", testStarted, "nowhere"))
	promote := func(body, contentType string) (int, []string) {
		rec := w.do(http.MethodPost, buildsPath+"/promote/app/1", "admin", body,
			"Content-Type", contentType)
		if rec.Code != http.StatusBadRequest {
			return rec.Code, nil
		}
		return rec.Code, promotionLines(t, rec.Body.Bytes())
	}
	for _, tc := range []struct{ body, contentType, want string }{
		{`{}`, "text/plain", "ERROR The promotion request must be JSON"},
		{`{"timestamp":"soon"}`, "application/json", "ERROR The timestamp soon is not in the layout"},
		{
			`{"properties":{"k":{"a":1}}}`, "application/json",
			"ERROR The value of property 'k' must be a string or an array of strings",
		},
		{
			`{"targetRepo":"virt-nodeploy"}`, "application/json",
			"ERROR the virtual repository 'virt-nodeploy' has no default deployment repository",
		},
		{
			`{"targetRepo":"other-local"}`, promotionType,
			"ERROR Unable to find artifact 'a.bin' of build 'app' #1",
		},
	} {
		code, lines := promote(tc.body, tc.contentType)
		if code != http.StatusBadRequest || len(lines) == 0 ||
			!strings.HasPrefix(lines[0], tc.want) {
			t.Errorf("%s: %d %q, want 400 %q", tc.body, code, lines, tc.want)
		}
	}
	// Without fail-fast the same promotion reports the error and answers 200.
	rec := w.do(http.MethodPost, buildsPath+"/promote/app/1", "admin",
		`{"targetRepo":"other-local","failFast":false,"artifacts":true}`,
		"Content-Type", "application/json")
	wantStatus(t, rec, 200)
	rec = w.do(http.MethodPost, buildsPath+"/promote/app/1", "admin", `{"targetRepo":"absent"}`,
		"Content-Type", "application/json")
	wantStatus(t, rec, 404)
	if !strings.Contains(rec.Body.String(), "Cannot find target repository by the key 'absent'.") {
		t.Errorf("body %s", rec.Body)
	}
	rec = w.do(http.MethodPost, buildsPath+"/promote/app/2", "admin", `{}`,
		"Content-Type", "application/json")
	wantStatus(t, rec, 404)
	if !strings.Contains(rec.Body.String(), "Unable to find build 'app' #2.") {
		t.Errorf("body %s", rec.Body)
	}
}

// TestBuildRetention is jf rt bdi's request: count as a string, protected
// numbers, async off and on, and the 400 Artifactory gives a count of 0.
func TestBuildRetention(t *testing.T) {
	w := newItemWorld(t, false)
	for i, n := range []string{"1", "2", "3", "4", "5"} {
		w.publish(buildDoc("app", n, startedAt(i)))
	}
	retain := func(query, body string) int {
		return w.do(http.MethodPost, buildsPath+"/retention/app"+query, "admin", body,
			"Content-Type", "application/json").Code
	}
	if got := retain("?async=false", `{"count":"3","buildNumbersNotToBeDiscarded":["1"],`+
		`"deleteBuildArtifacts":false}`); got != 204 {
		t.Fatalf("retention: %d", got)
	}
	want := []string{
		"/5@" + startedAt(4), "/4@" + startedAt(3), "/3@" + startedAt(2),
		"/1@" + startedAt(0),
	}
	if runs := w.runsOf("app"); strings.Join(runs, " ") != strings.Join(want, " ") {
		t.Errorf("runs %v, want %v", runs, want)
	}

	if got := retain("?async=true&project=default",
		`{"minimumBuildDate":"`+startedAt(4)+`"}`); got != 204 {
		t.Fatalf("async retention: %d", got)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(w.runsOf("app")) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the background discard left %v", w.runsOf("app"))
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, tc := range []struct {
		query, body string
		want        int
	}{
		{"", `{"count":0}`, 400},
		{"", `{"count":"x"}`, 400},
		{"", `{"minimumBuildDate":"soon"}`, 400},
		{"?async=true", `{"count":1}`, 204},
	} {
		if got := retain(tc.query, tc.body); got != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.query, tc.body, got, tc.want)
		}
	}
	rec := w.do(http.MethodPost, buildsPath+"/retention/app", "admin", ``,
		"Content-Type", "application/json")
	wantStatus(t, rec, 400)
	rec = w.do(http.MethodPost, buildsPath+"/retention/absent?async=true", "admin", `{"count":1}`,
		"Content-Type", "application/json")
	wantStatus(t, rec, 404)
}

// TestBuildPublishRetention applies a published document's buildRetention
// and logs, rather than refuses, one the publisher may not apply.
func TestBuildPublishRetention(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "deploy-builds",
		Resources: map[types.ResourceType]types.Resource{types.ResourceBuild: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"deployer": types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{types.BuildInfoRepository: {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	w.publish(buildDoc("app", "1", startedAt(0)))
	retained := func(number string, at int) string {
		return `{"name":"app","number":"` + number + `","started":"` + startedAt(at) +
			`","buildRetention":{"count":1}}`
	}
	wantStatus(t, w.do(http.MethodPut, buildsPath, "deployer", retained("2", 1),
		"Content-Type", vendorJSON), 200)
	if runs := w.runsOf("app"); len(runs) != 2 {
		t.Errorf("runs %v after a publisher without delete", runs)
	}
	w.publish(retained("3", 2))
	if runs := w.runsOf("app"); len(runs) != 1 {
		t.Errorf("runs %v", runs)
	}
}

func (w *itemWorld) props(key, path string) types.Properties {
	w.t.Helper()
	ctx := context.Background()
	it := w.item(key, false, path)
	p, err := w.db.Items().Properties(ctx, it.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}
