package huggingface_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/huggingface"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const sha = "0123456789012345678901234567890123456789"

type fixture struct {
	t      *testing.T
	db     *inmem.Store
	items  *item.Service
	server *httptest.Server
}

func newFixture(t *testing.T, class repo.Class, setup ...func(*fixture) *source.Service) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs, Formats: formats.For}
	c := *repo.Defaults(&repo.Config{Key: "hf", Class: class, PackageType: repo.HuggingFaceML})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	keys, err := crypt.New(bytes.Repeat([]byte("h"), 32))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: db, items: items}
	var remoteSources *source.Service
	if len(setup) != 0 {
		remoteSources = setup[0](f)
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(""))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			if name == "" {
				name = "admin"
			}
			p, err := auth.Authorize(
				r.Context(),
				authz.Sources{
					Permissions:      db.Permissions(),
					Groups:           db.Groups(),
					AnonymousEnabled: true,
				},
				auth.Principal{Name: name, Admin: name == "admin"},
			)
			if err != nil {
				t.Error(err)
				http.Error(w, err.Error(), 500)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	pkgroute.Mount(r, pkgroute.Options{
		Store: db, Items: func() *item.Service { return items },
		ItemError: func(_ context.Context, _ string, e error) error { return e },
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
			rc, _, e := blobs.Open(r.Context(), it.Actual.SHA256)
			if e != nil {
				t.Error(e)
				http.Error(w, e.Error(), 500)
				return
			}
			defer rc.Close()
			w.Header().Set("X-Repo-Commit", sha)
			w.Header().Set("ETag", fmt.Sprintf("\"%s\"", it.Actual.SHA256))
			w.Header().Set("Content-Length", fmt.Sprint(it.Size))
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, rc)
			}
		},
	}, huggingface.Protocol(huggingface.Options{Keys: keys, RemoteSources: remoteSources}))
	f.server = httptest.NewServer(r)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixture) request(method, path, media string, body []byte) *http.Response {
	return f.requestAs(method, path, media, body, "")
}

func (f *fixture) requestAs(method, path, media string, body []byte, user string) *http.Response {
	f.t.Helper()
	req, e := http.NewRequest(
		method,
		f.server.URL+"/artifactory/api/huggingfaceml/hf/"+path,
		bytes.NewReader(body),
	)
	if e != nil {
		f.t.Fatal(e)
	}
	if media != "" {
		req.Header.Set("Content-Type", media)
	}
	if user != "" {
		req.Header.Set("X-User", user)
	}
	resp, e := f.server.Client().Do(req)
	if e != nil {
		f.t.Fatal(e)
	}
	return resp
}

func readResponse(t *testing.T, r *http.Response) []byte {
	t.Helper()
	defer r.Body.Close()
	b, e := io.ReadAll(r.Body)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestUnitNativeCommitReadAndIntegrity(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			prefix := "api/models/acme/demo/"
			pre := []byte(`{"files":[{"path":"config.json","sample":"e30=","size":2}]}`)
			r := f.request("POST", prefix+"preupload/"+sha, "application/json", pre)
			b := readResponse(t, r)
			if r.StatusCode != 200 || !bytes.Contains(b, []byte(`"uploadMode":"regular"`)) {
				t.Fatalf("preupload %d %s", r.StatusCode, b)
			}
			lines := `{"key":"header","value":{"summary":"publish"}}` + "\n" + `{"key":"file","value":{"path":"config.json","content":"` + base64.StdEncoding.EncodeToString(
				[]byte(`{}`),
			) + `","encoding":"base64"}}` + "\n"
			r = f.request("POST", prefix+"commit/"+sha, "application/x-ndjson", []byte(lines))
			b = readResponse(t, r)
			if r.StatusCode != 200 {
				t.Fatalf("commit %d %s", r.StatusCode, b)
			}
			r = f.request("GET", prefix+"revision/"+sha, "", nil)
			b = readResponse(t, r)
			if r.StatusCode != 200 || !bytes.Contains(b, []byte(`"rfilename":"config.json"`)) {
				t.Fatalf("info %d %s", r.StatusCode, b)
			}
			r = f.request("HEAD", "acme/demo/resolve/"+sha+"/config.json", "", nil)
			_ = readResponse(t, r)
			if r.StatusCode != 200 || r.Header.Get("X-Repo-Commit") != sha ||
				r.Header.Get("ETag") == "" {
				t.Fatalf("head %d %v", r.StatusCode, r.Header)
			}
			r = f.request("GET", "acme/demo/resolve/"+sha+"/config.json", "", nil)
			b = readResponse(t, r)
			if r.StatusCode != 200 || string(b) != "{}" {
				t.Fatalf("resolve %d %s", r.StatusCode, b)
			}
			bad := strings.Replace(
				lines,
				base64.StdEncoding.EncodeToString([]byte(`{}`)),
				base64.StdEncoding.EncodeToString([]byte(`bad`)),
				1,
			)
			r = f.request("POST", prefix+"commit/"+sha, "application/x-ndjson", []byte(bad))
			_ = readResponse(t, r)
			if r.StatusCode != 409 {
				t.Fatalf("mutable revision: %d", r.StatusCode)
			}
		})
	}
}

