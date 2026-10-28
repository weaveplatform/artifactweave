package machinelearning_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	ml "github.com/weaveplatform/artifactweave/internal/format/machinelearning"
	"github.com/weaveplatform/artifactweave/internal/server"
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
	c      repo.Config
	server *httptest.Server
}

func newFixture(t *testing.T, class repo.Class, configure ...func(*server.Config)) *fixture {
	t.Helper()
	db := inmem.New()
	b, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "ml", Class: class, PackageType: repo.MachineLearning})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		t:     t,
		db:    db,
		c:     c,
		items: &item.Service{Store: db, Blobs: b, Formats: formats.For},
	}
	config := server.Config{Store: db, Blobs: b}
	for _, apply := range configure {
		apply(&config)
	}
	h := server.New(config)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			auth.Principal{Name: name, Admin: name == "admin", Anonymous: name == "anonymous"},
		)
		if err != nil {
			http.Error(w, "authorization failed", 500)
			return
		}
		h.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixture) request(
	method, path string,
	body []byte,
	media, user string,
	headers ...string,
) (int, []byte, http.Header) {
	f.t.Helper()
	r, err := http.NewRequestWithContext(
		f.t.Context(),
		method,
		f.server.URL+path,
		bytes.NewReader(body),
	)
	if err != nil {
		f.t.Fatal(err)
	}
	if media != "" {
		r.Header.Set("Content-Type", media)
	}
	if user != "" {
		r.Header.Set("X-User", user)
	}
	for i := 0; i < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	resp, err := f.server.Client().Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp.StatusCode, data, resp.Header
}

func (f *fixture) status(want int, method, path string, body []byte, media, user string) []byte {
	f.t.Helper()
	status, data, _ := f.request(method, path, body, media, user)
	if status != want {
		f.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, data)
	}
	return data
}

const api = "/artifactory/api/machinelearning/ml/model/"

func (f *fixture) start(name, version string) map[string]string {
	f.t.Helper()
	path := api + name + "/start-transaction"
	if version != "" {
		path += "/" + version
	}
	data := f.status(200, "POST", path, nil, "", "")
	var tx map[string]string
	if err := json.Unmarshal(data, &tx); err != nil {
		f.t.Fatal(err)
	}
	return tx
}

func checksum(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }

func manifest(data []byte) ml.Manifest {
	return ml.Manifest{
		Created: time.Now().UTC().Format(time.RFC3339Nano),
		Artifacts: []ml.Artifact{
			{
				Path:      "model.pkl",
				Size:      int64(len(data)),
				Checksums: ml.Checksums{SHA256: checksum(data)},
			},
		},
		Format: ml.Serialization{
			Framework:      "files",
			Format:         "pkl",
			Runtime:        "python",
			RuntimeVersion: "3.13",
		},
	}
}

func multipartBody(t *testing.T, m ml.Manifest) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, err := w.CreateFormFile("modelManifest", "modelManifest")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(part).Encode(m); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField(
		"additionalData",
		`{"training_dataset":"controlled","accuracy":"0.99"}`,
	); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), w.FormDataContentType()
}

func (f *fixture) commit(name, version string, tx map[string]string, m ml.Manifest, want int) {
	f.t.Helper()
	body, media := multipartBody(f.t, m)
	f.status(
		want,
		"PUT",
		api+"entity-manifest/"+name+"/"+version+"/"+tx["transactionId"],
		body,
		media,
		"",
	)
}

