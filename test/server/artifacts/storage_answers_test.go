package artifacts_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// storageAnswer is one storage API request and the error it answers: as is
// the caller ("" for anonymous), target the path below /api/storage/.
type storageAnswer struct {
	as, target string
	status     int
	message    string
}

// basicChallenge is the Basic challenge every 401 of the storage API carries.
const basicChallenge = `Basic realm="Artifactory Realm"`

// wantStorageAnswers fails t unless each request answers its status and
// message in the error envelope, a 401 with the challenge and anything
// else without one.
func wantStorageAnswers(t *testing.T, w *itemWorld, answers []storageAnswer) {
	t.Helper()
	for _, a := range answers {
		rec := w.do(http.MethodGet, "/artifactory/api/storage/"+a.target, a.as, "")
		want := ""
		if a.status == http.StatusUnauthorized {
			want = basicChallenge
		}
		if rec.Code != a.status || rec.Body.String() != errorBody(t, a.status, a.message) ||
			rec.Header().Get("WWW-Authenticate") != want {
			t.Errorf("%s as %q: %d %s (challenge %q), want %d %q", a.target, a.as, rec.Code,
				rec.Body, rec.Header().Get("WWW-Authenticate"), a.status, a.message)
		}
	}
}

// refusedAs is prepareUnAuthorizedResponse's 403 text.
func refusedAs(repoPath, user string) string {
	return "Request for '" + repoPath + "' is forbidden for user: '" + user + "'."
}

