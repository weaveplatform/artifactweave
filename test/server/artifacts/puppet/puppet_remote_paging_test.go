package puppet_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	puppetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet"
)

// cappedForge is an upstream Forge that serves acme-widget's releases
// newest first, sorted by version, and serves at most limitCap of them a
// page whatever limit is asked, as forgeapi.puppet.com serves at most 100
// (it answered limit=1000 with "limit": 100 on 2026-11-12). Its pages carry
// the Forge's whole pagination object. It records the limit and offset of
// each release page asked for.
type cappedForge struct {
	*httptest.Server
	versions []string
	archives map[string][]byte
	limitCap int
	// short serves one release fewer than a page that has more after it
	// should hold, while its pagination still states limitCap.
	short bool
	mu    sync.Mutex
	asked map[string]bool
	// files counts the archive requests.
	files int
}

func newCappedForge(t *testing.T, releases, limitCap int, short bool) *cappedForge {
	t.Helper()
	f := &cappedForge{
		archives: map[string][]byte{}, limitCap: limitCap, short: short,
		asked: map[string]bool{},
	}
	for i := releases - 1; i >= 0; i-- {
		v := fmt.Sprintf("1.%d.0", i)
		f.versions = append(f.versions, v)
		f.archives[v], _ = puppetArchive(t, v, "forge")
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *cappedForge) serve(out http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v3/releases" {
		f.mu.Lock()
		f.files++
		f.mu.Unlock()
		v, _ := strings.CutPrefix(r.URL.Path, "/v3/files/acme-widget-")
		if data, ok := f.archives[strings.TrimSuffix(v, ".tar.gz")]; ok {
			_, _ = out.Write(data)
			return
		}
		http.NotFound(out, r)
		return
	}
	q := r.URL.Query()
	// The Forge pages 20 when no limit is asked (RTFACT-14274).
	limit, offset := 20, 0
	if q.Has("limit") {
		limit, _ = strconv.Atoi(q.Get("limit"))
	}
	if q.Has("offset") {
		offset, _ = strconv.Atoi(q.Get("offset"))
	}
	f.mu.Lock()
	f.asked[fmt.Sprintf("limit=%s&offset=%d", q.Get("limit"), offset)] = true
	f.mu.Unlock()
	limit = min(limit, f.limitCap)
	total := len(f.versions)
	offset = min(offset, total)
	end := min(offset+limit, total)
	served := f.versions[offset:end]
	if f.short && end < total {
		served = served[:len(served)-1]
	}
	link := func(at int) string {
		return fmt.Sprintf("/v3/releases?module=acme-widget&limit=%d&offset=%d&sort_by=version",
			limit, at)
	}
	var previous, next any
	if offset > 0 {
		previous = link(max(offset-limit, 0))
	}
	if end < total {
		next = link(end)
	}
	results := []any{}
	for _, v := range served {
		data, manifest := f.archives[v], f.manifest(v)
		results = append(results, map[string]any{
			"uri": "/v3/releases/acme-widget-" + v, "slug": "acme-widget-" + v, "version": v,
			"file_uri":    "/v3/files/acme-widget-" + v + ".tar.gz",
			"file_sha256": checksum(data), "file_size": len(data),
			"metadata": manifest,
		})
	}
	out.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(out).Encode(map[string]any{
		"pagination": map[string]any{
			"limit": limit, "offset": offset, "first": link(0), "previous": previous,
			"current": link(offset), "next": next, "total": total,
		},
		"results": results,
	})
}

// manifest returns the metadata.json the archive of version v holds.
func (f *cappedForge) manifest(v string) puppetfmt.Manifest {
	return puppetfmt.Manifest{
		Name: "acme-widget", Version: v, Summary: "forge",
		Dependencies: []puppetfmt.Dependency{},
	}
}

// pagesAsked returns the limit and offset of every release page the
// remote asked for, sorted.
func (f *cappedForge) pagesAsked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for page := range f.asked {
		out = append(out, page)
	}
	slices.Sort(out)
	return out
}

