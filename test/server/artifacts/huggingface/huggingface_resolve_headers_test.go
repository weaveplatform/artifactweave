package huggingface_test

import (
	"crypto/sha1" //nolint:gosec // Git blob ids are SHA-1 protocol identifiers
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// resolveFile is a file of acme/demo as a resolve answers it.
type resolveFile struct {
	name, media, etag string
	body              []byte
}

// gitBlob is the Git blob id of b, the Hub's ETag of a file kept in Git.
func gitBlob(b []byte) string {
	h := sha1.New() //nolint:gosec // a Git blob id
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(b))
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// resolveFiles are a file kept in Git and one kept in Git LFS, which is
// larger than the 1 MiB a commit takes inline.
func resolveFiles() (resolveFile, resolveFile) {
	small := []byte(`{"model_type":"bert"}`)
	large := make([]byte, 1<<20+10)
	for i := range large {
		large[i] = byte(i * 7)
	}
	sum := sha256.Sum256(large)
	return resolveFile{
		name: "config.json", media: "application/json", etag: gitBlob(small), body: small,
	}, resolveFile{
		name: "model.safetensors", media: "application/octet-stream",
		etag: hex.EncodeToString(sum[:]), body: large,
	}
}

// newResolveWorld is a runtime with the local repository hf, the federated
// hf-f, the virtual hf-v over hf, a Hugging Face remote, remote, over a
// fake Hub, and the virtual hf-rv over remote. The router has a keyring,
// which signs Git LFS uploads.
func newResolveWorld(t *testing.T) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t)
	w.H = harness.Router(harness.Config{
		Store: w.Runtime.Store, Blobs: w.Runtime.Blobs, Auth: w.Runtime.Auth,
		Tokens: w.Runtime.Tokens, Keys: support.TerraformBackendKeys(t),
		RemoteSources: w.Runtime.Artifacts.RemoteSources(),
		RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
	})
	for _, c := range []repo.Config{
		{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML},
		{Key: "hf-f", Class: repo.Federated, PackageType: repo.HuggingFaceML},
		{
			Key: "hf-v", Class: repo.Virtual, PackageType: repo.HuggingFaceML,
			Repositories: []string{"hf"},
		},
		{
			Key: "hf-rv", Class: repo.Virtual, PackageType: repo.HuggingFaceML,
			Repositories: []string{"remote"},
		},
	} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	w.Repo = *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.HuggingFaceML, URL: w.Upstream.URL,
	})
	w.Update()
	return w
}

