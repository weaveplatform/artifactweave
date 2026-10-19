package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, files[n])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// report decodes a copy or move report.
func report(t *testing.T, body []byte) []map[string]string {
	t.Helper()
	var out struct {
		Messages []map[string]string `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return out.Messages
}

func (w *itemWorld) put(p, body string) {
	w.t.Helper()
	wantStatus(w.t, w.do("PUT", p, "admin", body), http.StatusCreated)
}

func TestCopyAndMoveOverHTTP(t *testing.T) {
	w := newItemWorld(t, false)
	w.put("/artifactory/libs-local/d/a.txt", "a")
	w.put("/artifactory/libs-local/d/b.txt", "b")

	rec := w.do(
		"POST",
		"/artifactory/api/copy/libs-local/d/a.txt?to=/other-local/c/a.txt",
		"admin",
		"",
	)
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != copyOrMoveType {
		t.Fatalf("content type %q", ct)
	}
	if m := report(t, rec.Body.Bytes()); len(m) != 1 || m[0]["level"] != "INFO" {
		t.Fatalf("report %v", m)
	}
	wantStatus(t, w.do("GET", "/artifactory/other-local/c/a.txt", "admin", ""), http.StatusOK)

	rec = w.do("POST", "/artifactory/api/move/libs-local/d?to=/other-local/m&dry=1", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if m := report(t, rec.Body.Bytes()); !strings.HasPrefix(m[0]["message"], "Dry run for moving") {
		t.Fatalf("dry %v", m)
	}
	wantStatus(t, w.do("GET", "/artifactory/libs-local/d/a.txt", "admin", ""), http.StatusOK)
	wantStatus(t, w.do("POST", "/artifactory/api/move/libs-local/d?to=/other-local/m", "admin", ""),
		http.StatusOK)
	wantStatus(t, w.do("GET", "/artifactory/libs-local/d/a.txt", "admin", ""), http.StatusNotFound)
	wantStatus(t, w.do("GET", "/artifactory/other-local/m/b.txt", "admin", ""), http.StatusOK)

	// The whole repository, through a trailing slash, with layouts kept.
	wantStatus(t, w.do("POST",
		"/artifactory/api/copy/other-local/?to=/libs-local/all/&suppressLayouts=1&failFast=0",
		"admin", ""), http.StatusOK)
	wantStatus(t, w.do("GET", "/artifactory/libs-local/all/m/b.txt", "admin", ""), http.StatusOK)
}

// A Docker digest folder keeps the digest's colon, sha256:<hex>, as a 7.x
// local repository does (decision 0031), so a path holding a colon goes
// through deploys, the storage API, properties, AQL, copy, move and the
// folder listing like any other: jf docker build searches the exact path
// and sets its build properties on the folder.
func TestColonPathsOverHTTP(t *testing.T) {
	w := newItemWorld(t, false)
	const dir = "img/sha256:ab12"
	w.put("/artifactory/libs-local/"+dir+"/manifest.json", "m")
	w.put("/artifactory/libs-local/"+dir+"/sha256__cd34", "layer")

	folder := w.do("GET", "/artifactory/api/storage/libs-local/"+dir, "admin", "")
	wantStatus(t, folder, http.StatusOK)
	for _, want := range []string{`"path":"/` + dir + `"`, `"uri":"/manifest.json"`} {
		if !strings.Contains(folder.Body.String(), want) {
			t.Fatalf("folder info lacks %s: %s", want, folder.Body)
		}
	}
	// A client that escapes the colon names the same item.
	file := w.do("GET", "/artifactory/api/storage/libs-local/img/sha256%3Aab12/manifest.json",
		"admin", "")
	wantStatus(t, file, http.StatusOK)
	if !strings.Contains(file.Body.String(), `"path":"/`+dir+`/manifest.json"`) {
		t.Fatalf("file info %s", file.Body)
	}
	wantStatus(t, w.do("PUT",
		"/artifactory/api/storage/libs-local/"+dir+"?properties=build.name=app&recursive=1",
		"admin", ""), http.StatusNoContent)
	if !w.props("libs-local", dir+"/sha256__cd34").Has("build.name", "app") {
		t.Fatal("recursive properties missed the digest folder's files")
	}

	for query, want := range map[string]int{
		`items.find({"repo":"libs-local","path":{"$eq":"img/sha256:ab12"}})`: 2,
		`items.find({"repo":"libs-local","path":{"$match":"img/sha256:*"}})`: 2,
		`items.find({"repo":"libs-local","path":{"$match":"img/sha256_*"}})`: 0,
	} {
		rec := w.do("POST", "/artifactory/api/search/aql", "admin", query)
		wantStatus(t, rec, http.StatusOK)
		var res struct {
			Results []map[string]any `json:"results"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if len(res.Results) != want {
			t.Errorf("%s: %s", query, rec.Body)
		}
	}

	wantStatus(t, w.do("POST",
		"/artifactory/api/copy/libs-local/"+dir+"?to=/other-local/"+dir, "admin", ""),
		http.StatusOK)
	wantStatus(t, w.do("POST",
		"/artifactory/api/move/other-local/"+dir+"?to=/other-local/moved/sha256:ab12", "admin", ""),
		http.StatusOK)
	wantStatus(t, w.do("GET", "/artifactory/other-local/moved/sha256:ab12/manifest.json",
		"admin", ""), http.StatusOK)
	wantStatus(t, w.do("GET", "/artifactory/other-local/"+dir+"/manifest.json", "admin", ""),
		http.StatusNotFound)

	list := w.do("GET", "/artifactory/libs-local/img/", "admin", "")
	if !strings.Contains(list.Body.String(), `<a href="./sha256:ab12/">sha256:ab12/</a>`) {
		t.Fatalf("listing %s", list.Body)
	}
}

