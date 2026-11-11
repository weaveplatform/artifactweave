package skillshttp_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	skillsfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/skills"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
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
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "skills-limited",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{
				"limited": api.ActionRead,
			}},
			Targets: map[string]accesstest.Target{f.repo.Key: {
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
		path     string
		expected string
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
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), tc.expected) {
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
	packages, _, err := f.db.Packages().List(t.Context(), f.repo.Key, kernel.Page{})
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
		path, expected string
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
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), tc.expected) {
			t.Errorf("%s: %d %s, want %q", tc.path, r.Code, r.Body, tc.expected)
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
		path     string
		expected int
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
		if r := f.read("reader", tc.path); r.Code != tc.expected {
			t.Errorf("%s: %d %s, want %d", tc.path, r.Code, r.Body, tc.expected)
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

// TestAccSkillsDownloadPolicy checks Artifactory's download policy on the
// Skills reads (#1237). A bundle's download and a file of it refuse a
// blacked-out repository and a bundle the include and exclude patterns
// reject with the item routes' 404 and message, for an administrator too.
// A blacked-out repository's detail, versions, version and resolve, which
// a client resolves a version from, answer the blackout's 404 naming the
// skill's folder, and its downloads and single-file reads refuse before the
// index is read, so a version or skill that does not exist is refused
// alike. The list and
// search keep their answers, a version whose bundle the patterns reject
// stays listed, the scan status still answers for the stored bundle, and
// discovery stays anonymous. A caller who may not read the skill learns
// nothing of how the repository is set.
func TestAccSkillsDownloadPolicy(t *testing.T) {
	f := newFixture(t)
	published(t, f, "alpha", "1.0.0")
	latest := published(t, f, "alpha", "2.0.0")
	patch := func(change func(*repo.Config)) {
		t.Helper()
		current, err := f.db.Repos().Get(t.Context(), f.repo.Key)
		if err != nil {
			t.Fatal(err)
		}
		change(&current)
		if err := f.db.Repos().Update(t.Context(), current); err != nil {
			t.Fatal(err)
		}
	}
	answers := func(as, path string, status int, body string) {
		t.Helper()
		r := f.read(as, path)
		if r.Code != status || !strings.Contains(r.Body.String(), body) {
			t.Errorf("%s %s: %d %s, want %d with %q", as, path, r.Code, r.Body, status, body)
		}
	}
	// failed checks the whole answer of a refused read: its status, the
	// message in Artifactory's envelope and no header but the envelope's
	// type, so that nothing of a served bundle leaks into a refusal.
	failed := func(as, path string, status int, message string) {
		t.Helper()
		r := f.read(as, path)
		body := `{"errors":[{"message":"` + message + `","status":` +
			strconv.Itoa(status) + "}]}\n"
		if r.Code != status || r.Body.String() != body || !maps.EqualFunc(
			r.Header(), http.Header{"Content-Type": {"application/json"}}, slices.Equal) {
			t.Errorf("%s %s: %d %v %s, want %d %s", as, path, r.Code, r.Header(), r.Body,
				status, body)
		}
	}
	bundle := "/api/v1/download?slug=alpha&version=2.0.0"
	file := "/api/v1/skills/alpha/file?version=2.0.0&path=src%2Frun.js"
	malformed := "/api/v1/skills/alpha/file?version=2.0.0&path=..%2Fsecret"
	// bundleReads maps each read of a bundle, or of a file in it, that a
	// blacked-out repository refuses before the index is read to the path
	// its refusal names: a named version's bundle, stored or not, and the
	// skill's folder for latest, which only the index could resolve. A
	// file's name is checked after the index read, so a malformed one is
	// refused alike.
	bundleReads := map[string]string{
		bundle:    "alpha/2.0.0/alpha-2.0.0.zip",
		file:      "alpha/2.0.0/alpha-2.0.0.zip",
		malformed: "alpha/2.0.0/alpha-2.0.0.zip",

		"/api/v1/download?slug=alpha&version=9.9.9":    "alpha/9.9.9/alpha-9.9.9.zip",
		"/api/v1/download?slug=missing&version=1.0.0":  "missing/1.0.0/missing-1.0.0.zip",
		"/api/v1/download?slug=alpha&version=latest":   "alpha",
		"/api/v1/download?slug=missing&version=latest": "missing",

		"/api/v1/skills/alpha/file?version=9.9.9&path=SKILL.md":    "alpha/9.9.9/alpha-9.9.9.zip",
		"/api/v1/skills/missing/file?version=1.0.0&path=SKILL.md":  "missing/1.0.0/missing-1.0.0.zip",
		"/api/v1/skills/alpha/file?version=latest&path=SKILL.md":   "alpha",
		"/api/v1/skills/missing/file?version=latest&path=SKILL.md": "missing",
	}
	resolution := []string{
		"/api/v1/skills/alpha", "/api/v1/skills/alpha/versions",
		"/api/v1/skills/alpha/versions/2.0.0", "/api/v1/skills/alpha/versions/latest",
		"/api/v1/resolve?slug=alpha&hash=" + latest,
	}
	kept := func() {
		t.Helper()
		answers("reader", "/api/v1/skills", http.StatusOK, `"slug":"alpha"`)
		answers("reader", "/api/v1/search?q=alpha", http.StatusOK, `"version":"2.0.0"`)
		answers("reader", "/xrayStatus?path=alpha/2.0.0/alpha-2.0.0.zip", http.StatusOK,
			`"status":"NOT_IN_ENTITLEMENT"`)
		answers("", "/.well-known/clawhub.json", http.StatusOK, "apiBase")
	}

	patch(func(c *repo.Config) { c.ExcludesPattern = new("alpha/2.0.0/**") })
	rejected := "The repository 'skills-local' rejected the artifact " +
		"'skills-local:alpha/2.0.0/alpha-2.0.0.zip' due to its include/exclude pattern settings."
	for _, as := range []string{"admin", "reader"} {
		answers(as, bundle, http.StatusNotFound, rejected)
		answers(as, file, http.StatusNotFound, rejected)
		answers(as, "/api/v1/skills/alpha/file?version=latest&path=SKILL.md",
			http.StatusNotFound, rejected)
	}
	answers("reader", "/api/v1/download?slug=alpha&version=1.0.0", http.StatusOK, "PK")
	answers("reader", "/api/v1/skills/alpha", http.StatusOK, `"latestVersion":"2.0.0"`)
	answers("reader", "/api/v1/skills/alpha/versions", http.StatusOK, `"version":"2.0.0"`)
	answers(
		"reader",
		"/api/v1/skills/alpha/versions/2.0.0",
		http.StatusOK,
		`"fingerprint":"`+latest+`"`,
	)
	answers("reader", "/api/v1/resolve?slug=alpha&hash="+latest, http.StatusOK, `"version":"2.0.0"`)
	kept()

	patch(func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = new(true), new("") })
	blackedOut := func(path string) string {
		return "The repository 'skills-local' is blacked out and cannot serve artifact " +
			"'skills-local:" + path + "'."
	}
	// A read that names no version is malformed, and answers 400 before
	// any check, as without the blackout.
	unversioned := []string{
		"/api/v1/download?slug=alpha",
		"/api/v1/skills/alpha/file?path=SKILL.md",
	}
	for _, as := range []string{"admin", "reader"} {
		for path, named := range bundleReads {
			failed(as, path, http.StatusNotFound, blackedOut(named))
		}
		for _, path := range resolution {
			answers(as, path, http.StatusNotFound, blackedOut("alpha"))
		}
		// The refusal comes before the index is read: a skill or version
		// that does not exist is refused alike.
		answers(as, "/api/v1/skills/missing/versions", http.StatusNotFound, blackedOut("missing"))
		answers(as, "/api/v1/skills/alpha/versions/9.9.9", http.StatusNotFound, blackedOut("alpha"))
		for _, path := range unversioned {
			failed(as, path, http.StatusBadRequest, "invalid skill slug or version")
		}
	}
	kept()
	// Read permission first (DV-057).
	for path := range bundleReads {
		failed("nobody", path, http.StatusForbidden, "Forbidden")
	}
	for _, path := range resolution {
		r := f.read("nobody", path)
		if r.Code != http.StatusForbidden || strings.Contains(r.Body.String(), "blacked out") {
			t.Errorf("nobody %s: %d %s, want 403 without the setting", path, r.Code, r.Body)
		}
	}

	patch(func(c *repo.Config) { c.BlackedOut = new(false) })
	answers("reader", bundle, http.StatusOK, "PK")
	answers("reader", "/api/v1/download?slug=alpha&version=latest", http.StatusOK, "PK")
	answers("reader", file, http.StatusOK, "run 2.0.0")
	answers("reader", "/api/v1/skills/alpha/file?version=latest&path=SKILL.md", http.StatusOK,
		"name: alpha")
	for _, path := range resolution {
		answers("reader", path, http.StatusOK, "2.0.0")
	}
	// Without the blackout the index answers for a version or skill it
	// does not hold, and a file's name is checked once the version is.
	for _, path := range []string{
		"/api/v1/download?slug=alpha&version=9.9.9",
		"/api/v1/download?slug=missing&version=1.0.0",
		"/api/v1/download?slug=missing&version=latest",
		"/api/v1/skills/alpha/file?version=9.9.9&path=SKILL.md",
		"/api/v1/skills/missing/file?version=1.0.0&path=SKILL.md",
		"/api/v1/skills/missing/file?version=latest&path=SKILL.md",
	} {
		failed("reader", path, http.StatusNotFound, "Could not find resource")
	}
	failed("reader", malformed, http.StatusBadRequest, "invalid skill file path")
	for _, path := range unversioned {
		failed("reader", path, http.StatusBadRequest, "invalid skill slug or version")
	}
}
