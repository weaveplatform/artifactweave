package conda_test

import (
	"crypto/md5"  //nolint:gosec // the checksum header 7.x sends
	"crypto/sha1" //nolint:gosec // the checksum header and ETag 7.x sends
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	condafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda"
)

// wantStoredDocument fails unless a GET and a HEAD that do sends to path
// answer 200 with the headers 7.33.12 sent on a Conda API repodata.json
// (mamba-org/mamba#2105): the document's type, Cache-Control: no-store
// (cacheControl, for the CEP-16 documents), X-Artifactory-Filename, the
// quoted name alone as Content-Disposition, the three checksums of the
// bytes served, their bare SHA-1 as the ETag, Accept-Ranges and, where the
// document's index has a time, Last-Modified. It returns the GET.
func wantStoredDocument(
	t *testing.T,
	do func(method, path string, headers ...string) *httptest.ResponseRecorder,
	path, name, media, cacheControl string,
	timed bool,
) *httptest.ResponseRecorder {
	t.Helper()
	get := do(http.MethodGet, path)
	wantStatus(t, get, http.StatusOK)
	body := get.Body.Bytes()
	s1, s256, m5 := sha1.Sum(
		body,
	), sha256.Sum256(
		body,
	), md5.Sum(
		body,
	) //nolint:gosec // checksum headers
	want := map[string]string{
		"Content-Type":           media,
		"Cache-Control":          cacheControl,
		"X-Artifactory-Filename": name,
		"Content-Disposition":    `attachment; filename="` + name + `"`,
		"Etag":                   hex.EncodeToString(s1[:]),
		"X-Checksum-Sha1":        hex.EncodeToString(s1[:]),
		"X-Checksum-Sha256":      hex.EncodeToString(s256[:]),
		"X-Checksum-Md5":         hex.EncodeToString(m5[:]),
		"Accept-Ranges":          "bytes",
		"Content-Length":         strconv.Itoa(len(body)),
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := get
		if method == http.MethodHead {
			rec = do(method, path)
			wantStatus(t, rec, http.StatusOK)
			if rec.Body.Len() != 0 {
				t.Errorf("HEAD %s: %d bytes of body", path, rec.Body.Len())
			}
		}
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s %s: %s %q, want %q", method, path, k, got, v)
			}
		}
		modified := rec.Header().Get("Last-Modified")
		if _, err := http.ParseTime(modified); timed && err != nil || !timed && modified != "" {
			t.Errorf("%s %s: Last-Modified %q, want one only when timed (%t)", method, path,
				modified, timed)
		}
	}
	return get
}

// wantRevalidated fails unless the document at path, whose GET was get,
// answers If-None-Match with its ETag, bare or quoted, and If-Modified-Since
// with its time with 304 carrying the ETag, as 7.x's download decides
// (DownloadServiceImpl.isNotModified, 7.161.26; RTFACT-18716 for a virtual
// repository), and another tag or an older time with the document.
func wantRevalidated(
	t *testing.T,
	do func(method, path string, headers ...string) *httptest.ResponseRecorder,
	path string,
	get *httptest.ResponseRecorder,
) {
	t.Helper()
	etag := get.Header().Get("ETag")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, tag := range []string{etag, `"` + etag + `"`, `W/"` + etag + `"`} {
			rec := do(method, path, "If-None-Match", tag)
			if rec.Code != http.StatusNotModified || rec.Header().Get("ETag") != etag ||
				rec.Body.Len() != 0 {
				t.Errorf("%s %s If-None-Match %s: %d ETag %q, want 304 with %s", method, path, tag,
					rec.Code, rec.Header().Get("ETag"), etag)
			}
		}
		if rec := do(method, path, "If-None-Match", `"other"`); rec.Code != http.StatusOK {
			t.Errorf("%s %s If-None-Match other: %d, want 200", method, path, rec.Code)
		}
		if modified := get.Header().Get("Last-Modified"); modified != "" {
			if rec := do(method, path, "If-Modified-Since", modified); rec.Code !=
				http.StatusNotModified {
				t.Errorf("%s %s If-Modified-Since its time: %d, want 304", method, path, rec.Code)
			}
			when, _ := http.ParseTime(modified)
			earlier := when.Add(-time.Hour).Format(http.TimeFormat)
			if rec := do(method, path, "If-Modified-Since", earlier); rec.Code != http.StatusOK {
				t.Errorf("%s %s If-Modified-Since an hour before: %d, want 200", method, path,
					rec.Code)
			}
		}
	}
	body := get.Body.String()
	part := do(http.MethodGet, path, "Range", "bytes=1-", "If-Range", etag)
	if part.Code != http.StatusPartialContent || part.Body.String() != body[1:] ||
		part.Header().Get("ETag") != etag {
		t.Errorf("GET %s Range If-Range %s: %d ETag %q, want 206 with the rest", path, etag,
			part.Code, part.Header().Get("ETag"))
	}
	// 7.x answers a range past the end before it sets the file's headers
	// (RequestResponseHelper.sendBodyResponse, 7.161.26).
	past := do(http.MethodGet, path, "Range", "bytes="+strconv.Itoa(len(body)+10)+"-")
	if past.Code != http.StatusRequestedRangeNotSatisfiable || past.Body.Len() != 0 ||
		past.Header().Get("Content-Range") != "bytes */"+strconv.Itoa(len(body)) ||
		past.Header().Get("Cache-Control") != "" || past.Header().Get("ETag") != "" ||
		past.Header().Get("X-Artifactory-Filename") != "" {
		t.Errorf("GET %s Range past the end: %d %v, want 416 with Content-Range alone", path,
			past.Code, past.Header())
	}
}

