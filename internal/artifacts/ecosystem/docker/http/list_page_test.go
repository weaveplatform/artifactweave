package dockerhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

// putManifests stores a manifest file at each folder of repoKey, straight
// into the store: a listing reads the layout alone.
func (w *blobWorld) putManifests(repoKey string, folders ...string) {
	w.t.Helper()
	for _, f := range folders {
		if _, err := w.db.Items().Put(context.Background(), artmodel.Item{
			RepoKey: repoKey, Name: f + "/" + layout.ManifestFile,
		}); err != nil {
			w.t.Fatal(err)
		}
	}
}

// collected is a listing's answer as the registry built it before it read
// pages from the store: every name collected and sorted, then cut by
// paginateHeaders and written by json.Encoder. head holds the map's other
// keys and key the list's.
func collected(
	target string, names []string, head map[string]any, key string,
) (string, string) {
	sorted := slices.Clone(names)
	slices.SortFunc(sorted, layout.Compare)
	r := httptest.NewRequest(http.MethodGet, target, nil)
	header := http.Header{}
	body := map[string]any{key: paginateHeaders(header, r, sorted)}
	for k, v := range head {
		body[k] = v
	}
	var out bytes.Buffer
	_ = json.NewEncoder(&out).Encode(body)
	return out.String(), header.Get("Link")
}

// pageQueries are the n and last of the listings compared with collected:
// pages that fit the buffer, pages longer than it, the rest without n, and
// the edges of each.
func pageQueries(names []string) []string {
	sorted := slices.Clone(names)
	slices.SortFunc(sorted, layout.Compare)
	mid := sorted[len(sorted)/2]
	out := []string{
		"", "?n=0", "?n=1", "?n=-1", "?n=x", "?n=+3", "?last=" + mid,
		"?last=" + strings.ToUpper(mid), "?last=" + sorted[len(sorted)-1], "?last=~",
		"?n=3&last=" + mid, "?n=0&last=" + mid, "?n=2&last=~",
	}
	for _, n := range []int{
		listBuffer - 1, listBuffer, listBuffer + 1, len(names) - 1, len(names), len(names) + 1,
		2 * listBuffer,
	} {
		out = append(out, "?n="+strconv.Itoa(n), "?n="+strconv.Itoa(n)+"&last="+sorted[2])
	}
	return out
}

// A listing longer than the buffer streams, and every page, with its
// Link, is byte for byte the page collected and cut (#1402).
func TestAccLongListingsAnswerAsCollected(t *testing.T) {
	w := newBlobWorld(t)
	var tags, folders []string
	for i := range listBuffer + 5 {
		// Tags that differ only in case, and _ . - around the digits.
		tag := []string{"v", "V", "_v", "v.", "v-"}[i%5] + fmt.Sprintf("%04d", i/5)
		tags = append(tags, tag)
		folders = append(folders, "big/"+tag)
	}
	w.putManifests("docker-local", folders...)
	var images []string
	for i := range listBuffer + 5 {
		images = append(images, fmt.Sprintf("img-%04d", i))
		w.putManifests("docker-local", fmt.Sprintf("img-%04d/v1", i))
	}
	const base = "/v2/docker-local/big/tags/list"
	for _, query := range pageQueries(tags) {
		want, link := collected(
			base+query,
			tags,
			map[string]any{"name": "docker-local/big"},
			"tags",
		)
		rec := w.do(http.MethodGet, base+query, "", nil)
		if rec.Code != http.StatusOK || rec.Body.String() != want ||
			rec.Header().Get("Link") != link {
			t.Errorf("tags %s: %d Link %q, want %q; body differs: %v", query, rec.Code,
				rec.Header().Get("Link"), link, rec.Body.String() != want)
		}
	}
	all := append(slices.Clone(images), "big")
	const catalog = "/v2/docker-local/_catalog"
	for _, query := range pageQueries(all) {
		want, link := collected(catalog+query, all, nil, "repositories")
		rec := w.do(http.MethodGet, catalog+query, "", nil)
		if rec.Code != http.StatusOK || rec.Body.String() != want ||
			rec.Header().Get("Link") != link {
			t.Errorf("catalog %s: %d Link %q, want %q; body differs: %v", query, rec.Code,
				rec.Header().Get("Link"), link, rec.Body.String() != want)
		}
	}
}