func TestUnitSignedLFSAndTamper(t *testing.T) {
	f := newFixture(t, repo.Local)
	content := bytes.Repeat([]byte("A"), 1<<20+1)
	digest := sha256.Sum256(content)
	oid := hex.EncodeToString(digest[:])
	batch := fmt.Sprintf(
		`{"operation":"upload","transfers":["basic"],"hash_algo":"sha256","objects":[{"oid":"%s","size":%d}],"ref":{"name":"refs/heads/main"}}`,
		oid,
		len(content),
	)
	r := f.request(
		"POST",
		"acme/demo.git/info/lfs/objects/batch",
		"application/json",
		[]byte(batch),
	)
	b := readResponse(t, r)
	if r.StatusCode != 200 {
		t.Fatalf("batch %d %s", r.StatusCode, b)
	}
	var doc struct {
		Objects []struct {
			Actions struct {
				Upload struct {
					Href string `json:"href"`
				} `json:"upload"`
			} `json:"actions"`
		} `json:"objects"`
	}
	if e := json.Unmarshal(b, &doc); e != nil || len(doc.Objects) != 1 {
		t.Fatalf("batch JSON: %s %v", b, e)
	}
	datasetBatch := f.request("POST", "datasets/acme/demo.git/info/lfs/objects/batch",
		"application/json", []byte(batch))
	datasetResponse := readResponse(t, datasetBatch)
	if datasetBatch.StatusCode != 200 || !bytes.Contains(datasetResponse, []byte(oid)) {
		t.Fatalf("dataset LFS identity: %d %s", datasetBatch.StatusCode, datasetResponse)
	}
	href := doc.Objects[0].Actions.Upload.Href
	expired, err := url.Parse(href)
	if err != nil {
		t.Fatal(err)
	}
	query := expired.Query()
	query.Set("exp", "0")
	expired.RawQuery = query.Encode()
	req, err := http.NewRequest("PUT", expired.String(), bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	r, err = f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = readResponse(t, r)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("expired signed LFS upload accepted: %d", r.StatusCode)
	}
	req, _ = http.NewRequest("PUT", href+"&size=5", bytes.NewReader(content))
	r, e := f.server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	_ = readResponse(t, r)
	if r.StatusCode == 200 {
		t.Fatal("tampered signed URL accepted")
	}
	// The HMAC authorizes an object identity, not arbitrary bytes at that URL.
	req, _ = http.NewRequest("PUT", href, bytes.NewReader(bytes.Repeat([]byte("B"), len(content))))
	r, e = f.server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	_ = readResponse(t, r)
	if r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong LFS digest accepted: %d", r.StatusCode)
	}
	req, _ = http.NewRequest("PUT", href, bytes.NewReader(content[:len(content)-1]))
	r, e = f.server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	_ = readResponse(t, r)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong LFS length accepted: %d", r.StatusCode)
	}
	req, _ = http.NewRequest("PUT", href, bytes.NewReader(content))
	r, e = f.server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	b = readResponse(t, r)
	if r.StatusCode != 200 {
		t.Fatalf("lfs upload %d %s", r.StatusCode, b)
	}
	r = f.request("POST", "acme/demo.git/info/lfs/objects/batch", "application/json", []byte(batch))
	b = readResponse(t, r)
	if r.StatusCode != 200 || bytes.Contains(b, []byte(`"actions"`)) {
		t.Fatalf("existing LFS blob requested another upload: %d %s", r.StatusCode, b)
	}
	line := fmt.Sprintf(
		`{"key":"header","value":{"summary":"large"}}`+"\n"+`{"key":"lfsFile","value":{"path":"weights.bin","algo":"sha256","oid":"%s","size":%d}}`+"\n",
		oid,
		len(content),
	)
	r = f.request("POST", "api/models/acme/demo/commit/"+sha, "application/x-ndjson", []byte(line))
	b = readResponse(t, r)
	if r.StatusCode != 200 {
		t.Fatalf("lfs commit %d %s", r.StatusCode, b)
	}
	r = f.request("POST", "api/models/acme/demo/commit/"+sha, "application/x-ndjson", []byte(line))
	b = readResponse(t, r)
	if r.StatusCode != 200 {
		t.Fatalf("idempotent LFS commit %d %s", r.StatusCode, b)
	}
	r = f.request("GET", "api/models/acme/demo/tree/"+sha+"?recursive=true", "", nil)
	b = readResponse(t, r)
	if r.StatusCode != 200 || !bytes.Contains(b, []byte(oid)) ||
		!bytes.Contains(b, []byte(`"lfs"`)) {
		t.Fatalf("LFS tree omitted pointer metadata: %d %s", r.StatusCode, b)
	}
	r = f.request("GET", "acme/demo/resolve/"+sha+"/weights.bin", "", nil)
	b = readResponse(t, r)
	if r.StatusCode != 200 || !bytes.Equal(b, content) {
		t.Fatalf("lfs resolve %d len=%d", r.StatusCode, len(b))
	}
	replacement := bytes.Repeat([]byte("B"), len(content))
	replacementHash := sha256.Sum256(replacement)
	replacementOID := hex.EncodeToString(replacementHash[:])
	upload, err := f.items.Blobs.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Append(t.Context(), bytes.NewReader(replacement)); err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Commit(t.Context(), blob.Digests{SHA256: replacementOID}); err != nil {
		t.Fatal(err)
	}
	replacementLine := fmt.Sprintf(
		`{"key":"header","value":{"summary":"large"}}`+"\n"+`{"key":"lfsFile","value":{"path":"weights.bin","algo":"sha256","oid":"%s","size":%d}}`+"\n",
		replacementOID,
		len(replacement),
	)
	r = f.request(
		"POST",
		"api/models/acme/demo/commit/"+sha,
		"application/x-ndjson",
		[]byte(replacementLine),
	)
	b = readResponse(t, r)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("immutable LFS revision replaced: %d %s", r.StatusCode, b)
	}
}

