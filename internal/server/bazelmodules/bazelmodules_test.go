package bazelmodules_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format"
	fmtbazel "github.com/weaveplatform/artifactweave/internal/format/bazelmodules"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/bazelmodules"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type fixture struct {
	t      *testing.T
	db     *inmem.Store
	items  *item.Service
	router http.Handler
	local  repo.Config
}

func newFixture(t *testing.T) *fixture {
	return newFixtureWith(t, nil)
}

func newFixtureWith(t *testing.T, makeRemote func(*item.Service) *source.Service) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	local := *repo.Defaults(&repo.Config{Key: "bazel-local", Class: repo.Local, PackageType: repo.BazelModules})
	if err := db.Repos().Create(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware("http://registry.example"))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			if name == "" {
				name = "admin"
			}
			p, e := auth.Authorize(
				r.Context(),
				authz.Sources{
					Permissions:      db.Permissions(),
					Groups:           db.Groups(),
					AnonymousEnabled: true,
				},
				auth.Principal{Name: name, Admin: name == "admin"},
			)
			if e != nil {
				t.Fatal(e)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	var remote *source.Service
	if makeRemote != nil {
		remote = makeRemote(items)
	}
	pkgroute.Mount(r, pkgroute.Options{
		Store: db, Items: func() *item.Service { return items },
		ItemError: func(_ context.Context, _ string, err error) error {
			var refused *format.Rejection
			if errors.As(err, &refused) {
				return apierror.New(refused.Status, refused.Message)
			}
			return err
		},
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
			rc, _, e := blobs.Open(r.Context(), it.Actual.SHA256)
			if e != nil {
				t.Fatal(e)
			}
			defer rc.Close()
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, rc)
			}
		},
	}, bazelmodules.Protocol(remote))
	return &fixture{t: t, db: db, items: items, router: r, local: local}
}

func (f *fixture) request(method, location string) *httptest.ResponseRecorder {
	return f.requestAs(method, location, "admin")
}

func (f *fixture) requestAs(method, location, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, location, nil)
	r.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func (f *fixture) publish(c repo.Config, path string, body []byte) {
	f.t.Helper()
	_, err := f.items.Deploy(
		f.t.Context(),
		item.Deploy{
			Target: item.Target{Repo: c, Path: path},
			Body:   bytes.NewReader(body),
			By:     "admin",
		},
	)
	if err != nil {
		f.t.Fatal(err)
	}
}

