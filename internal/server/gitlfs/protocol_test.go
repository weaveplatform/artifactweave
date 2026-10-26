package gitlfs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/gitlfs"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type fixture struct {
	t      *testing.T
	db     *inmem.Store
	items  *item.Service
	server *httptest.Server
	user   string
}

func newFixture(t *testing.T, class repo.Class) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs, Formats: formats.For}
	key := "lfs"
	if class == repo.Virtual {
		for _, key := range []string{"lfs-empty", "lfs-local"} {
			local := *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.GitLFS})
			if key == "lfs-empty" {
				excluded := "objects/**"
				local.ExcludesPattern = &excluded
			}
			if err = db.Repos().Create(t.Context(), local); err != nil {
				t.Fatal(err)
			}
		}
	}
	c := *repo.Defaults(&repo.Config{Key: key, Class: class, PackageType: repo.GitLFS})
	if class == repo.Virtual {
		c.Repositories = []string{"lfs-empty", "lfs-local"}
		c.DefaultDeploymentRepo = "lfs-local"
	}
	if err = db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err = db.Permissions().
		Create(t.Context(), types.Permission{Name: "lfs-access", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{
				"reader": types.ActionRead, "writer": types.ActionWrite,
			}}, Targets: map[string]types.Target{key: {Include: []string{"objects/**"}}}},
		}}); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(""))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, e := auth.Authorize(r.Context(), authz.Sources{
				Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true,
			}, auth.Principal{Name: r.Header.Get("X-User"), Admin: r.Header.Get("X-User") == ""})
			if e != nil {
				t.Error(e)
				http.Error(w, e.Error(), http.StatusInternalServerError)
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
				http.Error(w, e.Error(), http.StatusInternalServerError)
				return
			}
			defer rc.Close()
			w.Header().Set("Content-Length", fmt.Sprint(it.Size))
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, rc)
			}
		},
	}, gitlfs.Protocol(gitlfs.Options{}))
	f := &fixture{t: t, db: db, items: items, server: httptest.NewServer(r)}
	t.Cleanup(f.server.Close)
	return f
}