type faultingBlobStore struct {
	blob.Store
	statErr  error
	beginErr error
}

func (b faultingBlobStore) Stat(ctx context.Context, digest string) (blob.Info, error) {
	if b.statErr != nil {
		return blob.Info{}, b.statErr
	}
	return b.Store.Stat(ctx, digest)
}

func (b faultingBlobStore) BeginUpload(
	ctx context.Context,
	opts ...blob.UploadOption,
) (blob.Upload, error) {
	if b.beginErr != nil {
		return nil, b.beginErr
	}
	return b.Store.BeginUpload(ctx, opts...)
}

func TestUnitLFSStorageFailuresNeverAdvertiseOrAcceptAnUpload(t *testing.T) {
	f := newFixture(t, repo.Local)
	base := f.items.Blobs
	content := bytes.Repeat([]byte("L"), 1<<20+1)
	digest := sha256.Sum256(content)
	oid := hex.EncodeToString(digest[:])
	batch := fmt.Sprintf(
		`{"operation":"upload","objects":[{"oid":"%s","size":%d}]}`,
		oid, len(content),
	)
	broken := errors.New("blob backend unavailable")
	f.items.Blobs = faultingBlobStore{Store: base, statErr: broken}
	r := f.request(http.MethodPost, "acme/demo.git/info/lfs/objects/batch",
		"application/json", []byte(batch))
	if body := readResponse(t, r); r.StatusCode < 500 || bytes.Contains(body, []byte("href")) {
		t.Fatalf("batch advertised an upload during blob failure: %d %s", r.StatusCode, body)
	}
	f.items.Blobs = base
	r = f.request(http.MethodPost, "acme/demo.git/info/lfs/objects/batch",
		"application/json", []byte(batch))
	body := readResponse(t, r)
	var doc struct {
		Objects []struct {
			Actions struct {
				Upload struct {
					Href string `json:"href"`
				} `json:"upload"`
			} `json:"actions"`
		} `json:"objects"`
	}
	if r.StatusCode != http.StatusOK || json.Unmarshal(body, &doc) != nil ||
		len(doc.Objects) != 1 || doc.Objects[0].Actions.Upload.Href == "" {
		t.Fatalf("valid LFS batch did not produce a signed upload: %d %s", r.StatusCode, body)
	}
	f.items.Blobs = faultingBlobStore{Store: base, beginErr: broken}
	req, err := http.NewRequest(http.MethodPut, doc.Objects[0].Actions.Upload.Href,
		bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	r, err = f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if body := readResponse(t, r); r.StatusCode < 500 {
		t.Fatalf("LFS object accepted during blob failure: %d %s", r.StatusCode, body)
	}
	if _, err := base.Stat(t.Context(), oid); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("failed LFS upload published object: %v", err)
	}
}