// The registry-wide catalog merges its repositories' sorted pages into one
// order: keys that sort apart from their images' names, such as a-b before
// a/, and a last that names a repository in another case.
func TestAccRegistryCatalogMergesRepositories(t *testing.T) {
	w := newBlobWorld(t)
	for _, key := range []string{"a", "a-b", "ab", "A.c", "empty"} {
		if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&repo.Config{
			Key: key, Class: repo.Local, PackageType: repo.Docker,
		})); err != nil {
			t.Fatal(err)
		}
	}
	var all []string
	for key, images := range map[string][]string{
		"a": {"b", "x/y", "c"}, "a-b": {"c", "a"}, "ab": {"a"}, "A.c": {"d"},
		"docker-local": {"app", "Bad"}, "oci-local": {"tools"},
	} {
		for _, im := range images {
			w.putManifests(key, im+"/v1")
			if ValidName(im) {
				all = append(all, key+"/"+im)
			}
		}
	}
	queries := pageQueries(all)
	for _, last := range []string{
		"a", "a/", "A/B", "a/b", "a-b/a", "A-B/A", "a.c/d", "A.C/D", "ab",
		"docker-local", "zzz", "a0",
	} {
		queries = append(queries, "?last="+last, "?n=2&last="+last)
	}
	for _, query := range queries {
		want, link := collected("/v2/_catalog"+query, all, nil, "repositories")
		rec := w.do(http.MethodGet, "/v2/_catalog"+query, "", nil)
		if rec.Code != http.StatusOK || rec.Body.String() != want ||
			rec.Header().Get("Link") != link {
			t.Errorf("%s: %d %s Link %q, want %s Link %q", query, rec.Code, rec.Body,
				rec.Header().Get("Link"), want, link)
		}
	}
	// A repository without images lists [], never null.
	for _, query := range []string{"", "?n=0", "?n=1", "?last=a", "?n=2000"} {
		want, _ := collected("/v2/empty/_catalog"+query, nil, nil, "repositories")
		rec := w.do(http.MethodGet, "/v2/empty/_catalog"+query, "", nil)
		if rec.Code != http.StatusOK || rec.Body.String() != want ||
			rec.Header().Get("Link") != "" {
			t.Errorf("empty%s: %d %s, want %s", query, rec.Code, rec.Body, want)
		}
	}
}

// pageFaults fails the page reader's queries once after of them have
// answered.
type pageFaults struct {
	port.ItemStore
	after *int
}

func (f pageFaults) DockerPages() port.DockerPageStore {
	return faultyPages{f.ItemStore.DockerPages(), f.after}
}

type faultyPages struct {
	port.DockerPageStore
	after *int
}

func (f faultyPages) fail() bool {
	*f.after--
	return *f.after < 0
}

func (f faultyPages) ManifestFolders(
	ctx context.Context, q port.DockerFolderQuery,
) ([]string, error) {
	if f.fail() {
		return nil, errBoom
	}
	return f.DockerPageStore.ManifestFolders(ctx, q) //nolint:wrapcheck // the store's own
}

func (f faultyPages) ImageManifests(
	ctx context.Context, q port.DockerImageQuery,
) ([]port.DockerImageManifest, error) {
	if f.fail() {
		return nil, errBoom
	}
	return f.DockerPageStore.ImageManifests(ctx, q) //nolint:wrapcheck // the store's own
}

