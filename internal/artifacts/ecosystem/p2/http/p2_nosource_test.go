package p2http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestUnitP2RemoteReadsWithoutRemoteSources: on a server built without a
// remote source service, every remote P2 read the overlay serves answers
// 500 "internal error", a fault of the server's own, rather than the
// upstream's 502, whether the remote is asked directly or through a
// virtual repository. A composite child path that is not well formed is
// refused before any read, so it keeps its 400.
func TestUnitP2RemoteReadsWithoutRemoteSources(t *testing.T) {
	w := kittest.New(t, inmem.New())
	kit := w.Kit(itemkit.Deps{
		Logger:   slog.New(slog.DiscardHandler),
		Overlays: []itemkit.Overlay{TryP2Item},
	})
	for _, c := range []repo.Config{
		{
			Key: "p2-remote", Class: repo.Remote, PackageType: repo.P2,
			URL: "https://site.example/updates/",
		},
		{
			Key: "p2-archive", Class: repo.Remote, PackageType: repo.P2,
			URL: "https://site.example/site.zip!/",
		},
		{
			Key: "p2-virtual", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-remote", "p2-archive"},
		},
	} {
		if err := w.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	child := "__p2_ext/" + strings.Repeat("a", 64) + "/content.jar"
	for _, tc := range []struct {
		name, path string
		status     int
		message    string
	}{
		{"remote composite", "p2-remote/compositeContent.xml", 500, "internal error"},
		{"remote composite child", "p2-remote/" + child, 500, "internal error"},
		{"archive composite", "p2-archive/compositeContent.xml", 500, "internal error"},
		{"archive entry", "p2-archive/content.jar", 500, "internal error"},
		{"virtual over the remote", "p2-virtual/p2-remote/compositeContent.xml", 500, "internal error"},
		{"virtual over the archive", "p2-virtual/p2-archive/content.jar", 500, "internal error"},
		{"child path not well formed", "p2-remote/__p2_ext/zzz/content.jar", 400, "Bad Request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			kit.GetItem(rec, kittest.Request(t, http.MethodGet, "/artifactory/"+tc.path))
			wantError(t, rec, tc.status, tc.message)
		})
	}
}

// wantError fails the test unless rec answered status with the Artifactory
// error envelope that carries message, as the item route writes an error.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Status != status ||
		body.Errors[0].Message != message {
		t.Errorf("answered %d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
}