func TestAccPinnedClient(t *testing.T) {
	python := os.Getenv("HF_CLIENT_PYTHON")
	if python == "" {
		t.Skip("set HF_CLIENT_PYTHON to pinned huggingface_hub 1.18.0 environment")
	}
	f := newFixture(t, repo.Local)
	root := t.TempDir()
	if e := os.WriteFile(
		filepath.Join(root, "config.json"),
		[]byte(`{"model_type":"bert"}`),
		0o600,
	); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(
		filepath.Join(root, "weights.bin"),
		bytes.Repeat([]byte("X"), 1<<20+1),
		0o600,
	); e != nil {
		t.Fatal(e)
	}
	script := `import os,sys
from huggingface_hub import HfApi,snapshot_download
api=HfApi(endpoint=os.environ['HF_ENDPOINT'],token='fixture-token')
commit=api.upload_folder(repo_id='acme/demo',folder_path=sys.argv[1])
assert len(commit.oid)==40
out=snapshot_download(repo_id='acme/demo',endpoint=os.environ['HF_ENDPOINT'],token='fixture-token',local_dir=sys.argv[2],max_workers=1)
assert open(os.path.join(out,'config.json'),'rb').read()==b'{"model_type":"bert"}'
assert os.stat(os.path.join(out,'weights.bin')).st_size==1048577
`
	cmd := exec.Command(python, "-c", script, root, filepath.Join(t.TempDir(), "download"))
	cmd.Env = append(
		os.Environ(),
		"HF_ENDPOINT="+f.server.URL+"/artifactory/api/huggingfaceml/hf",
		"HF_HUB_DISABLE_XET=1",
		"HF_HUB_DISABLE_TELEMETRY=1",
	)
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("pinned client: %v\n%s", e, b)
	}
}

