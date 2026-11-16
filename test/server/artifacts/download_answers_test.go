package artifacts_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
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
// "Could not find resource", which a virtual repository's miss keeps with
// the virtual's path (#1533; virtual_status_test.go). A file that lacks a
// mandatory matrix key ("key+=value") answers DbStoringRepoMixin.getInfo's
// PROPERTY_MISMATCH text.
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
		{"virt/a/missing.bin", "Could not find resource; Path: 'virt:a/missing.bin'"},
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
// listing, keeps its answers. Through a virtual repository the members
// decide, and an anonymous caller refused by one gets the same 401
// (#1533; before, "Authentication is required").
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
		{"virt/a/file.bin", "", 401, "Unauthorized"},
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

// overlayAnswersWorld is a harness.RemoteWorld whose remote, over an
// upstream that holds nothing, is of type pt, beside the local repository
// fmt-local of pt, with anonymous access on and reader's read on both.
func overlayAnswersWorld(t *testing.T, pt repo.PackageType) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t)
	w.Repo.PackageType = pt
	w.Update()
	ctx := t.Context()
	if err := w.Runtime.Store.Repos().Create(ctx, *repo.Defaults(&repo.Config{
		Key: "fmt-local", Class: repo.Local, PackageType: pt,
	})); err != nil {
		t.Fatal(err)
	}
	if err := w.Runtime.Store.Permissions().Create(ctx, accesstest.Permission{
		Name: "fmt-local-read", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: map[string]accesstest.Target{"fmt-local": {}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Runtime.Store.Settings().
		Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	return w
}

// TestAccFormatOverlayDownloadAnswers (#1599): a file a format overlay
// serves on the item route, an index, a remote's package or a path the
// format keeps from the item route, misses and is refused as any file on
// that route is (#1572), since 7.161.26's RepoFilter.doDownload hands every
// GET and HEAD of /artifactory/<key>/<path> to DownloadServiceImpl,
// whatever the package type. A miss takes DbStoringRepoMixin.getInfo's
// "File not found." in a local repository and a remote's cache, and the
// upstream's reason phrase, "Not Found", in a remote (HttpRepo.
// returnUnfoundResource), with respondResourceNotFound's path; a caller
// without read takes RealRepoBase.assertReadPermissions' reason for the
// path the download checks, a remote's cache, and an anonymous one 401
// "Unauthorized" with the Basic challenge. Before, each answered 404
// "Could not find resource" or "Not Found", 403 "Forbidden" and 401
// "Authentication is required". A path a format keeps from the item route
// (Chef's api/v1 documents, Conda's shards, Machine Learning's manifests,
// a CRAN PACKAGES.rds, a path outside a format's layout) holds no file the
// download serves on 7.x, so it answers the miss; on a remote, whose
// upstream 7.x would ask, that is [I].
func TestAccFormatOverlayDownloadAnswers(t *testing.T) {
	refused := func(key, checked, p, user string) string {
		return "Download request for repo:path '" + checked + ":" + p +
			"' is forbidden for user: '" + user + "'.; Path: '" + key + ":" + p + "'"
	}
	type answer struct {
		path, user string
		status     int
		message    string
	}
	// file answers p, a file the overlay serves in fmt-local and remote,
	// missed by both and refused to denied and anonymous.
	file := func(p string) []answer {
		return []answer{
			{"fmt-local/" + p, "reader", 404, "File not found.; Path: 'fmt-local:" + p + "'"},
			{"remote/" + p, "reader", 404, "Not Found; Path: 'remote:" + p + "'"},
			{"remote-cache/" + p, "reader", 404, "File not found.; Path: 'remote-cache:" + p + "'"},
			{"fmt-local/" + p, "denied", 403, refused("fmt-local", "fmt-local", p, "denied")},
			{"remote/" + p, "denied", 403, refused("remote", "remote-cache", p, "denied")},
			{"fmt-local/" + p, "", 401, "Unauthorized"},
			{"remote/" + p, "", 401, "Unauthorized"},
		}
	}
	// kept answers p, a path the overlay keeps from the item route in a
	// local repository, as file does there.
	kept := func(p string) []answer {
		return []answer{
			{"fmt-local/" + p, "reader", 404, "File not found.; Path: 'fmt-local:" + p + "'"},
			{"fmt-local/" + p, "denied", 403, refused("fmt-local", "fmt-local", p, "denied")},
			{"fmt-local/" + p, "", 401, "Unauthorized"},
		}
	}
	// remoteMiss answers p, a remote's file whose upstream read misses.
	remoteMiss := func(p string) answer {
		return answer{"remote/" + p, "reader", 404, "Not Found; Path: 'remote:" + p + "'"}
	}
	for _, tc := range []struct {
		pt      repo.PackageType
		answers []answer
	}{
		{repo.Alpine, append(file("v3.20/main/x86_64/APKINDEX.tar.gz"),
			append(kept("hello-1.0-r0.apk"), remoteMiss("v3.20/main/APKINDEX.tar.gz"))...)},
		{repo.Debian, file("dists/stable/main/binary-amd64/Packages")},
		{repo.Opkg, append(file("feed/Packages"),
			remoteMiss("feed/464xlat_13_x86_64.ipk"),
			answer{
				"remote/feed/464xlat_13_x86_64.ipk", "denied", 403,
				refused("remote", "remote-cache", "feed/464xlat_13_x86_64.ipk", "denied"),
			})},
		{repo.Cran, append(append(file("src/contrib/PACKAGES"), kept("src/contrib/PACKAGES.rds")...),
			remoteMiss("src/contrib/assertthat_0.2.1.tar.gz"))},
		{repo.P2, []answer{
			remoteMiss("readme.txt"),
			{"remote/readme.txt", "denied", 403, refused("remote", "remote-cache", "readme.txt", "denied")},
			{"remote/readme.txt", "", 401, "Unauthorized"},
		}},
		{repo.Chef, append(kept("api/v1/cookbooks"), remoteMiss("api/v1/cookbooks"))},
		{repo.Conda, kept("noarch/repodata_shards.msgpack.zst")},
		{repo.MachineLearning, kept("models/widget/1.0/model-manifest.json")},
	} {
		t.Run(string(tc.pt), func(t *testing.T) {
			w := overlayAnswersWorld(t, tc.pt)
			for _, a := range tc.answers {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					rec := w.Request(method, "/artifactory/"+a.path, a.user, "")
					if rec.Code != a.status || method == http.MethodGet &&
						rec.Body.String() != errorBody(t, a.status, a.message) {
						t.Errorf("%s %s as %q: %d %s, want %d %s", method, a.path, a.user,
							rec.Code, rec.Body, a.status, a.message)
					}
					challenge := rec.Header().Get("WWW-Authenticate")
					if a.status == 401 && challenge != `Basic realm="Artifactory Realm"` ||
						a.status != 401 && challenge != "" {
						t.Errorf("%s %s as %q: challenge %q", method, a.path, a.user, challenge)
					}
				}
			}
		})
	}
}

// TestAccFormatOverlayPolicyRefusalNamesThePath (#1630): a file a format
// overlay serves on the item route, refused by the download policy of a
// local repository, a remote or its cache, answers as 7.161.26 answers
// every such refusal of a file: RealRepoBase.assertValidPath's reason with
// the path the download names after it (DownloadServiceImpl.
// respondResourceNotFound), traced as createUnfoundRepoResource logs it.
// A cache's reason names the cache, as 7.x's cache repository checks the
// remote's settings under its own key. Before, the reason came alone, a
// cache's named the remote, and the trace named no refusal.
func TestAccFormatOverlayPolicyRefusalNamesThePath(t *testing.T) {
	for _, tc := range []struct {
		pt    repo.PackageType
		paths []string
	}{
		{repo.Alpine, []string{"v3.20/main/x86_64/APKINDEX.tar.gz", "hello-1.0-r0.apk"}},
		{repo.Debian, []string{"dists/stable/main/binary-amd64/Packages"}},
		{repo.Opkg, []string{"feed/Packages", "feed/464xlat_13_x86_64.ipk"}},
		{repo.Cran, []string{"src/contrib/PACKAGES", "src/contrib/PACKAGES.rds", "src/contrib/assertthat_0.2.1.tar.gz"}},
		{repo.P2, []string{"readme.txt"}},
		{repo.Chef, []string{"api/v1/cookbooks"}},
		{repo.Conda, []string{"noarch/repodata_shards.msgpack.zst"}},
		{repo.MachineLearning, []string{"models/widget/1.0/model-manifest.json"}},
	} {
		t.Run(string(tc.pt), func(t *testing.T) {
			w := overlayAnswersWorld(t, tc.pt)
			c, err := w.Runtime.Store.Repos().Get(t.Context(), "fmt-local")
			if err != nil {
				t.Fatal(err)
			}
			c.BlackedOut = ptr(true)
			if err := w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			w.Repo.BlackedOut = ptr(true)
			w.Update()
			for _, p := range tc.paths {
				for _, key := range []string{"fmt-local", "remote", "remote-cache"} {
					message := "The repository '" + key + "' is blacked out and cannot serve " +
						"artifact '" + key + ":" + p + "'."
					answer := message + "; Path: '" + key + ":" + p + "'"
					path := "/artifactory/" + key + "/" + p
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						wantAnswer(t, method, w.Request(method, path, "reader", ""),
							http.StatusNotFound, errorBody(t, http.StatusNotFound, answer))
					}
					rec := w.Request(http.MethodGet, path+"?trace", "reader", "")
					for _, step := range []string{
						" Download denied (" + message + ") - returning un-found resource\n",
						" Sending a response with the status '404' and the message '" + answer + "'\n",
					} {
						if !strings.Contains(rec.Body.String(), step) {
							t.Errorf("%s?trace: %s, want %q", path, rec.Body, step)
						}
					}
				}
			}
		})
	}
}

