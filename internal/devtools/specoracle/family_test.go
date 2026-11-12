package specoracle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-cmp/cmp"
)

// docsSite models the parts of docs.jfrog.com Fetch reads: per-family
// llms.txt indexes, the parent index of each docs project, which lists every
// family under a "## API Reference: <title>" heading, and the endpoint pages.
// Pages are served from pages by name; an unknown page answers 404.
type docsSite struct {
	server  *httptest.Server
	router  chi.Router
	parents map[string]*atomic.Int32
}

func newDocsSite(t *testing.T, pages map[string]string) *docsSite {
	t.Helper()
	s := &docsSite{router: chi.NewRouter(), parents: map[string]*atomic.Int32{}}
	s.router.Get("/{project}/reference/{page}.md", func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[chi.URLParam(r, "page")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	s.server = httptest.NewServer(s.router)
	t.Cleanup(s.server.Close)
	return s
}

// index serves an llms.txt at path; {URL} in body becomes the site's URL.
func (s *docsSite) index(path string, status int, body string) {
	s.router.Get(path, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(strings.ReplaceAll(body, "{URL}", s.server.URL)))
	})
}

// parent serves a parent index and counts how often it is read.
func (s *docsSite) parent(path string, status int, body string) {
	n := &atomic.Int32{}
	s.parents[path] = n
	s.router.Get(path, func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(strings.ReplaceAll(body, "{URL}", s.server.URL)))
	})
}

func (s *docsSite) url(path string) string { return s.server.URL + path }