func TestUnitDeniedModelReadsAndWrites(t *testing.T) {
	f := newFixture(t, repo.Local)
	body := `{"key":"header","value":{"summary":"publish"}}` + "\n" + `{"key":"file","value":{"path":"config.json","content":"e30=","encoding":"base64"}}` + "\n"
	r := f.request("POST", "api/models/acme/demo/commit/"+sha, "application/x-ndjson", []byte(body))
	b := readResponse(t, r)
	if r.StatusCode != 200 {
		t.Fatalf("admin publish %d %s", r.StatusCode, b)
	}
	r = f.requestAs("GET", "api/models/acme/demo/revision/"+sha, "", nil, "intruder")
	_ = readResponse(t, r)
	if r.StatusCode != 404 {
		t.Fatalf("denied info leaked: %d", r.StatusCode)
	}
	r = f.requestAs("GET", "acme/demo/resolve/"+sha+"/config.json", "", nil, "intruder")
	_ = readResponse(t, r)
	if r.StatusCode != 404 {
		t.Fatalf("denied file leaked: %d", r.StatusCode)
	}
	r = f.requestAs(
		"POST",
		"api/models/acme/demo/preupload/"+sha,
		"application/json",
		[]byte(`{"files":[{"path":"other.txt","sample":"eA==","size":1}]}`),
		"intruder",
	)
	_ = readResponse(t, r)
	if r.StatusCode != 403 {
		t.Fatalf("denied preupload: %d", r.StatusCode)
	}
	r = f.requestAs(
		"POST",
		"api/models/acme/demo/commit/"+sha,
		"application/x-ndjson",
		[]byte(body),
		"intruder",
	)
	_ = readResponse(t, r)
	if r.StatusCode != 403 {
		t.Fatalf("denied commit: %d", r.StatusCode)
	}
}

func TestUnitRepeatedMainCommitKeepsRevision(t *testing.T) {
	f := newFixture(t, repo.Local)
	body := `{"key":"header","value":{"summary":"publish"}}` + "\n" + `{"key":"file","value":{"path":"config.json","content":"e30=","encoding":"base64"}}` + "\n"
	var revision string
	for i := 0; i < 2; i++ {
		r := f.request(
			"POST",
			"api/models/acme/demo/commit/main",
			"application/x-ndjson",
			[]byte(body),
		)
		b := readResponse(t, r)
		if r.StatusCode != 200 {
			t.Fatalf("commit %d: %d %s", i, r.StatusCode, b)
		}
		var doc struct {
			CommitOID string `json:"commitOid"`
		}
		if e := json.Unmarshal(b, &doc); e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			revision = doc.CommitOID
		} else if revision != doc.CommitOID {
			t.Fatal("idempotent retry changed revision")
		}
	}
	r := f.request("GET", "api/models/acme/demo/revision/main", "", nil)
	b := readResponse(t, r)
	if r.StatusCode != 200 || !bytes.Contains(b, []byte(revision)) {
		t.Fatalf("main ref %d %s", r.StatusCode, b)
	}
}