// A copied or moved file keeps its creation and last-modified times, as
// Artifactory 7.x does by default (7.125.4 release notes, #349): the
// storage API and AQL report the source's, not the operation's.
func TestCopyAndMoveKeepTimes(t *testing.T) {
	w := newItemWorld(t, false)
	w.put("/artifactory/libs-local/d/a.txt", "a")
	ctx := context.Background()
	src, err := w.db.Items().Get(ctx, "libs-local", false, "d/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	then := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	src.Created, src.Modified, src.Updated = then, then.Add(time.Hour), then.Add(2*time.Hour)
	src.CreatedBy, src.ModifiedBy = "alice", "carol"
	if _, err := w.db.Items().Put(ctx, src); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do("POST",
		"/artifactory/api/copy/libs-local/d/a.txt?to=/other-local/c/a.txt", "admin", ""),
		http.StatusOK)
	wantStatus(t, w.do("POST",
		"/artifactory/api/move/libs-local/d/a.txt?to=/other-local/m/a.txt", "admin", ""),
		http.StatusOK)

	for _, p := range []string{"c/a.txt", "m/a.txt"} {
		rec := w.do("GET", "/artifactory/api/storage/other-local/"+p, "admin", "")
		wantStatus(t, rec, http.StatusOK)
		var info map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		if info["created"] != "2020-01-02T03:04:05.000Z" ||
			info["lastModified"] != "2020-01-02T04:04:05.000Z" ||
			info["lastUpdated"] != "2020-01-02T05:04:05.000Z" ||
			info["createdBy"] != "alice" || info["modifiedBy"] != "carol" {
			t.Errorf("%s: storage info %s", p, rec.Body)
		}
	}
	// The folders the copy made are new, made by the caller.
	rec := w.do("GET", "/artifactory/api/storage/other-local/c", "admin", "")
	if !strings.Contains(rec.Body.String(), `"createdBy":"admin"`) {
		t.Errorf("folder %s", rec.Body)
	}

	query := `items.find({"repo":"other-local","name":"a.txt"}).include("path","created","modified")`
	rec = w.do("POST", "/artifactory/api/search/aql", "admin", query, "Content-Type", "text/plain")
	wantStatus(t, rec, http.StatusOK)
	var res struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || len(res.Results) != 2 {
		t.Fatalf("aql %s", rec.Body)
	}
	for _, r := range res.Results {
		if r["created"] != "2020-01-02T03:04:05.000Z" ||
			r["modified"] != "2020-01-02T04:04:05.000Z" {
			t.Errorf("aql %v", r)
		}
	}
}

