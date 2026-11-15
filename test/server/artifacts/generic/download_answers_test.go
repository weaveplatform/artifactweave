package generic_test

// This file holds the answers #1572 gives a download from a remote
// repository, or from its cache, that misses or that the caller may not
// read: 7.161.26's DownloadServiceImpl.respondResourceNotFound, which
// appends "; Path: '<repoKey>:<path>'" to the reason the remote's getInfo
// gives.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// errorBody is the Artifactory envelope of status and message.
func errorBody(t *testing.T, status int, message string) string {
	t.Helper()
	b, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return `{"errors":[{"message":` + string(b) + `,"status":` + strconv.Itoa(status) + `}]}` + "\n"
}

// wantError fails t unless rec has status and, on a GET, the envelope of
// message. A HEAD carries the status alone.
func wantError(
	t *testing.T,
	method string,
	rec *httptest.ResponseRecorder,
	status int,
	message string,
) {
	t.Helper()
	if rec.Code != status ||
		method == http.MethodGet && rec.Body.String() != errorBody(t, status, message) {
		t.Fatalf("%s: %d %s, want %d %q", method, rec.Code, rec.Body, status, message)
	}
}

// TestAccRemoteDownloadMissNamesThePath (#1572): a file a remote's upstream
// does not have answers 404 with the reason HttpRepo.returnUnfoundResource
// takes from the upstream's status line, "Not Found" for a 404, and the
// remote's path; an offline remote, which asks no upstream, answers
// RemoteRepoBase.internalGetInfo's "<key>: is offline, '<repoPath>' is not
// found at '<path>'."; and the "-cache" key, which reads the cache alone,
// DbStoringRepoMixin.getInfo's "File not found.". A checksum names the file
// it is read from. Before, the first two answered "Not Found" and the third
// "Could not find resource", on every remote type.
func TestAccRemoteDownloadMissNamesThePath(t *testing.T) {
	const missing = "pkg/missing.bin"
	for _, pt := range []repo.PackageType{repo.Generic, repo.Helm, repo.PyPI} {
		for _, tc := range []struct {
			name, path, message string
			offline             bool
		}{
			{"miss", "remote/" + missing, "Not Found; Path: 'remote:" + missing + "'", false},
			{"checksum", "remote/" + missing + ".sha1", "Not Found; Path: 'remote:" + missing + "'", false},
			{
				"offline", "remote/" + missing,
				"remote: is offline, 'remote:" + missing + "' is not found at '" + missing +
					"'.; Path: 'remote:" + missing + "'",
				true,
			},
			{"cache", "remote-cache/" + missing, "File not found.; Path: 'remote-cache:" + missing + "'", false},
			{
				"cache offline", "remote-cache/" + missing,
				"File not found.; Path: 'remote-cache:" + missing + "'", true,
			},
		} {
			t.Run(string(pt)+" "+tc.name, func(t *testing.T) {
				w := newRemoteWorld(t)
				remoteOfType(t, w, pt, func(c *repo.Config) { c.Offline = new(tc.offline) })
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					rec := w.request(method, "/artifactory/"+tc.path, "reader", "")
					wantError(t, method, rec, http.StatusNotFound, tc.message)
				}
			})
		}
	}
}

// TestAccRemoteDownloadRefusalNamesTheCache (#1572): a remote's download
// refused for want of Read answers 403 with RealRepoBase.
// assertReadPermissions' text for the cache's path, which
// RemoteRepoBase.checkDownloadIsAllowed checks ("permissions are always on
// the cache repo key"), and the path of the repository the request named;
// an anonymous caller, 401 "Unauthorized" with the Basic challenge. The
// upstream is never asked. Before, they answered 403 "Forbidden" and 401
// "Authentication is required".
func TestAccRemoteDownloadRefusalNamesTheCache(t *testing.T) {
	w := newRemoteWorld(t)
	if err := w.rt.Store.Settings().
		Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	w.up.Set("/pkg/file.bin", declaring([]byte("upstream bytes"), true))
	const refused = "Download request for repo:path 'remote-cache:pkg/file.bin' is forbidden for user: 'denied'."
	for _, tc := range []struct {
		path, user string
		status     int
		message    string
	}{
		{"remote/pkg/file.bin", "denied", 403, refused + "; Path: 'remote:pkg/file.bin'"},
		{"remote/pkg/file.bin.sha1", "denied", 403, refused + "; Path: 'remote:pkg/file.bin'"},
		{"remote-cache/pkg/file.bin", "denied", 403, refused + "; Path: 'remote-cache:pkg/file.bin'"},
		{"remote/pkg/file.bin", "", 401, "Unauthorized"},
		{"remote-cache/pkg/file.bin", "", 401, "Unauthorized"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := w.request(method, "/artifactory/"+tc.path, tc.user, "")
			wantError(t, method, rec, tc.status, tc.message)
			challenge := rec.Header().Get("WWW-Authenticate")
			if tc.status == 401 && challenge != `Basic realm="Artifactory Realm"` ||
				tc.status != 401 && challenge != "" {
				t.Fatalf("%s %s: challenge %q", method, tc.path, challenge)
			}
		}
	}
	if n := len(w.up.Requests()); n != 0 {
		t.Fatal("asked the upstream", w.up.Requests())
	}
}
