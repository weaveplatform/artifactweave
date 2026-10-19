package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tfFileList is the Terraform provider's FileListAPIModel, which the
// artifactory_file_list data source decodes the response into
// (pkg/artifactory/datasource/artifact/datasource_artifactory_file_list.go in
// jfrog/terraform-provider-artifactory). The acceptance harness does not run
// Terraform, so these tests decode into the same shape: Size is an int64 and
// the times are time.Time, so a string size or a time that is not RFC 3339
// fails here as it fails the data source.
type tfFileList struct {
	URI     string    `json:"uri"`
	Created time.Time `json:"created"`
	Files   []struct {
		URI                string    `json:"uri"`
		Size               int64     `json:"size"`
		LastModified       time.Time `json:"lastModified"`
		IsFolder           bool      `json:"folder"`
		SHA1               string    `json:"sha1"`
		SHA2               string    `json:"sha2"`
		MetadataTimestamps *struct {
			Properties string `json:"properties"`
		} `json:"mdTimestamps"`
	} `json:"files"`
}

// tfList reads a folder as the data source does: every listing parameter
// sent, as 0 or 1, with depth 0 when unset. A folder_path of "/" is sent as
// an empty path, which leaves a trailing slash.
func tfList(
	t *testing.T,
	w *itemWorld,
	repoKey, folder string,
	deep, listFolders, mdTimestamps, includeRootPath bool,
	depth string,
) tfFileList {
	t.Helper()
	bit := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	if folder == "/" {
		folder = ""
	}
	rec := w.do(http.MethodGet, "/artifactory/api/storage/"+repoKey+"/"+folder+
		"?deep="+bit(deep)+"&depth="+depth+"&includeRootPath="+bit(includeRootPath)+
		"&list=&listFolders="+bit(listFolders)+"&mdTimestamps="+bit(mdTimestamps), "admin", "")
	wantStatus(t, rec, http.StatusOK)
	var out tfFileList
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("the data source cannot decode %s: %v", rec.Body, err)
	}
	return out
}

// tfUpload deploys a file and sets test=1 on it, as the provider's
// acceptance tests do (uploadArtifact).
func tfUpload(t *testing.T, w *itemWorld, repoKey, path string) {
	t.Helper()
	wantStatus(t, w.do(http.MethodPut, "/artifactory/"+repoKey+"/"+path, "admin", path),
		http.StatusCreated)
	wantStatus(
		t,
		w.do(http.MethodPut, "/artifactory/api/storage/"+repoKey+"/"+path+"?properties=test=1",
			"admin", ""),
		http.StatusNoContent,
	)
}

// TestStorageListTerraformDataSource repeats the provider's acceptance
// tests of the artifactory_file_list data source, which JFrog runs against
// a live Artifactory, one subtest each.
func TestStorageListTerraformDataSource(t *testing.T) {
	const base = "https://reg.example/artifactory/api/storage/"
	t.Run("TestAccDataSourceFileList", func(t *testing.T) {
		w := newItemWorld(t, false)
		tfUpload(t, w, "libs-local", "foo/bar.txt")
		got := tfList(t, w, "libs-local", "foo", false, false, false, false, "0")
		if got.URI != base+"libs-local/foo" || got.Created.IsZero() || len(got.Files) != 1 {
			t.Fatalf("list %+v", got)
		}
		f := got.Files[0]
		if f.URI != "/bar.txt" || f.LastModified.IsZero() || f.Size != int64(len("foo/bar.txt")) ||
			f.IsFolder || f.SHA1 != sha1Hex("foo/bar.txt") || f.SHA2 != sha256Hex("foo/bar.txt") ||
			f.MetadataTimestamps != nil {
			t.Fatalf("file %+v", f)
		}
	})
	t.Run("TestAccDataSourceFileList_deep_listing", func(t *testing.T) {
		w := newItemWorld(t, false)
		tfUpload(t, w, "libs-local", "foo/bar/fizz.txt")
		got := tfList(t, w, "libs-local", "/", true, false, false, false, "3")
		if got.URI != base+"libs-local" || len(got.Files) != 1 ||
			got.Files[0].URI != "/foo/bar/fizz.txt" || got.Files[0].SHA2 == "" {
			t.Fatalf("list %+v", got)
		}
	})
	listed := func(got tfFileList) string {
		var out []string
		for _, f := range got.Files {
			kind := "file"
			if f.IsFolder {
				kind = "folder"
			}
			out = append(out, f.URI+" "+kind+" "+strconv.FormatInt(f.Size, 10))
		}
		return strings.Join(out, ", ")
	}
	t.Run("TestAccDataSourceFileList_list_folders", func(t *testing.T) {
		w := newItemWorld(t, false)
		tfUpload(t, w, "libs-local", "foo/bar/fizz.txt")
		got := tfList(t, w, "libs-local", "/", true, true, false, false, "3")
		const want = "/foo folder -1, /foo/bar folder -1, /foo/bar/fizz.txt file 16"
		if got := listed(got); got != want {
			t.Fatalf("files %q, want %q", got, want)
		}
	})
	t.Run("TestAccDataSourceFileList_include_root_path", func(t *testing.T) {
		w := newItemWorld(t, false)
		tfUpload(t, w, "libs-local", "foo/bar/fizz.txt")
		got := tfList(t, w, "libs-local", "/", true, true, false, true, "3")
		const want = "/ folder -1, /foo folder -1, /foo/bar folder -1, /foo/bar/fizz.txt file 16"
		if got := listed(got); got != want {
			t.Fatalf("files %q, want %q", got, want)
		}
		if f := got.Files[3]; f.SHA1 == "" || f.SHA2 == "" || f.LastModified.IsZero() {
			t.Fatalf("file %+v", f)
		}
	})
	t.Run("TestAccDataSourceFileList_metadata_timestamps", func(t *testing.T) {
		w := newItemWorld(t, false)
		before := time.Now().Truncate(time.Millisecond)
		tfUpload(t, w, "libs-local", "foo/bar.txt")
		got := tfList(t, w, "libs-local", "foo", false, false, true, false, "0")
		if len(got.Files) != 1 || got.Files[0].MetadataTimestamps == nil {
			t.Fatalf("list %+v", got)
		}
		when, err := time.Parse(time.RFC3339, got.Files[0].MetadataTimestamps.Properties)
		if err != nil || when.Before(before) || when.After(time.Now()) {
			t.Fatalf("properties time %q: %v", got.Files[0].MetadataTimestamps.Properties, err)
		}
	})
}

