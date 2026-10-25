package bower

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitBowerRoutesDoNotDiscloseInvalidOrDeniedPackages(t *testing.T) {
	f := newFixture(t)
	local := f.repo("bower-local", repo.Local)
	f.deploy(local, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
	for _, path := range []string{
		"packages?unexpected=true", "packages/search/hello?unexpected=true",
		"packages/hello?unexpected=true", "packages/bad%2fname",
		"refs/bower-local/hello?unexpected=true", "refs/other/hello",
		"binaries/other/hello.git/v1.0.0", "binaries/bower-local/hello/v1.0.0",
		"binaries/bower-local/hello.git/unknown",
		"binaries/bower-local/hello.git/v2.0.0",
	} {
		r := f.get("/artifactory/api/bower/bower-local/"+path, "admin")
		if r.Code == http.StatusOK {
			t.Errorf("accepted invalid route %q", path)
		}
	}
	for _, path := range []string{
		"packages", "packages/search/hello", "packages/hello",
		"refs/bower-local/hello", "binaries/bower-local/hello.git/v1.0.0",
	} {
		r := f.get("/artifactory/api/bower/bower-local/"+path, "guest")
		if r.Code == http.StatusOK {
			t.Errorf("guest read %q", path)
		}
	}
	if r := f.get(
		"/artifactory/api/bower/bower-local/packages/absent",
		"admin",
	); r.Code != http.StatusNotFound {
		t.Fatalf("missing package returned %d", r.Code)
	}
	if r := f.get(
		"/artifactory/api/bower/bower-local/refs/bower-local/absent",
		"admin",
	); r.Code != http.StatusNotFound {
		t.Fatalf("missing refs returned %d", r.Code)
	}
	if r := f.get(
		"/artifactory/api/bower/bower-local/packages",
		"admin",
	); r.Code != http.StatusOK ||
		!strings.Contains(r.Body.String(), `"name":"hello"`) {
		t.Fatalf("local catalog returned %d %s", r.Code, r.Body)
	}
	if r := f.get(
		"/artifactory/api/bower/bower-local/packages/search/ello",
		"admin",
	); r.Code != http.StatusOK ||
		!strings.Contains(r.Body.String(), `"name":"hello"`) {
		t.Fatalf("local search returned %d %s", r.Code, r.Body)
	}
}

func TestUnitBowerVirtualCatalogDedupeAndCycleDenial(t *testing.T) {
	f := newFixture(t)
	a := f.repo("bower-a", repo.Local)
	b := f.repo("bower-b", repo.Federated)
	_ = f.repo("bower-v", repo.Virtual, a.Key, b.Key)
	for _, c := range []repo.Config{a, b} {
		f.deploy(c, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
	}
	if r := f.get(
		"/artifactory/api/bower/bower-v/packages",
		"admin",
	); r.Code != http.StatusOK ||
		strings.Count(r.Body.String(), `"name":"hello"`) != 1 {
		t.Fatalf("virtual catalog deduped incorrectly: %d %s", r.Code, r.Body)
	}
	if r := f.get(
		"/artifactory/api/bower/bower-v/packages/search/hello",
		"admin",
	); r.Code != http.StatusOK ||
		strings.Count(r.Body.String(), `"name":"hello"`) != 1 {
		t.Fatalf("virtual search deduped incorrectly: %d %s", r.Code, r.Body)
	}
	_ = f.repo("bower-cycle", repo.Virtual, "bower-cycle")
	if r := f.get("/artifactory/api/bower/bower-cycle/packages", "admin"); r.Code == http.StatusOK {
		t.Fatal("virtual cycle exposed a catalog")
	}
	if r := f.get(
		"/artifactory/api/bower/bower-cycle/refs/bower-cycle/hello",
		"admin",
	); r.Code == http.StatusOK {
		t.Fatal("virtual cycle exposed refs")
	}
	_ = f.repo("bower-nested", repo.Virtual, "bower-v")
	_ = f.repo("bower-invalid", repo.Virtual, "bower-nested")
	if r := f.get(
		"/artifactory/api/bower/bower-invalid/packages",
		"admin",
	); r.Code == http.StatusOK {
		t.Fatal("nested virtual member exposed a catalog")
	}
	_ = f.repo("bower-missing", repo.Virtual, "does-not-exist")
	if r := f.get(
		"/artifactory/api/bower/bower-missing/packages",
		"admin",
	); r.Code != http.StatusOK ||
		r.Body.String() != "[]\n" {
		t.Fatalf("missing member catalog = %d %s", r.Code, r.Body)
	}
}

func TestUnitBowerArchiveRequiresEveryFileReadable(t *testing.T) {
	f := newFixture(t)
	c := f.repo("bower-private", repo.Local)
	f.deploy(c, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
	f.deploy(c, "hello", "1.0.0", "secret.txt", "private content")
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "manifest-only",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{
				Users: map[string]types.Actions{"manifest-reader": types.ActionRead},
			},
			Targets: map[string]types.Target{
				"bower-private": {Include: []string{"hello/1.0.0/bower.json"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	path := "/artifactory/api/bower/bower-private/binaries/bower-private/hello.git/v1.0.0"
	if r := f.get(path, "manifest-reader"); r.Code != http.StatusForbidden {
		t.Fatalf("archive leaked unreadable file: %d %s", r.Code, r.Body)
	}
}

func TestUnitBowerArchiveFailsClosedWhenStoredContentIsMissing(t *testing.T) {
	f := newFixture(t)
	c := f.repo("bower-corrupt", repo.Local)
	f.deploy(c, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
	content := "published content"
	f.deploy(c, "hello", "1.0.0", "index.js", content)
	sha := sha256.Sum256([]byte(content))
	if err := f.items.Blobs.Delete(t.Context(), fmt.Sprintf("%x", sha)); err != nil {
		t.Fatal(err)
	}
	path := "/artifactory/api/bower/bower-corrupt/binaries/bower-corrupt/hello.git/v1.0.0"
	if r := f.get(path, "admin"); r.Code == http.StatusOK {
		t.Fatal("archive served a package with missing stored bytes")
	}
}

func TestUnitBowerArchiveRejectsOversizedAndTruncatedStoredItems(t *testing.T) {
	f := newFixture(t)
	c := f.repo("bower-damaged", repo.Local)
	f.deploy(c, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
	f.deploy(c, "hello", "1.0.0", "index.js", "published content")
	path := "hello/1.0.0/index.js"
	it, err := f.db.Items().Get(t.Context(), c.Key, false, path)
	if err != nil {
		t.Fatal(err)
	}
	originalSize := it.Size
	archive := "/artifactory/api/bower/" + c.Key + "/binaries/" + c.Key + "/hello.git/v1.0.0"
	it.Size = maxArchiveBytes + 1
	if _, err := f.db.Items().Put(t.Context(), it); err != nil {
		t.Fatal(err)
	}
	if r := f.get(archive, "admin"); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized archive = %d %s", r.Code, r.Body)
	}
	it.Size = originalSize + 1
	if _, err := f.db.Items().Put(t.Context(), it); err != nil {
		t.Fatal(err)
	}
	if r := f.get(archive, "admin"); r.Code == http.StatusOK {
		t.Fatal("archive served fewer bytes than its stored item declares")
	}
}

func TestUnitBowerArchiveRejectsIndexWithMissingManifest(t *testing.T) {
	f := newFixture(t)
	c := f.repo("bower-incomplete", repo.Local)
	f.deploy(c, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
	f.deploy(c, "hello", "1.0.0", "index.js", "published content")
	if _, err := f.db.Items().
		Delete(t.Context(), c.Key, false, "hello/1.0.0/bower.json"); err != nil {
		t.Fatal(err)
	}
	archive := "/artifactory/api/bower/" + c.Key + "/binaries/" + c.Key + "/hello.git/v1.0.0"
	if r := f.get(archive, "admin"); r.Code != http.StatusNotFound {
		t.Fatalf("archive with missing manifest = %d %s", r.Code, r.Body)
	}
}

func TestUnitBowerRemoteMetadataRejectsAmbiguityAndOversize(t *testing.T) {
	goodRef := strings.Repeat("a", 44) + " refs/tags/v1.0.0\n"
	if refs, err := parseRefs([]byte("\n" + goodRef + "\n")); err != nil || len(refs) != 1 {
		t.Fatalf("blank lines changed valid refs: %v, %v", refs, err)
	}
	for _, data := range []string{
		goodRef + goodRef,
		strings.Repeat("z", 44) + " refs/tags/v1.0.0\n",
		strings.Repeat("a", maxRefsBytes+1),
		strings.Repeat("a", 70<<10) + " refs/tags/v1.0.0\n",
	} {
		if _, err := parseRefs([]byte(data)); err == nil {
			t.Errorf("accepted invalid refs size=%d", len(data))
		}
	}
	for _, data := range []string{
		`not json`, `[{"name":"hello","url":"https://evil.example/repo.git"}]`,
		`[{"name":"../escape","url":"art://repo/escape"}]`,
		`[{"name":"hello","url":"art://repo/hello"},{"name":"hello","url":"art://repo/hello"}]`,
	} {
		if _, err := parsePackages([]byte(data)); err == nil {
			t.Errorf("accepted invalid package catalog %q", data)
		}
	}
	got, err := parsePackages(
		[]byte(
			`[{"name":"zeta","url":"art://repo/zeta"},{"name":"alpha","url":"art://repo/alpha"}]`,
		),
	)
	if err != nil || strings.Join(got, ",") != "alpha,zeta" {
		t.Fatalf("sorted package catalog = %v, %v", got, err)
	}
	if err := validateRefs(
		t.Context(),
		bytes.NewReader([]byte(goodRef)),
		int64(len(goodRef)),
		source.MetadataResponse{},
	); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int64{0, maxRefsBytes + 1, int64(len(goodRef) + 1)} {
		if err := validateRefs(
			t.Context(),
			bytes.NewReader([]byte(goodRef)),
			size,
			source.MetadataResponse{},
		); err == nil {
			t.Errorf("accepted refs with declared size %d", size)
		}
	}
	goodPackages := []byte(`[{"name":"hello","url":"art://repo/hello"}]`)
	if err := validatePackages(
		t.Context(),
		bytes.NewReader(goodPackages),
		int64(len(goodPackages)),
		source.MetadataResponse{},
	); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int64{-1, maxRefsBytes + 1, int64(len(goodPackages) + 1)} {
		if err := validatePackages(
			t.Context(),
			bytes.NewReader(goodPackages),
			size,
			source.MetadataResponse{},
		); err == nil {
			t.Errorf("accepted catalog with declared size %d", size)
		}
	}
	var excessive strings.Builder
	for i := range 4097 {
		if i == 0 {
			excessive.WriteByte('[')
		} else {
			excessive.WriteByte(',')
		}
		fmt.Fprintf(&excessive, `{"name":"n%d","url":"art://repo/n%d"}`, i, i)
	}
	excessive.WriteByte(']')
	if _, err := parsePackages([]byte(excessive.String())); err == nil {
		t.Fatal("accepted over-limit package catalog")
	}
	var excessiveRefs strings.Builder
	for i := range 4097 {
		fmt.Fprintf(&excessiveRefs, "%s refs/tags/v1.0.%d\n", strings.Repeat("a", 44), i)
	}
	if _, err := parseRefs([]byte(excessiveRefs.String())); err == nil {
		t.Fatal("accepted more than 4096 Bower refs")
	}
}

type archiveEntry struct {
	name, body string
	kind       byte
}

func bowerArchive(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(
			&tar.Header{Name: e.name, Typeflag: e.kind, Size: int64(len(e.body))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
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

func TestUnitBowerArchiveRejectsUnsafeGitTrees(t *testing.T) {
	manifest := archiveEntry{
		"hello-1.0.0/bower.json",
		`{"name":"hello","version":"1.0.0"}`,
		tar.TypeReg,
	}
	for _, tc := range []struct {
		name    string
		entries []archiveEntry
	}{
		{"missing manifest", []archiveEntry{{"hello-1.0.0/index.js", "x", tar.TypeReg}}},
		{"wrong identity", []archiveEntry{{"hello-1.0.0/bower.json", `{"name":"other","version":"1.0.0"}`, tar.TypeReg}}},
		{"duplicate manifest", []archiveEntry{manifest, manifest}},
		{"second root", []archiveEntry{manifest, {"other/index.js", "x", tar.TypeReg}}},
		{"traversal", []archiveEntry{manifest, {"hello-1.0.0/../evil", "x", tar.TypeReg}}},
		{"symlink", []archiveEntry{manifest, {"hello-1.0.0/link", "", tar.TypeSymlink}}},
		{"absolute", []archiveEntry{manifest, {"/etc/passwd", "x", tar.TypeReg}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := bowerArchive(t, tc.entries)
			if err := validateArchive(
				"hello",
				"1.0.0",
				true,
			)(
				t.Context(),
				bytes.NewReader(data),
				int64(len(data)),
			); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
	data := bowerArchive(t, []archiveEntry{manifest})
	data[len(data)-5] ^= 1
	if err := validateArchive(
		"hello",
		"1.0.0",
	)(
		t.Context(),
		bytes.NewReader(data),
		int64(len(data)),
	); err == nil {
		t.Fatal("accepted corrupt gzip trailer")
	}
	for _, size := range []int64{0, maxArchiveBytes + 1} {
		if err := validateArchive(
			"hello",
			"1.0.0",
		)(
			t.Context(),
			bytes.NewReader(data),
			size,
		); err == nil {
			t.Errorf("accepted archive declared size %d", size)
		}
	}
	var malformedTar bytes.Buffer
	brokenGzip := gzip.NewWriter(&malformedTar)
	if _, err := brokenGzip.Write([]byte("not a tar stream")); err != nil {
		t.Fatal(err)
	}
	if err := brokenGzip.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateArchive(
		"hello",
		"1.0.0",
	)(
		t.Context(),
		bytes.NewReader(malformedTar.Bytes()),
		int64(malformedTar.Len()),
	); err == nil {
		t.Fatal("accepted gzip containing malformed tar")
	}
	for _, tc := range []struct {
		name    string
		entries []archiveEntry
	}{
		{"single root file", []archiveEntry{{"hello-1.0.0", "x", tar.TypeReg}}},
		{"backslash path", []archiveEntry{manifest, {"hello-1.0.0/a\\b", "x", tar.TypeReg}}},
		{"invalid nested path", []archiveEntry{manifest, {"hello-1.0.0/a:b", "x", tar.TypeReg}}},
		{"wrong version", []archiveEntry{{"hello-1.0.0/bower.json", `{"name":"hello","version":"2.0.0"}`, tar.TypeReg}}},
		{"invalid manifest", []archiveEntry{{"hello-1.0.0/bower.json", `{"name":`, tar.TypeReg}}},
		{"oversized manifest", []archiveEntry{{"hello-1.0.0/bower.json", strings.Repeat("x", 1<<20+1), tar.TypeReg}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := bowerArchive(t, tc.entries)
			if err := validateArchive(
				"hello",
				"1.0.0",
				true,
			)(
				t.Context(),
				bytes.NewReader(archive),
				int64(len(archive)),
			); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
}

func TestUnitBowerArchiveAllowsSafeDirectories(t *testing.T) {
	data := bowerArchive(t, []archiveEntry{
		{"hello-1.0.0/", "", tar.TypeDir},
		{"hello-1.0.0/lib/", "", tar.TypeDir},
		{"hello-1.0.0/bower.json", `{"name":"hello","version":"1.0.0"}`, tar.TypeReg},
		{"hello-1.0.0/lib/index.js", "module.exports = 1", tar.TypeReg},
	})
	if err := validateArchive("hello", "1.0.0")(
		t.Context(), bytes.NewReader(data), int64(len(data)),
	); err != nil {
		t.Fatalf("safe directory entries rejected: %v", err)
	}
}

func TestUnitBowerArchiveEnforcesGlobalPAXPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		accept           bool
	}{
		{"safe comment", "comment", "created by GitHub", true},
		{"oversized comment", "comment", strings.Repeat("x", 4097), false},
		{"path override", "path", "../../escape", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			gz := gzip.NewWriter(&out)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&tar.Header{
				Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader,
				PAXRecords: map[string]string{tc.key: tc.value},
			}); err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"name":"hello","version":"1.0.0"}`)
			if err := tw.WriteHeader(&tar.Header{
				Name: "hello-1.0.0/bower.json", Typeflag: tar.TypeReg, Size: int64(len(body)),
			}); err != nil {
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
			err := validateArchive("hello", "1.0.0")(
				t.Context(), bytes.NewReader(out.Bytes()), int64(out.Len()),
			)
			if (err == nil) != tc.accept {
				t.Fatalf("global PAX acceptance = %t, want %t: %v", err == nil, tc.accept, err)
			}
		})
	}
}
