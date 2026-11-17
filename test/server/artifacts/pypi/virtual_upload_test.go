package pypi_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const virtualUploadBase = "/artifactory/api/pypi/virtual/"

func pythonMultipart(t *testing.T, name, version, file string, content []byte, action string) (string, string) {
	t.Helper()
	var out bytes.Buffer
	mw := multipart.NewWriter(&out)
	for k, v := range map[string]string{":action": action, "name": name, "version": version} {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	part, err := mw.CreateFormFile("content", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err = mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), out.String()
}

func pythonPost(t *testing.T, w *harness.RemoteWorld, key, name, version, file string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	media, body := pythonMultipart(t, name, version, file, content, "file_upload")
	return w.Request("POST", "/artifactory/api/pypi/"+key+"/", "admin", body, "Content-Type", media)
}

func pythonSource(t *testing.T, name, version string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	body := []byte("Metadata-Version: 2.1\nName: " + name + "\nVersion: " + version + "\n")
	if err := tw.WriteHeader(&tar.Header{Name: name + "-" + version + "/PKG-INFO", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestAccPypiVirtualPublishBackends(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, target := range []string{"first", "second"} {
				t.Run(target, func(t *testing.T) {
					var mutate []func(*config.Config)
					if backend == "postgres" {
						dsn := platsqltest.ScratchDSN(t, platsqltest.PostgresDSN(t))
						mutate = append(mutate, func(c *config.Config) { c.Database.Driver = "postgres"; c.Database.DSN = dsn })
					}
					w := virtualPythonWorld(t, mutate...)
					pythonConfig(t, w, "virtual", func(c *repo.Config) { c.DefaultDeploymentRepo = target })
					wheel := pypiWheel(t, "demo", "1.0")
					const file = "demo-1.0-py3-none-any.whl"
					r := pythonPost(t, w, "virtual", "demo", "1.0", file, wheel)
					if r.Code != 200 || r.Body.Len() != 0 {
						t.Fatal(r.Code, r.Body.String())
					}
					it, err := w.Runtime.Store.Items().Get(t.Context(), target, false, "demo/1.0/"+file)
					if err != nil {
						t.Fatal(err)
					}
					stored, _, err := w.Runtime.Blobs.Open(t.Context(), it.Actual.SHA256)
					if err != nil {
						t.Fatal(err)
					}
					var actual bytes.Buffer
					if _, err = actual.ReadFrom(stored); err != nil {
						t.Fatal(err)
					}
					_ = stored.Close()
					if !bytes.Equal(actual.Bytes(), wheel) {
						t.Fatal("DDR bytes changed")
					}
					r = pythonPost(t, w, "virtual", "sources", "1.0", "sources-1.0.tar.gz", pythonSource(t, "sources", "1.0"))
					if r.Code != 200 {
						t.Fatal(r.Code, r.Body.String())
					}
					for _, path := range []string{"simple/demo/", "simple/sources/", "pypi/demo/json", "demo/1.0/" + file} {
						r = w.Request("GET", virtualUploadBase+path, "admin", "")
						if r.Code != 200 {
							t.Fatal(path, r.Code, r.Body.String())
						}
					}
					const put = "put/demo-2.0-py3-none-any.whl"
					r = w.Request("PUT", virtualUploadBase+put+";build=42", "admin", string(pypiWheel(t, "demo", "2.0")))
					if r.Code != 201 || decodeObject(t, r.Body.String())["repo"] != target {
						t.Fatal(r.Code, r.Body.String())
					}
					deployed, err := w.Runtime.Store.Items().Get(t.Context(), target, false, put)
					if err != nil {
						t.Fatal(err)
					}
					props, err := w.Runtime.Store.Items().Properties(t.Context(), deployed.ID)
					if err != nil || len(props["build"]) != 1 || props["build"][0] != "42" {
						t.Fatal(props, err)
					}
					r = w.Request("PUT", virtualUploadBase+"reuse/"+file, "admin", "", "X-Checksum-Deploy", "true", "X-Checksum-Sha256", it.Actual.SHA256)
					if r.Code != 201 || decodeObject(t, r.Body.String())["repo"] != target {
						t.Fatal(r.Code, r.Body.String())
					}
					r = w.Request("GET", virtualUploadBase+"simple/demo/", "admin", "")
					if r.Code != 200 || !strings.Contains(r.Body.String(), "demo-2.0") {
						t.Fatal(r.Code, r.Body.String())
					}
					for _, key := range []string{"first", "second"} {
						if key == target {
							continue
						}
						if _, err = w.Runtime.Store.Items().Get(t.Context(), key, false, "demo/1.0/"+file); !errors.Is(err, kernel.ErrNotFound) {
							t.Fatal("wrote another member", err)
						}
					}
				})
			}
		})
	}
}

type virtualUploadBody struct{ reads int }

func (b *virtualUploadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("unexpected upload body read")
}
func (*virtualUploadBody) Close() error { return nil }

func TestAccPypiVirtualPublishRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, user, target string
		hide                             bool
		status                           int
		message, allow                   string
	}{
		{name: "no deployment target", user: "admin", status: 405, allow: "GET", message: "No local repository was configured as local deployment repository for the (virtual) virtual repository."},
		{name: "missing target", user: "admin", target: "gone", status: 405, allow: "GET"},
		{name: "not a member", user: "admin", target: "outside", status: 405, allow: "GET"},
		{name: "reader", user: "reader", target: "first", status: 403},
		{name: "anonymous", target: "first", status: 401},
		{name: "hidden authenticated", user: "reader", target: "first", hide: true, status: 404, message: "Resource not found; Path: 'virtual:'"},
		{name: "hidden anonymous", target: "first", hide: true, status: 404, message: "Resource not found; Path: 'virtual:'"},
		{name: "simple no target", path: "simple", user: "reader", status: 415, message: "Unsupported Media Type"},
		{name: "simple slash", path: "simple/", target: "first", user: "reader", status: 415, message: "Unsupported Media Type"},
		{name: "folder", path: "folder/", target: "first", user: "reader", status: 405, message: "Twine is allowed to deploy packages only to the base level"},
		{name: "PUT no target", method: "PUT", path: "demo/file.whl", user: "admin", status: 405, allow: "GET"},
		{name: "PUT anonymous", method: "PUT", path: "demo/file.whl", target: "first", status: 401},
		{name: "PUT hidden anonymous", method: "PUT", path: "demo/file.whl", target: "first", hide: true, status: 404, message: "Resource not found; Path: 'virtual:demo/file.whl'"},
		{name: "PUT reader", method: "PUT", path: "demo/file.whl", user: "reader", target: "first", status: 403},
		{name: "PUT hidden", method: "PUT", path: "demo/file.whl;k=v", user: "reader", target: "first", hide: true, status: 404, message: "Resource not found; Path: 'virtual:demo/file.whl'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := virtualPythonWorld(t)
			if err := w.Runtime.Store.Settings().Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
			if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{Key: "outside", Class: repo.Local, PackageType: repo.PyPI})); err != nil {
				t.Fatal(err)
			}
			pythonConfig(t, w, "virtual", func(c *repo.Config) { c.DefaultDeploymentRepo = tc.target; c.HideUnauthorizedResources = &tc.hide })
			method := tc.method
			if method == "" {
				method = "POST"
			}
			req := httptest.NewRequest(method, virtualUploadBase+tc.path, nil)
			body := &virtualUploadBody{}
			req.Body = body
			req.Header.Set("Content-Type", "multipart/form-data; boundary=untouched")
			if tc.user != "" {
				req.SetBasicAuth(tc.user, "pw")
			}
			out := httptest.NewRecorder()
			w.H.ServeHTTP(out, req)
			if out.Code != tc.status || body.reads != 0 || out.Header().Get("Allow") != tc.allow || !strings.Contains(out.Body.String(), tc.message) {
				t.Fatalf("status=%d reads=%d allow=%q body=%s", out.Code, body.reads, out.Header().Get("Allow"), out.Body)
			}
			challenge, message := "", tc.message
			switch tc.status {
			case 401:
				challenge, message = apierror.BasicChallenge, kernel.ErrUnauthenticated.Error()
			case 403:
				message = kernel.ErrForbidden.Error()
			}
			if out.Header().Get("WWW-Authenticate") != challenge {
				t.Fatalf("challenge=%q want=%q", out.Header().Get("WWW-Authenticate"), challenge)
			}
			if message != "" {
				expected := fmt.Sprintf("{\"errors\":[{\"message\":%q,\"status\":%d}]}\n", message, tc.status)
				if out.Body.String() != expected {
					t.Fatalf("envelope=%q want=%q", out.Body.String(), expected)
				}
			}
		})
	}
}

