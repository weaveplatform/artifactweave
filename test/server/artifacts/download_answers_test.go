package artifacts_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
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

// wantAnswer fails t unless rec has status and, on a GET, body. A HEAD
// carries the status alone.
func wantAnswer(
	t *testing.T,
	method string,
	rec *httptest.ResponseRecorder,
	status int,
	body string,
) {
	t.Helper()
	if rec.Code != status || method == http.MethodGet && rec.Body.String() != body {
		t.Fatalf("%s: %d %s, want %d %s", method, rec.Code, rec.Body, status, body)
	}
}

// downloadWorld is an item world, with anonymous access as anonymous says,
// holding libs-local/a/file.bin and a federated repository fed-local, which
// reader reads as it reads every repository, and in which user may read
// everything but a/secret.bin.
func downloadWorld(t *testing.T, anonymous bool) *itemWorld {
	t.Helper()
	w := newItemWorld(t, anonymous)
	ctx := context.Background()
	if err := w.db.Repos().Create(ctx, *repo.Defaults(&repo.Config{
		Key: "fed-local", Class: repo.Federated,
	})); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(ctx, accesstest.Permission{
		Name: "user-other",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"user": api.ActionRead}},
			Targets: map[string]accesstest.Target{
				"other-local": {Include: []string{"**"}, Exclude: []string{"a/secret.bin"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"libs-local/a/file.bin", "other-local/a/secret.bin", "fed-local/a/file.bin"} {
		wantStatus(
			t,
			w.do(http.MethodPut, "/artifactory/"+p, "admin", "content"),
			http.StatusCreated,
		)
	}
	return w
}

// TestAccItemDownloadMissNamesThePath (#1572): a download through the item
// route of a file a local or federated repository does not hold answers
// 404 with 7.161.26's text: DbStoringRepoMixin.getInfo's "File not found."
// and the "; Path: '<repoKey>:<path>'" DownloadServiceImpl.
// respondResourceNotFound appends. A checksum and an archive entry name the
// file they are read from (DownloadRequestContext.getResourcePath,
// ArtifactoryRequestBase.zipResourcePath). Before, every one answered
// "Could not find resource", which a virtual repository's miss keeps
// (P10's #1533 owns it). A file that lacks a mandatory matrix key
// ("key+=value") answers DbStoringRepoMixin.getInfo's PROPERTY_MISMATCH text.
func TestAccItemDownloadMissNamesThePath(t *testing.T) {
	w := downloadWorld(t, false)
	for _, tc := range []struct{ path, message string }{
		{"libs-local/a/missing.bin", "File not found.; Path: 'libs-local:a/missing.bin'"},
		{"libs-local/a/missing.bin.sha1", "File not found.; Path: 'libs-local:a/missing.bin'"},
		{"libs-local/a/missing.bin.sha256", "File not found.; Path: 'libs-local:a/missing.bin'"},
		{"libs-local/a/missing.zip!/x.txt", "File not found.; Path: 'libs-local:a/missing.zip'"},
		{"fed-local/a/missing.bin", "File not found.; Path: 'fed-local:a/missing.bin'"},
		{
			"libs-local/a/file.bin;x+=1",
			"File 'libs-local:a/file.bin' was found, but mandatory properties do not match.; " +
				"Path: 'libs-local:a/file.bin'",
		},
		{"virt/a/missing.bin", "Could not find resource"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := w.do(method, "/artifactory/"+tc.path, "reader", "")
			wantAnswer(
				t,
				method,
				rec,
				http.StatusNotFound,
				errorBody(t, http.StatusNotFound, tc.message),
			)
		}
	}
}

// TestAccItemDownloadRefusalNamesTheUser (#1572): a download of a file the
// caller may not read answers 403 with RealRepoBase.assertReadPermissions'
// text and respondResourceNotFound's path, whether the file exists or not,
// as 7.161.26's checkDownloadIsAllowed refuses before getInfo looks. An
// anonymous caller gets 401 "Unauthorized" with the Basic challenge:
// shouldSendAuthRequired turns the 403 into HttpArtifactoryResponse.
// sendAuthorizationRequired, which writes that text in place of the reason.
// Before, they answered 403 "Forbidden" and 401 "Authentication is
// required". A folder request, which 7.x answers with its directory
// listing, and a virtual repository keep their answers.
func TestAccItemDownloadRefusalNamesTheUser(t *testing.T) {
	w := downloadWorld(t, true)
	forbidden := func(key, p, user string) string {
		return "Download request for repo:path '" + key + ":" + p + "' is forbidden for user: '" +
			user + "'.; Path: '" + key + ":" + p + "'"
	}
	for _, tc := range []struct {
		path, user string
		status     int
		message    string
	}{
		{"libs-local/a/file.bin", "user", 403, forbidden("libs-local", "a/file.bin", "user")},
		{"libs-local/a/missing.bin", "user", 403, forbidden("libs-local", "a/missing.bin", "user")},
		{"libs-local/a/file.bin.sha1", "user", 403, forbidden("libs-local", "a/file.bin", "user")},
		{"libs-local/a/file.zip!/x.txt", "user", 403, forbidden("libs-local", "a/file.zip", "user")},
		{"fed-local/a/file.bin", "user", 403, forbidden("fed-local", "a/file.bin", "user")},
		// The folder check passes, as user may read below a/secret.bin/;
		// the file's own check refuses.
		{"other-local/a/secret.bin", "user", 403, forbidden("other-local", "a/secret.bin", "user")},
		// Before, 404 "Could not find resource": 7.x checks the file a
		// checksum names (DownloadRequestContext.getResourcePath).
		{"other-local/a/secret.bin.sha1", "user", 403, forbidden("other-local", "a/secret.bin", "user")},
		{"libs-local/a/file.bin", "", 401, "Unauthorized"},
		{"libs-local/a/missing.bin", "", 401, "Unauthorized"},
		{"fed-local/a/file.bin", "", 401, "Unauthorized"},
		{"libs-local/a/", "user", 403, "Forbidden"},
		{"libs-local/a/", "", 401, "Authentication is required"},
		{"virt/a/file.bin", "", 401, "Authentication is required"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := w.do(method, "/artifactory/"+tc.path, tc.user, "")
			wantAnswer(t, method, rec, tc.status, errorBody(t, tc.status, tc.message))
			challenge := rec.Header().Get("WWW-Authenticate")
			if tc.status == 401 && challenge != `Basic realm="Artifactory Realm"` ||
				tc.status != 401 && challenge != "" {
				t.Fatalf("%s %s: challenge %q", method, tc.path, challenge)
			}
		}
	}
}