// storageAnswersWorld is an item world, with anonymous access as anonymous
// says, holding libs-local/a/file.bin, a remote repository nostore that
// keeps no cache, and read for user on other-local but for its a/*.bin and
// on server-local below x/y alone.
func storageAnswersWorld(t *testing.T, anonymous bool) *itemWorld {
	t.Helper()
	w := newItemWorld(t, anonymous)
	ctx := context.Background()
	if err := w.db.Repos().Create(ctx, *repo.Defaults(&repo.Config{
		Key: "nostore", Class: repo.Remote, URL: "https://nostore.example",
		StoreArtifactsLocally: ptr(false),
	})); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(ctx, accesstest.Permission{
		Name: "user-other",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"user": api.ActionRead}},
			Targets: map[string]accesstest.Target{
				"other-local":  {Include: []string{"**"}, Exclude: []string{"a/*.bin"}},
				"server-local": {Include: []string{"x/y/**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do(http.MethodPut, "/artifactory/libs-local/a/file.bin", "admin", "content"),
		http.StatusCreated)
	return w
}

// TestAccStorageAnswersMissAndRefusal: each storage view answers a path
// that is not there, a repository that does not exist, a remote repository
// that keeps no cache and a caller without the right, signed in or
// anonymous, in 7.161.26's words (ArtifactResource, #1600).
func TestAccStorageAnswersMissAndRefusal(t *testing.T) {
	w := storageAnswersWorld(t, true)
	const (
		noItem     = "Unable to find item"
		noItemPath = "Unable to find item path"
		noProps    = "No properties could be found."
		notLocal   = "This method can only be invoked on local/cached repositories."
	)
	wantStorageAnswers(t, w, []storageAnswer{
		// The item info: refused naming the path and the caller, a remote
		// repository's under its cache's key, and anonymous with the same
		// 403; a path the caller may not read as a file is refused even
		// where nothing is; a missing path, repository or uncached remote
		// is not found, before any right is weighed.
		{"reader", "libs-local/a/missing.bin", 404, noItem},
		{"user", "libs-local/a/file.bin", 403, refusedAs("libs-local:a/file.bin", "user")},
		{"", "libs-local/a/file.bin", 403, refusedAs("libs-local:a/file.bin", "anonymous")},
		{"user", "libs-local", 403, refusedAs("libs-local:", "user")},
		{"user", "up/pkg/missing.bin", 403, refusedAs("up-cache:pkg/missing.bin", "user")},
		{"", "up/pkg/missing.bin", 404, noItem},
		{"user", "other-local/a/gone.bin", 403, refusedAs("other-local:a/gone.bin", "user")},
		{"user", "other-local/a/gone.txt", 404, noItem},
		{"user", "server-local/x", 403, refusedAs("server-local:x", "user")},
		{"user", "server-local/x/y/z.bin", 404, noItem},
		{"user", "missing/x", 404, noItem},
		{"user", "libs-local-cache/x", 404, noItem},
		{"user", "nostore/pkg/x", 404, noItem},
		{"reader", "nostore", 404, noItem},
		// The statistics: read first, under the key as given, then the
		// path; a missing repository grants only to plain ANY, which the
		// readers group holds.
		{"reader", "libs-local/a/missing.bin?stats", 404, noItemPath},
		{"user", "libs-local/a/file.bin?stats", 403, refusedAs("libs-local:a/file.bin", "user")},
		{"", "libs-local/a/file.bin?stats", 403, refusedAs("libs-local:a/file.bin", "anonymous")},
		{"user", "up/pkg/missing.bin?stats", 403, refusedAs("up:pkg/missing.bin", "user")},
		{"user", "other-local/a/gone.bin?stats", 403, refusedAs("other-local:a/gone.bin", "user")},
		{"user", "server-local/x?stats", 403, refusedAs("server-local:x", "user")},
		{"reader", "missing/x?stats", 404, noItemPath},
		{"admin", "missing/x?stats", 404, noItemPath},
		{"user", "missing/x?stats", 403, refusedAs("missing:x", "user")},
		{"", "missing/x?stats", 403, refusedAs("missing:x", "anonymous")},
		{"deployer", "libs-local-cache/x?stats", 403, refusedAs("libs-local-cache:x", "deployer")},
		{"reader", "nostore/pkg/x?stats", 404, noItemPath},
		{"user", "nostore?stats", 403, refusedAs("nostore:", "user")},
		// The properties service reads nothing it may not show.
		{"user", "libs-local/a/file.bin?properties", 404, noProps},
		{"", "libs-local/a/file.bin?properties", 404, noProps},
		{"reader", "libs-local/a/missing.bin?properties", 404, noProps},
		{"reader", "missing/x?properties", 404, noProps},
		{"user", "nostore/pkg/x?properties", 404, noProps},
		// The add-on's views: 403 to a user, 401 "Unauthorized" with the
		// challenge to anonymous, a miss named under the cache's key, and
		// only local repositories and caches served.
		{"reader", "libs-local/a/file.bin?lastModified", 403, "Forbidden"},
		{"", "libs-local/a/file.bin?lastModified", 401, "Unauthorized"},
		{
			"deployer", "libs-local/a/missing.bin?lastModified", 404,
			"Could not find item: libs-local:a/missing.bin",
		},
		{
			"admin", "up/pkg/missing.bin?lastModified", 404,
			"Could not find item: up-cache:pkg/missing.bin",
		},
		{"reader", "missing/x?lastModified", 400, notLocal},
		{"admin", "nostore/pkg/x?lastModified", 400, notLocal},
		{"deployer", "libs-local/a/file.bin?permissions", 403, "Forbidden"},
		{"", "libs-local/a/file.bin?permissions", 401, "Unauthorized"},
		{
			"admin", "libs-local/a/missing.bin?permissions", 404,
			"Item libs-local:a/missing.bin does not exist",
		},
		{"user", "missing/x?permissions", 400, notLocal},
		{"admin", "nostore?permissions", 400, notLocal},
		// The file list names a file it cannot list by FolderExpectedException;
		// its refusal, miss and missing repository keep their answers.
		{
			"admin", "libs-local/a/file.bin?list", 400,
			"Expected a folder but found a file, at: libs-local:a/file.bin",
		},
		{"user", "libs-local/a?list", 403, "Forbidden"},
		{"reader", "libs-local/a/missing.bin?list", 404, "Could not find resource"},
		{"admin", "missing/x?list", 404, "Repository 'missing' does not exist"},
		{"admin", "nostore/pkg/x?list", 404, "Could not find resource"},
		{"", "missing/x?list", 403, "This resource is available to authenticated users only."},
		// A virtual repository keeps its answers, which P10 settles.
		{"reader", "virt/a/missing.bin", 404, "Could not find resource"},
		{"deployer", "virt/a/file.bin", 403, "Forbidden"},
		{"", "virt/a/file.bin", 401, "Authentication is required"},
		{"admin", "virt/a/file.bin?list", 400, "/a/file.bin is a file; list needs a folder"},
	})
}

// TestAccStorageAnswersAnonymousOff: while anonymous access is off, a
// request without credentials is told to authenticate in every view,
// before the storage API reads anything (#1600).
func TestAccStorageAnswersAnonymousOff(t *testing.T) {
	w := storageAnswersWorld(t, false)
	var answers []storageAnswer
	for _, target := range []string{
		"libs-local/a/file.bin", "libs-local/a/missing.bin", "missing/x", "nostore/pkg/x",
	} {
		for _, view := range []string{
			"", "?stats", "?properties", "?lastModified", "?permissions", "?list",
		} {
			answers = append(answers,
				storageAnswer{"", target + view, 401, "Authentication is required"})
		}
	}
	wantStorageAnswers(t, w, answers)
}

// TestAccStorageAnswersTrash: the trash can's views refuse and miss as a
// local repository's do, under the auto-trashcan key, while it is on; an
// anonymous caller is refused even holding deploy (decision 0010), in the
// view's words. While it is off every view answers 404 "Could not find
// resource" to everyone (#1600).
func TestAccStorageAnswersTrash(t *testing.T) {
	w := storageAnswersWorld(t, true)
	setTrashcan(t, w.db, true)
	wantStatus(t, w.do(http.MethodPut, "/artifactory/libs-local/t/gone.bin", "admin", "gone"),
		http.StatusCreated)
	wantStatus(t, w.do(http.MethodDelete, "/artifactory/libs-local/t/gone.bin", "admin", ""),
		http.StatusNoContent)
	grant(t, w, "anon-deploy", "anonymous", "libs-local", api.ActionRead|api.ActionWrite)
	const (
		trashed = "auto-trashcan/libs-local/t/gone.bin"
		absent  = "auto-trashcan/libs-local/t/absent.bin"
	)
	wantStorageAnswers(t, w, []storageAnswer{
		{"user", "auto-trashcan", 403, refusedAs("auto-trashcan:", "user")},
		{"user", "auto-trashcan?stats", 403, refusedAs("auto-trashcan:", "user")},
		{"user", "auto-trashcan?properties", 404, "No properties could be found."},
		{"user", "auto-trashcan?lastModified", 403, "Forbidden"},
		{"user", "auto-trashcan?permissions", 403, "Forbidden"},
		{"user", "auto-trashcan?list", 403, "Forbidden"},
		{"deployer", "auto-trashcan?permissions", 403, "Forbidden"},
		{"", trashed, 403, refusedAs("auto-trashcan:libs-local/t/gone.bin", "anonymous")},
		{
			"", trashed + "?stats", 403,
			refusedAs("auto-trashcan:libs-local/t/gone.bin", "anonymous"),
		},
		{"", trashed + "?properties", 404, "No properties could be found."},
		{"", trashed + "?lastModified", 401, "Unauthorized"},
		{"", trashed + "?permissions", 401, "Unauthorized"},
		{"", trashed + "?list", 403, "This resource is available to authenticated users only."},
		{
			"admin", trashed + "?list", 400,
			"Expected a folder but found a file, at: auto-trashcan:libs-local/t/gone.bin",
		},
		{"deployer", absent, 404, "Unable to find item"},
		{"deployer", absent + "?stats", 404, "Unable to find item path"},
		{"deployer", absent + "?properties", 404, "No properties could be found."},
		{
			"deployer", absent + "?lastModified", 404,
			"Could not find item: auto-trashcan:libs-local/t/absent.bin",
		},
		{
			"admin", absent + "?permissions", 404,
			"Item auto-trashcan:libs-local/t/absent.bin does not exist",
		},
		{"deployer", absent + "?list", 404, "Could not find resource"},
	})
	setTrashcan(t, w.db, false)
	var off []storageAnswer
	for _, as := range []string{"admin", "user", ""} {
		for _, view := range []string{"", "?stats", "?properties", "?lastModified", "?list"} {
			off = append(off, storageAnswer{as, trashed + view, 404, "Could not find resource"})
		}
	}
	wantStorageAnswers(t, w, off)
}

// storageBody is the JSON object GET /api/storage/<target> answers as as,
// failing t unless it answers 200 with media.
func storageBody(t *testing.T, w *itemWorld, as, target, media string) map[string]any {
	t.Helper()
	rec := w.do(http.MethodGet, "/artifactory/api/storage/"+target, as, "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != media {
		t.Fatalf("%s as %q: %d %s %s, want 200 %s", target, as, rec.Code,
			rec.Header().Get("Content-Type"), rec.Body, media)
	}
	return decodeObject(t, rec.Body.String())
}

// TestAccStorageAnswersStats: the statistics view answers a file with its
// local and remote counts, and a file never downloaded, a folder and a root
// with zero counts, each under its download URL as the request named it,
// trailing "/" and a remote's own key included, in a local repository, a
// remote's cache by either key and the trash can.
func TestAccStorageAnswersStats(t *testing.T) {
	w := storageAnswersWorld(t, true)
	setTrashcan(t, w.db, true)
	w.cacheFile("up", "pkg/f.bin", "cached")
	w.cacheFile("up", "pkg/never.bin", "never")
	wantStatus(t, w.do(http.MethodPut, "/artifactory/libs-local/a/never.bin", "admin", "n"),
		http.StatusCreated)
	wantStatus(t, w.do(http.MethodPut, "/artifactory/libs-local/t/gone.bin", "admin", "gone"),
		http.StatusCreated)
	wantStatus(t, w.do(http.MethodDelete, "/artifactory/libs-local/t/gone.bin", "admin", ""),
		http.StatusNoContent)
	const kept = `"downloadCount":3,"lastDownloaded":1700000000000,"lastDownloadedBy":"admin",` +
		`"remoteDownloadCount":2,"remoteLastDownloaded":1700000001000,` +
		`"remoteLastDownloadedBy":"edge"`
	for _, p := range []string{"libs-local/a/file.bin", "up-cache/pkg/f.bin"} {
		wantStatus(t, w.do(http.MethodPatch, "/artifactory/api/metadata/"+p, "admin",
			`{"stats":{`+kept+`}}`), http.StatusNoContent)
	}
	const base = "https://reg.example/artifactory/"
	zero := func(p string) string {
		return `{"uri":"` + base + p + `","downloadCount":0,"lastDownloaded":0,` +
			`"remoteDownloadCount":0,"remoteLastDownloaded":0}`
	}
	counted := func(p string) string { return `{"uri":"` + base + p + `",` + kept + `}` }
	for _, c := range []struct{ target, want string }{
		{"libs-local/a/file.bin", counted("libs-local/a/file.bin")},
		{"libs-local/a/never.bin", zero("libs-local/a/never.bin")},
		{"libs-local/a", zero("libs-local/a")},
		{"libs-local/a/", zero("libs-local/a/")},
		{"libs-local", zero("libs-local")},
		{"libs-local/", zero("libs-local/")},
		{"up/pkg/f.bin", counted("up/pkg/f.bin")},
		{"up-cache/pkg/f.bin", counted("up-cache/pkg/f.bin")},
		{"up/pkg/never.bin", zero("up/pkg/never.bin")},
		{"up/pkg", zero("up/pkg")},
		{"up", zero("up")},
		{"up-cache", zero("up-cache")},
		{"auto-trashcan/libs-local/t/gone.bin", zero("auto-trashcan/libs-local/t/gone.bin")},
		{"auto-trashcan/libs-local/t", zero("auto-trashcan/libs-local/t")},
		{"auto-trashcan", zero("auto-trashcan")},
		{"auto-trashcan/", zero("auto-trashcan/")},
	} {
		got := storageBody(t, w, "admin", c.target+"?stats", mediaStats)
		if want := decodeObject(t, c.want); !reflect.DeepEqual(got, want) {
			t.Errorf("%s?stats: %v, want %v", c.target, got, want)
		}
	}
	// A virtual repository keeps its statistics body until P10 rewrites it
	// (#1537): a member's file under the storage API URL, 400 for a folder.
	got := storageBody(t, w, "admin", "virt/a/file.bin?stats", mediaStats)
	if want := decodeObject(
		t,
		`{"uri":"https://reg.example/artifactory/api/storage/virt/a/file.bin",`+
			`"downloadCount":3,"lastDownloaded":1700000000000,"lastDownloadedBy":"admin"}`,
	); !reflect.DeepEqual(
		got,
		want,
	) {
		t.Errorf("virt/a/file.bin?stats: %v, want %v", got, want)
	}
	wantStorageAnswers(t, w, []storageAnswer{
		{"admin", "virt/a?stats", 400, "/a is a folder; statistics are kept for files"},
	})
}

// TestAccStorageAnswersRemoteKey: the item info, properties, last modified
// and permissions views name a remote's item under its cache's key, by
// either key, as fixPathParamterIfNeededForRemoteCached rewrites the path
// before them; the file list keeps the key as given.
func TestAccStorageAnswersRemoteKey(t *testing.T) {
	w := storageAnswersWorld(t, true)
	w.cacheFile("up", "pkg/f.bin", "cached")
	// A fetch records when the file was modified, which the last modified
	// view reports.
	cached := w.item("up", true, "pkg/f.bin")
	cached.Modified = time.Now()
	if _, err := w.db.Items().Put(context.Background(), cached); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do(http.MethodPut, "/artifactory/api/storage/up-cache/pkg/f.bin?properties=k=v",
		"admin", ""), http.StatusNoContent)
	const (
		storage  = "https://reg.example/artifactory/api/storage/"
		download = "https://reg.example/artifactory/"
	)
	for _, key := range []string{"up", "up-cache"} {
		file := storageBody(t, w, "admin", key+"/pkg/f.bin", mediaFileInfo)
		if file["repo"] != "up-cache" || file["path"] != "/pkg/f.bin" ||
			file["uri"] != storage+"up-cache/pkg/f.bin" ||
			file["downloadUri"] != download+"up-cache/pkg/f.bin" {
			t.Errorf("%s file info %v", key, file)
		}
		folder := storageBody(t, w, "admin", key+"/pkg", mediaFolderInfo)
		if folder["repo"] != "up-cache" || folder["uri"] != storage+"up-cache/pkg" ||
			!reflect.DeepEqual(folder["children"], []any{
				map[string]any{"uri": "/f.bin", "folder": false},
			}) {
			t.Errorf("%s folder info %v", key, folder)
		}
		root := storageBody(t, w, "admin", key, mediaFolderInfo)
		if root["repo"] != "up-cache" || root["uri"] != storage+"up-cache" {
			t.Errorf("%s root info %v", key, root)
		}
		for _, view := range []struct{ query, media, uri string }{
			{"pkg/f.bin?properties", mediaProperties, "up-cache/pkg/f.bin"},
			{"pkg?lastModified", mediaLastModified, "up-cache/pkg/f.bin"},
			{"pkg/f.bin?permissions", "application/json", "up-cache/pkg/f.bin"},
			{"pkg?list", mediaFileList, key + "/pkg"},
		} {
			if got := storageBody(t, w, "admin", key+"/"+view.query, view.media); got["uri"] !=
				storage+view.uri {
				t.Errorf("%s/%s uri %v, want %s", key, view.query, got["uri"], storage+view.uri)
			}
		}
	}
}
