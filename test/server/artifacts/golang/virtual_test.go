package golang_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/mod/module"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const pseudo = "v0.0.0-20261016120000-abcdefabcdef"

func virtualModule(t *testing.T, m, v, label string) map[string][]byte {
	t.Helper()
	gomod := []byte("module " + m + "\n\ngo 1.22\n// " + label + "\n")
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, body := range map[string][]byte{"go.mod": gomod, "lib.go": []byte("package lib\nconst Greeting = \"" + label + "\"\n")} {
		f, err := z.Create(m + "@" + v + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{v + ".mod": gomod, v + ".zip": b.Bytes(), v + ".info": []byte(fmt.Sprintf(`{"Version":%q,"Time":"2026-10-16T00:00:00Z"}`, v))}
}

func goVirtualWorld(t *testing.T) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t)
	w.Repo.PackageType = repo.Go
	w.Update()
	for _, cfg := range []repo.Config{
		{Key: "local", Class: repo.Local, PackageType: repo.Go},
		{Key: "virtual", Class: repo.Virtual, PackageType: repo.Go, Repositories: []string{"local", "remote"}},
	} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&cfg)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func seedVirtualModule(t *testing.T, w *harness.RemoteWorld, m, v, member string) {
	t.Helper()
	escaped, err := module.EscapePath(m)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range virtualModule(t, m, v, member) {
		if member == "remote" {
			w.Upstream.Set("/"+escaped+"/@v/"+name, fakeupstream.Resource{Body: body})
			continue
		}
		res := w.Request(http.MethodPut, "/artifactory/local/"+m+"/@v/"+name, "admin", string(body))
		if res.Code != http.StatusCreated {
			t.Fatalf("deploy: %d %s", res.Code, res.Body.String())
		}
	}
}

func TestAccGoVirtualReads(t *testing.T) {
	w := goVirtualWorld(t)
	const m = "example.com/Team/Mod"
	seedVirtualModule(t, w, m, "v1.0.0", "local")
	seedVirtualModule(t, w, m, "v1.0.0", "remote")
	seedVirtualModule(t, w, m, "v1.2.0", "remote")
	seedVirtualModule(t, w, m, pseudo, "remote")
	w.Upstream.Set("/example.com/!team/!mod/@v/list", fakeupstream.Resource{Body: []byte("v1.0.0\nv1.2.0\n" + pseudo + "\n")})
	base := "/artifactory/api/go/virtual/" + m
	for _, tc := range []struct {
		path, contains string
		status         int
	}{
		{"/@v/list", "v1.2.0", 200},
		{"/@latest", `"Version":"v1.2.0"`, 200},
		{"/@v/latest.info", `"Version":"v1.2.0"`, 200},
		{"/@v/v1.0.0.mod", "// local", 200},
		{"/@v/v1.0.0.info", `"Version":"v1.0.0"`, 200},
		{"/@v/v1.2.0.mod", "// remote", 200},
		{"/@v/v1.2.0.zip", "", 200},
		{"/@v/" + pseudo + ".info", pseudo, 200},
		{"/@v/v1.9.0.mod", "Version v1.9.0 of module " + m + " is not in virtual.", 404},
	} {
		res := w.Request("GET", base+tc.path, "admin", "")
		if res.Code != tc.status || !strings.Contains(res.Body.String(), tc.contains) {
			t.Errorf("%s: %d %q", tc.path, res.Code, res.Body.String())
		}
	}
	res := w.Request("GET", base+"/@v/list", "admin", "")
	if strings.Count(res.Body.String(), "v1.0.0") != 1 || !strings.HasPrefix(res.Body.String(), pseudo+"\n") {
		t.Fatal(res.Body.String())
	}
	// Both immutable files were warmed; an offline member must not need its list/index.
	w.Upstream.Close()
	for _, ext := range []string{"mod", "zip"} {
		res = w.Request("GET", base+"/@v/v1.2.0."+ext, "admin", "")
		if res.Code != 200 {
			t.Fatalf("warm %s: %d %s", ext, res.Code, res.Body.String())
		}
	}
}

func TestAccGoVirtualMissesAndGuards(t *testing.T) {
	w := goVirtualWorld(t)
	const m = "example.com/missing"
	for _, status := range []int{404, 410, 500} {
		w.Upstream.Set("/"+m+"/@v/list", fakeupstream.Resource{Status: status})
		res := w.Request("GET", "/artifactory/api/go/virtual/"+m+"/@v/list", "admin", "")
		if res.Code != 404 || !strings.Contains(res.Body.String(), "Module "+m+" has no version in virtual.") {
			t.Fatalf("upstream %d: %d %s", status, res.Code, res.Body.String())
		}
	}
	for _, tc := range []struct {
		method, path, user string
		status             int
	}{
		{"GET", "remote/" + m + "/@v/list", "admin", 501},
		{"PUT", "virtual/" + m + "/@v/v1.0.0.zip", "admin", 405},
		{"GET", "virtual/sumdb/sum.golang.org/supported", "", 404},
		{"GET", "virtual/" + m + "/@v/list", "denied", 404},
		{"GET", "virtual/" + m + "/@v/v1.0.0.mod", "denied", 404},
	} {
		res := w.Request(tc.method, "/artifactory/api/go/"+tc.path, tc.user, "")
		if res.Code != tc.status {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, res.Code, res.Body.String())
		}
	}
}

func TestAccGoVirtualMemberPermissionsPatternsAndHEAD(t *testing.T) {
	w := goVirtualWorld(t)
	const m = "example.com/visible"
	seedVirtualModule(t, w, m, "v1.0.0", "remote")
	w.Upstream.Set("/"+m+"/@v/list", fakeupstream.Resource{Body: []byte("v1.0.0\n")})
	for _, p := range []string{"/@v/list", "/@latest", "/@v/v1.0.0.info", "/@v/v1.0.0.mod", "/@v/v1.0.0.zip"} {
		res := w.Request("HEAD", "/artifactory/api/go/virtual/"+m+p, "reader", "")
		if res.Code != 200 {
			t.Errorf("HEAD %s: %d %s", p, res.Code, res.Body.String())
		}
	}
	// Reader has a grant only on the serving member; virtual grants are not consulted.
	res := w.Request("GET", "/artifactory/api/go/virtual/"+m+"/@v/v1.0.0.mod", "reader", "")
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	virtual, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	excluded := "**/*.mod"
	virtual.ExcludesPattern = &excluded
	if err = w.Runtime.Store.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	res = w.Request("GET", "/artifactory/api/go/virtual/"+m+"/@v/list", "admin", "")
	if res.Code != 404 {
		t.Fatal("list leaked excluded version:", res.Code, res.Body.String())
	}
	res = w.Request("GET", "/artifactory/api/go/virtual/"+m+"/@v/v1.0.0.mod", "admin", "")
	if res.Code != 404 {
		t.Fatal("virtual pattern:", res.Code, res.Body.String())
	}
}