// TestAccCachePolicyRefusalNamesTheCache (#1630): a download through a
// remote repository's -cache alias that the remote's download policy
// refuses names the cache in its reason, as 7.161.26's cache repository
// checks the remote's settings under its own key, "<remote>-cache"
// (LocalCacheRepoConfig 40), and its policy exceptions name that key and
// its repo path (RepoBase.getRepoPath 122-123). A file, its checksum and an
// archive entry all answer so, with the path after the reason and the
// reason in the trace; the remote's own key keeps naming the remote, and
// the upstream is not asked. Before, a cache's reason named the remote.
func TestAccCachePolicyRefusalNamesTheCache(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	for _, tc := range []struct {
		name   string
		tune   func(*repo.Config)
		reason func(key, file string) string
	}{
		{
			"blacked out",
			func(c *repo.Config) { c.BlackedOut = ptr(true) },
			func(key, file string) string {
				return "The repository '" + key + "' is blacked out and cannot serve artifact '" +
					key + ":" + file + "'."
			},
		},
		{
			"excluded",
			func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = ptr(false), ptr("**/*.txt, **/*.zip") },
			func(key, file string) string {
				return "The repository '" + key + "' rejected the artifact '" + key + ":" + file +
					"' due to its include/exclude pattern settings."
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.tune(&w.Repo)
			w.Update()
			for _, key := range []string{"remote", "remote-cache"} {
				for asked, file := range map[string]string{
					"x.txt": "x.txt", "x.txt.sha1": "x.txt", "a.zip!/f.txt": "a.zip",
				} {
					reason := tc.reason(key, file)
					answer := reason + "; Path: '" + key + ":" + file + "'"
					path := "/artifactory/" + key + "/" + asked
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						wantAnswer(t, method, w.Request(method, path, "reader", ""),
							http.StatusNotFound, errorBody(t, http.StatusNotFound, answer))
					}
					rec := w.Request(http.MethodGet, path+"?trace", "reader", "")
					step := " Download denied (" + reason + ") - returning un-found resource\n"
					if !strings.Contains(rec.Body.String(), step) {
						t.Errorf("%s?trace: %s, want %q", path, rec.Body, step)
					}
				}
			}
		})
	}
	if n := len(w.Upstream.Requests()); n != 0 {
		t.Errorf("the refused downloads asked the upstream %d times", n)
	}
}