type batchReply struct {
	Objects []struct {
		Actions map[string]struct {
			Href string `json:"href"`
		} `json:"actions"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	} `json:"objects"`
}

func (f *fixture) batch(op, oid string, size int64) (int, batchReply) {
	f.t.Helper()
	body, err := json.Marshal(map[string]any{
		"operation": op, "objects": []map[string]any{{"oid": oid, "size": size}},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost,
		f.server.URL+"/artifactory/api/lfs/lfs/objects/batch", bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/vnd.git-lfs+json; charset=utf-8")
	req.Header.Set("Accept", "application/vnd.git-lfs+json")
	if f.user != "" {
		req.Header.Set("X-User", f.user)
	}
	res, err := f.server.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	var reply batchReply
	if err = json.NewDecoder(res.Body).Decode(&reply); err != nil {
		f.t.Fatal(err)
	}
	return res.StatusCode, reply
}

func TestUnitLocalFederatedAndVirtualTransfers(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated, repo.Virtual} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			body := []byte("unchanged Git LFS object bytes\n")
			sum := sha256.Sum256(body)
			oid := hex.EncodeToString(sum[:])
			status, upload := f.batch("upload", oid, int64(len(body)))
			if status != 200 || len(upload.Objects) != 1 ||
				upload.Objects[0].Actions["upload"].Href == "" {
				t.Fatalf("batch upload: status=%d response=%+v", status, upload)
			}
			req, err := http.NewRequest(
				http.MethodPut,
				upload.Objects[0].Actions["upload"].Href,
				bytes.NewReader(body),
			)
			if err != nil {
				t.Fatal(err)
			}
			res, err := f.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatalf("upload status=%d", res.StatusCode)
			}
			status, download := f.batch("download", oid, int64(len(body)))
			if status != 200 || len(download.Objects) != 1 ||
				download.Objects[0].Actions["download"].Href == "" {
				t.Fatalf("batch download: status=%d response=%+v", status, download)
			}
			res, err = f.server.Client().Get(download.Objects[0].Actions["download"].Href)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if err != nil || res.StatusCode != 200 || !bytes.Equal(got, body) {
				t.Fatalf("download status=%d body=%q err=%v", res.StatusCode, got, err)
			}
			status, again := f.batch("upload", oid, int64(len(body)))
			if status != 200 || len(again.Objects) != 1 || len(again.Objects[0].Actions) != 0 {
				t.Fatalf("idempotent upload batch: status=%d response=%+v", status, again)
			}
		})
	}
}

func TestUnitInvalidDigestCannotPublish(t *testing.T) {
	f := newFixture(t, repo.Local)
	good := []byte("original")
	sum := sha256.Sum256(good)
	oid := hex.EncodeToString(sum[:])
	_, upload := f.batch("upload", oid, int64(len(good)))
	req, err := http.NewRequest(http.MethodPut, upload.Objects[0].Actions["upload"].Href,
		bytes.NewReader([]byte("tampered")))
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("tampered upload status=%d", res.StatusCode)
	}
	_, download := f.batch("download", oid, int64(len(good)))
	if len(download.Objects) != 1 || download.Objects[0].Error == nil ||
		download.Objects[0].Error.Code != http.StatusNotFound {
		t.Fatalf("tampered object published: %+v", download)
	}
}

func TestUnitBatchPermissionsDoNotRevealObjects(t *testing.T) {
	f := newFixture(t, repo.Local)
	body := []byte("private lfs bytes")
	sum := sha256.Sum256(body)
	oid := hex.EncodeToString(sum[:])
	f.user = "intruder"
	_, deniedUpload := f.batch("upload", oid, int64(len(body)))
	_, deniedDownload := f.batch("download", oid, int64(len(body)))
	for _, reply := range []batchReply{deniedUpload, deniedDownload} {
		if len(reply.Objects) != 1 || reply.Objects[0].Error == nil ||
			reply.Objects[0].Error.Code != http.StatusForbidden ||
			len(reply.Objects[0].Actions) != 0 {
			t.Fatalf("unexpected denied response: %+v", reply)
		}
	}
	f.user = "reader"
	_, readOnlyUpload := f.batch("upload", oid, int64(len(body)))
	if readOnlyUpload.Objects[0].Error == nil ||
		readOnlyUpload.Objects[0].Error.Code != http.StatusForbidden {
		t.Fatalf("read-only upload response: %+v", readOnlyUpload)
	}
	f.user = "writer"
	_, writeOnlyUpload := f.batch("upload", oid, int64(len(body)))
	if writeOnlyUpload.Objects[0].Actions["upload"].Href == "" {
		t.Fatalf("write-only upload response: %+v", writeOnlyUpload)
	}
	f.user = ""
	_, adminUpload := f.batch("upload", oid, int64(len(body)))
	req, err := http.NewRequest(
		http.MethodPut,
		adminUpload.Objects[0].Actions["upload"].Href,
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin seed upload status=%d", res.StatusCode)
	}
	f.user = "writer"
	_, existingWriteOnly := f.batch("upload", oid, int64(len(body)))
	if existingWriteOnly.Objects[0].Actions["upload"].Href == "" {
		t.Fatalf("write-only batch disclosed existing OID: %+v", existingWriteOnly)
	}
	// A writer gets the same batch action and successful basic transfer for
	// an existing matching object as for an object they publish for the first time.
	for _, tc := range []struct {
		name string
		body []byte
		href string
	}{
		{name: "existing", body: body, href: existingWriteOnly.Objects[0].Actions["upload"].Href},
		{name: "new", body: []byte("another private lfs object")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			href := tc.href
			if href == "" {
				digest := sha256.Sum256(tc.body)
				_, reply := f.batch("upload", hex.EncodeToString(digest[:]), int64(len(tc.body)))
				href = reply.Objects[0].Actions["upload"].Href
			}
			req, err := http.NewRequest(http.MethodPut, href, bytes.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-User", "writer")
			res, err := f.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("write-only transfer status=%d", res.StatusCode)
			}
		})
	}
}

func TestUnitDeleteRestoreAndReindexRebuildBatchView(t *testing.T) {
	f := newFixture(t, repo.Local)
	body := []byte("lifecycle object bytes")
	sum := sha256.Sum256(body)
	oid := hex.EncodeToString(sum[:])
	_, upload := f.batch("upload", oid, int64(len(body)))
	req, err := http.NewRequest(
		http.MethodPut,
		upload.Objects[0].Actions["upload"].Href,
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload status=%d", res.StatusCode)
	}
	c, err := f.db.Repos().Get(t.Context(), "lfs")
	if err != nil {
		t.Fatal(err)
	}
	path := "objects/" + oid[:2] + "/" + oid[2:4] + "/" + oid
	if _, err = f.items.Delete(t.Context(), item.Target{Repo: c, Path: path}, "admin"); err != nil {
		t.Fatal(err)
	}
	_, absent := f.batch("download", oid, int64(len(body)))
	if absent.Objects[0].Error == nil || absent.Objects[0].Error.Code != http.StatusNotFound {
		t.Fatalf("deleted object visible: %+v", absent)
	}
	trash, err := f.db.Trash().Find(t.Context(), "lfs", path)
	if err != nil || len(trash) == 0 {
		t.Fatalf("trash: %v records=%d", err, len(trash))
	}
	if _, err = f.items.Restore(
		t.Context(),
		item.Target{Repo: c, Path: path},
		trash[0].Record,
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if err = f.items.Reindex(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	_, restored := f.batch("download", oid, int64(len(body)))
	if restored.Objects[0].Actions["download"].Href == "" {
		t.Fatalf("restored object unavailable: %+v", restored)
	}
}