func (s *docsSite) fetch(
	t *testing.T,
	dir string,
	indexes []string,
	known map[string][]string,
) Manifest {
	t.Helper()
	m, err := Fetch(context.Background(), FetchOptions{
		Indexes:  indexes,
		CacheDir: dir,
		Client:   s.server.Client(),
		Delay:    time.Millisecond,
		Retries:  2,
		Known:    known,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// artifactoryParent is the shape of docs.jfrog.com/artifactory/reference/llms.txt
// on 2026-11-12: the repositories family is listed under its new heading,
// while its own index, repositories-api/llms.txt, answers 404.
const artifactoryParent = `# Binary Management Documentation

> Documentation for Binary Management

- [Stray](https://docs.example/artifactory/reference/stray.md): before any heading

## API Reference: Artifacts & Storage API
- Item Management APIs
  - [Get Storage Item](https://docs.example/artifactory/reference/getstorageitem.md): d

## API Reference: JFrog Artifactory Repositories API
- Repositories
  - [Create Repository]({URL}/artifactory/reference/createrepository.md): d
  - [Get Repository Configuration]({URL}/artifactory/reference/getrepositoryconfiguration.md): d
  - [Get Connectable Repositories]({URL}/artifactory/reference/getconnectablerepositories.md): d
  - [Gone]({URL}/artifactory/reference/gone.md): d

## API Reference: Federated Repositories API
- Federated Repositories
  - [Create Repository]({URL}/artifactory/reference/createrepository.md): d
  - [Convert to Federated]({URL}/artifactory/reference/converttofederated.md): d
`

func TestAccFetchTakesAFailedFamilyIndexFromItsParent(t *testing.T) {
	site := newDocsSite(t, map[string]string{
		"createrepository":           theirRepoPage,
		"getrepositoryconfiguration": theirRepoPage,
		"getconnectablerepositories": theirRepoPage,
		"converttofederated":         theirRepoPage,
		"getstorageitem":             theirRepoPage,
	})
	site.index(
		"/artifactory/reference/artifacts-storage-api/llms.txt",
		http.StatusOK,
		"## API Reference: Artifacts & Storage API\n- [Get Storage Item]({URL}/artifactory/reference/getstorageitem.md): d\n",
	)
	site.index("/artifactory/reference/repositories-api/llms.txt", http.StatusNotFound, "Not Found")
	site.index("/artifactory/reference/federated-repositories-api/llms.txt",
		http.StatusServiceUnavailable, "down")
	site.parent("/artifactory/reference/llms.txt", http.StatusOK, artifactoryParent)

	storage := site.url("/artifactory/reference/artifacts-storage-api/llms.txt")
	repos := site.url("/artifactory/reference/repositories-api/llms.txt")
	federated := site.url("/artifactory/reference/federated-repositories-api/llms.txt")
	parent := site.url("/artifactory/reference/llms.txt")
	dir := t.TempDir()
	m := site.fetch(t, dir, []string{storage, repos, federated}, map[string][]string{
		// Two of the renamed family's pages, without the .md the parent adds:
		// the federated section lists one of them too, and must lose.
		"artifactory-repositories-api": {
			site.url("/artifactory/reference/createrepository"),
			site.url("/artifactory/reference/getrepositoryconfiguration.md"),
		},
		"artifactory-federated-repositories-api": {
			site.url("/artifactory/reference/converttofederated.md"),
		},
	})

	got := map[string]FamilyRecord{}
	for _, fam := range m.Families {
		got[fam.Family] = fam
	}
	if diff := cmp.Diff(FamilyRecord{
		Index:  storage,
		Family: "artifactory-artifacts-storage-api",
		Source: storage,
	}, got["artifactory-artifacts-storage-api"]); diff != "" {
		t.Errorf("a working index is its own source (-want +got):\n%s", diff)
	}
	if fam := got["artifactory-repositories-api"]; fam.Source != parent ||
		fam.Section != "API Reference: JFrog Artifactory Repositories API" ||
		!strings.Contains(fam.Error, "status 404") {
		t.Errorf("the renamed family should come from its parent section: %+v", fam)
	}
	if fam := got["artifactory-federated-repositories-api"]; fam.Source != parent ||
		fam.Section != "API Reference: Federated Repositories API" ||
		!strings.Contains(fam.Error, "giving up") {
		t.Errorf("a family index that keeps failing should come from its parent section: %+v", fam)
	}
	if n := site.parents["/artifactory/reference/llms.txt"].Load(); n != 1 {
		t.Errorf("the parent index was read %d times, want once for both families", n)
	}

	var files, failed []string
	for _, e := range m.Entries {
		if e.File != "" {
			files = append(files, e.File)
		}
		if e.Error != "" {
			failed = append(failed, e.URL)
		}
	}
	// Every link of the chosen section is fetched, the new
	// getconnectablerepositories page included, and the one that answers 404
	// does not stop the rest.
	want := []string{
		"artifactory-artifacts-storage-api/getstorageitem.json",
		"artifactory-federated-repositories-api/converttofederated.json",
		"artifactory-federated-repositories-api/createrepository.json",
		"artifactory-repositories-api/createrepository.json",
		"artifactory-repositories-api/getconnectablerepositories.json",
		"artifactory-repositories-api/getrepositoryconfiguration.json",
	}
	if diff := cmp.Diff(want, files); diff != "" {
		t.Errorf("cached blocks (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{site.url("/artifactory/reference/gone.md")}, failed); diff != "" {
		t.Errorf("failed pages (-want +got):\n%s", diff)
	}
	for _, f := range want {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Error(err)
		}
	}
	if len(m.Missing()) != 0 {
		t.Errorf("no family should be missing: %+v", m.Missing())
	}
	if err := CheckFamilies(dir); err != nil {
		t.Errorf("a cache with every family should pass: %v", err)
	}
}

func TestAccFetchRecordsEachFamilyNoSourceGave(t *testing.T) {
	site := newDocsSite(t, map[string]string{
		"noblock":        "# nothing here\n",
		"getstorageitem": theirRepoPage,
	})
	site.parent("/artifactory/reference/llms.txt", http.StatusOK, artifactoryParent)
	site.parent("/integrations/reference/llms.txt", http.StatusNotFound, "Not Found")
	site.parent("/projects/reference/llms.txt", http.StatusServiceUnavailable, "down")
	storage := "- [Get Storage Item]({URL}/artifactory/reference/getstorageitem.md): d\n"
	// Each family fails in its own way. why is what its record and
	// CheckFamilies must say; none means the family is not missing.
	families := []struct {
		path, body string
		status     int
		known      []string
		why        []string
	}{
		{
			"/artifactory/reference/repositories-api/llms.txt", "Not Found", http.StatusNotFound,
			[]string{"/artifactory/reference/unlisted.md"},
			[]string{"status 404", "no section of the parent index lists a known page"},
		},
		{
			"/artifactory/reference/searches-api/llms.txt",
			"<html>soft 404</html>",
			http.StatusOK,
			nil,
			[]string{"index lists no page", "no page of the family is known"},
		},
		{
			"/integrations/reference/jfrog-artifactory-builds-api/llms.txt",
			"Not Found",
			http.StatusNotFound,
			[]string{"/integrations/reference/getallbuilds.md"},
			[]string{
				"status 404",
				"parent index " + site.url("/integrations/reference/llms.txt") + ": status 404",
			},
		},
		{
			"/projects/reference/jfrog-platform-projects-api/llms.txt",
			"Not Found",
			http.StatusNotFound,
			[]string{"/projects/reference/getprojects.md"},
			[]string{
				"status 404",
				site.url("/projects/reference/llms.txt") + ": giving up after 2 attempts",
			},
		},
		{
			"/llms.txt", "Not Found", http.StatusNotFound,
			[]string{"/x.md"},
			[]string{"status 404", "index has no parent llms.txt"},
		},
		{
			"/artifactory/reference/noblock-api/llms.txt",
			"- [No Block]({URL}/artifactory/reference/noblock.md): d\n",
			http.StatusOK,
			nil,
			[]string{"no page it lists gave an OpenAPI block"},
		},
		{
			"/artifactory/reference/blocked-api/llms.txt", storage, http.StatusOK, nil,
			[]string{"create family dir"},
		},
		{"/artifactory/reference/artifacts-storage-api/llms.txt", storage, http.StatusOK, nil, nil},
	}
	dir := t.TempDir()
	// A file where the family's cache directory belongs.
	if err := os.WriteFile(filepath.Join(dir, "artifactory-blocked-api"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var indexes, wantMissing []string
	known := map[string][]string{}
	for _, f := range families {
		site.index(f.path, f.status, f.body)
		index := site.url(f.path)
		indexes = append(indexes, index)
		for _, k := range f.known {
			known[FamilySlug(index)] = append(known[FamilySlug(index)], site.url(k))
		}
		if f.why != nil {
			wantMissing = append(wantMissing, FamilySlug(index))
		}
	}
	m := site.fetch(t, dir, indexes, known)

	var missing []string
	for _, fam := range m.Missing() {
		missing = append(missing, fam.Family)
		// A family whose pages hold no block had a working index, so only a
		// family that failed must name no source.
		if fam.Error != "" && fam.Source != "" {
			t.Errorf(
				"%s: no source gave its pages, yet the record names %s",
				fam.Family,
				fam.Source,
			)
		}
	}
	if diff := cmp.Diff(wantMissing, missing); diff != "" {
		t.Errorf("missing families (-want +got):\n%s", diff)
	}
	if n := site.parents["/artifactory/reference/llms.txt"].Load(); n != 1 {
		t.Errorf(
			"the artifactory parent was read %d times, want once: only one of its families knows a page",
			n,
		)
	}

	err := CheckFamilies(dir)
	if !errors.Is(err, ErrMissingFamily) {
		t.Fatalf("CheckFamilies = %v, want ErrMissingFamily", err)
	}
	lines := map[string]string{}
	for _, line := range strings.Split(err.Error(), "\n") {
		family, rest, _ := strings.Cut(
			strings.TrimPrefix(line, ErrMissingFamily.Error()+": "),
			", ",
		)
		lines[family] = rest
	}
	for i, f := range families {
		family, line := FamilySlug(indexes[i]), lines[FamilySlug(indexes[i])]
		if f.why == nil {
			if line != "" {
				t.Errorf("%s has a block, yet CheckFamilies names it: %s", family, line)
			}
			continue
		}
		if !strings.HasPrefix(line, "index "+indexes[i]+": ") {
			t.Errorf("CheckFamilies should name %s with its index %s: %q", family, indexes[i], line)
		}
		for _, why := range f.why {
			if !strings.Contains(line, why) {
				t.Errorf("%s: %q should say %q", family, line, why)
			}
		}
	}
}

func TestUnitCheckFamiliesReadsTheManifest(t *testing.T) {
	if err := CheckFamilies(t.TempDir()); err == nil {
		t.Fatal("a cache without a manifest should fail")
	}
	write := func(t *testing.T, data string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if err := CheckFamilies(write(t, "{bad")); err == nil {
		t.Fatal("a manifest that does not parse should fail")
	}
	// A manifest from before Fetch recorded families cannot show a missing
	// one, so it is refused rather than passed.
	old := write(t, `{"entries":[{"index":"i","family":"f","url":"u","file":"f/p.json"}]}`)
	if err := CheckFamilies(old); !errors.Is(err, ErrNoFamilies) {
		t.Fatalf("CheckFamilies = %v, want ErrNoFamilies", err)
	}
	good, err := json.Marshal(Manifest{
		Families: []FamilyRecord{{Index: "i", Family: "f", Source: "i"}},
		Entries:  []ManifestEntry{{Index: "i", Family: "f", URL: "u", File: "f/p.json"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckFamilies(write(t, string(good))); err != nil {
		t.Fatalf("CheckFamilies = %v, want nil", err)
	}
}

func TestUnitKnownPagesReadsTheInventory(t *testing.T) {
	dir := t.TempDir()
	got, err := KnownPages(filepath.Join(dir, "absent.json"))
	if err != nil || len(got) != 0 {
		t.Fatalf("an absent inventory should give no pages: %v %v", got, err)
	}
	if _, err := KnownPages(dir); err == nil {
		t.Fatal("a directory should fail to read")
	}
	path := filepath.Join(dir, "inventory.json")
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := KnownPages(path); err == nil {
		t.Fatal("an inventory that does not parse should fail")
	}
	inv := Inventory{Operations: []OpRecord{
		{Family: "a", Source: "https://d/a/one.md", Key: "GET /1"},
		{Family: "a", Source: "https://d/a/one.md", Key: "PUT /1"},
		{Family: "a", Source: "https://d/a/two.md", Key: "GET /2"},
		{Family: "b", Source: "https://d/b/three.md", Key: "GET /3"},
		{Family: "b", Key: "GET /no-source"},
	}}
	if err := inv.WriteJSON(path); err != nil {
		t.Fatal(err)
	}
	got, err = KnownPages(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string][]string{
		"a": {"https://d/a/one.md", "https://d/a/two.md"},
		"b": {"https://d/b/three.md"},
	}, got); diff != "" {
		t.Fatalf("known pages (-want +got):\n%s", diff)
	}
}

func TestUnitParentURLAndSections(t *testing.T) {
	for index, want := range map[string]string{
		"https://docs.jfrog.com/artifactory/reference/repositories-api/llms.txt": "https://docs.jfrog.com/artifactory/reference/llms.txt",
		"https://docs.jfrog.com/dead/llms.txt":                                   "https://docs.jfrog.com/llms.txt",
		"https://docs.jfrog.com/llms.txt":                                        "",
		"https://docs.jfrog.com/artifactory/reference/index.md":                  "",
		"http://x":  "",
		"::bad url": "",
	} {
		got, err := parentURL(index)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("parentURL(%q) = %q, %v; want %q", index, got, err, want)
		}
	}
	sections := parseSections(
		"- [Stray](https://d/s.md): x\n## A\n- [One](https://d/1.md): x\n### Sub\n- [Two](https://d/2.md): x\n## B\n",
	)
	if diff := cmp.Diff([]indexSection{
		{title: "A", links: [][2]string{{"One", "https://d/1.md"}, {"Two", "https://d/2.md"}}},
		{title: "B"},
	}, sections, cmp.AllowUnexported(indexSection{})); diff != "" {
		t.Fatalf("sections (-want +got):\n%s", diff)
	}
}
