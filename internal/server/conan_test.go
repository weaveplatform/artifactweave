package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func conanTestArchive(t *testing.T, name, content string) string {
	return conanTestArchiveEntries(t, map[string]string{name: content})
}

func conanTestArchiveEntries(t *testing.T, entries map[string]string) string {
	t.Helper()
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(
			&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return body.String()
}

func TestAccConanSearchAndPackageDeletion(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "conan-local", Class: repo.Local, PackageType: repo.Conan},
		{Key: "conan-virtual", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-local"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	recipe := "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc"
	put := func(path, body string) {
		t.Helper()
		if rec := w.do("PUT", path, "admin", body); rec.Code != http.StatusCreated {
			t.Fatalf("PUT %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	put(recipe+"/files/conanfile.py", "recipe")
	put(recipe+"/files/conanmanifest.txt", fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("recipe")))
	packagePath := recipe + "/packages/id1/revisions/p1"
	put(packagePath+"/files/conan_package.tgz", conanTestArchive(t, "conaninfo.txt", "settings"))
	put(packagePath+"/files/conaninfo.txt", "settings")
	put(
		packagePath+"/files/conanmanifest.txt",
		fmt.Sprintf("1\nconaninfo.txt: %s\n", md5Hex("settings")),
	)
	for _, key := range []string{"conan-local", "conan-virtual"} {
		base := "/artifactory/api/conan/" + key + "/v2/conans"
		for _, tc := range []struct{ path, contains string }{
			{"/search?q=hello*", "hello/1.0@_/_"},
			{"/search", "hello/1.0@_/_"},
			{"/hello/1.0/_/_/search?list_only=True", "id1"},
			{"/hello/1.0/_/_/revisions/abc/search", "settings"},
			{"/hello/1.0/_/_/revisions", "abc"},
			{"/hello/1.0/_/_/revisions/abc/packages/id1/revisions", "p1"},
			{"/hello/1.0/_/_/revisions/abc/packages/id1/latest", "p1"},
			{"/hello/1.0/_/_/revisions/abc/packages/id1/revisions/p1/files/conaninfo.txt", "settings"},
		} {
			rec := w.do("GET", base+tc.path, "admin", "")
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.contains) {
				t.Fatalf("%s%s: %d %s", key, tc.path, rec.Code, rec.Body)
			}
		}
	}
	oversizedInfo := strings.Repeat("x", 1<<20+1)
	largePackage := recipe + "/packages/id2/revisions/p1"
	put(
		largePackage+"/files/conan_package.tgz",
		conanTestArchive(t, "conaninfo.txt", oversizedInfo),
	)
	put(largePackage+"/files/conaninfo.txt", oversizedInfo)
	put(
		largePackage+"/files/conanmanifest.txt",
		fmt.Sprintf("1\nconaninfo.txt: %s\n", md5Hex(oversizedInfo)),
	)
	if rec := w.do("GET", recipe+"/search", "admin", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("oversized Conan package metadata disclosed: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("DELETE", packagePath, "admin", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete package revision: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"GET",
		packagePath+"/files/conanmanifest.txt",
		"admin",
		"",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted package still visible: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("DELETE", recipe+"/packages", "admin", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete packages: %d %s", rec.Code, rec.Body)
	}
}

func TestAccConanRevisionOrderingAndVirtualDeduplication(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "conan-first", Class: repo.Local, PackageType: repo.Conan},
		{Key: "conan-second", Class: repo.Federated, PackageType: repo.Conan},
		{Key: "conan-union", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-first", "conan-second"}, DefaultDeploymentRepo: "conan-first"},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	base := func(key string) string {
		return "/artifactory/api/conan/" + key + "/v2/conans/hello/1.0/_/_"
	}
	put := func(path, body string) {
		t.Helper()
		if rec := w.do(http.MethodPut, path, "admin", body); rec.Code != http.StatusCreated {
			t.Fatalf("PUT %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	commitRecipe := func(key, rev, content string) {
		t.Helper()
		files := base(key) + "/revisions/" + rev + "/files/"
		put(files+"conanfile.py", content)
		put(files+"conanmanifest.txt", fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex(content)))
	}
	commitRecipe("conan-first", "rev-old", "old recipe")
	time.Sleep(time.Millisecond)
	commitRecipe("conan-first", "rev-new", "new recipe")
	commitRecipe("conan-second", "rev-new", "new recipe")
	commitPackage := func(rev, content string) {
		t.Helper()
		files := base(
			"conan-first",
		) + "/revisions/rev-new/packages/id1/revisions/" + rev + "/files/"
		put(files+"conan_package.tgz", conanTestArchive(t, "conaninfo.txt", content))
		put(files+"conaninfo.txt", content)
		put(files+"conanmanifest.txt", fmt.Sprintf("1\nconaninfo.txt: %s\n", md5Hex(content)))
	}
	commitPackage("pkg-old", "settings=old")
	time.Sleep(time.Millisecond)
	commitPackage("pkg-new", "settings=new")
	for _, key := range []string{"conan-first", "conan-union"} {
		for _, tc := range []struct {
			path, contains string
		}{
			{"/revisions", `"revision":"rev-new"`},
			{"/latest", `"revision":"rev-new"`},
			{"/revisions/rev-new/packages/id1/revisions", `"revision":"pkg-new"`},
			{"/revisions/rev-new/packages/id1/latest", `"revision":"pkg-new"`},
			{"/revisions/rev-new/search", "settings=new"},
		} {
			rec := w.do(http.MethodGet, base(key)+tc.path, "admin", "")
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.contains) {
				t.Fatalf("%s%s: %d %s", key, tc.path, rec.Code, rec.Body)
			}
		}
		rec := w.do(http.MethodGet, base(key)+"/revisions", "admin", "")
		var list struct {
			Revisions []struct{ Revision string } `json:"revisions"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Revisions) != 2 {
			t.Fatalf("%s revision dedup: %v %s", key, err, rec.Body)
		}
		if list.Revisions[0].Revision != "rev-new" || list.Revisions[1].Revision != "rev-old" {
			t.Fatalf("%s revision order: %+v", key, list.Revisions)
		}
	}
	for _, tc := range []struct{ path, want string }{
		{"/search?q=HELLO*&ignorecase=False", `"results":null`},
		{"/search?q=HELLO*&ignorecase=True", "hello/1.0@_/_"},
		{"/search?q=hello?1.0*", "hello/1.0@_/_"},
	} {
		rec := w.do(
			http.MethodGet,
			"/artifactory/api/conan/conan-first/v2/conans"+tc.path,
			"admin",
			"",
		)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("case-sensitive Conan search %s: %d %s", tc.path, rec.Code, rec.Body)
		}
	}
	longSearch := "/artifactory/api/conan/conan-first/v2/conans/search?q=" + strings.Repeat(
		"a",
		257,
	)
	if rec := w.do(http.MethodGet, longSearch, "admin", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("unbounded Conan search accepted: %d %s", rec.Code, rec.Body)
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "conan-virtual-only-reader",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			Targets: map[string]types.Target{"conan-union": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, "/artifactory/api/conan/conan-union/v2/conans/search", "", http.StatusOK},
		{http.MethodGet, base("conan-union") + "/revisions", "", http.StatusNotFound},
		{http.MethodGet, base("conan-union") + "/revisions/rev-new/search", "", http.StatusOK},
		{http.MethodGet, base("conan-union") + "/revisions/rev-new/files", "", http.StatusNotFound},
		{http.MethodGet, base("conan-union") + "/revisions/rev-new/files/conanfile.py", "", http.StatusNotFound},
		{http.MethodPut, base("conan-union") + "/revisions/rev-new/files/conanfile.py", "changed", http.StatusForbidden},
	} {
		rec := w.do(tc.method, tc.path, "user", tc.body)
		if rec.Code != tc.want {
			t.Errorf(
				"virtual-only reader %s %s: %d, want %d: %s",
				tc.method,
				tc.path,
				rec.Code,
				tc.want,
				rec.Body,
			)
		}
		if rec.Code == http.StatusOK && (strings.Contains(rec.Body.String(), "hello/1.0") ||
			strings.Contains(rec.Body.String(), "id1")) {
			t.Errorf("virtual-only reader saw a member's Conan revision: %s", rec.Body)
		}
	}
	for _, path := range []string{
		base("conan-first") + "/revisions",
		base("conan-first") + "/revisions/rev-new/files",
	} {
		if rec := w.do(http.MethodGet, path, "user", ""); rec.Code != http.StatusForbidden {
			t.Errorf(
				"member-only revision metadata escaped to virtual reader %s: %d %s",
				path,
				rec.Code,
				rec.Body,
			)
		}
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "conan-manifest-only-reader",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			Targets: map[string]types.Target{"conan-first": {
				Include: []string{"_/hello/1.0/_/rev-new/export/conanmanifest.txt"},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, base("conan-first") + "/revisions", http.StatusOK},
		{http.MethodGet, base("conan-first") + "/revisions/rev-new/files", http.StatusForbidden},
		{http.MethodGet, base("conan-first") + "/revisions/rev-new/files/conanfile.py", http.StatusForbidden},
		{http.MethodPut, base("conan-first") + "/revisions/rev-new/files/conanfile.py", http.StatusForbidden},
		{http.MethodGet, base("conan-first") + "/revisions/rev-new/search", http.StatusForbidden},
	} {
		rec := w.do(tc.method, tc.path, "user", "changed")
		if rec.Code != tc.want {
			t.Errorf(
				"manifest-only reader %s %s: %d, want %d: %s",
				tc.method,
				tc.path,
				rec.Code,
				tc.want,
				rec.Body,
			)
		}
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "conan-virtual-write-only",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{
				Users: map[string]types.Actions{"deployer": types.ActionRead | types.ActionWrite},
			},
			Targets: map[string]types.Target{"conan-union": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if rec := w.do(
		http.MethodPut,
		base("conan-union")+"/revisions/rev-new/files/conanfile.py",
		"deployer",
		"changed",
	); rec.Code != http.StatusForbidden {
		t.Fatalf("virtual writer without target permission deployed: %d %s", rec.Code, rec.Body)
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "conan-content-without-manifest",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"deployer": types.ActionRead}},
			Targets: map[string]types.Target{"conan-first": {
				Include: []string{"_/hello/1.0/_/rev-new/export/conanfile.py"},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		base("conan-first") + "/revisions/rev-new/files",
		base("conan-first") + "/revisions/rev-new/files/conanfile.py",
	} {
		if rec := w.do(http.MethodGet, path, "deployer", ""); rec.Code != http.StatusForbidden {
			t.Fatalf(
				"uncommitted Conan content visible without manifest %s: %d %s",
				path,
				rec.Code,
				rec.Body,
			)
		}
	}
	if _, err := w.db.Items().Delete(
		t.Context(), "conan-first", false,
		"_/hello/1.0/_/rev-new/export/conanfile.py",
	); err != nil {
		t.Fatal(err)
	}
	if rec := w.do(
		http.MethodGet,
		base("conan-union")+"/revisions/rev-new/files/conanfile.py",
		"admin", "",
	); rec.Code != http.StatusOK || rec.Body.String() != "new recipe" {
		t.Fatalf(
			"virtual Conan file did not fall through missing member content: %d %s",
			rec.Code,
			rec.Body,
		)
	}
}

func TestAccConanInvalidRoutesAndClassPolicy(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{
			Resolve: sourceIdentity,
			HTTP: sourceBackend(
				func(context.Context, source.Request, source.Permit) (source.Result, error) {
					return source.Result{}, source.ErrUnsupported
				},
			),
		}
	})
	for _, c := range []repo.Config{
		{Key: "conan-local", Class: repo.Local, PackageType: repo.Conan},
		{Key: "conan-remote", Class: repo.Remote, PackageType: repo.Conan, URL: "https://up.example/artifactory/api/conan/up"},
		{Key: "conan-virtual", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-local"}},
		{Key: "conan-bad-virtual", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-remote"}, DefaultDeploymentRepo: "conan-remote"},
		{Key: "conan-missing-deploy", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"missing-repo"}, DefaultDeploymentRepo: "missing-repo"},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	base := "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_"
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/artifactory/api/conan/conan-local/v1/ping", "", 405},
		{"GET", "/artifactory/api/conan/conan-local/v2/unknown", "", 404},
		{"GET", "/artifactory/api/conan/conan-local/v2/conans/bad~/1.0/_/_/latest", "", 400},
		{"GET", base + "/revisions/bad~/files", "", 400},
		{"GET", base + "/revisions/abc/packages/bad~/latest", "", 400},
		{"GET", base + "/revisions/abc/packages/id/revisions/bad~/files", "", 400},
		{"GET", base + "/revisions/abc/unknown", "", 404},
		{"GET", base + "/revisions/abc/packages/id/unknown", "", 404},
		{"POST", base + "/latest", "", 405},
		{"POST", base + "/revisions", "", 405},
		{"POST", base + "/search", "", 405},
		{"POST", base + "/revisions/abc/packages/id/latest", "", 405},
		{"POST", base + "/revisions/abc/files", "", 405},
		{"POST", base + "/revisions/abc/files/conanfile.py", "", 405},
		{"GET", base + "/revisions/abc/files/conanfile.py?unexpected=1", "", 400},
		{"PUT", base + "/revisions/abc/files/conanmanifest.txt", "invalid", 400},
		{"PUT", "/artifactory/api/conan/conan-remote/v2/conans/hello/1.0/_/_/revisions/abc/files/conanfile.py", "recipe", 405},
		{"PUT", "/artifactory/api/conan/conan-virtual/v2/conans/hello/1.0/_/_/revisions/abc/files/conanfile.py", "recipe", 405},
		{"PUT", "/artifactory/api/conan/conan-bad-virtual/v2/conans/hello/1.0/_/_/revisions/abc/files/conanfile.py", "recipe", 400},
		{"PUT", "/artifactory/api/conan/conan-missing-deploy/v2/conans/hello/1.0/_/_/revisions/abc/files/conanfile.py", "recipe", 404},
	} {
		rec := w.do(tc.method, tc.path, "admin", tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body)
		}
	}
}

func TestAccConanManifestAndUploadRejections(t *testing.T) {
	w := newItemWorld(t, false)
	c := repo.Config{Key: "conan-local", Class: repo.Local, PackageType: repo.Conan}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc/files/"
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", base + "conanfile.py", "", 404},
		{"PUT", base + "conanfile.py", "recipe", 201},
		{"PUT", base + "conanfile.py", "changed", 409},
		{"PUT", base + "conanmanifest.txt", "1\nmissing: " + md5Hex("missing"), 422},
		{"PUT", base + "other.txt", "other", 201},
		{"PUT", base + "conanmanifest.txt", "1\nother.txt: " + md5Hex("other"), 400},
		{"PUT", base + "conanmanifest.txt", strings.Repeat("x", 1<<20+1), 400},
		{"PUT", base + "conanmanifest.txt", "1\nconanfile.py: " + md5Hex("recipe"), 201},
		{"PUT", base + "metadata/signature", "first", 201},
		{"PUT", base + "metadata/signature", "second", 201},
		{"PUT", "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc/packages/id/revisions/prev/files/conaninfo.txt", "settings", 201},
		{"PUT", "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc/packages/id/revisions/prev/files/conanmanifest.txt", "1\nconaninfo.txt: " + md5Hex("settings"), 422},
		{"PUT", "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/other/packages/id/revisions/prev/files/conaninfo.txt", "settings", 404},
		{"GET", "/artifactory/api/conan/conan-local/v2/conans/search?q=" + strings.Repeat("a", 257), "", 400},
	} {
		rec := w.do(tc.method, tc.path, "admin", tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body)
		}
	}
	archiveBase := "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/badarchive/files/"
	if rec := w.do(
		"PUT",
		archiveBase+"conan_export.tgz",
		"admin",
		conanTestArchive(t, "conanfile.py", "actual"),
	); rec.Code != 201 {
		t.Fatalf("archive upload: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"PUT",
		archiveBase+"conanmanifest.txt",
		"admin",
		"1\nconanfile.py: "+md5Hex("different"),
	); rec.Code != 422 {
		t.Fatalf("archive mismatch accepted: %d %s", rec.Code, rec.Body)
	}
	unlistedRecipe := "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/unlisted/files/"
	if rec := w.do(
		"PUT",
		unlistedRecipe+"conan_export.tgz",
		"admin",
		conanTestArchiveEntries(
			t,
			map[string]string{"conanfile.py": "recipe", "extra.txt": "unlisted"},
		),
	); rec.Code != 201 {
		t.Fatalf("recipe archive upload: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"PUT",
		unlistedRecipe+"conanmanifest.txt",
		"admin",
		"1\nconanfile.py: "+md5Hex("recipe"),
	); rec.Code != 422 {
		t.Fatalf("unlisted recipe archive content accepted: %d %s", rec.Code, rec.Body)
	}
	unlistedPackage := "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc/packages/id/revisions/unlisted/files/"
	if rec := w.do(
		"PUT",
		unlistedPackage+"conan_package.tgz",
		"admin",
		conanTestArchiveEntries(
			t,
			map[string]string{"conaninfo.txt": "settings", "extra.txt": "unlisted"},
		),
	); rec.Code != 201 {
		t.Fatalf("package archive upload: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("PUT", unlistedPackage+"conaninfo.txt", "admin", "settings"); rec.Code != 201 {
		t.Fatalf("package metadata upload: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"PUT",
		unlistedPackage+"conanmanifest.txt",
		"admin",
		"1\nconaninfo.txt: "+md5Hex("settings"),
	); rec.Code != 422 {
		t.Fatalf("unlisted package archive content accepted: %d %s", rec.Code, rec.Body)
	}
	badRecipe := "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/badgzip/files/"
	if rec := w.do(
		"PUT",
		badRecipe+"conan_export.tgz",
		"admin",
		"not a gzip archive",
	); rec.Code != 201 {
		t.Fatalf("malformed recipe archive upload: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"PUT", badRecipe+"conanmanifest.txt", "admin",
		"1\nconanfile.py: "+md5Hex("recipe"),
	); rec.Code != 422 {
		t.Fatalf("malformed recipe archive committed: %d %s", rec.Code, rec.Body)
	}
	badPackage := "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc/packages/id/revisions/badgzip/files/"
	if rec := w.do(
		"PUT",
		badPackage+"conan_package.tgz",
		"admin",
		"not a gzip archive",
	); rec.Code != 201 {
		t.Fatalf("malformed package archive upload: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("PUT", badPackage+"conaninfo.txt", "admin", "settings"); rec.Code != 201 {
		t.Fatalf("malformed package metadata upload: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"PUT", badPackage+"conanmanifest.txt", "admin",
		"1\nconaninfo.txt: "+md5Hex("settings"),
	); rec.Code != 422 {
		t.Fatalf("malformed package archive committed: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"DELETE",
		"/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc",
		"admin",
		"",
	); rec.Code != 204 {
		t.Fatalf("delete recipe with uncommitted package: %d %s", rec.Code, rec.Body)
	}
}

func TestAccConanVirtualMemberOrderAndValidation(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "conan-empty", Class: repo.Local, PackageType: repo.Conan},
		{Key: "conan-data", Class: repo.Local, PackageType: repo.Conan},
		{Key: "conan-order", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-empty", "conan-data"}},
		{Key: "conan-missing", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"missing-repo", "conan-data"}},
		{Key: "conan-duplicate", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-data", "conan-data"}},
		{Key: "conan-nested", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-order"}},
		{Key: "conan-self", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-self"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	recipe := "/artifactory/api/conan/conan-data/v2/conans/hello/1.0/_/_/revisions/abc/files/"
	for _, tc := range []struct{ name, body string }{
		{"conanfile.py", "recipe"},
		{"conanmanifest.txt", fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("recipe"))},
	} {
		if rec := w.do("PUT", recipe+tc.name, "admin", tc.body); rec.Code != 201 {
			t.Fatalf("upload %s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	for _, key := range []string{"conan-order", "conan-missing"} {
		prefix := "/artifactory/api/conan/" + key + "/v2/conans/hello/1.0/_/_"
		for _, path := range []string{"/latest", "/revisions/abc/files/conanfile.py"} {
			if rec := w.do("GET", prefix+path, "admin", ""); rec.Code != 200 {
				t.Errorf("%s%s: %d %s", key, path, rec.Code, rec.Body)
			}
		}
	}
	for _, key := range []string{"conan-duplicate", "conan-nested", "conan-self"} {
		if rec := w.do(
			"GET",
			"/artifactory/api/conan/"+key+"/v2/conans/search",
			"admin",
			"",
		); rec.Code != 400 {
			t.Errorf("invalid virtual %s: %d %s", key, rec.Code, rec.Body)
		}
	}
	for _, path := range []string{
		"/latest", "/search", "/revisions/abc/files/conanfile.py",
	} {
		base := "/artifactory/api/conan/conan-duplicate/v2/conans/hello/1.0/_/_"
		if rec := w.do("GET", base+path, "admin", ""); rec.Code != 400 {
			t.Errorf("duplicate virtual %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-order/v2/conans/hello/1.0/_/_/search",
		"admin",
		"",
	); rec.Code != 200 {
		t.Errorf("virtual package search without packages: %d %s", rec.Code, rec.Body)
	}
}

func TestAccConanAuthenticationBoundary(t *testing.T) {
	w := newItemWorld(t, false)
	c := repo.Config{Key: "conan-local", Class: repo.Local, PackageType: repo.Conan}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, as string
		want             int
	}{
		{"GET", "/v1/users/check_credentials", "admin", 200},
		{"POST", "/v1/users/check_credentials", "admin", 405},
		{"GET", "/v1/users/check_credentials", "", 401},
		{"GET", "/v1/users/authenticate", "", 401},
		{"POST", "/v1/users/authenticate", "admin", 405},
		{"GET", "/v1/users/authenticate", "admin", 501},
	} {
		path := "/artifactory/api/conan/conan-local" + tc.path
		rec := w.do(tc.method, path, tc.as, "")
		if rec.Code != tc.want {
			t.Errorf(
				"%s %s as %q: %d, want %d: %s",
				tc.method,
				path,
				tc.as,
				rec.Code,
				tc.want,
				rec.Body,
			)
		}
	}
}

func TestAccConanNativeRevisionLifecycle(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "conan-local", Class: repo.Local, PackageType: repo.Conan},
		{Key: "conan-fed", Class: repo.Federated, PackageType: repo.Conan},
		{Key: "conan-virt", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-local"}, DefaultDeploymentRepo: "conan-local"},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	base := "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc/files/"
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-local/v1/ping",
		"admin",
		"",
	); rec.Code != 200 {
		t.Fatalf("ping: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("PUT", base+"conanfile.py", "admin", "recipe"); rec.Code != 201 {
		t.Fatalf("file upload: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("GET", strings.TrimSuffix(base, "/"), "admin", ""); rec.Code != 404 {
		t.Fatalf("uncommitted file list exposed: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc/packages/missing/latest",
		"admin",
		"",
	); rec.Code != 404 {
		t.Fatalf("nonexistent binary revision exposed: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{base + "conanfile.py", base + "conanmanifest.txt"} {
		if rec := w.do("GET", path, "admin", ""); rec.Code != 404 {
			t.Fatalf("uncommitted %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	wrong := fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("changed"))
	if rec := w.do("PUT", base+"conanmanifest.txt", "admin", wrong); rec.Code != 422 {
		t.Fatalf("incorrect manifest checksum accepted: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/latest",
		"admin",
		"",
	); rec.Code != 404 {
		t.Fatalf("invalid revision became visible: %d %s", rec.Code, rec.Body)
	}
	manifest := fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("recipe"))
	if rec := w.do("PUT", base+"conanmanifest.txt", "admin", manifest); rec.Code != 201 {
		t.Fatalf("manifest upload: %d %s", rec.Code, rec.Body)
	}
	if packages, _, err := w.db.Packages().
		List(t.Context(), "conan-local", store.Page{}); err != nil ||
		len(packages) != 1 {
		t.Fatalf("committed recipe index: %d %v", len(packages), err)
	}
	for _, key := range []string{"conan-local", "conan-virt"} {
		prefix := "/artifactory/api/conan/" + key + "/v2/conans/hello/1.0/_/_"
		if rec := w.do("GET", prefix+"/latest", "admin", ""); rec.Code != 200 {
			t.Fatalf("%s latest: %d %s", key, rec.Code, rec.Body)
		}
		if rec := w.do(
			"GET",
			prefix+"/revisions/abc/files/conanfile.py",
			"admin",
			"",
		); rec.Code != 200 ||
			rec.Body.String() != "recipe" {
			t.Fatalf("%s read: %d %s", key, rec.Code, rec.Body)
		}
		if rec := w.do("GET", prefix+"/revisions/abc/files", "admin", ""); rec.Code != 200 {
			t.Fatalf("%s files: %d %s", key, rec.Code, rec.Body)
		} else {
			var v map[string]any
			if json.Unmarshal(rec.Body.Bytes(), &v) != nil {
				t.Fatal(rec.Body)
			}
		}
	}
	for _, tc := range []struct {
		as   string
		want int
	}{{"user", 403}, {"", 401}} {
		if rec := w.do("GET", base+"conanfile.py", tc.as, ""); rec.Code != tc.want {
			t.Fatalf("read as %q: %d %s", tc.as, rec.Code, rec.Body)
		}
		if rec := w.do(
			"GET",
			"/artifactory/api/conan/conan-local/v2/conans/search?q=*",
			tc.as,
			"",
		); rec.Code != tc.want {
			t.Fatalf("search as %q: %d %s", tc.as, rec.Code, rec.Body)
		}
	}
	if rec := w.do(
		"PUT",
		"/artifactory/conan-local/_/hello/1.0/_/abc/export/conanfile.py",
		"admin",
		"changed",
	); rec.Code != 409 {
		t.Fatalf("generic bypass: %d %s", rec.Code, rec.Body)
	}
	deletePath := "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc"
	if rec := w.do("GET", deletePath, "admin", ""); rec.Code != 405 {
		t.Fatalf("revision GET accepted as delete: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("DELETE", deletePath, "user", ""); rec.Code != 403 {
		t.Fatalf("unauthorized revision delete: %d %s", rec.Code, rec.Body)
	}
	virtualDelete := "/artifactory/api/conan/conan-virt/v2/conans/hello/1.0/_/_/revisions/abc"
	if rec := w.do("DELETE", virtualDelete, "admin", ""); rec.Code != 405 {
		t.Fatalf("virtual revision delete accepted: %d %s", rec.Code, rec.Body)
	}
	setTrashcan(t, w.db, true)
	if rec := w.do(
		"DELETE",
		"/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc",
		"admin",
		"",
	); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("GET", base+"conanfile.py", "admin", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted read: %d %s", rec.Code, rec.Body)
	}
	if packages, _, err := w.db.Packages().
		List(t.Context(), "conan-local", store.Page{}); err != nil ||
		len(packages) != 0 {
		t.Fatalf("deleted recipe index: %d %v", len(packages), err)
	}
	if rec := w.do(
		"POST",
		"/artifactory/api/trash/restore/conan-local/_/hello/1.0/_/abc?to=conan-local",
		"admin",
		"",
	); rec.Code != 202 {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"GET",
		base+"conanfile.py",
		"admin",
		"",
	); rec.Code != 200 ||
		rec.Body.String() != "recipe" {
		t.Fatalf("restored recipe: %d %s", rec.Code, rec.Body)
	}
	if packages, _, err := w.db.Packages().
		List(t.Context(), "conan-local", store.Page{}); err != nil ||
		len(packages) != 1 {
		t.Fatalf("restored recipe index: %d %v", len(packages), err)
	}
}

func TestAccConanRemoteAndVirtualSource(t *testing.T) {
	manifest := fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("remote recipe"))
	packageManifest := fmt.Sprintf("1\nconaninfo.txt: %s\n", md5Hex("settings"))
	var calls []string
	used := 0
	corrupt := false
	archiveMode := ""
	corruptPackageArchive := false
	badSearch := false
	malformedSearch := false
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
				calls = append(calls, q.Resource.UpstreamPath)
				var body string
				switch q.Resource.UpstreamPath {
				case "v2/conans/search":
					body = `{"results":["hello/1.0@_/_"]}`
					if badSearch {
						body = `{"results":["../1.0@_/_"]}`
					}
					if malformedSearch {
						body = `{"results":"invalid"}`
					}
				case "v2/conans/hello/1.0/_/_/search",
					"v2/conans/hello/1.0/_/_/revisions/abc/search":
					body = `{"id1":{"content":"settings"}}`
				case "v2/conans/hello/1.0/_/_/revisions":
					body = `{"revisions":[{"revision":"def","time":"2026-10-23T00:00:00Z"},{"revision":"abc","time":"2026-10-24T00:00:00Z"}]}`
				case "v2/conans/hello/1.0/_/_/revisions/abc/files":
					body = `{"files":{"conanfile.py":{},"conanmanifest.txt":{}}}`
				case "v2/conans/hello/1.0/_/_/revisions/abc/files/conanmanifest.txt":
					body = manifest
				case "v2/conans/hello/1.0/_/_/revisions/abc/files/conanfile.py":
					body = "remote recipe"
					if corrupt {
						body = "tampered recipe"
					}
				case "v2/conans/hello/1.0/_/_/revisions/abc/files/conan_export.tgz":
					body = conanTestArchive(t, "conanfile.py", "remote recipe")
					if archiveMode == "tamper" {
						body = conanTestArchive(t, "conanfile.py", "tampered recipe")
					}
					if archiveMode == "invalid" {
						body = "not gzip"
					}
					if archiveMode == "unlisted" {
						body = conanTestArchiveEntries(
							t,
							map[string]string{
								"conanfile.py": "remote recipe",
								"extra.txt":    "unlisted",
							},
						)
					}
				case "v2/conans/hello/1.0/_/_/revisions/abc/packages/id1/revisions":
					body = `{"revisions":[{"revision":"p1","time":"2026-10-24T00:00:00Z"}]}`
				case "v2/conans/hello/1.0/_/_/revisions/abc/packages/id1/revisions/p1/files":
					body = `{"files":{"conaninfo.txt":{},"conanmanifest.txt":{},"conan_package.tgz":{}}}`
				case "v2/conans/hello/1.0/_/_/revisions/abc/packages/id1/revisions/p1/files/conanmanifest.txt":
					body = packageManifest
				case "v2/conans/hello/1.0/_/_/revisions/abc/packages/id1/revisions/p1/files/conaninfo.txt":
					body = "settings"
				case "v2/conans/hello/1.0/_/_/revisions/abc/packages/id1/revisions/p1/files/conan_package.tgz":
					body = conanTestArchive(t, "conaninfo.txt", "settings")
					if corruptPackageArchive {
						body = conanTestArchive(t, "conaninfo.txt", "tampered settings")
					}
				default:
					t.Fatalf("unexpected upstream %q", q.Resource.UpstreamPath)
				}
				if q.Resource.Validate != nil {
					if err := q.Resource.Validate(
						t.Context(),
						bytes.NewReader([]byte(body)),
						int64(len(body)),
					); err != nil {
						return source.Result{}, &source.HTTPError{
							Status: 502,
							Kind:   source.IntegrityFailure,
						}
					}
				}
				if q.Resource.Checksums.MD5 != "" && q.Resource.Checksums.MD5 != md5Hex(body) {
					return source.Result{}, &source.HTTPError{
						Status: 502,
						Kind:   source.IntegrityFailure,
					}
				}
				return source.Result{
					Used: func(context.Context) error { used++; return nil },
					Stream: &source.Stream{
						Body:   io.NopCloser(strings.NewReader(body)),
						Status: 200,
						Representation: source.Representation{
							Size:      int64(len(body)),
							MediaType: "application/json",
						},
					},
				}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	for _, c := range []repo.Config{
		{Key: "conan-remote", Class: repo.Remote, PackageType: repo.Conan, URL: "https://up.example/artifactory/api/conan/up"},
		{Key: "conan-virtual", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-remote"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"conan-remote", "conan-virtual"} {
		base := "/artifactory/api/conan/" + key + "/v2/conans/hello/1.0/_/_"
		for _, tc := range []struct{ path, body string }{
			{"/latest", "\"revision\":\"abc\""},
			{"/revisions/abc/files", "conanfile.py"},
			{"/revisions/abc/files/conanfile.py", "remote recipe"},
		} {
			rec := w.do("GET", base+tc.path, "admin", "")
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), tc.body) {
				t.Fatalf("%s%s: %d %s", key, tc.path, rec.Code, rec.Body)
			}
		}
		if rec := w.do(
			"GET",
			base+"/revisions/abc/files/conan_export.tgz",
			"admin",
			"",
		); rec.Code != 200 {
			t.Fatalf("%s archive: %d %s", key, rec.Code, rec.Body)
		}
		packageBase := base + "/revisions/abc/packages/id1"
		for _, tc := range []struct{ path, contains string }{
			{"/latest", "p1"},
			{"/revisions/p1/files", "conaninfo.txt"},
			{"/revisions/p1/files/conaninfo.txt", "settings"},
		} {
			rec := w.do("GET", packageBase+tc.path, "admin", "")
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), tc.contains) {
				t.Fatalf("%s%s: %d %s", packageBase, tc.path, rec.Code, rec.Body)
			}
		}
		if rec := w.do(
			"GET",
			packageBase+"/revisions/p1/files/conan_package.tgz",
			"admin",
			"",
		); rec.Code != 200 {
			t.Fatalf("%s package archive: %d %s", key, rec.Code, rec.Body)
		}
		for _, tc := range []struct{ path, value string }{
			{"/artifactory/api/conan/" + key + "/v2/conans/search?q=hello*", "hello/1.0@_/_"},
			{base + "/search", "id1"},
			{base + "/revisions/abc/search", "settings"},
		} {
			rec := w.do("GET", tc.path, "admin", "")
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), tc.value) {
				t.Fatalf("%s: %d %s", tc.path, rec.Code, rec.Body)
			}
		}
	}
	if len(calls) < 6 {
		t.Fatalf("missing upstream requests: %v", calls)
	}
	if used == 0 {
		t.Fatal("successful Conan metadata reads did not record source use")
	}
	corrupt = true
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-remote/v2/conans/hello/1.0/_/_/revisions/abc/files/conanfile.py",
		"admin",
		"",
	); rec.Code != 502 {
		t.Fatalf("upstream integrity failure: %d %s", rec.Code, rec.Body)
	}
	for _, mode := range []string{"tamper", "invalid", "unlisted"} {
		archiveMode = mode
		if rec := w.do(
			"GET",
			"/artifactory/api/conan/conan-remote/v2/conans/hello/1.0/_/_/revisions/abc/files/conan_export.tgz",
			"admin",
			"",
		); rec.Code != 502 {
			t.Fatalf("upstream archive %s integrity failure: %d %s", mode, rec.Code, rec.Body)
		}
	}
	corruptPackageArchive = true
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-remote/v2/conans/hello/1.0/_/_/revisions/abc/packages/id1/revisions/p1/files/conan_package.tgz",
		"admin",
		"",
	); rec.Code != 502 {
		t.Fatalf("upstream package archive integrity failure: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-remote/v2/conans/hello/1.0/_/_/revisions/abc/files/missing.txt",
		"admin",
		"",
	); rec.Code != 404 {
		t.Fatalf("unlisted upstream file exposed: %d %s", rec.Code, rec.Body)
	}
	badSearch = true
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-remote/v2/conans/search",
		"admin",
		"",
	); rec.Code != 502 {
		t.Fatalf("malformed upstream search admitted: %d %s", rec.Code, rec.Body)
	}
	badSearch = false
	malformedSearch = true
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-remote/v2/conans/search",
		"admin",
		"",
	); rec.Code != 502 {
		t.Fatalf("non-list upstream search admitted: %d %s", rec.Code, rec.Body)
	}
}

func TestAccConanMalformedUpstreamMetadata(t *testing.T) {
	const recipe = "v2/conans/hello/1.0/_/_"
	manifest := fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("recipe"))
	deny := ""
	responses := map[string]string{
		"v2/conans/search":    `{"results":["hello/1.0@_/_"]}`,
		recipe + "/revisions": "{\"revisions\":[{\"revision\":\"abc\",\"time\":\"2026-10-24T00:00:00Z\"}]}",
		recipe + "/revisions/abc/files/conanmanifest.txt": manifest,
		recipe + "/revisions/abc/files":                   "{\"files\":{\"conanfile.py\":{},\"conanmanifest.txt\":{}}}",
		recipe + "/search":                                "{\"id1\":{\"content\":\"settings\"}}",
	}
	svc := &source.Service{
		Resolve: sourceIdentity,
		Revalidate: func(_ context.Context, q source.Request, _ source.Permit) error {
			if q.Resource.UpstreamPath == "" && q.Resource.Path == deny {
				return source.ErrDenied
			}
			return nil
		},
		HTTP: sourceBackend(
			func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
				body, ok := responses[q.Resource.UpstreamPath]
				if !ok {
					return source.Result{}, source.ErrUnsupported
				}
				return source.Result{Stream: &source.Stream{
					Body:   io.NopCloser(strings.NewReader(body)),
					Status: 200,
					Representation: source.Representation{
						Size:      int64(len(body)),
						MediaType: "application/json",
					},
				}}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	c := repo.Config{
		Key:         "conan-remote",
		Class:       repo.Remote,
		PackageType: repo.Conan,
		URL:         "https://up.example/artifactory/api/conan/up",
	}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/conan/conan-remote/" + recipe
	for _, tc := range []struct {
		upstream, value, path string
	}{
		{recipe + "/revisions", `not-json`, base + "/latest"},
		{recipe + "/revisions", `[]`, base + "/latest"},
		{recipe + "/revisions", `{"revisions":[{"revision":"../escape","time":"2026-10-24T00:00:00Z"}]}`, base + "/latest"},
		{recipe + "/revisions", `{"revisions":[{"revision":"abc"}]}`, base + "/latest"},
		{recipe + "/revisions/abc/files/conanmanifest.txt", `invalid`, base + "/revisions/abc/files"},
		{recipe + "/revisions/abc/files/conanmanifest.txt", `invalid`, base + "/revisions/abc/files/conanfile.py"},
		{recipe + "/revisions/abc/files/conanmanifest.txt", "1\nother.txt: " + md5Hex("other"), base + "/revisions/abc/files"},
		{recipe + "/revisions/abc/files", `not-json`, base + "/revisions/abc/files"},
		{recipe + "/revisions/abc/files", `{"files":[]}`, base + "/revisions/abc/files"},
		{recipe + "/revisions/abc/files", `{"files":{"../escape":{},"conanmanifest.txt":{}}}`, base + "/revisions/abc/files"},
		{recipe + "/revisions/abc/files", `{"files":{"conanfile.py":{}}}`, base + "/revisions/abc/files"},
		{recipe + "/search", `{"../escape":{}}`, base + "/search"},
		{recipe + "/search", `[]`, base + "/search"},
	} {
		prior := responses[tc.upstream]
		responses[tc.upstream] = tc.value
		rec := w.do("GET", tc.path, "admin", "")
		responses[tc.upstream] = prior
		if rec.Code != http.StatusBadGateway {
			t.Errorf(
				"invalid %s accepted through %s: %d %s",
				tc.upstream,
				tc.path,
				rec.Code,
				rec.Body,
			)
		}
	}
	for _, tc := range []struct {
		deny, path, wantBody string
		wantStatus           int
	}{
		{"_/hello/1.0/_/abc/export/conanmanifest.txt", base + "/latest", "", 404},
		{"_/hello/1.0/_/abc/export/conanfile.py", base + "/revisions/abc/files", "", 403},
		{"_/hello/1.0/_", "/artifactory/api/conan/conan-remote/v2/conans/search", `"results":null`, 200},
		{"_/hello/1.0/_/package/id1", base + "/search", `{}`, 200},
	} {
		deny = tc.deny
		rec := w.do("GET", tc.path, "admin", "")
		if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("denied %s via %s: %d %s", tc.deny, tc.path, rec.Code, rec.Body)
		}
	}
	deny = ""
	for _, grant := range []types.Permission{
		{
			Name: "conan-search-only",
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"deployer": types.ActionRead}},
				Targets: map[string]types.Target{"conan-remote": {Include: []string{".conan/**"}}},
			}},
		},
		{
			Name: "conan-recipe-metadata-only",
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
				Targets: map[string]types.Target{"conan-remote": {
					Include: []string{"_/hello/1.0/_/abc/export/**"},
				}},
			}},
		},
	} {
		if err := w.db.Permissions().Create(t.Context(), grant); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		as, path, wantBody string
	}{
		{"deployer", "/artifactory/api/conan/conan-remote/v2/conans/search", `"results":null`},
		{"user", base + "/search", `{}`},
	} {
		rec := w.do(http.MethodGet, tc.path, tc.as, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("scoped Conan metadata %s as %s: %d %s", tc.path, tc.as, rec.Code, rec.Body)
		}
	}
}

func TestAccConanRemoteSourceFailures(t *testing.T) {
	mode := ""
	revalidations := 0
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
				switch mode {
				case "missing":
					return source.Result{}, store.ErrNotFound
				case "backend-invalid":
					return source.Result{}, source.ErrInvalidResult
				case "empty":
					return source.Result{}, nil
				case "missing-cache-blob":
					path := q.Resource.Path
					at := strings.LastIndexByte(path, '/')
					item := types.Item{
						RepoKey: q.Repo.Key, Cached: true,
						Path: path[:at], Name: path[at+1:],
						Actual: types.Checksums{SHA256: strings.Repeat("0", 64)},
					}
					return source.Result{Item: &item}, nil
				}
				body := `{"results":["hello/1.0@_/_"]}`
				if mode == "malformed" {
					body = `{"results":`
				}
				result := source.Result{Stream: &source.Stream{
					Body:   io.NopCloser(strings.NewReader(body)),
					Status: 200,
					Representation: source.Representation{
						Size:      int64(len(body)),
						MediaType: "application/json",
					},
				}}
				if mode == "revoked" {
					result.Revalidate = func(context.Context) error { return source.ErrDenied }
				}
				if mode == "revoked-during-delivery" {
					result.Revalidate = func(context.Context) error {
						revalidations++
						if revalidations > 1 {
							return source.ErrDenied
						}
						return nil
					}
				}
				return result, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	c := repo.Config{
		Key:         "conan-remote",
		Class:       repo.Remote,
		PackageType: repo.Conan,
		URL:         "https://up.example/artifactory/api/conan/up",
	}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode string
		want int
	}{
		{"missing", 404},
		{"backend-invalid", 502},
		{"empty", 502},
		{"missing-cache-blob", 500},
		{"malformed", 502},
		{"revoked", 403},
		{"revoked-during-delivery", 403},
	} {
		mode = tc.mode
		revalidations = 0
		rec := w.do("GET", "/artifactory/api/conan/conan-remote/v2/conans/search", "admin", "")
		if rec.Code != tc.want {
			t.Errorf("source %s: %d, want %d: %s", mode, rec.Code, tc.want, rec.Body)
		}
	}
}

func TestAccConanRemoteManifestDeliveryBoundary(t *testing.T) {
	mode := ""
	revalidations := 0
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
				if mode == "missing-cache-blob" {
					path := q.Resource.Path
					at := strings.LastIndexByte(path, '/')
					item := types.Item{
						RepoKey: q.Repo.Key, Cached: true,
						Path: path[:at], Name: path[at+1:],
						Actual: types.Checksums{SHA256: strings.Repeat("0", 64)},
					}
					return source.Result{Item: &item}, nil
				}
				body := fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("recipe"))
				if mode == "oversized" {
					body = strings.Repeat("x", 1<<20+1)
				}
				result := source.Result{Stream: &source.Stream{
					Body: io.NopCloser(strings.NewReader(body)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(body))},
				}}
				if mode == "revoked-during-delivery" {
					result.Revalidate = func(context.Context) error {
						revalidations++
						if revalidations > 1 {
							return source.ErrDenied
						}
						return nil
					}
				}
				return result, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	c := repo.Config{
		Key: "conan-remote", Class: repo.Remote, PackageType: repo.Conan,
		URL: "https://up.example/artifactory/api/conan/up",
	}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	path := "/artifactory/api/conan/conan-remote/v2/conans/hello/1.0/_/_/revisions/abc/files/conanfile.py"
	for _, tc := range []struct {
		mode string
		want int
	}{
		{"revoked-during-delivery", http.StatusForbidden},
		{"oversized", http.StatusBadGateway},
		{"missing-cache-blob", http.StatusInternalServerError},
	} {
		mode, revalidations = tc.mode, 0
		rec := w.do(http.MethodGet, path, "admin", "")
		if rec.Code != tc.want {
			t.Errorf("manifest %s: %d, want %d: %s", mode, rec.Code, tc.want, rec.Body)
		}
	}
}

func TestAccConanVirtualPropagatesMemberFailure(t *testing.T) {
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return source.Result{}, source.ErrInvalidResult
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	for _, c := range []repo.Config{
		{Key: "conan-remote", Class: repo.Remote, PackageType: repo.Conan, URL: "https://up.example/artifactory/api/conan/up"},
		{Key: "conan-virtual", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-remote"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	base := "/artifactory/api/conan/conan-virtual/v2/conans/hello/1.0/_/_"
	for _, path := range []string{
		"/artifactory/api/conan/conan-virtual/v2/conans/search",
		base + "/search",
		base + "/latest",
		base + "/revisions/abc/files",
		base + "/revisions/abc/files/conanfile.py",
	} {
		if rec := w.do("GET", path, "admin", ""); rec.Code != http.StatusBadGateway {
			t.Errorf("virtual member failure %s: %d %s", path, rec.Code, rec.Body)
		}
	}
}