// A Cargo repository can name the cargo-default layout Artifactory 7.x
// bundles (#354), and a copy that translates layouts reads it. Before
// cargo-default was bundled, the copy failed to resolve it. cargo-default
// names no organisation, so rust/serde/serde-1.0.197.crate in a
// simple-default repository would take the path other/serde/... takes too:
// the copy answers 200 and reports the crate it refused (#496).
func TestCopyTranslatesToTheCargoLayout(t *testing.T) {
	w := newItemWorld(t, false)
	for key, body := range map[string]string{
		"crates-local": `{"rclass":"local","packageType":"cargo","repoLayoutRef":"cargo-default"}`,
		"simple-local": `{"rclass":"local","packageType":"generic","repoLayoutRef":"simple-default"}`,
	} {
		wantStatus(t, w.do("PUT", "/artifactory/api/repositories/"+key, "admin", body,
			"Content-Type", "application/json"), http.StatusOK)
	}
	rec := w.do("GET", "/artifactory/api/repositories/crates-local", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	var c struct {
		RepoLayoutRef string `json:"repoLayoutRef"`
	}
	err := json.Unmarshal(rec.Body.Bytes(), &c)
	if err != nil || c.RepoLayoutRef != "cargo-default" {
		t.Fatalf("configuration %s", rec.Body)
	}

	w.put("/artifactory/simple-local/rust/serde/serde-1.0.197.crate", "crate")
	rec = w.do("POST",
		"/artifactory/api/copy/simple-local/rust?to=/crates-local/&suppressLayouts=0", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	var report struct {
		Messages []struct{ Level, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil || len(report.Messages) != 2 ||
		report.Messages[0].Level != "ERROR" || report.Messages[0].Message !=
		"Cannot transfer simple-local:rust/serde/serde-1.0.197.crate to "+
			"crates-local:crates/serde/serde-1.0.197.crate: layout cargo-default has no place "+
			"for [orgPath] rust from layout simple-default, so another path could translate to "+
			"crates/serde/serde-1.0.197.crate" {
		t.Fatalf("report %s", rec.Body)
	}
	wantStatus(t,
		w.do("GET", "/artifactory/crates-local/crates/serde/serde-1.0.197.crate", "admin", ""),
		http.StatusNotFound)
}

func TestCopyAndMoveRefused(t *testing.T) {
	w := newItemWorld(t, true)
	w.put("/artifactory/libs-local/d/a.txt", "a")
	w.put("/artifactory/other-local/taken.txt", "taken")
	cached := &item.Service{Store: w.db, Blobs: w.blobs}
	up, err := w.db.Repos().Get(context.Background(), "up")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cached.Deploy(context.Background(), item.Deploy{
		Target: item.Target{
			Repo:   up,
			Cached: true,
			Path:   "lib/x.jar",
		},
		Body: strings.NewReader("x"),
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, as string
		want     int
	}{
		{"/artifactory/api/copy/libs-local/d/a.txt", "admin", 400},
		{"/artifactory/api/copy/libs-local/d/a.txt?to=/other-local/x&dry=maybe", "admin", 400},
		{"/artifactory/api/copy/libs-local/d/a.txt?to=/other-local/x&suppressLayouts=x", "admin", 400},
		{"/artifactory/api/copy/libs-local/d/a.txt?to=/other-local/x&failFast=x", "admin", 400},
		// A source that does not exist is a bad request with a report, as
		// JFrog's copy and move pages say (#400).
		{"/artifactory/api/copy/libs-local/d/missing?to=/other-local/x", "admin", 400},
		{"/artifactory/api/move/libs-local/d/missing?to=/other-local/x", "admin", 400},
		{"/artifactory/api/copy/missing/a?to=/other-local/x", "admin", 400},
		{"/artifactory/api/copy/up-cache/missing.jar?to=/other-local/x", "admin", 400},
		// A caller that may not read the source learns nothing of it.
		{"/artifactory/api/copy/libs-local/d/missing?to=/other-local/x", "user", 403},
		{"/artifactory/api/copy/libs-local/d/a.txt?to=/virt/x", "admin", 400},
		{"/artifactory/api/copy/libs-local/d/a.txt?to=/nowhere/x", "admin", 400},
		{"/artifactory/api/copy/virt/d/a.txt?to=/other-local/x", "admin", 400},
		{"/artifactory/api/copy/libs-local/d?to=/libs-local/d/inside", "admin", 400},
		{"/artifactory/api/move/up/lib/x.jar?to=/other-local/x", "admin", 400},
		{"/artifactory/api/copy/libs-local/d/a.txt?to=/other-local/x", "user", 403},
		{"/artifactory/api/copy/libs-local/d/a.txt?to=/other-local/x", "", 401},
		// The deployer may write libs-local but not delete there.
		{"/artifactory/api/move/libs-local/d/a.txt?to=/libs-local/e/a.txt", "deployer", 403},
		{"/artifactory/api/copy/libs-local/d/a.txt?to=/other-local/x", "deployer", 403},
	} {
		rec := w.do("POST", c.path, c.as, "")
		if rec.Code != c.want {
			t.Errorf("%s as %q: %d, want %d: %s", c.path, c.as, rec.Code, c.want, rec.Body)
		}
		if c.want == 400 {
			if m := report(t, rec.Body.Bytes()); m[0]["level"] != "ERROR" {
				t.Errorf("%s: %v", c.path, m)
			}
		}
	}
	for path, want := range map[string]string{
		"/artifactory/api/move/libs-local/d/missing?to=/other-local/x": "The source libs-local:d/missing does not exist",
		"/artifactory/api/copy/missing/a?to=/other-local/x":            "Repository 'missing' does not exist",
	} {
		rec := w.do("POST", path, "admin", "")
		if ct := rec.Header().Get("Content-Type"); ct != copyOrMoveType {
			t.Errorf("%s: Content-Type %q", path, ct)
		}
		if m := report(t, rec.Body.Bytes()); len(m) != 1 || m[0]["message"] != want {
			t.Errorf("%s: %v", path, m)
		}
	}
	// A remote repository's cache can be copied from, by either key.
	for _, src := range []string{"up-cache", "up"} {
		wantStatus(t, w.do(
			"POST",
			"/artifactory/api/copy/"+src+"/lib/x.jar?to=/other-local/from-"+src+".jar",
			"admin",
			"",
		),
			http.StatusOK)
	}
	// Without the right to delete there, an overwrite fails in the report.
	w.put("/artifactory/libs-local/taken.txt", "taken")
	rec := w.do("POST", "/artifactory/api/copy/libs-local/d/a.txt?to=/libs-local/taken.txt",
		"deployer", "")
	wantStatus(t, rec, http.StatusOK)
	if m := report(t, rec.Body.Bytes()); m[0]["level"] != "ERROR" {
		t.Fatalf("overwrite %v", m)
	}
}

func TestExplodeOverHTTP(t *testing.T) {
	w := newItemWorld(t, false)
	body := zipBytes(t, map[string]string{"inner.txt": "inner", "sub/deep.txt": "deep"})
	rec := w.do("PUT", "/artifactory/libs-local/ex/bundle.zip", "admin", string(body),
		"X-Explode-Archive", "true")
	wantStatus(t, rec, http.StatusCreated)
	if rec.Body.Len() != 0 ||
		rec.Header().Get("Location") != "https://reg.example/artifactory/libs-local/ex" {
		t.Fatalf("answer %v %q", rec.Header(), rec.Body)
	}
	wantStatus(
		t,
		w.do("GET", "/artifactory/libs-local/ex/sub/deep.txt", "admin", ""),
		http.StatusOK,
	)
	wantStatus(
		t,
		w.do("GET", "/artifactory/libs-local/ex/bundle.zip", "admin", ""),
		http.StatusNotFound,
	)
	// The atomic header alone explodes too.
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/at/bundle.zip", "admin", string(body),
		"X-Explode-Archive-Atomic", "true"), http.StatusCreated)
	evil := zipBytes(t, map[string]string{"../evil.txt": "x"})
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/ev/bundle.zip", "admin", string(evil),
		"X-Explode-Archive", "true"), http.StatusBadRequest)
	// Replacing exploded files needs the right to delete them.
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/ex/bundle.zip", "deployer", string(body),
		"X-Explode-Archive", "true"), http.StatusForbidden)
}