// TestAccCondaIndexAnswersAsAStoredFile: a Conda API channel document,
// repodata.json, current_repodata.json and their .zst forms, GET and HEAD,
// from a local, a federated and a virtual repository and from a remote, on
// its first fetch and from its cache, answers with a stored file's headers
// in the package download service's form, and revalidates against its
// SHA-1 and time. 7.x calculates these documents into the repository, or
// caches a remote's, and serves them through its download: 7.33.12 sent
// X-Artifactory-Filename, Content-Disposition: attachment;
// filename="repodata.json", the checksums, the SHA-1 as the ETag,
// Last-Modified, Accept-Ranges and Cache-Control: no-store
// (mamba-org/mamba#2105). The Conda API sent none of them and
// Cache-Control: private, no-store (#1582). notices.json, which 7.x does
// not store, keeps its answer.
func TestAccCondaIndexAnswersAsAStoredFile(t *testing.T) {
	documents := map[string]string{
		"repodata.json": "application/json", "current_repodata.json": "application/json",
		"repodata.json.zst": "application/zstd", "current_repodata.json.zst": "application/zstd",
	}

	t.Run("local, federated and virtual", func(t *testing.T) {
		w := newCondaShardWorld(t, true, nil)
		for _, c := range []repo.Config{
			{Key: "conda-local", Class: repo.Local, PackageType: repo.Conda},
			{Key: "conda-fed", Class: repo.Federated, PackageType: repo.Conda},
			{
				Key: "conda-virtual", Class: repo.Virtual, PackageType: repo.Conda,
				Repositories: []string{"conda-local"},
			},
		} {
			if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
		for _, key := range []string{"conda-local", "conda-fed"} {
			wantStatus(t, w.do(http.MethodPut, "/artifactory/"+key+"/noarch/"+packageName, "admin",
				string(condaFixture(t))), http.StatusCreated)
		}
		do := func(method, path string, headers ...string) *httptest.ResponseRecorder {
			return w.do(method, path, "admin", "", headers...)
		}
		for _, key := range []string{"conda-local", "conda-fed", "conda-virtual"} {
			base := "/artifactory/api/conda/" + key + "/noarch/"
			for name, media := range documents {
				get := wantStoredDocument(t, do, base+name, name, media, "no-store", true)
				wantRevalidated(t, do, base+name, get)
			}
			// The CEP-16 documents take the stored file's headers and keep
			// their Cache-Control [I].
			index := wantStoredDocument(t, do, base+condafmt.ShardIndexFilename,
				condafmt.ShardIndexFilename, "application/zstd", "private, no-store", true)
			decoded, err := condafmt.DecodeShardIndex(index.Body.Bytes())
			if err != nil || len(decoded.Shards) != 1 {
				t.Fatalf("%s shard index: %+v %v", key, decoded, err)
			}
			for _, digest := range decoded.Shards {
				shard := hex.EncodeToString(digest) + ".msgpack.zst"
				wantStoredDocument(t, do, base+shard, shard, "application/zstd",
					"private, max-age=0, must-revalidate, immutable", true)
			}
		}
		// A channel with no package has no stored index and so no time.
		empty := "/artifactory/api/conda/conda-local/linux-64/repodata.json"
		get := wantStoredDocument(t, do, empty, "repodata.json", "application/json",
			"no-store", false)
		wantRevalidated(t, do, empty, get)
		notices := w.do(http.MethodGet, "/artifactory/api/conda/conda-local/noarch/notices.json",
			"admin", "")
		wantStatus(t, notices, http.StatusOK)
		if notices.Header().Get("Cache-Control") != "private, no-store" ||
			notices.Header().Get("X-Artifactory-Filename") != "" ||
			notices.Header().Get("ETag") != "" {
			t.Errorf("notices.json: %v", notices.Header())
		}
	})

	t.Run("remote first fetch and cache", func(t *testing.T) {
		w, _, _ := newCondaRemoteWorld(t)
		w.up.Delete("/noarch/" + condafmt.ShardIndexFilename)
		if err := w.rt.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
			Key: "conda-virtual", Class: repo.Virtual, PackageType: repo.Conda,
			Repositories: []string{"remote"},
		})); err != nil {
			t.Fatal(err)
		}
		index := condafmt.EmptyRepodata("noarch")
		encoded, err := condafmt.EncodeRepodata(index, "", "noarch")
		if err != nil {
			t.Fatal(err)
		}
		w.up.Set("/noarch/repodata.json", fakeupstream.Resource{
			Body:   encoded["repodata.json"],
			Header: http.Header{"Content-Type": {"application/json"}},
		})
		do := func(method, path string, headers ...string) *httptest.ResponseRecorder {
			return w.request(method, path, "admin", "", headers...)
		}
		path := "/artifactory/api/conda/remote/noarch/repodata.json"
		first := wantStoredDocument(t, do, path, "repodata.json", "application/json", "no-store",
			true)
		cached := wantStoredDocument(t, do, path, "repodata.json", "application/json", "no-store",
			true)
		if first.Header().Get("ETag") != cached.Header().Get("ETag") {
			t.Errorf("ETag %q on the first fetch, %q from the cache", first.Header().Get("ETag"),
				cached.Header().Get("ETag"))
		}
		wantRevalidated(t, do, path, cached)
		virtual := "/artifactory/api/conda/conda-virtual/noarch/repodata.json"
		wantRevalidated(t, do, virtual, wantStoredDocument(t, do, virtual, "repodata.json",
			"application/json", "no-store", true))
	})
}