// A store failure before a listing's first byte answers 500, as when the
// list was collected; one after it ends the connection with the body
// unclosed, so the client cannot take a short list for the whole one.
func TestAccListingStoreFailures(t *testing.T) {
	w := newBlobWorld(t)
	var folders, images []string
	for i := range listBuffer + 5 {
		folders = append(folders, fmt.Sprintf("big/v%04d", i))
		images = append(images, fmt.Sprintf("img-%04d/v1", i))
	}
	w.putManifests("docker-local", folders...)
	w.putManifests("docker-local", images...)
	for _, c := range []struct {
		target string
		after  int
		abort  bool
	}{
		// The page and the found check read before the answer starts.
		{"/v2/docker-local/big/tags/list", 0, false},
		{"/v2/docker-local/big/tags/list?n=0", 0, false},
		{"/v2/docker-local/big/tags/list?n=2&last=zzz", 1, false},
		{"/v2/docker-local/_catalog", 0, false},
		{"/v2/docker-local/_catalog?n=" + strconv.Itoa(listBuffer+3), 1, false},
		// The rest of a listing without n, and a page longer than the
		// buffer read again, stream after the first bytes.
		{"/v2/docker-local/big/tags/list", 1, true},
		{"/v2/docker-local/big/tags/list?n=" + strconv.Itoa(listBuffer+3), 2, true},
		{"/v2/docker-local/_catalog", 2, true},
		{"/v2/docker-local/_catalog?n=" + strconv.Itoa(listBuffer+3), 3, true},
	} {
		after := c.after
		var noted error
		h := router(New(Options{
			Store: itemsFault{Store: w.db, items: pageFaults{w.db.Items(), &after}},
			Fault: func(_ *http.Request, err error) { noted = err }, Formats: ecosystem.For,
		}))
		rec := httptest.NewRecorder()
		aborted := func() (aborted bool) {
			defer func() {
				if v := recover(); v != nil {
					if v != http.ErrAbortHandler { //nolint:errorlint // the panic's own value
						panic(v)
					}
					aborted = true
				}
			}()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.target, nil))
			return false
		}()
		if !errors.Is(noted, errBoom) || aborted != c.abort {
			t.Errorf("%s after %d: noted %v, aborted %v", c.target, c.after, noted, aborted)
			continue
		}
		switch {
		case !c.abort && rec.Code != http.StatusInternalServerError:
			t.Errorf("%s after %d: %d %s", c.target, c.after, rec.Code, rec.Body)
		case c.abort && (rec.Code != http.StatusOK || strings.HasSuffix(rec.Body.String(), "]}\n")):
			t.Errorf("%s after %d: %d, body closed", c.target, c.after, rec.Code)
		}
	}
}

// countPages counts the rows the page reader's queries return, and runs
// before, when it is set, ahead of each query with the number of queries
// made before it.
type countPages struct {
	port.ItemStore
	rows   *int
	before func(queries int)
}

func (c countPages) DockerPages() port.DockerPageStore {
	return &countedPages{DockerPageStore: c.ItemStore.DockerPages(), c: c}
}

type countedPages struct {
	port.DockerPageStore
	c       countPages
	queries int
}

func (p *countedPages) query() {
	if p.c.before != nil {
		p.c.before(p.queries)
	}
	p.queries++
}

func (p *countedPages) ManifestFolders(
	ctx context.Context, q port.DockerFolderQuery,
) ([]string, error) {
	p.query()
	out, err := p.DockerPageStore.ManifestFolders(ctx, q)
	*p.c.rows += len(out)
	return out, err //nolint:wrapcheck // the store's own
}

func (p *countedPages) ImageManifests(
	ctx context.Context, q port.DockerImageQuery,
) ([]port.DockerImageManifest, error) {
	p.query()
	out, err := p.DockerPageStore.ImageManifests(ctx, q)
	*p.c.rows += len(out)
	return out, err //nolint:wrapcheck // the store's own
}

