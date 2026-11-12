package artifacts_test

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

const archiveDownload = "/artifactory/api/archive/download/"

// archiveWorld is an item world whose folder download setting allows two
// files, with d/a.txt, d/s/b.txt and d/s/c.tmp deployed to libs-local and
// cached for the remote up.
func archiveWorld(t *testing.T) *itemWorld {
	t.Helper()
	w := newItemWorld(t, false)
	if err := w.db.Settings().Put(context.Background(), settings.SettingFolderDownload,
		artmodel.FolderDownloadSettings{Enabled: true, MaxFiles: 2}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"d/a.txt", "d/s/b.txt", "d/s/c.tmp"} {
		w.put("/artifactory/libs-local/"+p, p)
		w.cacheFile("up", p, p)
	}
	return w
}

// cacheFile caches body at p for the remote repository key, with the
// folders above it, as a fetch from its upstream would have.
func (w *itemWorld) cacheFile(key, p, body string) {
	w.t.Helper()
	for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
		parent, name := path.Split(dir)
		if _, err := w.db.Items().Put(context.Background(), artmodel.Item{
			RepoKey: key, Cached: true, Path: strings.TrimSuffix(parent, "/"), Name: name,
			Folder: true,
		}); err != nil {
			w.t.Fatal(err)
		}
	}
	w.storeCached(key, p, body)
}

// archiveNames downloads the folder p as a zip archive, as an
// administrator, and returns the names of the files it holds.
func archiveNames(t *testing.T, w *itemWorld, p string) []string {
	t.Helper()
	rec := w.do(http.MethodGet, archiveDownload+p+"?archiveType=zip", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	return names
}

// A blacked-out repository or remote cache is not downloaded as an
// archive (RTFACT-30253, fixed in 7.68.14 and 7.71.11; RTDEV-38679): after
// read permission, any folder of it answers the blackout's 404, a missing
// one too, because the policy is checked before anything is listed.
func TestAccFolderArchiveOfBlackedOutRepository(t *testing.T) {
	w := archiveWorld(t)
	w.patchRepo("libs-local", func(c *repo.Config) { c.BlackedOut = ptr(true) })
	w.patchRepo("up", func(c *repo.Config) { c.BlackedOut = ptr(true) })
	const msg = " is blacked out and cannot serve artifact "
	for _, c := range []struct{ path, msg string }{
		{"libs-local/d/s", "The repository 'libs-local'" + msg + "'libs-local:d/s'."},
		{"libs-local/", "The repository 'libs-local'" + msg + "'libs-local:'."},
		{"libs-local/missing", "The repository 'libs-local'" + msg + "'libs-local:missing'."},
		{"up-cache/d/s", "The repository 'up'" + msg + "'up:d/s'."},
	} {
		wantRefusal(t, w, http.MethodGet, archiveDownload+c.path+"?archiveType=zip", "admin", c.msg)
	}
	// A caller who may not read the folder is answered as before, without
	// learning how the repository is set (DV-057).
	rec := w.do(http.MethodGet, archiveDownload+"libs-local/d/s?archiveType=zip", "user", "")
	wantStatus(t, rec, http.StatusForbidden)

	w.patchRepo("libs-local", func(c *repo.Config) { c.BlackedOut = ptr(false) })
	w.patchRepo("up", func(c *repo.Config) { c.BlackedOut = ptr(false) })
	for _, key := range []string{"libs-local", "up-cache"} {
		got := archiveNames(t, w, key+"/d/s")
		if !slices.Equal(got, []string{"b.txt", "c.tmp"}) {
			t.Fatalf("%s archive holds %v", key, got)
		}
	}
}

// A folder the repository's patterns reject answers their 404, as the item
// route answers it. A file they reject inside an accepted folder is left out
// of the archive, as a folder listing leaves it out, and does not count
// towards the folder download limits [I].
func TestAccFolderArchiveUnderPatterns(t *testing.T) {
	w := archiveWorld(t)
	// Without patterns, d holds three files, over the limit of two.
	rec := w.do(http.MethodGet, archiveDownload+"libs-local/d?archiveType=zip", "admin", "")
	wantStatus(t, rec, http.StatusBadRequest)
	const msg = " due to its include/exclude pattern settings."
	for _, c := range []struct{ repo, archive string }{
		{"libs-local", "libs-local"},
		{"up", "up-cache"},
	} {
		tmp, hidden := "**/*.tmp", "d/s/**"
		w.patchRepo(c.repo, func(r *repo.Config) { r.ExcludesPattern = &tmp })
		got := archiveNames(t, w, c.archive+"/d")
		if !slices.Equal(got, []string{"a.txt", "s/b.txt"}) {
			t.Fatalf("%s archive holds %v", c.archive, got)
		}
		w.patchRepo(c.repo, func(r *repo.Config) { r.ExcludesPattern = &hidden })
		wantRefusal(t, w, http.MethodGet, archiveDownload+c.archive+"/d/s?archiveType=zip", "admin",
			"The repository '"+c.repo+"' rejected the artifact '"+c.repo+":d/s'"+msg)
		got = archiveNames(t, w, c.archive+"/d")
		if !slices.Equal(got, []string{"a.txt"}) {
			t.Fatalf("%s archive holds %v", c.archive, got)
		}
	}
}