// releaseRecord is a release as every Puppet route answers it, in the
// order of its fields.
type releaseRecord struct {
	URI        string             `json:"uri"`
	Slug       string             `json:"slug"`
	Version    string             `json:"version"`
	FileURI    string             `json:"file_uri"`
	FileSHA256 string             `json:"file_sha256"`
	FileSize   int                `json:"file_size"`
	Metadata   puppetfmt.Manifest `json:"metadata"`
	Tags       []string           `json:"tags"`
}

type pageObject struct {
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
	Total  int     `json:"total"`
	Next   *string `json:"next"`
}

func (f *cappedForge) record(v string) releaseRecord {
	data := f.archives[v]
	return releaseRecord{
		URI: "/v3/releases/acme-widget-" + v, Slug: "acme-widget-" + v, Version: v,
		FileURI: "/v3/files/acme-widget-" + v + ".tar.gz", FileSHA256: checksum(data),
		FileSize: len(data), Metadata: f.manifest(v), Tags: []string{},
	}
}

func (f *cappedForge) moduleRecord() map[string]any {
	releases := []map[string]string{}
	for _, v := range f.versions {
		releases = append(releases, map[string]string{
			"version": v, "uri": "/v3/releases/acme-widget-" + v,
		})
	}
	return map[string]any{
		"name": "widget", "slug": "acme-widget", "uri": "/v3/modules/acme-widget",
		"owner":           map[string]string{"username": "acme"},
		"current_release": f.record(f.versions[0]), "releases": releases,
	}
}

// answers returns the exact body each route of a remote, or of a virtual
// repository over it, answers for acme-widget once it has read every
// release the Forge holds: the release list a page of 100 at a time, the
// newest release, the oldest one, which the Forge serves last, the module
// and the module search.
func (f *cappedForge) answers(t *testing.T) map[string]string {
	t.Helper()
	encode := func(doc any) string {
		var b bytes.Buffer
		if err := json.NewEncoder(&b).Encode(doc); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	newest, oldest := f.versions[0], f.versions[len(f.versions)-1]
	out := map[string]string{
		"/v3/releases/acme-widget-" + newest: encode(f.record(newest)),
		"/v3/releases/acme-widget-" + oldest: encode(f.record(oldest)),
		"/v3/modules/acme-widget":            encode(f.moduleRecord()),
		"/v3/modules?query=acme-widget": encode(map[string]any{
			"results":    []any{f.moduleRecord()},
			"pagination": pageObject{Limit: 1000, Total: 1},
		}),
	}
	total := len(f.versions)
	for offset := 0; offset < total; offset += 100 {
		end := min(offset+100, total)
		page := pageObject{Limit: 100, Offset: offset, Total: total}
		if end < total {
			next := fmt.Sprintf("/v3/releases?module=acme-widget&limit=100&offset=%d", end)
			page.Next = &next
		}
		records := []releaseRecord{}
		for _, v := range f.versions[offset:end] {
			records = append(records, f.record(v))
		}
		route := fmt.Sprintf("/v3/releases?module=acme-widget&limit=100&offset=%d", offset)
		out[route] = encode(struct {
			Results    []releaseRecord `json:"results"`
			Pagination pageObject      `json:"pagination"`
		}{records, page})
	}
	return out
}

// wantAnswers fails unless every route under prefix answers 200 with the
// body answers gives it, and the oldest release's archive, which the
// Forge's last page lists, is the Forge's archive.
func (f *cappedForge) wantAnswers(t *testing.T, w *remoteWorld, prefix, user string) {
	t.Helper()
	for route, body := range f.answers(t) {
		r := w.request("GET", prefix+route, user, "")
		if r.Code != 200 || r.Body.String() != body {
			t.Fatalf("%s: got %d %s\nwant 200 %s", route, r.Code, r.Body.String(), body)
		}
	}
	oldest := f.versions[len(f.versions)-1]
	r := w.request("GET", prefix+"/v3/files/acme-widget-"+oldest+".tar.gz", user, "")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), f.archives[oldest]) {
		t.Fatalf("archive %s: %d %s", oldest, r.Code, r.Body.String())
	}
}