// clientFileList is jfrog-client-go's FileListResponse
// (artifactory/services/utils/storageutils.go), whose FileListFile reads
// size as a json.Number.
type clientFileList struct {
	URI     string `json:"uri,omitempty"`
	Created string `json:"created,omitempty"`
	Files   []struct {
		URI                string      `json:"uri,omitempty"`
		Size               json.Number `json:"size,omitempty"`
		LastModified       string      `json:"lastModified,omitempty"`
		Folder             bool        `json:"folder,omitempty"`
		Sha1               string      `json:"sha1,omitempty"`
		Sha2               string      `json:"sha2,omitempty"`
		MetadataTimestamps struct {
			Properties string `json:"properties,omitempty"`
		} `json:"mdTimestamps,omitempty"`
	} `json:"files,omitempty"`
}

// TestStorageListJFrogClient repeats jfrog-client-go's fileListTest
// (tests/artifactorystorage_test.go), an integration test against a live
// Artifactory: four items deployed as uploadDummyFile deploys them, a.in
// with dummy=yes as a matrix parameter, list five entries.
func TestStorageListJFrogClient(t *testing.T) {
	w := newItemWorld(t, false)
	for _, p := range []string{"test/a.in;dummy=yes", "b.in", "c.tar.gz"} {
		wantStatus(
			t,
			w.do(http.MethodPut, "/artifactory/libs-local/"+p, "admin", "a"),
			http.StatusCreated,
		)
	}
	rec := w.do(http.MethodGet, "/artifactory/api/storage/libs-local/"+
		"?list&deep=1&depth=2&listFolders=1&mdTimestamps=1&includeRootPath=1", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	var got clientFileList
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.URI != "https://reg.example/artifactory/api/storage/libs-local" || got.Created == "" ||
		len(got.Files) != 5 {
		t.Fatalf("list %s", rec.Body)
	}
	byURI := map[string]int{}
	for i, f := range got.Files {
		byURI[f.URI] = i
	}
	a, okA := byURI["/test/a.in"]
	b, okB := byURI["/b.in"]
	if !okA || !okB {
		t.Fatalf("list %s", rec.Body)
	}
	if f := got.Files[a]; f.Size != "1" || f.LastModified == "" || f.Folder || f.Sha1 == "" ||
		f.Sha2 == "" || f.MetadataTimestamps.Properties == "" {
		t.Fatalf("a.in %+v", f)
	}
	// b.in has no properties, so no time to report.
	if f := got.Files[b]; f.MetadataTimestamps.Properties != "" {
		t.Fatalf("b.in %+v", f)
	}
}

// TestStorageListPropertiesTimes follows mdTimestamps.properties through
// every route that writes properties. Each changing write stamps the item
// with its own time, a write that leaves none clears it, and a copy, move
// or restore stamps the item it writes rather than carrying the source's
// time, as 6.20 stamps the new node a copy or move creates.
func TestStorageListPropertiesTimes(t *testing.T) {
	w := newItemWorld(t, false)
	const (
		api  = "/artifactory/api/storage/"
		meta = "/artifactory/api/metadata/"
	)
	// times lists repoKey with every entry's properties time, "" for none.
	times := func(repoKey string) map[string]string {
		t.Helper()
		rec := w.do(http.MethodGet, api+repoKey+
			"?list&deep=1&listFolders=1&mdTimestamps=1&includeRootPath=1", "admin", "")
		wantStatus(t, rec, http.StatusOK)
		var got tfFileList
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, f := range got.Files {
			out[f.URI] = ""
			if f.MetadataTimestamps != nil {
				out[f.URI] = f.MetadataTimestamps.Properties
			}
		}
		return out
	}
	// since checks that uri was stamped at or after start. Each write runs
	// a few milliseconds after the one before (next), so a time carried
	// over from an earlier write fails it.
	since := func(what, repoKey, uri string, start time.Time) string {
		t.Helper()
		got := times(repoKey)[uri]
		when, err := time.Parse(time.RFC3339, got)
		if err != nil || when.Before(start.Truncate(time.Millisecond)) || when.After(time.Now()) {
			t.Fatalf("%s: %s properties time %q, want from %s: %v", what, uri, got, start, err)
		}
		return got
	}
	next := func() time.Time {
		time.Sleep(2 * time.Millisecond)
		return time.Now()
	}
	stamp := func(what, repoKey, uri, want string) {
		t.Helper()
		if got := times(repoKey)[uri]; got != want {
			t.Fatalf("%s: %s properties time %q, want %q", what, uri, got, want)
		}
	}

	// A deploy whose matrix parameters carry properties stamps the file,
	// as jfrog-client-go's fileListTest finds for a.in; one without leaves
	// it unstamped, and so do the folders the deploy creates.
	start := time.Now()
	wantStatus(t, w.do(http.MethodPut, "/artifactory/libs-local/d/m.txt;team=core", "admin", "m"),
		http.StatusCreated)
	deployed := since("deploy", "libs-local", "/d/m.txt", start)
	wantStatus(t, w.do(http.MethodPut, "/artifactory/libs-local/d/plain.txt", "admin", "p"),
		http.StatusCreated)
	stamp("deploy without properties", "libs-local", "/d/plain.txt", "")
	stamp("a folder the deploy made", "libs-local", "/d", "")
	stamp("the root", "libs-local", "/", "")

	// Setting what is already there changes nothing.
	next()
	wantStatus(t, w.do(http.MethodPut, api+"libs-local/d/m.txt?properties=team=core", "admin", ""),
		http.StatusNoContent)
	stamp("an unchanged set", "libs-local", "/d/m.txt", deployed)

	start = next()
	wantStatus(t, w.do(http.MethodPut, api+"libs-local/d/plain.txt?properties=a=1", "admin", ""),
		http.StatusNoContent)
	since("set", "libs-local", "/d/plain.txt", start)
	wantStatus(t, w.do(http.MethodDelete, api+"libs-local/d/plain.txt?properties=a", "admin", ""),
		http.StatusNoContent)
	stamp("delete of the last property", "libs-local", "/d/plain.txt", "")
	start = next()
	wantStatus(
		t,
		w.do(http.MethodPatch, meta+"libs-local/d/plain.txt", "admin", `{"props":{"b":"2"}}`),
		http.StatusNoContent,
	)
	since("PATCH metadata", "libs-local", "/d/plain.txt", start)

	// The repository itself, the listing's "/" entry.
	start = next()
	wantStatus(
		t,
		w.do(http.MethodPut, api+"libs-local?properties=owner=x&recursive=0", "admin", ""),
		http.StatusNoContent,
	)
	since("the repository", "libs-local", "/", start)

	start = next()
	wantStatus(t, w.do(
		http.MethodPost,
		"/artifactory/api/copy/libs-local/d/m.txt?to=/other-local/c/m.txt",
		"admin",
		"",
	), http.StatusOK)
	since("copy", "other-local", "/c/m.txt", start)
	stamp("the copy's source", "libs-local", "/d/m.txt", deployed)
	start = next()
	wantStatus(t, w.do(
		http.MethodPost,
		"/artifactory/api/move/other-local/c/m.txt?to=/other-local/v/m.txt",
		"admin",
		"",
	), http.StatusOK)
	since("move", "other-local", "/v/m.txt", start)

	wantStatus(t, w.do(http.MethodDelete, "/artifactory/libs-local/d/m.txt", "admin", ""),
		http.StatusNoContent)
	start = next()
	wantStatus(t, w.do(http.MethodPost,
		"/artifactory/api/trash/restore/libs-local/d/m.txt?to=libs-local", "admin", ""),
		http.StatusAccepted)
	since("restore", "libs-local", "/d/m.txt", start)
}
