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

// PATCH /api/metadata takes props, stats or both, and refuses a body with
// neither (7.x's updateitemproperties: 400 when "props/stats fields are
// missing"; 6.20's ArtifactMetadataResource merges stats into StatsInfo).
func TestAccPatchMetadataStats(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	w.put("/artifactory/libs-local/d/a.txt;team=core", "a")
	const meta = "/artifactory/api/metadata/libs-local/d/a.txt"
	file := w.item("libs-local", false, "d/a.txt")
	stats := func() types.ItemStats {
		t.Helper()
		st, err := w.db.Items().Stats(ctx, file.ID)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	props := func(p string) types.Properties {
		t.Helper()
		got, err := w.db.Items().Properties(ctx, w.item("libs-local", false, p).ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// stats alone sets the download statistics and leaves the properties.
	wantStatus(t, w.do(http.MethodPatch, meta, "admin",
		`{"stats":{"downloadCount":5,"lastDownloaded":1700000000000,"lastDownloadedBy":"bob",`+
			`"remoteDownloadCount":2,"remoteLastDownloaded":1600000000000,`+
			`"remoteLastDownloadedBy":"edge"}}`), http.StatusNoContent)
	want := types.ItemStats{
		DownloadCount: 5, LastDownloaded: time.UnixMilli(1700000000000).UTC(),
		LastDownloadedBy: "bob", RemoteDownloadCount: 2,
		RemoteLastDownloaded: time.UnixMilli(1600000000000).UTC(), RemoteLastDownloadedBy: "edge",
	}
	if got := stats(); got != want {
		t.Fatalf("stats %+v, want %+v", got, want)
	}
	if got := props("d/a.txt"); len(got) != 1 || !got.Has("team", "core") {
		t.Fatalf("properties %v", got)
	}
	// The ?stats view reads them back as the PATCH wrote them.
	rec := w.do(http.MethodGet, "/artifactory/api/storage/libs-local/d/a.txt?stats", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil ||
		view["downloadCount"] != float64(5) || view["lastDownloaded"] != float64(1700000000000) ||
		view["lastDownloadedBy"] != "bob" {
		t.Fatalf("?stats %s, %v", rec.Body, err)
	}

	// A merge: fields left out keep their values, null clears one, and props
	// beside stats are applied in the same request.
	wantStatus(t, w.do(
		http.MethodPatch,
		meta,
		"admin",
		`{"props":{"team":null,"tier":"gold"},"stats":{"downloadCount":7,"lastDownloadedBy":null}}`,
	),
		http.StatusNoContent)
	want.DownloadCount, want.LastDownloadedBy = 7, ""
	if got := stats(); got != want {
		t.Fatalf("merged stats %+v, want %+v", got, want)
	}
	if got := props("d/a.txt"); len(got) != 1 || !got.Has("tier", "gold") {
		t.Fatalf("properties %v", got)
	}
	// Zero, like null, means never downloaded.
	wantStatus(t, w.do(http.MethodPatch, meta, "admin",
		`{"stats":{"lastDownloaded":0,"remoteLastDownloaded":null,"remoteDownloadCount":null}}`),
		http.StatusNoContent)
	if got := stats(); !got.LastDownloaded.IsZero() || !got.RemoteLastDownloaded.IsZero() ||
		got.RemoteDownloadCount != 0 || got.DownloadCount != 7 {
		t.Fatalf("cleared stats %+v", got)
	}
	// An empty props changes nothing, and answers 204.
	wantStatus(t, w.do(http.MethodPatch, meta, "admin", `{"props":{}}`), http.StatusNoContent)
	if got := props("d/a.txt"); len(got) != 1 || !got.Has("tier", "gold") {
		t.Fatalf("properties after empty props %v", got)
	}

	for _, c := range []struct {
		path, as, body string
		status         int
		message        string
	}{
		{meta, "admin", `{}`, 400, "props or stats fields required"},
		{meta, "admin", `{"props":null,"stats":null}`, 400, "props or stats fields required"},
		{meta, "admin", `{"stats":{"downloads":1}}`, 400, "stats has no field"},
		{meta, "admin", `{"stats":{"downloadCount":-1}}`, 400, "must be a whole number of 0 or more"},
		{meta, "admin", `{"stats":{"downloadCount":"3"}}`, 400, "must be a whole number of 0 or more"},
		{meta, "admin", `{"stats":{"lastDownloaded":"2026-10-19"}}`, 400, "must be a time in Unix milliseconds"},
		{meta, "admin", `{"stats":{"lastDownloadedBy":3}}`, 400, "must be a string or null"},
		{meta, "admin", `{"stats":[]}`, 400, "malformed JSON body"},
		// Statistics are kept for files, as the ?stats view answers.
		{
			"/artifactory/api/metadata/libs-local/d", "admin", `{"stats":{"downloadCount":1}}`, 400,
			"is a folder; statistics are kept for files",
		},
		{
			"/artifactory/api/metadata/libs-local", "admin", `{"stats":{"downloadCount":1}}`, 400,
			"is a folder; statistics are kept for files",
		},
		{"/artifactory/api/metadata/libs-local/d/none", "admin", `{"stats":{"downloadCount":1}}`, 404, ""},
		// Setting statistics needs Annotate, as setting properties does.
		{meta, "deployer", `{"stats":{"downloadCount":1}}`, 403, ""},
		{meta, "", `{"stats":{"downloadCount":1}}`, 401, ""},
	} {
		rec := w.do(http.MethodPatch, c.path, c.as, c.body)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.message) {
			t.Errorf("%s %s as %q: %d %s, want %d %q", c.path, c.body, c.as, rec.Code, rec.Body,
				c.status, c.message)
		}
	}
	// None of the refused requests changed anything.
	if got := stats(); got.DownloadCount != 7 {
		t.Fatalf("stats after refusals %+v", got)
	}
	// A statistics request on a folder is refused before the folder's
	// properties are touched.
	if got := props("d"); len(got) != 0 {
		t.Fatalf("folder properties %v", got)
	}
}