func TestAccModelPublicationLifecycle(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			data := []byte("controlled model bytes")
			m := manifest(data)
			tx := f.start("classifier", "1.2.3")
			stage := "/artifactory/ml/" + tx["filesUploadPath"] + "/model.pkl"
			f.status(201, "PUT", stage, data, "application/octet-stream", "")
			f.status(404, "GET", stage, nil, "", "")
			f.status(404, "GET", api+"entity-manifest/classifier/1.2.3", nil, "", "")
			bad := m
			bad.Artifacts = append([]ml.Artifact(nil), m.Artifacts...)
			bad.Artifacts[0].Checksums.SHA256 = strings.Repeat("0", 64)
			f.commit("classifier", "1.2.3", tx, bad, 409)
			f.status(
				404,
				"GET",
				"/artifactory/ml/models/classifier/1.2.3/files/model.pkl",
				nil,
				"",
				"",
			)
			f.commit("classifier", "1.2.3", tx, m, 201)
			f.commit("classifier", "1.2.3", tx, m, 201)
			body := f.status(200, "GET", api+"entity-manifest/classifier/1.2.3", nil, "", "")
			var got ml.Manifest
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got.ID != "classifier" || got.Version != "1.2.3" ||
				!strings.Contains(
					got.Artifacts[0].DownloadURL,
					"/models/classifier/1.2.3/files/model.pkl",
				) {
				t.Fatalf("manifest: %+v", got)
			}
			path := "models/classifier/1.2.3/files/model.pkl"
			if got := f.status(
				200,
				"GET",
				"/artifactory/ml/"+path,
				nil,
				"",
				"",
			); !bytes.Equal(
				got,
				data,
			) {
				t.Fatalf("artifact changed: %q", got)
			}
			status, _, headers := f.request("HEAD", "/artifactory/ml/"+path, nil, "", "")
			if status != 200 || headers.Get("X-Checksum-Sha256") != checksum(data) {
				t.Fatalf("HEAD: %d %v", status, headers)
			}
			f.status(
				404,
				"GET",
				"/artifactory/ml/models/classifier/1.2.3/model-manifest.json",
				nil,
				"",
				"",
			)
			f.status(409, "PUT", "/artifactory/ml/"+path, []byte("replacement"), "", "")
			f.status(409, "POST", api+"classifier/start-transaction/1.2.3", nil, "", "")
			f.status(403, "GET", api+"entity-manifest/classifier/1.2.3", nil, "", "outsider")
			it, err := f.db.Items().Get(t.Context(), "ml", false, path)
			if err != nil {
				t.Fatal(err)
			}
			f.status(204, "DELETE", "/artifactory/ml/"+path, nil, "", "")
			f.status(404, "GET", api+"entity-manifest/classifier/1.2.3", nil, "", "")
			if _, err := f.items.Restore(
				t.Context(),
				item.Target{Repo: f.c, Path: path},
				types.TrashRecord{Item: it},
				"admin",
			); err != nil {
				t.Fatal(err)
			}
			if err := f.items.Reindex(t.Context(), f.c); err != nil {
				t.Fatal(err)
			}
			f.status(200, "GET", api+"entity-manifest/classifier/1.2.3", nil, "", "")
			p, err := f.db.Packages().Find(t.Context(), "ml", "classifier")
			if err != nil {
				t.Fatal(err)
			}
			versions, err := f.db.Packages().Versions(t.Context(), p.ID)
			if err != nil || len(versions) != 1 {
				t.Fatalf("versions: %+v %v", versions, err)
			}
		})
	}
}

func TestAccModelTransactionValidation(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, tc := range []struct {
		name, method, path, media, user string
		body                            []byte
		want                            int
	}{
		{"bad name", "POST", api + "bad%20name/start-transaction/1", "", "", nil, 400},
		{"query rejected", "POST", api + "model/start-transaction/1?x=y", "", "", nil, 400},
		{"bad version", "POST", api + "model/start-transaction/tmp", "", "", nil, 400},
		{"denied start", "POST", api + "model/start-transaction/1", "", "outsider", nil, 403},
		{"anonymous start", "POST", api + "model/start-transaction/1", "", "anonymous", nil, 401},
		{"bad transaction", "PUT", api + "entity-manifest/model/1/x", "", "", nil, 400},
		{"wrong media", "PUT", api + "entity-manifest/model/1/1793180000000", "application/json", "", []byte(`{}`), 415},
		{"bad manifest path", "GET", api + "entity-manifest/bad%20name/1", "", "", nil, 400},
		{"unknown repository", "POST", strings.Replace(api, "/ml/", "/absent/", 1) + "model/start-transaction/1", "", "", nil, 404},
	} {
		t.Run(
			tc.name,
			func(t *testing.T) { f.status(tc.want, tc.method, tc.path, tc.body, tc.media, tc.user) },
		)
	}
	tx := f.start("namespace/model", "2")
	data := []byte("model")
	m := manifest(data)
	f.commit("namespace/model", "2", tx, m, 409)
	f.status(201, "PUT", "/artifactory/ml/"+tx["filesUploadPath"]+"/model.pkl", data, "", "")
	f.commit("namespace/model", "2", tx, m, 201)
	f.status(200, "GET", api+"entity-manifest/namespace/model/2", nil, "", "")
	auto := f.start("auto", "")
	parts := strings.Split(auto["filesUploadPath"], "/")
	if len(parts) != 6 || !ml.Transaction(parts[3]) {
		t.Fatalf("auto version path: %v", auto)
	}
	for _, key := range []string{"filesUploadPath", "dependenciesUploadPath", "codeUploadPath"} {
		if auto[key] == "" {
			t.Fatal("missing upload path")
		}
	}
}