// TestAccFormatOverlayRemoteFillAnswers: a remote file a format overlay
// serves on the item route, an index or a package, answers a caller who
// may read the remote but not fill its cache, and a read while the
// upstream is assumed offline, as the item route answers any remote file
// (#1587, #1601), since 7.161.26 serves these files through
// DownloadServiceImpl and RemoteRepoBase: 403 "Rejected artifact download
// request: " and assertValidDeployPathAndPermissions' reason for the
// file's cache path, with no path suffix, and 401 with the challenge for
// an anonymous caller (respondFoundResource); and internalGetInfo's
// assumed-offline miss with respondResourceNotFound's path. A package the
// overlay finds through its index, an opkg IPK or a CRAN source package,
// answers for its own path [I]. Before, the Alpine, Debian, opkg and CRAN
// indexes and the opkg and CRAN packages answered the plain miss, 404 "Not
// Found", for both.
func TestAccFormatOverlayRemoteFillAnswers(t *testing.T) {
	cases := []struct {
		pt    repo.PackageType
		paths []string
	}{
		{repo.Alpine, []string{"v3.20/main/x86_64/APKINDEX.tar.gz"}},
		{repo.Debian, []string{"dists/stable/main/binary-amd64/Packages"}},
		{repo.Opkg, []string{"feed/Packages", "feed/464xlat_13_x86_64.ipk"}},
		{repo.Cran, []string{"src/contrib/PACKAGES", "src/contrib/assertthat_0.2.1.tar.gz"}},
		{repo.P2, []string{"artifacts.jar"}},
	}
	want := func(t *testing.T, w *harness.RemoteWorld, path, user string, status int, message string) {
		t.Helper()
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := w.Request(method, "/artifactory/remote/"+path, user, "")
			if rec.Code != status ||
				method == http.MethodGet && rec.Body.String() != errorBody(t, status, message) {
				t.Errorf("%s %s as %q: %d %s, want %d %s", method, path, user,
					rec.Code, rec.Body, status, message)
			}
			challenge := rec.Header().Get("WWW-Authenticate")
			if status == http.StatusUnauthorized &&
				challenge != `Basic realm="Artifactory Realm"` ||
				status != http.StatusUnauthorized && challenge != "" {
				t.Errorf("%s %s as %q: challenge %q", method, path, user, challenge)
			}
		}
	}
	for _, tc := range cases {
		t.Run(string(tc.pt)+"/fill", func(t *testing.T) {
			w := overlayAnswersWorld(t, tc.pt)
			ctx := t.Context()
			if err := w.Runtime.Store.Users().
				Create(ctx, accesstest.User{Name: "viewer", PasswordHash: support.CheapHash}); err != nil {
				t.Fatal(err)
			}
			if err := w.Runtime.Store.Permissions().Create(ctx, accesstest.Permission{
				Name: "remote-view",
				Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
					Actions: accesstest.Grants{Users: map[string]api.Actions{
						"viewer": api.ActionRead, kernel.AnonymousUser: api.ActionRead,
					}},
					Targets: map[string]accesstest.Target{"remote": {}},
				}},
			}); err != nil {
				t.Fatal(err)
			}
			for _, p := range tc.paths {
				w.Upstream.Set("/"+p, fakeupstream.Resource{Body: []byte("upstream bytes")})
			}
			for _, p := range tc.paths {
				want(t, w, p, "viewer", http.StatusForbidden,
					"Rejected artifact download request: User viewer is not permitted to deploy '"+
						p+"' into 'remote-cache:"+p+"'.")
				want(t, w, p, "", http.StatusUnauthorized, "Unauthorized")
				if n := w.Upstream.Count(http.MethodGet, "/"+p); n != 0 {
					t.Errorf("%s: a caller who may not fill the cache downloaded it %d times", p, n)
				}
			}
		})
		t.Run(string(tc.pt)+"/offline", func(t *testing.T) {
			w := overlayAnswersWorld(t, tc.pt)
			for _, p := range tc.paths {
				w.Upstream.Set("/"+p, fakeupstream.Resource{
					Fault: fakeupstream.Fault{DropConnection: true},
				})
			}
			// Connection failures have the upstream assumed offline, as in
			// the generic suite's TestAccRemoteAssumedOfflineAnswersFromTheCache.
			for range 10 {
				w.Request(http.MethodGet, "/artifactory/remote/"+tc.paths[0], "reader", "")
			}
			asked := len(w.Upstream.Requests())
			for _, p := range tc.paths {
				want(t, w, p, "reader", http.StatusNotFound, "remote: is assumed offline, 'remote:"+
					p+"' is not found at '"+p+"'.; Path: 'remote:"+p+"'")
			}
			if len(w.Upstream.Requests()) != asked {
				t.Errorf("an assumed-offline upstream was asked: %v", w.Upstream.Requests()[asked:])
			}
		})
	}
}