func TestAccPypiVirtualPublishTargetRulesAndDirectCompatibility(t *testing.T) {
	w := virtualPythonWorld(t)
	pythonConfig(t, w, "virtual", func(c *repo.Config) {
		c.DefaultDeploymentRepo = "first"
		c.HideUnauthorizedResources = new(true)
		c.ExcludesPattern = new("demo/**")
	})
	body := pypiWheel(t, "demo", "1.0")
	r := pythonPost(t, w, "virtual", "demo", "1.0", "demo-1.0-py3-none-any.whl", body)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r = w.Request("GET", virtualUploadBase+"simple/demo/", "admin", ""); r.Code != 404 {
		t.Fatal("virtual read policy ignored", r.Code)
	}
	pythonConfig(t, w, "first", func(c *repo.Config) { c.ExcludesPattern = new("blocked/**") })
	r = pythonPost(t, w, "virtual", "blocked", "1.0", "blocked-1.0-py3-none-any.whl", pypiWheel(t, "blocked", "1.0"))
	if r.Code != 409 {
		t.Fatal("DDR policy or policy status lost", r.Code, r.Body.String())
	}
	for _, name := range []string{"simple", "packages"} {
		r = pythonPost(t, w, "virtual", name, "1.0", name+"-1.0-py3-none-any.whl", pypiWheel(t, name, "1.0"))
		if r.Code != 400 {
			t.Fatal(name, r.Code, r.Body.String())
		}
	}
	for _, key := range []string{"first", "remote"} {
		want := 200
		if key == "remote" {
			want = 405
		}
		r = pythonPost(t, w, key, "direct", "1.0", "direct-1.0-py3-none-any.whl", pypiWheel(t, "direct", "1.0"))
		if r.Code != want {
			t.Fatal(key, r.Code, r.Body.String())
		}
	}
	media, form := pythonMultipart(t, "demo", "1.0", "demo-1.0-py3-none-any.whl", body, "file_upload")
	for _, suffix := range []string{"simple", "simple/", "folder/"} {
		r = w.Request("POST", "/artifactory/api/pypi/first/"+suffix, "admin", form, "Content-Type", media)
		if r.Code != 405 {
			t.Fatal("local path behavior changed", suffix, r.Code)
		}
	}
}