func TestUnitIncrementalCommitsPreserveSnapshotHistory(t *testing.T) {
	for _, kind := range []string{"models", "datasets"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, repo.Local)
			commit := func(branch string, operations ...string) string {
				t.Helper()
				body := `{"key":"header","value":{"summary":"publish"}}` + "\n" +
					strings.Join(operations, "\n") + "\n"
				r := f.request("POST", "api/"+kind+"/acme/demo/commit/"+branch,
					"application/x-ndjson", []byte(body))
				b := readResponse(t, r)
				if r.StatusCode != http.StatusOK {
					t.Fatalf("commit %s: %d %s", branch, r.StatusCode, b)
				}
				var doc struct {
					CommitOID string `json:"commitOid"`
				}
				if err := json.Unmarshal(b, &doc); err != nil || len(doc.CommitOID) != 40 {
					t.Fatalf("invalid commit response: %s: %v", b, err)
				}
				return doc.CommitOID
			}
			file := func(path, content string) string {
				return fmt.Sprintf(
					`{"key":"file","value":{"path":%q,"content":%q,"encoding":"base64"}}`,
					path,
					base64.StdEncoding.EncodeToString([]byte(content)),
				)
			}
			readFile := func(revision, path, want string) {
				t.Helper()
				prefix := ""
				if kind == "datasets" {
					prefix = "datasets/"
				}
				r := f.request("GET", prefix+"acme/demo/resolve/"+revision+"/"+path, "", nil)
				b := readResponse(t, r)
				if r.StatusCode != http.StatusOK || string(b) != want {
					t.Fatalf("%s %s: %d %q, want %q", revision, path, r.StatusCode, b, want)
				}
			}
			checkTree := func(revision string, want ...string) {
				t.Helper()
				r := f.request(
					"GET",
					"api/"+kind+"/acme/demo/tree/"+revision+"?recursive=true",
					"",
					nil,
				)
				b := readResponse(t, r)
				if r.StatusCode != http.StatusOK {
					t.Fatalf("tree %s: %d %s", revision, r.StatusCode, b)
				}
				var entries []struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(b, &entries); err != nil {
					t.Fatal(err)
				}
				got := make([]string, 0, len(entries))
				for _, entry := range entries {
					got = append(got, entry.Path)
				}
				slices.Sort(got)
				slices.Sort(want)
				if !slices.Equal(got, want) {
					t.Fatalf("tree %s: got %v, want %v", revision, got, want)
				}
			}

			first := commit("main", file("config.json", "old"))
			second := commit("main", file("weights.bin", "weights"))
			if first == second {
				t.Fatal("incremental commit reused its parent's revision")
			}
			checkTree("main", "config.json", "weights.bin")
			readFile("main", "config.json", "old")
			readFile(first, "config.json", "old")
			checkTree(first, "config.json")

			third := commit("main", file("config.json", "new"))
			checkTree("main", "config.json", "weights.bin")
			readFile(second, "config.json", "old")
			readFile("main", "config.json", "new")
			commit("main", `{"key":"deletedFile","value":{"path":"weights.bin"}}`)
			checkTree("main", "config.json")
			checkTree(third, "config.json", "weights.bin")

			branchFirst := commit("dev", file("config.json", "branch"))
			branchSecond := commit("dev", file("weights.bin", "branch weights"))
			if branchFirst == branchSecond {
				t.Fatal("branch commit reused its parent's revision")
			}
			checkTree("dev", "config.json", "weights.bin")
			readFile(branchFirst, "config.json", "branch")
			readFile("main", "config.json", "new")
			common := file("shared.txt", "same operation body")
			mainNext := commit("main", common)
			branchNext := commit("dev", common)
			if mainNext == branchNext {
				t.Fatal("same operation body on different parents collided")
			}
			checkTree("main", "config.json", "shared.txt")
			checkTree("dev", "config.json", "shared.txt", "weights.bin")
			withFolder := commit("main", file("nested/a.txt", "a"), file("nested/b.txt", "b"))
			checkTree("main", "config.json", "nested/a.txt", "nested/b.txt", "shared.txt")
			commit("main", `{"key":"deletedFolder","value":{"path":"nested/"}}`)
			checkTree("main", "config.json", "shared.txt")
			checkTree(withFolder, "config.json", "nested/a.txt", "nested/b.txt", "shared.txt")
		})
	}
}

func TestUnitCommitURLIsHubRepositoryURL(t *testing.T) {
	f := newFixture(t, repo.Local)
	body := []byte(`{"key":"header","value":{"summary":"publish"}}` + "\n" +
		`{"key":"file","value":{"path":"config.json","content":"e30=","encoding":"base64"}}` + "\n")
	for _, tc := range []struct {
		kind, repoPrefix string
	}{
		{kind: "models", repoPrefix: ""},
		{kind: "datasets", repoPrefix: "datasets/"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			r := f.request(
				"POST",
				"api/"+tc.kind+"/acme/demo/commit/main",
				"application/x-ndjson",
				body,
			)
			b := readResponse(t, r)
			if r.StatusCode != http.StatusOK {
				t.Fatalf("commit: %d %s", r.StatusCode, b)
			}
			var doc struct {
				CommitOID string `json:"commitOid"`
				CommitURL string `json:"commitUrl"`
			}
			if err := json.Unmarshal(b, &doc); err != nil {
				t.Fatal(err)
			}
			want := f.server.URL + "/artifactory/api/huggingfaceml/hf/" +
				tc.repoPrefix + "acme/demo/commit/" + doc.CommitOID
			if doc.CommitURL != want {
				t.Fatalf("commit URL = %q, want %q", doc.CommitURL, want)
			}
		})
	}
}
