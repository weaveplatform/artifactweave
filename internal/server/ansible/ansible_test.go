package ansible_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format"
	ansiblefmt "github.com/weaveplatform/artifactweave/internal/format/ansible"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/ansible"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const prefix = "/artifactory/api/ansible/ansible-local"

type fixture struct {
	t      *testing.T
	h      http.Handler
	db     *inmem.Store
	store  store.Store
	items  *item.Service
	source *source.Service
	repo   repo.Config
	data   []byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "ansible-local", Class: repo.Local, PackageType: repo.Ansible})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	data, err := os.ReadFile("../../format/ansible/testdata/weave-sample-1.2.3.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: db, items: items, repo: c, data: data}
	f.mount(nil)
	return f
}

func (f *fixture) mount(remote *source.Service) {
	f.source = remote
	t := f.t
	db := f.db
	items := f.items
	router := chi.NewRouter()
	router.Use(baseurl.Middleware(""))
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := auth.Principal{Name: "admin", Admin: true}
			if user := r.Header.Get("X-User"); user != "" && user != "denied" {
				p = auth.Principal{Name: user}
			}
			if r.Header.Get("X-User") == "denied" {
				p = auth.Anonymous
			}
			p, err := auth.Authorize(r.Context(), authz.Sources{
				Permissions: db.Permissions(), Groups: db.Groups(),
			}, p)
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	st := f.store
	if st == nil {
		st = db
	}
	pkgroute.Mount(router, pkgroute.Options{
		Store: st, Items: func() *item.Service { return items },
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
			rc, _, err := items.Blobs.Open(r.Context(), it.Actual.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			if _, err := io.Copy(w, rc); err != nil {
				t.Fatal(err)
			}
		},
		ItemError: func(_ context.Context, _ string, err error) error {
			var rejected *format.RejectionError
			if errors.As(err, &rejected) {
				return apierror.New(rejected.Status, rejected.Message)
			}
			return err
		},
	}, ansible.Protocol(remote))
	f.h = router
}

func (f *fixture) do(
	method, path string,
	body []byte,
	contentType, user string,
) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if user != "" {
		r.Header.Set("X-User", user)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *fixture) publish(data []byte, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	sum := sha256.Sum256(data)
	_ = mw.WriteField("sha256", hex.EncodeToString(sum[:]))
	file, err := mw.CreateFormFile("file", "weave-sample-1.2.3.tar.gz")
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		f.t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		f.t.Fatal(err)
	}
	return f.do(
		http.MethodPost,
		prefix+"/api/v3/artifacts/collections/",
		body.Bytes(),
		mw.FormDataContentType(),
		user,
	)
}

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body.String())
	}
}