func TestArchiveEntryOverHTTP(t *testing.T) {
	w := newItemWorld(t, false)
	body := string(zipBytes(t, map[string]string{"dir/in.txt": "inside"}))
	w.put("/artifactory/libs-local/z/a.zip", body)
	w.put("/artifactory/libs-local/top.zip", body)
	rec := w.do("GET", "/artifactory/libs-local/z/a.zip!/dir/in.txt", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.String() != "inside" || rec.Header().Get("Content-Length") != "6" ||
		rec.Header().Get("X-Artifactory-Filename") != "in.txt" ||
		rec.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("%v %q", rec.Header(), rec.Body)
	}
	rec = w.do("HEAD", "/artifactory/libs-local/z/a.zip!/dir/in.txt", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "6" {
		t.Fatalf("HEAD %v %q", rec.Header(), rec.Body)
	}
	// A single-segment archive takes the entry route itself, and a virtual
	// repository finds the archive in a member.
	for _, p := range []string{"/artifactory/libs-local/top.zip!/dir/in.txt", "/artifactory/virt/z/a.zip!/dir/in.txt"} {
		wantStatus(t, w.do("GET", p, "admin", ""), http.StatusOK)
	}
	for _, c := range []struct {
		path, as string
		want     int
	}{
		{"/artifactory/libs-local/z/a.zip!/missing", "admin", 404},
		{"/artifactory/libs-local/z/none.zip!/dir/in.txt", "admin", 404},
		{"/artifactory/libs-local/z!/x", "admin", 404},
		{"/artifactory/libs-local/z/a.zip!/dir/in.txt", "user", 403},
		{"/artifactory/libs-local/z/a.zip!/dir/in.txt", "", 401},
	} {
		if rec := w.do("GET", c.path, c.as, ""); rec.Code != c.want {
			t.Errorf("%s as %q: %d, want %d", c.path, c.as, rec.Code, c.want)
		}
	}
}

func TestFolderArchiveOverHTTP(t *testing.T) {
	w := newItemWorld(t, true)
	ctx := context.Background()
	w.put("/artifactory/libs-local/d/a.txt", "a")
	w.put("/artifactory/libs-local/d/s/b.txt", "bb")
	const base = "/artifactory/api/archive/download/"
	wantStatus(
		t,
		w.do("GET", base+"libs-local/d?archiveType=zip", "admin", ""),
		http.StatusForbidden,
	)
	if err := w.db.Settings().Put(ctx, types.SettingFolderDownload,
		types.FolderDownloadSettings{Enabled: true, MaxFiles: 2}); err != nil {
		t.Fatal(err)
	}
	rec := w.do("GET", base+"libs-local/d?archiveType=zip", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("Content-Type") != "application/zip" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), `filename="d.zip"`) {
		t.Fatalf("%v", rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil || len(zr.File) != 2 || zr.File[0].Name != "a.txt" ||
		zr.File[1].Name != "s/b.txt" {
		t.Fatalf("zip %v", err)
	}
	rec = w.do("GET", base+"libs-local/d?archiveType=tgz&includeChecksumFiles=true", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	if len(names) != 8 {
		t.Fatalf("tgz entries %v", names)
	}
	w.put("/artifactory/libs-local/more.txt", "x")
	for _, c := range []struct {
		path, as string
		want     int
	}{
		{base + "libs-local/d?archiveType=rar", "admin", 400},
		{base + "libs-local/d?archiveType=zip&includeChecksumFiles=maybe", "admin", 400},
		{base + "libs-local/d/a.txt?archiveType=zip", "admin", 400},
		{base + "libs-local/missing?archiveType=zip", "admin", 404},
		{base + "missing/d?archiveType=zip", "admin", 404},
		{base + "virt/d?archiveType=zip", "admin", 400},
		// The whole repository has three files, over the limit of two.
		{base + "libs-local/?archiveType=zip", "admin", 400},
		{base + "libs-local/d?archiveType=zip", "user", 403},
		{base + "up/x?archiveType=zip", "", 403},
	} {
		if rec := w.do("GET", c.path, c.as, ""); rec.Code != c.want {
			t.Errorf("%s as %q: %d, want %d: %s", c.path, c.as, rec.Code, c.want, rec.Body)
		}
	}
}
