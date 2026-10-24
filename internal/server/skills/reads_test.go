package skills_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	skillsfmt "github.com/weaveplatform/artifactweave/internal/format/skills"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const skillsBase = "/artifactory/api/skills/skills-local"

func (f *fixture) read(as, path string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(http.MethodGet, skillsBase+path, nil)
	r.Header.Set("X-User", as)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

func published(t *testing.T, f *fixture, slug, version string) string {
	t.Helper()
	manifest := "---\nname: " + slug + "\ndescription: " + slug + " summary\n" +
		"tags: [agent]\n---\n# " + slug + "\n"
	r := f.publish("admin", `{"slug":"`+slug+`","version":"`+version+`"}`,
		map[string]string{"SKILL.md": manifest, "src/run.js": "run " + version})
	if r.Code != http.StatusCreated {
		t.Fatalf("publish %s %s: %d %s", slug, version, r.Code, r.Body)
	}
	var response struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil || response.Fingerprint == "" {
		t.Fatalf("publish fingerprint: %v %s", err, r.Body)
	}
	return response.Fingerprint
}

func TestAccSkillsReadsRespectVersionPermissions(t *testing.T) {
	f := newFixture(t)
	old := published(t, f, "alpha", "1.0.0")
	newer := published(t, f, "alpha", "2.0.0")
	_ = published(t, f, "secret", "1.0.0")
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "skills-limited",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"limited": types.ActionRead,
			}},
			Targets: map[string]types.Target{f.repo.Key: {
				Include: []string{"alpha/1.0.0/**"},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	for _, endpoint := range []string{
		"/api/v1/skills", "/api/v1/search?q=alpha",
		"/api/v1/skills/alpha", "/api/v1/skills/alpha/versions",
		"/api/v1/download?slug=alpha&version=1.0.0",
		"/xrayStatus?path=alpha/1.0.0/alpha-1.0.0.zip",
	} {
		if r := f.read("", endpoint); r.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s: %d %s", endpoint, r.Code, r.Body)
		}
	}
	if r := f.read("", "/.well-known/clawhub.json"); r.Code != http.StatusOK ||
		!strings.Contains(r.Body.String(), "apiBase") {
		t.Fatalf("discovery: %d %s", r.Code, r.Body)
	}
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/api/v1/skills?limit=1", `"slug":"alpha"`},
		{"/api/v1/search?q=alpha", `"version":"1.0.0"`},
		{"/api/v1/skills/alpha", `"latestVersion":"1.0.0"`},
		{"/api/v1/skills/alpha/versions", `"fingerprint":"` + old + `"`},
		{"/api/v1/resolve?slug=alpha&hash=" + old, `"version":"1.0.0"`},
		{"/api/v1/skills/alpha/file?version=latest&path=src%2Frun.js", "run 1.0.0"},
		{"/xrayStatus?path=alpha/1.0.0/alpha-1.0.0.zip", `"status":"NOT_IN_ENTITLEMENT"`},
	} {
		r := f.read("limited", tc.path)
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), tc.want) {
			t.Errorf("limited %s: %d %s", tc.path, r.Code, r.Body)
		}
		if strings.Contains(r.Body.String(), newer) || strings.Contains(r.Body.String(), "secret") {
			t.Errorf("limited %s leaked denied skill: %s", tc.path, r.Body)
		}
	}
	if r := f.read("limited", "/api/v1/skills/secret"); r.Code == http.StatusOK {
		t.Fatalf("denied detail: %s", r.Body)
	}
	if r := f.read(
		"limited",
		"/api/v1/resolve?slug=alpha&hash="+newer,
	); r.Code != http.StatusNotFound {
		t.Fatalf("denied fingerprint: %d %s", r.Code, r.Body)
	}
	if r := f.read(
		"limited",
		"/api/v1/download?slug=alpha&version=2.0.0",
	); r.Code == http.StatusOK {
		t.Fatalf("denied download: %s", r.Body)
	}
	if r := f.read(
		"limited",
		"/xrayStatus?path=alpha/2.0.0/alpha-2.0.0.zip",
	); r.Code == http.StatusOK {
		t.Fatalf("denied scan status: %s", r.Body)
	}
}