func TestAccGalaxyV3PublishResolveAndDelete(t *testing.T) {
	f := newFixture(t)
	requireStatus(t, f.do(http.MethodGet, prefix+"/", nil, "", ""), http.StatusOK)
	w := f.publish(f.data, "")
	requireStatus(t, w, http.StatusCreated)
	var task map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.do(http.MethodGet, task["task"], nil, "", ""), http.StatusOK)
	requireStatus(
		t,
		f.do(http.MethodGet, prefix+"/api/v3/collections/weave/sample/", nil, "", ""),
		http.StatusOK,
	)
	w = f.do(
		http.MethodGet,
		prefix+"/api/v3/collections/weave/sample/versions/?limit=100&offset=0",
		nil,
		"",
		"",
	)
	requireStatus(t, w, http.StatusOK)
	var versions struct {
		Data []struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &versions); err != nil ||
		len(versions.Data) != 1 || versions.Data[0].Version != "1.2.3" {
		t.Fatalf("versions: %s, %v", w.Body.String(), err)
	}
	w = f.do(http.MethodGet, prefix+"/api/v3/collections/weave/sample/versions/1.2.3/", nil, "", "")
	requireStatus(t, w, http.StatusOK)
	var version struct {
		DownloadURL string `json:"download_url"`
		Artifact    struct {
			SHA256 string `json:"sha256"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &version); err != nil || version.DownloadURL == "" {
		t.Fatalf("version: %s, %v", w.Body.String(), err)
	}
	w = f.do(http.MethodGet, version.DownloadURL, nil, "", "")
	requireStatus(t, w, http.StatusOK)
	if !bytes.Equal(w.Body.Bytes(), f.data) {
		t.Fatal("downloaded archive changed")
	}
	requireStatus(t, f.publish(f.data, ""), http.StatusConflict)
	_, err := f.items.Delete(t.Context(), item.Target{
		Repo: f.repo,
		Path: ansiblefmt.Path("weave", "sample", "1.2.3"),
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, prefix+"/api/v3/collections/weave/sample/versions/", nil, "", "")
	requireStatus(t, w, http.StatusOK)
	if err := json.Unmarshal(w.Body.Bytes(), &versions); err != nil || len(versions.Data) != 0 {
		t.Fatalf("deleted version remained: %s", w.Body.String())
	}
}

func TestAccGalaxyV3RefusesBadArchiveAndDeniedReads(t *testing.T) {
	f := newFixture(t)
	requireStatus(t, f.publish([]byte("invalid"), ""), http.StatusBadRequest)
	requireStatus(t, f.publish(f.data, ""), http.StatusCreated)
	requireStatus(
		t,
		f.do(
			http.MethodGet,
			prefix+"/api/v3/collections/weave/sample/versions/1.2.3/",
			nil,
			"",
			"denied",
		),
		http.StatusUnauthorized,
	)
	requireStatus(
		t,
		f.do(
			http.MethodGet,
			prefix+"/api/v3/plugin/ansible/content/published/collections/artifacts/weave-sample-1.2.3.tar.gz",
			nil,
			"",
			"denied",
		),
		http.StatusUnauthorized,
	)
}

func remoteFixture(t *testing.T) (*fixture, *fakeupstream.Server, repo.Config, string) {
	f := newFixture(t)
	up := fakeupstream.New(t, fakeupstream.Options{})
	c := *repo.Defaults(&repo.Config{
		Key: "ansible-remote", Class: repo.Remote,
		PackageType: repo.Ansible, URL: up.URL,
	})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	resolver, err := remote.NewResolver(f.db, nil,
		remote.ResolverOptions{RevisionKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	policy := &remote.Policy{
		Store: f.db, Blobs: f.items.Blobs, Items: f.items,
		Pool: new(remote.Pool), Options: resolver.Options,
		Key: []byte(strings.Repeat("k", 32)),
	}
	t.Cleanup(policy.Pool.Close)
	svc := &source.Service{
		HTTP: policy, Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return f.db.RemoteCache().Source(ctx, c.Key)
		},
	}
	f.mount(svc)
	sum := sha256.Sum256(f.data)
	up.Set("/api/v3/collections/weave/sample/versions/", fakeupstream.Resource{
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   []byte(`{"data":[{"version":"1.2.3"}],"links":{"next":null}}`),
	})
	version := map[string]any{
		"version": "1.2.3", "namespace": map[string]string{"name": "weave"},
		"collection":   map[string]string{"name": "sample"},
		"download_url": up.URL + "/files/weave-sample-1.2.3.tar.gz",
		"artifact":     map[string]string{"sha256": hex.EncodeToString(sum[:])},
		"metadata":     map[string]any{"dependencies": map[string]string{}},
	}
	versionBytes, err := json.Marshal(version)
	if err != nil {
		t.Fatal(err)
	}
	up.Set("/api/v3/collections/weave/sample/versions/1.2.3/", fakeupstream.Resource{
		Header: http.Header{"Content-Type": {"application/json"}}, Body: versionBytes,
	})
	up.Set("/files/weave-sample-1.2.3.tar.gz", fakeupstream.Resource{
		Header: http.Header{"Content-Type": {"application/octet-stream"}}, Body: f.data,
	})
	path := "/artifactory/api/ansible/ansible-remote"
	return f, up, c, path
}

func TestAccGalaxyRemoteCacheAndOffline(t *testing.T) {
	f, up, c, path := remoteFixture(t)
	w := f.do(http.MethodGet, path+"/api/v3/collections/weave/sample/versions/1.2.3/", nil, "", "")
	requireStatus(t, w, http.StatusOK)
	var doc struct {
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil ||
		!strings.Contains(doc.DownloadURL, "/artifactory/api/ansible/ansible-remote/") ||
		strings.Contains(doc.DownloadURL, up.URL) {
		t.Fatalf("unsafe download URL: %s, %v", w.Body.String(), err)
	}
	w = f.do(http.MethodGet, doc.DownloadURL, nil, "", "")
	requireStatus(t, w, http.StatusOK)
	if !bytes.Equal(w.Body.Bytes(), f.data) {
		t.Fatal("remote archive changed")
	}
	c.Offline = new(true)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	up.Close()
	w = f.do(http.MethodGet, doc.DownloadURL, nil, "", "")
	requireStatus(t, w, http.StatusOK)
	if !bytes.Equal(w.Body.Bytes(), f.data) {
		t.Fatal("offline cache changed")
	}
}

func TestAccGalaxyRejectsMissingOrWrongUploadChecksumAndDeployPermission(t *testing.T) {
	f := newFixture(t)
	for _, digest := range []string{"", strings.Repeat("0", 64)} {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		if digest != "" {
			if err := mw.WriteField("sha256", digest); err != nil {
				t.Fatal(err)
			}
		}
		file, err := mw.CreateFormFile("file", "weave-sample-1.2.3.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(f.data); err != nil {
			t.Fatal(err)
		}
		if err := mw.Close(); err != nil {
			t.Fatal(err)
		}
		requireStatus(
			t,
			f.do(
				"POST",
				prefix+"/api/v3/artifacts/collections/",
				body.Bytes(),
				mw.FormDataContentType(),
				"",
			),
			400,
		)
	}
	requireStatus(t, f.publish(f.data, "reader"), 403)
	if _, err := f.db.Items().
		Get(t.Context(), f.repo.Key, false, ansiblefmt.Path("weave", "sample", "1.2.3")); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("rejected publication left an item: %v", err)
	}
}
