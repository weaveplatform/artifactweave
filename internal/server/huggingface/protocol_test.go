package huggingface_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	href := doc.Objects[0].Actions.Upload.Href
	req, _ := http.NewRequest("PUT", href+"&size=5", bytes.NewReader(content))
	r, e := f.server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	_ = readResponse(t, r)
	if r.StatusCode == 200 {
		t.Fatal("tampered signed URL accepted")
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
	r = f.request("GET", "acme/demo/resolve/"+sha+"/weights.bin", "", nil)
	b = readResponse(t, r)
	if r.StatusCode != 200 || !bytes.Equal(b, content) {
		t.Fatalf("lfs resolve %d len=%d", r.StatusCode, len(b))
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