func archive(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct{ name, body string }{{"hello-1.0.0/BUILD.bazel", "filegroup(name = \"all\", srcs = [\"message.txt\"], visibility = [\"//visibility:public\"])\n"}, {"hello-1.0.0/message.txt", "hello from Bazel\n"}} {
		if err := tw.WriteHeader(
			&tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.body))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestAccLocalAndFederatedRegistryLifecycle(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t)
			c := f.local
			if class == repo.Federated {
				c = *repo.Defaults(&repo.Config{Key: "bazel-fed", Class: repo.Federated, PackageType: repo.BazelModules})
				if err := f.db.Repos().Create(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
			root := "/artifactory/api/bazelmodules/" + c.Key
			if got := f.request("GET", root+"/modules/hello/metadata.json").Code; got != 404 {
				t.Fatalf("unpublished metadata: %d", got)
			}
			f.publish(
				c,
				"modules/hello/1.0.0/MODULE.bazel",
				[]byte("module(name = \"hello\", version = \"1.0.0\")\n"),
			)
			if got := f.request("GET", root+"/modules/hello/metadata.json").Code; got != 404 {
				t.Fatalf("incomplete version visible: %d", got)
			}
			if got := f.request("GET", root+"/modules/hello/1.0.0/MODULE.bazel").Code; got != 404 {
				t.Fatalf("manifest of incomplete version visible: %d", got)
			}
			f.publish(c, "modules/hello/1.0.0/hello-1.0.0.tar.gz", archive(t))
			if err := (fmtbazel.Handler{}).Reindex(
				t.Context(),
				format.Env{Store: f.db, Blobs: f.items.Blobs},
				c,
			); err != nil {
				t.Fatalf("reindex valid package: %v", err)
			}
			w := f.request("GET", root+"/modules/hello/metadata.json")
			if w.Code != 200 || !strings.Contains(w.Body.String(), "1.0.0") {
				t.Fatalf("metadata %d: %s", w.Code, w.Body.String())
			}
			w = f.request("GET", root+"/modules/hello/1.0.0/source.json")
			if w.Code != 200 {
				t.Fatalf("source %d: %s", w.Code, w.Body.String())
			}
			var source struct {
				URL         string `json:"url"`
				Integrity   string `json:"integrity"`
				ArchiveType string `json:"archive_type"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &source); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(source.URL, "/archives/hello/1.0.0/hello-1.0.0.tar.gz") ||
				!strings.HasPrefix(source.Integrity, "sha256-") ||
				source.ArchiveType != "tar.gz" {
				t.Fatalf("invalid source %+v", source)
			}
			if got := f.request("GET", source.URL).Code; got != 200 {
				t.Fatalf("archive %d", got)
			}
			if got := f.request("HEAD", root+"/modules/hello/1.0.0/MODULE.bazel").Code; got != 200 {
				t.Fatalf("module HEAD %d", got)
			}
			if got := f.request(
				"GET",
				root+"/modules/hello/1.0.0/../source.json",
			).Code; got == 200 {
				t.Fatal("unsafe path admitted")
			}
		})
	}
}

func TestAccRejectsDuplicateArchiveAndReindexDetectsUnexpectedContent(t *testing.T) {
	f := newFixture(t)
	f.publish(
		f.local,
		"modules/hello/1.0.0/MODULE.bazel",
		[]byte("module(name = \"hello\", version = \"1.0.0\")\n"),
	)
	f.publish(f.local, "modules/hello/1.0.0/hello-1.0.0.tar.gz", archive(t))
	_, err := f.items.Deploy(
		t.Context(),
		item.Deploy{
			Target: item.Target{Repo: f.local, Path: "modules/hello/1.0.0/second.tar.gz"},
			Body:   bytes.NewReader(archive(t)),
			By:     "admin",
		},
	)
	if err == nil {
		t.Fatal("second archive admitted")
	}
	f.items.Formats = func(repo.PackageType) format.Handler { return generic.Handler{} }
	f.publish(f.local, "modules/hello/1.0.0/unexpected.txt", []byte("not a Bazel module file"))
	if err := (fmtbazel.Handler{}).Reindex(
		t.Context(),
		format.Env{Store: f.db, Blobs: f.items.Blobs},
		f.local,
	); err == nil {
		t.Fatal("reindex ignored unexpected file")
	}
}

func TestAccRejectsWrongManifestAndCorruptArchiveBeforePublication(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		path string
		body []byte
	}{
		{"modules/hello/1.0.0/MODULE.bazel", []byte("module(name = \"other\", version = \"1.0.0\")\n")},
		{"modules/hello/1.0.0/MODULE.bazel", []byte("module(name = dynamic_name, version = \"1.0.0\")\n")},
		{"modules/hello/1.0.0/MODULE.bazel", bytes.Repeat([]byte("x"), 1<<20+1)},
		{"modules/hello/1.0.0/hello.zip", []byte("not a zip archive")},
		{"modules/hello/1.0.0/hello.tar.gz", []byte("not gzip")},
	} {
		_, err := f.items.Deploy(
			t.Context(),
			item.Deploy{
				Target: item.Target{Repo: f.local, Path: tc.path},
				Body:   bytes.NewReader(tc.body),
				By:     "admin",
			},
		)
		if err == nil {
			t.Errorf("invalid publication admitted: %s", tc.path)
		}
	}
	if got := f.request(
		http.MethodGet,
		"/artifactory/api/bazelmodules/"+f.local.Key+"/modules/hello/metadata.json",
	).Code; got != 404 {
		t.Fatalf("rejected package listed: %d", got)
	}
}

func TestAccLocalFailsClosedAfterBlobLossAndOnInvalidStoredPublication(t *testing.T) {
	f := newFixture(t)
	module := "modules/hello/1.0.0/MODULE.bazel"
	archivePath := "modules/hello/1.0.0/hello-1.0.0.tar.gz"
	f.publish(f.local, module, []byte("module(name = \"hello\", version = \"1.0.0\")\n"))
	f.publish(f.local, archivePath, archive(t))
	root := "/artifactory/api/bazelmodules/" + f.local.Key
	item, err := f.db.Items().Get(t.Context(), f.local.Key, false, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.items.Blobs.Delete(t.Context(), item.Actual.SHA256); err != nil {
		t.Fatal(err)
	}
	if w := f.request(http.MethodGet, root+"/modules/hello/1.0.0/source.json"); w.Code == 200 {
		t.Fatalf("source described missing archive bytes: %s", w.Body.String())
	}
	if err := (fmtbazel.Handler{}).Reindex(
		t.Context(),
		format.Env{Store: f.db, Blobs: f.items.Blobs},
		f.local,
	); err == nil {
		t.Fatal("reindex did not detect missing archive blob")
	}

	g := newFixture(t)
	g.items.Formats = func(repo.PackageType) format.Handler { return generic.Handler{} }
	g.publish(g.local, module, []byte("module(name = \"other\", version = \"1.0.0\")\n"))
	if err := (fmtbazel.Handler{}).Reindex(
		t.Context(),
		format.Env{Store: g.db, Blobs: g.items.Blobs},
		g.local,
	); err == nil {
		t.Fatal("reindex accepted a stored MODULE.bazel with wrong identity")
	}

	h := newFixture(t)
	h.items.Formats = func(repo.PackageType) format.Handler { return generic.Handler{} }
	h.publish(h.local, module, []byte("module(name = \"hello\", version = \"1.0.0\")\n"))
	h.publish(h.local, archivePath, []byte("corrupt archive"))
	if got := h.request(
		http.MethodGet,
		"/artifactory/api/bazelmodules/"+h.local.Key+"/modules/hello/1.0.0/source.json",
	).Code; got == 200 {
		t.Fatal("corrupt stored archive got a usable source document")
	}
	if err := (fmtbazel.Handler{}).Reindex(
		t.Context(),
		format.Env{Store: h.db, Blobs: h.items.Blobs},
		h.local,
	); err == nil {
		t.Fatal("reindex accepted corrupt stored archive")
	}

	j := newFixture(t)
	j.items.Formats = func(repo.PackageType) format.Handler { return generic.Handler{} }
	j.publish(j.local, module, []byte("module(name = \"hello\", version = \"1.0.0\")\n"))
	j.publish(j.local, archivePath, archive(t))
	j.publish(j.local, "modules/hello/1.0.0/second.tar.gz", archive(t))
	if got := j.request(
		http.MethodGet,
		"/artifactory/api/bazelmodules/"+j.local.Key+"/modules/hello/metadata.json",
	).Code; got == 200 {
		t.Fatal("ambiguous archive version was listed")
	}
}

func TestAccDeleteRestoreAndMalformedNativePaths(t *testing.T) {
	f := newFixture(t)
	f.publish(
		f.local,
		"modules/hello/1.0.0/MODULE.bazel",
		[]byte("module(name = \"hello\", version = \"1.0.0\")\n"),
	)
	f.publish(f.local, "modules/hello/1.0.0/hello-1.0.0.tar.gz", archive(t))
	root := "/artifactory/api/bazelmodules/" + f.local.Key
	for _, tc := range []struct {
		path string
		want int
	}{
		{root + "/bazel_registry.json", 200},
		{root + "/modules/hello/metadata.json?source=https://evil.example", 400},
		{root + "/modules/hello/1.0.0/README", 404},
		{root + "/modules/hello/1.0.0/patches/evil.patch", 404},
		{root + "/archives/hello/1.0.0/wrong.tar.gz", 404},
		{root + "/modules/hello/1.0.0/MODULE.bazel", 200},
	} {
		w := f.request(http.MethodGet, tc.path)
		if w.Code != tc.want {
			t.Errorf("%s: %d want %d", tc.path, w.Code, tc.want)
		}
	}
	p := "modules/hello/1.0.0/hello-1.0.0.tar.gz"
	it, err := f.db.Items().Get(t.Context(), f.local.Key, false, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.Delete(
		t.Context(),
		item.Target{Repo: f.local, Path: p},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if got := f.request(http.MethodGet, root+"/modules/hello/metadata.json").Code; got != 404 {
		t.Fatalf("deleted version listed: %d", got)
	}
	if _, err := f.items.Restore(
		t.Context(),
		item.Target{Repo: f.local, Path: p},
		types.TrashRecord{Item: it, PackageType: string(repo.BazelModules)},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if got := f.request(http.MethodGet, root+"/modules/hello/metadata.json").Code; got != 200 {
		t.Fatalf("restored version missing: %d", got)
	}
	if err := (fmtbazel.Handler{}).Reindex(
		t.Context(),
		format.Env{Store: f.db, Blobs: f.items.Blobs},
		f.local,
	); err != nil {
		t.Fatal(err)
	}
	blackout := true
	c := f.local
	c.BlackedOut = &blackout
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if got := f.request(http.MethodGet, root+"/modules/hello/metadata.json").Code; got != 404 {
		t.Fatalf("blacked-out registry disclosed metadata: %d", got)
	}
}

func TestAccVirtualMemberSelection(t *testing.T) {
	f := newFixture(t)
	f.publish(
		f.local,
		"modules/hello/1.0.0/MODULE.bazel",
		[]byte("module(name = \"hello\", version = \"1.0.0\")\n"),
	)
	f.publish(f.local, "modules/hello/1.0.0/hello-1.0.0.tar.gz", archive(t))
	v := *repo.Defaults(&repo.Config{Key: "bazel-virtual", Class: repo.Virtual, PackageType: repo.BazelModules, Repositories: []string{f.local.Key}, DefaultDeploymentRepo: f.local.Key})
	if err := f.db.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	root := "/artifactory/api/bazelmodules/" + v.Key
	for _, tail := range []string{"modules/hello/metadata.json", "modules/hello/1.0.0/MODULE.bazel", "modules/hello/1.0.0/source.json"} {
		w := f.request("GET", root+"/"+tail)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", tail, w.Code, w.Body.String())
		}
		if strings.HasSuffix(tail, "source.json") {
			var doc struct{ URL string }
			if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(doc.URL, "/bazel-virtual/archives/") {
				t.Fatalf("wrong virtual source URL: %s", doc.URL)
			}
			if got := f.request("GET", doc.URL).Code; got != 200 {
				t.Fatalf("virtual archive %d", got)
			}
		}
	}
}

// TestAccBazelClient exercises the actual Bzlmod resolver and archive fetch.
// CI's Linux acceptance lane opts in; other unit jobs remain Docker-free.
func TestAccBazelClient(t *testing.T) {
	if os.Getenv("ARTIFACTWEAVE_BAZEL_CLIENT") != "1" {
		t.Skip("set ARTIFACTWEAVE_BAZEL_CLIENT=1 for pinned Bazel client")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker unavailable")
	}
	up := fakeupstream.New(t, fakeupstream.Options{})
	f := newFixtureWith(
		t,
		func(items *item.Service) *source.Service { return remoteService(t, items) },
	)
	f.publish(
		f.local,
		"modules/hello/1.0.0/MODULE.bazel",
		[]byte("module(name = \"hello\", version = \"1.0.0\")\n"),
	)
	packageArchive := archive(t)
	f.publish(f.local, "modules/hello/1.0.0/hello-1.0.0.tar.gz", packageArchive)
	federated := *repo.Defaults(&repo.Config{Key: "bazel-fed", Class: repo.Federated, PackageType: repo.BazelModules})
	if err := f.db.Repos().Create(t.Context(), federated); err != nil {
		t.Fatal(err)
	}
	f.publish(
		federated,
		"modules/hello/1.0.0/MODULE.bazel",
		[]byte("module(name = \"hello\", version = \"1.0.0\")\n"),
	)
	f.publish(federated, "modules/hello/1.0.0/hello-1.0.0.tar.gz", packageArchive)
	remoteRepo := *repo.Defaults(&repo.Config{Key: "bazel-remote", Class: repo.Remote, PackageType: repo.BazelModules, URL: up.URL + "/registry/"})
	if err := f.db.Repos().Create(t.Context(), remoteRepo); err != nil {
		t.Fatal(err)
	}
	up.Set(
		"/registry/modules/hello/metadata.json",
		fakeupstream.Resource{Body: []byte(`{"versions":["1.0.0"]}`)},
	)
	up.Set(
		"/registry/modules/hello/1.0.0/MODULE.bazel",
		fakeupstream.Resource{Body: []byte("module(name = \"hello\", version = \"1.0.0\")\n")},
	)
	sum := sha256.Sum256(packageArchive)
	remoteSource, _ := json.Marshal(
		map[string]any{
			"type":         "archive",
			"url":          up.URL + "/registry/archives/hello.tar.gz",
			"integrity":    "sha256-" + base64.StdEncoding.EncodeToString(sum[:]),
			"strip_prefix": "hello-1.0.0",
		},
	)
	up.Set("/registry/modules/hello/1.0.0/source.json", fakeupstream.Resource{Body: remoteSource})
	up.Set("/registry/archives/hello.tar.gz", fakeupstream.Resource{Body: packageArchive})
	v := *repo.Defaults(&repo.Config{Key: "bazel-virtual", Class: repo.Virtual, PackageType: repo.BazelModules, Repositories: []string{f.local.Key, remoteRepo.Key}, DefaultDeploymentRepo: f.local.Key})
	if err := f.db.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			port := listener.Addr().(*net.TCPAddr).Port
			host := "127.0.0.1"
			if runtime.GOOS == "darwin" {
				host = "host.docker.internal"
			}
			r.Header.Set(baseurl.OverrideHeader, "http://"+host+":"+fmt.Sprint(port))
			f.router.ServeHTTP(w, r)
		}),
	)
	server.Listener = listener
	server.Start()
	defer server.Close()
	project := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(project, "MODULE.bazel"),
		[]byte("module(name = \"consumer\")\nbazel_dep(name = \"hello\", version = \"1.0.0\")\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(project, "BUILD.bazel"),
		[]byte("filegroup(name = \"root\", srcs = [\"@hello//:all\"])\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	host := "127.0.0.1"
	if runtime.GOOS == "darwin" {
		host = "host.docker.internal"
	}
	for _, key := range []string{f.local.Key, remoteRepo.Key, v.Key, federated.Key} {
		t.Run(key, func(t *testing.T) {
			registry := "http://" + host + ":" + fmt.Sprint(
				listener.Addr().(*net.TCPAddr).Port,
			) + "/artifactory/api/bazelmodules/" + key
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
			defer cancel()
			args := []string{"run", "--rm", "--platform", "linux/amd64"}
			if runtime.GOOS == "linux" {
				args = append(args, "--network", "host")
			}
			args = append(
				args,
				"-v",
				project+":/workspace",
				"-w",
				"/workspace",
				"gcr.io/bazel-public/bazel@sha256:d090e62af5f0190bef545f278677bab7175d7db9c028d1e492bb4bfb3e1975fa",
				"--output_user_root=/tmp/bazel-output",
				"build",
				"--enable_bzlmod",
				"--registry="+registry,
				"--registry=https://bcr.bazel.build",
				"--lockfile_mode=off",
				"//:root",
			)
			cmd := exec.CommandContext(ctx, "docker", args...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Bazel 9.3.1 Bzlmod build: %v\n%s", err, output)
			}
			t.Logf("Bazel 9.3.1 Bzlmod build passed via %s", registry)
		})
	}
}