// A page with n reads from the store the names it answers and the one after
// them, wherever it starts and however many the image or repository holds,
// rather than every manifest (#1402).
func TestAccListingReadsOnlyItsPage(t *testing.T) {
	w := newBlobWorld(t)
	var folders, images []string
	for i := range listBuffer + 5 {
		folders = append(folders, fmt.Sprintf("big/v%04d", i))
		images = append(images, fmt.Sprintf("img-%04d/v1", i))
	}
	w.putManifests("docker-local", folders...)
	w.putManifests("docker-local", images...)
	for _, c := range []struct {
		target string
		rows   int
	}{
		{"/v2/docker-local/big/tags/list?n=2", 3},
		{"/v2/docker-local/big/tags/list?n=2&last=v0900", 3},
		{"/v2/docker-local/big/tags/list?n=2&last=v1003", 1},
		// n=0 reads no page; whether the image exists takes one query of
		// Found's 16 folders.
		{"/v2/docker-local/big/tags/list?n=0", 16},
		// The first image, big, holds 1005 manifests: one query reads three
		// of them, and the next starts after the image.
		{"/v2/docker-local/_catalog?n=2", 6},
		{"/v2/docker-local/_catalog?n=2&last=img-0900", 3},
		{"/v2/docker-local/_catalog?n=0", 0},
		// Longer than the buffer: the count reads the page and the rest of
		// the last query, and the re-read only the names past the buffer.
		{"/v2/docker-local/big/tags/list?n=" + strconv.Itoa(listBuffer+3), listBuffer + 5 + 3},
	} {
		rows := 0
		h := router(New(Options{
			Store: itemsFault{
				Store: w.db,
				items: countPages{ItemStore: w.db.Items(), rows: &rows},
			},
			Formats: ecosystem.For,
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.target, nil))
		if rec.Code != http.StatusOK || rows != c.rows {
			t.Errorf("%s: %d, read %d rows, want %d", c.target, rec.Code, rows, c.rows)
		}
	}
}

// A tag written or deleted while a page longer than the buffer streams,
// after the count and before the re-read, is listed or left out, and no tag
// that was there throughout is lost: the page ends at the name its Link
// names, and the next page continues after it.
func TestAccLongPageWithWritesLosesNoTag(t *testing.T) {
	w := newBlobWorld(t)
	var folders []string
	for i := range listBuffer + 5 {
		folders = append(folders, fmt.Sprintf("big/v%04d", i))
	}
	w.putManifests("docker-local", folders...)
	rows := 0
	h := router(New(Options{
		Store: itemsFault{Store: w.db, items: countPages{
			ItemStore: w.db.Items(), rows: &rows,
			before: func(queries int) {
				// The count takes two queries; the re-read starts at the third.
				if queries != 2 {
					return
				}
				w.putManifests("docker-local", "big/v1000a", "big/v1001a")
				// v1002 is the n-th name, which the Link names: the page
				// still ends there.
				for _, gone := range []string{"big/v1001", "big/v1002"} {
					if _, err := w.db.Items().Delete(context.Background(), "docker-local", false,
						gone+"/"+layout.ManifestFile); err != nil {
						w.t.Fatal(err)
					}
				}
			},
		}},
		Formats: ecosystem.For,
	}))
	get := func(target string) (tags []string, link string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		var body struct{ Tags []string }
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Body)
		}
		return body.Tags, rec.Header().Get("Link")
	}
	const base = "/v2/docker-local/big/tags/list"
	first, link := get(base + "?n=" + strconv.Itoa(listBuffer+3))
	want := `<` + base + `?last=v1002&n=` + strconv.Itoa(listBuffer+3) + `>; rel="next"`
	if link != want {
		t.Fatalf("Link %q, want %q", link, want)
	}
	// v1000a and v1001a are listed and v1001 is not; v1002 is gone.
	if got := first[listBuffer:]; !slices.Equal(got, []string{"v1000", "v1000a", "v1001a"}) {
		t.Fatalf("names past the buffer %v", got)
	}
	second, _ := get(base + "?n=" + strconv.Itoa(listBuffer+3) + "&last=v1002")
	if !slices.Equal(second, []string{"v1003", "v1004"}) {
		t.Fatalf("next page %v", second)
	}
}