// publishResolveFiles commits small inline and large through Git LFS to
// acme/demo's main in the repository key, as huggingface_hub uploads them,
// and returns the revision.
func publishResolveFiles(t *testing.T, w *harness.RemoteWorld, key string, small,
	large resolveFile,
) string {
	t.Helper()
	endpoint := "/artifactory/api/huggingfaceml/" + key
	batch := w.Request(http.MethodPost, endpoint+"/acme/demo.git/info/lfs/objects/batch", "admin",
		fmt.Sprintf(`{"operation":"upload","transfers":["basic"],"objects":[{"oid":%q,"size":%d}]}`,
			large.etag, len(large.body)), "Content-Type", "application/vnd.git-lfs+json")
	wantStatus(t, batch, http.StatusOK)
	var doc struct {
		Objects []struct {
			Actions struct {
				Upload struct {
					Href string `json:"href"`
				} `json:"upload"`
			} `json:"actions"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(batch.Body.Bytes(), &doc); err != nil || len(doc.Objects) != 1 {
		t.Fatalf("LFS batch: %s %v", batch.Body, err)
	}
	href, err := url.Parse(doc.Objects[0].Actions.Upload.Href)
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.Request(http.MethodPut, href.RequestURI(), "", string(large.body)),
		http.StatusOK)
	commit := w.Request(http.MethodPost, endpoint+"/api/models/acme/demo/commit/main", "admin",
		`{"key":"header","value":{"summary":"publish"}}`+"\n"+
			fmt.Sprintf(`{"key":"file","value":{"path":%q,"content":%q,"encoding":"base64"}}`,
				small.name, base64.StdEncoding.EncodeToString(small.body))+"\n"+
			fmt.Sprintf(`{"key":"lfsFile","value":{"path":%q,"algo":"sha256","oid":%q,"size":%d}}`,
				large.name, large.etag, len(large.body))+"\n",
		"Content-Type", "application/x-ndjson")
	wantStatus(t, commit, http.StatusOK)
	var result struct {
		OID string `json:"commitOid"`
	}
	if err := json.Unmarshal(commit.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.OID
}

// serveOnHub has the fake Hub serve small and large at revision on main,
// with the info a remote reads first.
func serveOnHub(w *harness.RemoteWorld, revision string, small, large resolveFile) {
	pointer := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n",
		large.etag, len(large.body))
	info, _ := json.Marshal(map[string]any{
		"id": "acme/demo", "sha": revision, "siblings": []map[string]any{
			{"rfilename": small.name, "size": len(small.body), "blobId": small.etag},
			{
				"rfilename": large.name, "size": len(large.body),
				"blobId": gitBlob([]byte(pointer)),
				"lfs": map[string]any{
					"sha256": large.etag, "size": len(large.body), "pointerSize": len(pointer),
				},
			},
		},
	})
	w.Upstream.Set("/api/models/acme/demo/revision/main", fakeupstream.Resource{
		Body: info, Header: http.Header{"Content-Type": {"application/json"}},
	})
	for _, f := range []resolveFile{small, large} {
		w.Upstream.Set("/acme/demo/resolve/main/"+f.name, fakeupstream.Resource{
			Body: f.body, Modified: time.Now().Add(-time.Hour),
			Header: http.Header{
				"Content-Type": {f.media}, "X-Repo-Commit": {revision},
				"ETag": {`"` + f.etag + `"`},
			},
		})
	}
}

// wantHeaders fails unless rec carries exactly the headers want, less the
// request id every answer carries.
func wantHeaders(t *testing.T, label string, rec *httptest.ResponseRecorder,
	want map[string]string,
) {
	t.Helper()
	var got []string
	for k := range rec.Header() {
		if k != "X-Request-Id" {
			got = append(got, k)
		}
	}
	var names []string
	for k, v := range want {
		names = append(names, http.CanonicalHeaderKey(k))
		if rec.Header().Get(k) != v {
			t.Errorf("%s: %s %q, want %q", label, k, rec.Header().Get(k), v)
		}
	}
	slices.Sort(got)
	slices.Sort(names)
	if !slices.Equal(got, names) {
		t.Errorf("%s: headers %v, want %v", label, got, names)
	}
}

// wantResolved fails unless a resolve of f at path, GET and HEAD, answers
// 200 with the headers 7.117.12 sent on a remote resolve (RTFACT-31280):
// the path's type, the length, the bare ETag (the LFS SHA-256, or the Git
// blob id [I]) and X-Repo-Commit, with Accept-Ranges for the ranges it
// serves [I], and none of the item download's X-Artifactory-Filename,
// Content-Disposition, X-Checksum-* or Last-Modified (#1581).
func wantResolved(
	t *testing.T,
	do func(method, path string, headers ...string) *httptest.ResponseRecorder,
	path, commit string,
	f resolveFile,
) {
	t.Helper()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := do(method, path)
		wantStatus(t, rec, http.StatusOK)
		if method == http.MethodGet && rec.Body.String() != string(f.body) ||
			method == http.MethodHead && rec.Body.Len() != 0 {
			t.Errorf("%s %s: %d bytes of body", method, path, rec.Body.Len())
		}
		wantHeaders(t, method+" "+path, rec, map[string]string{
			"Content-Type": f.media, "Content-Length": strconv.Itoa(len(f.body)),
			"ETag": f.etag, "X-Repo-Commit": commit, "Accept-Ranges": "bytes",
		})
	}
}

// wantResolveRevalidated fails unless a resolve of f at path answers an
// If-None-Match naming its ETag, bare, quoted or weak, with 304 carrying
// the ETag and X-Repo-Commit alone, and another tag with the file; serves a
// range, under an If-Range naming the ETag too, and the whole file under
// one naming another tag; answers a range past the end with 416 and its
// Content-Range, as before; and still judges an If-Modified-Since alone
// against the file's time, as it did before [I].
func wantResolveRevalidated(
	t *testing.T,
	do func(method, path string, headers ...string) *httptest.ResponseRecorder,
	path, commit string,
	f resolveFile,
) {
	t.Helper()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, tag := range []string{f.etag, `"` + f.etag + `"`, `W/"` + f.etag + `"`} {
			rec := do(method, path, "If-None-Match", tag)
			wantStatus(t, rec, http.StatusNotModified)
			wantHeaders(t, method+" "+path+" If-None-Match "+tag, rec,
				map[string]string{"ETag": f.etag, "X-Repo-Commit": commit})
		}
		other := do(method, path, "If-None-Match", `"`+strings.Repeat("0", 40)+`"`)
		wantStatus(t, other, http.StatusOK)
		future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
		since := do(method, path, "If-Modified-Since", future)
		wantStatus(t, since, http.StatusNotModified)
		wantHeaders(t, method+" "+path+" If-Modified-Since", since,
			map[string]string{"ETag": f.etag, "X-Repo-Commit": commit})
		past := time.Unix(0, 0).UTC().Format(http.TimeFormat)
		wantStatus(t, do(method, path, "If-Modified-Since", past), http.StatusOK)
	}
	for _, ifRange := range []string{"", f.etag, `"` + f.etag + `"`} {
		part := do(http.MethodGet, path, "Range", "bytes=1-4", "If-Range", ifRange)
		wantStatus(t, part, http.StatusPartialContent)
		if part.Body.String() != string(f.body[1:5]) {
			t.Errorf("GET %s Range If-Range %q: %q", path, ifRange, part.Body)
		}
		wantHeaders(t, "GET "+path+" Range If-Range "+ifRange, part, map[string]string{
			"Content-Type": f.media, "Content-Length": "4",
			"Content-Range": "bytes 1-4/" + strconv.Itoa(len(f.body)),
			"ETag":          f.etag, "X-Repo-Commit": commit, "Accept-Ranges": "bytes",
		})
	}
	whole := do(http.MethodGet, path, "Range", "bytes=1-4", "If-Range", `"other"`)
	wantStatus(t, whole, http.StatusOK)
	if whole.Body.Len() != len(f.body) {
		t.Errorf("GET %s If-Range other: %d bytes, want the whole file", path, whole.Body.Len())
	}
	refused := do(http.MethodGet, path, "Range", "bytes="+strconv.Itoa(len(f.body)+10)+"-")
	wantStatus(t, refused, http.StatusRequestedRangeNotSatisfiable)
	wantHeaders(t, "GET "+path+" Range past the end", refused, map[string]string{
		"Content-Length": "0", "Content-Range": "bytes */" + strconv.Itoa(len(f.body)),
		"X-Repo-Commit": commit,
	})
}

// TestAccHuggingFaceResolveAnswersWithTheHubsHeaders (#1581): a resolve of
// a file kept in Git and of one kept in Git LFS, GET and HEAD, from a
// local, a federated and a virtual repository, and from a remote on its
// first fetch and from its cache and a virtual over it, answers with the
// headers 7.117.12 sent on a remote resolve (RTFACT-31280): the bare
// SHA-256 of an LFS file as its ETag, X-Repo-Commit, the type and the
// length. It answered with the item download's X-Artifactory-Filename,
// Content-Disposition, X-Checksum-* and SHA-1 ETag, and so 304 to that
// SHA-1 and never to the ETag the Hub's client keeps.
func TestAccHuggingFaceResolveAnswersWithTheHubsHeaders(t *testing.T) {
	w := newResolveWorld(t)
	small, large := resolveFiles()
	do := func(method, path string, headers ...string) *httptest.ResponseRecorder {
		return w.Request(method, path, "admin", "", headers...)
	}
	commits := map[string]string{}
	for _, key := range []string{"hf", "hf-f"} {
		commits[key] = publishResolveFiles(t, w, key, small, large)
	}
	commits["hf-v"] = commits["hf"]
	revision := strings.Repeat("c", 40)
	serveOnHub(w, revision, small, large)
	commits["remote"], commits["hf-rv"] = revision, revision

	// The remote's first fetch: a GET of one file and a HEAD of the other.
	base := "/artifactory/api/huggingfaceml/remote/acme/demo/resolve/main/"
	first := do(http.MethodGet, base+small.name)
	wantStatus(t, first, http.StatusOK)
	wantHeaders(t, "first GET "+small.name, first, map[string]string{
		"Content-Type": small.media, "Content-Length": strconv.Itoa(len(small.body)),
		"ETag": small.etag, "X-Repo-Commit": revision, "Accept-Ranges": "bytes",
	})
	head := do(http.MethodHead, base+large.name)
	wantStatus(t, head, http.StatusOK)
	wantHeaders(t, "first HEAD "+large.name, head, map[string]string{
		"Content-Type": large.media, "Content-Length": strconv.Itoa(len(large.body)),
		"ETag": large.etag, "X-Repo-Commit": revision, "Accept-Ranges": "bytes",
	})

	for _, key := range []string{"hf", "hf-f", "hf-v", "remote", "hf-rv"} {
		for _, f := range []resolveFile{small, large} {
			path := "/artifactory/api/huggingfaceml/" + key + "/acme/demo/resolve/main/" + f.name
			wantResolved(t, do, path, commits[key], f)
			wantResolveRevalidated(t, do, path, commits[key], f)
		}
	}
}