func TestAccSkillsPaginationFilesAndLifecycle(t *testing.T) {
	f := newFixture(t)
	for _, slug := range []string{"alpha", "beta", "gamma"} {
		published(t, f, slug, "1.0.0")
	}
	first := f.read("reader", "/api/v1/skills?limit=1&sort=name")
	if first.Code != http.StatusOK {
		t.Fatalf("first page: %d %s", first.Code, first.Body)
	}
	var page struct {
		Items []struct {
			Slug string `json:"slug"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"` //nolint:tagliatelle // ClawHub wire format
		Total      int    `json:"total"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil ||
		page.Total != 3 || len(page.Items) != 1 || page.Items[0].Slug != "alpha" || page.NextCursor == "" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	second := f.read(
		"reader",
		"/api/v1/skills?limit=1&sort=name&cursor="+url.QueryEscape(page.NextCursor),
	)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"slug":"beta"`) {
		t.Fatalf("second page: %d %s", second.Code, second.Body)
	}
	for _, bad := range []string{
		"/api/v1/skills?cursor=not-base64!", "/api/v1/skills?sort=unknown",
		"/api/v1/skills/alpha/file?version=1.0.0&path=../secret",
		"/api/v1/skills/alpha/file?version=1.0.0&path=%2Fetc%2Fpasswd",
		"/api/v1/resolve?slug=alpha&hash=INVALID",
		"/xrayStatus?path=../alpha",
	} {
		if r := f.read("reader", bad); r.Code != http.StatusBadRequest {
			t.Errorf("malformed %s: %d %s", bad, r.Code, r.Body)
		}
	}
	if r := f.read(
		"reader",
		"/api/v1/skills/alpha/file?version=1.0.0&path=missing",
	); r.Code != http.StatusNotFound {
		t.Fatalf("missing file: %d %s", r.Code, r.Body)
	}
	if r := f.read(
		"reader",
		"/api/v1/download?slug=alpha&version=latest",
	); r.Code != http.StatusOK ||
		!strings.HasPrefix(r.Body.String(), "PK") {
		t.Fatalf("download: %d %s", r.Code, r.Body)
	}
	path := skillsfmt.Path("alpha", "1.0.0")
	if _, err := f.items.Delete(
		t.Context(),
		item.Target{Repo: f.repo, Path: path},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if r := f.read("reader", "/api/v1/skills/alpha"); r.Code != http.StatusNotFound {
		t.Fatalf("deleted detail: %d %s", r.Code, r.Body)
	}
	trash, err := f.db.Trash().Find(t.Context(), f.repo.Key, path)
	if err != nil || len(trash) != 1 {
		t.Fatalf("trash: %v %v", trash, err)
	}
	if _, err := f.items.Restore(
		t.Context(),
		item.Target{Repo: f.repo, Path: path},
		trash[0].Record,
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if r := f.read("reader", "/api/v1/skills/alpha"); r.Code != http.StatusOK {
		t.Fatalf("restored detail: %d %s", r.Code, r.Body)
	}
	// Reindex from the persisted ZIPs must preserve discoverability.
	packages, _, err := f.db.Packages().List(t.Context(), f.repo.Key, store.Page{})
	if err != nil || len(packages) != 3 {
		t.Fatalf("packages: %v %v", packages, err)
	}
	if err := f.db.Packages().Delete(t.Context(), packages[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := (skillsfmt.Handler{}).Reindex(t.Context(), f.items.Env(), f.repo); err != nil {
		t.Fatal(err)
	}
	if r := f.read("reader", "/api/v1/skills/alpha"); r.Code != http.StatusOK {
		t.Fatalf("reindexed detail: %d %s", r.Code, r.Body)
	}
}

func TestAccSkillsVersionAndSearchResponses(t *testing.T) {
	f := newFixture(t)
	for _, version := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		r := f.publish("admin", `{"slug":"catalog","version":"`+version+
			`","displayName":"Readable Catalog","summary":"Checks metadata",`+
			`"changelog":"Release `+version+`","tags":["automation"]}`,
			map[string]string{"SKILL.md": "# catalog\n", "src/run.js": "run " + version})
		if r.Code != http.StatusCreated {
			t.Fatalf("publish %s: %d %s", version, r.Code, r.Body)
		}
	}
	for _, tc := range []struct {
		path, want string
	}{
		{"/api/v1/skills/catalog", `"latestVersion":"3.0.0"`},
		{"/api/v1/skills/catalog/versions/2.0.0", `"version":"2.0.0"`},
		{"/api/v1/skills/catalog/versions/2.0.0", `"downloadUrl":`},
		{"/api/v1/search?q=catalog", `"match":"slug"`},
		{"/api/v1/search?q=readable", `"match":"displayName"`},
		{"/api/v1/search?q=metadata", `"match":"summary"`},
		{"/api/v1/search?q=automation", `"match":"tags"`},
		{"/api/v1/search?q=catalog&limit=200", `"limit":100`},
		{"/api/v1/search?q=catalog&limit=0", `"limit":1`},
		{"/api/v1/skills?sort=downloads", `"version":"3.0.0"`},
		{"/api/v1/skills/catalog/file?version=2.0.0&path=src%2Frun.js", "run 2.0.0"},
	} {
		r := f.read("reader", tc.path)
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), tc.want) {
			t.Errorf("%s: %d %s, want %q", tc.path, r.Code, r.Body, tc.want)
		}
	}
	first := f.read("reader", "/api/v1/skills/catalog/versions?limit=1")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"version":"3.0.0"`) {
		t.Fatalf("first version page: %d %s", first.Code, first.Body)
	}
	var page struct {
		Items []struct {
			Version string `json:"version"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"` //nolint:tagliatelle // JFrog CLI wire format
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil ||
		len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("first version page: %+v %v", page, err)
	}
	second := f.read("reader", "/api/v1/skills/catalog/versions?limit=1&cursor="+
		url.QueryEscape(page.NextCursor))
	if second.Code != http.StatusOK ||
		!strings.Contains(second.Body.String(), `"version":"2.0.0"`) {
		t.Fatalf("second version page: %d %s", second.Code, second.Body)
	}
	for _, bad := range []string{
		"/api/v1/skills/catalog/versions?limit=0",
		"/api/v1/skills/catalog/versions?cursor=bad!",
		"/api/v1/search?q=catalog&offset=-1",
		"/api/v1/search?q=catalog&limit=bad",
		"/api/v1/search?q=",
		"/api/v1/skills/Catalog",
		"/api/v1/skills/catalog/versions/not-a-version",
	} {
		if r := f.read("reader", bad); r.Code != http.StatusBadRequest {
			t.Errorf("bad %s: %d %s", bad, r.Code, r.Body)
		}
	}
	if r := f.read(
		"reader",
		"/api/v1/skills/catalog/versions/9.0.0",
	); r.Code != http.StatusNotFound {
		t.Errorf("missing version: %d %s", r.Code, r.Body)
	}
	if r := f.read("", "/api/v1/resolve?slug=catalog&hash=bad"); r.Code != http.StatusUnauthorized {
		t.Errorf("anonymous invalid hash: %d %s", r.Code, r.Body)
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/v1/download?slug=catalog&version=nope", http.StatusBadRequest},
		{"/api/v1/download?slug=missing&version=1.0.0", http.StatusNotFound},
		{"/api/v1/skills/catalog/file?version=bogus&path=SKILL.md", http.StatusBadRequest},
		{"/api/v1/skills/catalog/file?version=9.0.0&path=SKILL.md", http.StatusNotFound},
		{"/xrayStatus?path=missing/1.0.0/missing-1.0.0.zip", http.StatusNotFound},
		{"/api/v1/skills?limit=0", http.StatusBadRequest},
		{"/api/v1/skills/missing/versions", http.StatusNotFound},
		{"/api/v1/skills/Catalog/versions", http.StatusBadRequest},
		{"/api/v1/resolve?slug=Catalog&hash=" + strings.Repeat("a", 64), http.StatusBadRequest},
		{"/api/v1/skills?cursor=" + base64.RawURLEncoding.EncodeToString([]byte("invalid")), http.StatusBadRequest},
	} {
		if r := f.read("reader", tc.path); r.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.path, r.Code, r.Body, tc.want)
		}
	}
	if r := f.read("reader", "/api/v1/skills?cursor="+
		base64.RawURLEncoding.EncodeToString(
			[]byte(`{"offset":99}`),
		)); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"items":[]`) {
		t.Errorf("past-end cursor: %d %s", r.Code, r.Body)
	}
	if r := f.read(
		"reader",
		"/api/v1/search?q=unrelated",
	); r.Code != http.StatusOK ||
		!strings.Contains(r.Body.String(), `"results":[]`) {
		t.Errorf("empty search: %d %s", r.Code, r.Body)
	}
}

type skillBlobFault struct {
	blob.Store
	data []byte
	size int64
	err  error
}

type skillReadSeekCloser struct{ *bytes.Reader }

func (skillReadSeekCloser) Close() error { return nil }

func (b skillBlobFault) Open(_ context.Context, _ string) (io.ReadSeekCloser, blob.Info, error) {
	if b.err != nil {
		return nil, blob.Info{}, b.err
	}
	return skillReadSeekCloser{bytes.NewReader(b.data)}, blob.Info{Size: b.size}, nil
}

func TestAccSingleFileStoredBlobFaults(t *testing.T) {
	f := newFixture(t)
	published(t, f, "faulty", "1.0.0")
	original := f.items.Blobs
	path := "/api/v1/skills/faulty/file?version=1.0.0&path=src%2Frun.js"
	var corrupt bytes.Buffer
	zw := zip.NewWriter(&corrupt)
	w, err := zw.Create("src/run.js")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "marker")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	corruptData := bytes.Replace(corrupt.Bytes(), []byte("marker"), []byte("broken"), 1)
	for _, tc := range []struct {
		name  string
		fault skillBlobFault
	}{
		{"open error", skillBlobFault{Store: original, err: errors.New("read failed")}},
		{"oversized", skillBlobFault{Store: original, size: skillsfmt.MaxArchiveBytes + 1}},
		{"short read", skillBlobFault{Store: original, data: []byte("short"), size: 10}},
		{"bad zip", skillBlobFault{Store: original, data: []byte("bad"), size: 3}},
		{"bad entry checksum", skillBlobFault{
			Store: original, data: corruptData, size: int64(len(corruptData)),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.items.Blobs = tc.fault
			r := f.read("reader", path)
			if r.Code != http.StatusInternalServerError {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		})
	}
	f.items.Blobs = original
}