// pagedRemote returns a world whose remote is a Puppet remote over up.
func pagedRemote(t *testing.T, up *cappedForge) *remoteWorld {
	t.Helper()
	w := newRemoteWorld(t)
	w.c.PackageType = repo.Puppet
	w.c.URL = up.URL
	w.update()
	return w
}

// A Puppet remote asks its upstream for 100 releases a page and starts the
// next page after the releases a page held, so a module with more
// releases than the Forge serves a page lists, resolves and serves every
// one of them, through the remote and through a virtual repository over
// it. A page that says it serves fewer than 100 is read as complete
// (#1285).
func TestAccPuppetRemoteReadsEveryForgePage(t *testing.T) {
	const remote = "/artifactory/api/puppet/remote"
	t.Run("the Forge's cap of 100", func(t *testing.T) {
		up := newCappedForge(t, 150, 100, false)
		w := pagedRemote(t, up)
		up.wantAnswers(t, w, remote, "reader")
		if got := up.pagesAsked(); !slices.Equal(got,
			[]string{"limit=100&offset=0", "limit=100&offset=100"}) {
			t.Fatalf("pages asked: %v", got)
		}
		v := *repo.Defaults(&repo.Config{
			Key: "pvirtual", Class: repo.Virtual, PackageType: repo.Puppet,
			Repositories: []string{w.c.Key},
		})
		if err := w.rt.Store.Repos().Create(t.Context(), v); err != nil {
			t.Fatal(err)
		}
		up.wantAnswers(t, w, "/artifactory/api/puppet/pvirtual", "admin")
	})
	t.Run("a smaller limit the upstream states", func(t *testing.T) {
		up := newCappedForge(t, 150, 20, false)
		up.wantAnswers(t, pagedRemote(t, up), remote, "reader")
		want := []string{}
		for offset := 0; offset < 150; offset += 20 {
			want = append(want, fmt.Sprintf("limit=100&offset=%d", offset))
		}
		slices.Sort(want)
		if got := up.pagesAsked(); !slices.Equal(got, want) {
			t.Fatalf("pages asked: %v", got)
		}
	})
}

// A module whose releases fit on one page, and a page that is short for no
// reason the upstream states, answer as they did before #1285: every route
// of the one, and 502 on every route of the other.
func TestAccPuppetRemotePagingKeepsItsOtherAnswers(t *testing.T) {
	const remote = "/artifactory/api/puppet/remote"
	for _, releases := range []int{2, 100} {
		t.Run(fmt.Sprint(releases, " releases"), func(t *testing.T) {
			up := newCappedForge(t, releases, 100, false)
			up.wantAnswers(t, pagedRemote(t, up), remote, "reader")
		})
	}
	t.Run("short with no reason stated", func(t *testing.T) {
		up := newCappedForge(t, 150, 100, true)
		w := pagedRemote(t, up)
		// The remote's source refuses the page while it validates it, and
		// answers its own 502 rather than parseVersions' message.
		const refused = `{"errors":[{"message":"Bad Gateway","status":502}]}` + "\n"
		for _, route := range []string{
			"/v3/releases?module=acme-widget", "/v3/releases/acme-widget-1.149.0",
			"/v3/releases/acme-widget-1.0.0", "/v3/modules/acme-widget",
			"/v3/modules?query=acme-widget", "/v3/files/acme-widget-1.0.0.tar.gz",
		} {
			r := w.request("GET", remote+route, "reader", "")
			if r.Code != 502 || r.Body.String() != refused {
				t.Fatalf("%s: got %d %s", route, r.Code, r.Body.String())
			}
		}
		up.mu.Lock()
		defer up.mu.Unlock()
		if up.files != 0 {
			t.Fatal("an archive was fetched from a refused release list")
		}
	})
}
