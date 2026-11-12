package puppet_test

import (
	"bytes"
	"crypto/md5" //nolint:gosec // the Forge's file_md5, which 7.x relays
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	puppetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet"
)

// cappedForge is an upstream Forge that serves acme-widget's releases
// newest first, sorted by version, and serves at most limitCap of them a
// page whatever limit is asked, as forgeapi.puppet.com serves at most 100
// (it answered limit=1000 with "limit": 100 on 2026-11-12). Its pages carry
// the Forge's whole pagination object, and its release records the fields
// a remote relays, with a REFERENCE.md of reference bytes, and leave out
// the fields exclude_fields names, as the Forge does. It serves the
// module's record too. It records the limit and offset of each release
// page asked for, and each exclude_fields asked.
type cappedForge struct {
	*httptest.Server
	versions []string
	archives map[string][]byte
	// dates are the created_at and updated_at the Forge gives each version.
	dates    map[string]string
	limitCap int
	// short serves one release fewer than a page that has more after it
	// should hold, while its pagination still states limitCap.
	short     bool
	reference int
	mu        sync.Mutex
	asked     map[string]bool
	excluded  map[string]bool
	// files counts the archive requests.
	files int
}

func newCappedForge(t *testing.T, releases, limitCap int, short bool) *cappedForge {
	t.Helper()
	f := &cappedForge{
		archives: map[string][]byte{}, dates: map[string]string{}, limitCap: limitCap,
		short: short, asked: map[string]bool{}, excluded: map[string]bool{},
	}
	zone := time.FixedZone("", -7*60*60)
	for i := releases - 1; i >= 0; i-- {
		v := fmt.Sprintf("1.%d.0", i)
		f.versions = append(f.versions, v)
		f.archives[v], _ = puppetArchive(t, v, "forge")
		f.dates[v] = time.Date(2020, 1, 1, 0, 0, 0, 0, zone).Add(time.Duration(i) * time.Hour).
			Format("2006-01-02 15:04:05 -0700")
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *cappedForge) serve(out http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v3/modules/acme-widget" {
		f.mu.Lock()
		f.excluded["module: "+r.URL.Query().Get("exclude_fields")] = true
		f.mu.Unlock()
		out.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(out).Encode(f.upstreamModule())
		return
	}
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
	f.excluded["releases: "+q.Get("exclude_fields")] = true
	f.mu.Unlock()
	omit := map[string]bool{}
	for _, field := range strings.Fields(q.Get("exclude_fields")) {
		omit[field] = true
	}
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
		data := f.archives[v]
		rel := map[string]any{
			"uri": "/v3/releases/acme-widget-" + v, "slug": "acme-widget-" + v,
			"module": map[string]any{
				"uri": "/v3/modules/acme-widget", "slug": "acme-widget", "name": "widget",
				"deprecated_at": nil, "owner": f.owner(),
			},
			"version": v,
			"metadata": struct {
				puppetfmt.Manifest
				ProjectPage string `json:"project_page"`
			}{f.manifest(v), forgeHomepage},
			"supported": true, "file_uri": "/v3/files/acme-widget-" + v + ".tar.gz",
			"file_size": len(data), "file_md5": md5Hex(data), "file_sha256": checksum(data),
			"downloads": f.downloads(v), "readme": "# acme-widget " + v, "changelog": nil,
			"license": "Apache License", "created_at": f.dates[v], "updated_at": f.dates[v],
			"deleted_at": nil, "reference": strings.Repeat("r", f.reference),
		}
		for field := range omit {
			delete(rel, field)
		}
		results = append(results, rel)
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

// upstreamModule is the module's record as the Forge gives it, with the
// values a module record built from the releases would have: their
// downloads summed, the oldest release's created_at and the newest's
// updated_at, and the releases' links, so that a virtual repository over a
// remote, which builds its record from the releases, answers what the
// remote relays.
func (f *cappedForge) upstreamModule() map[string]any {
	m := f.moduleRecord()
	return map[string]any{
		"uri": m.URI, "slug": m.Slug, "name": m.Name, "downloads": m.Downloads,
		"created_at": m.CreatedAt, "updated_at": m.UpdatedAt, "supported": true,
		"endorsement": nil, "module_group": "base", "owner": f.owner(),
		"homepage_url": forgeHomepage, "issues_url": nil, "deprecated_at": nil,
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

// forgeHomepage is the project_page of every release's metadata, which a
// module record answers as its homepage_url, "&" as it is.
const forgeHomepage = "https://example.invalid/widget?tab=readme&lang=en"

// md5Hex returns the MD5 of data in hex, as the Forge's file_md5 gives it.
func md5Hex(data []byte) string {
	digest := md5.Sum(data) //nolint:gosec // the Forge's file_md5, which 7.x relays
	return hex.EncodeToString(digest[:])
}

// ownerRecord is a module's owner, as the Forge and a remote answer it.
type ownerRecord struct {
	URI        string `json:"uri"`
	Slug       string `json:"slug"`
	Username   string `json:"username"`
	GravatarID string `json:"gravatar_id"`
}

// owner is the module's owner as the Forge answers it.
func (f *cappedForge) owner() ownerRecord {
	return ownerRecord{"/v3/users/acme", "acme", "acme", "fdd009b7c1ec96e088b389f773e87aec"}
}

// downloads is the count the Forge gives version v: its minor version
// plus one.
func (f *cappedForge) downloads(v string) int {
	minor, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(v, "1."), ".0"))
	return minor + 1
}

// The records a remote answers relay the Forge's fields (RTFACT-19130),
// in the order of the module's own records: its record.go.
type moduleRef struct {
	URI          string      `json:"uri"`
	Slug         string      `json:"slug"`
	Name         string      `json:"name"`
	Owner        ownerRecord `json:"owner"`
	DeprecatedAt *string     `json:"deprecated_at"`
}

type releaseRecord struct {
	URI        string          `json:"uri"`
	Slug       string          `json:"slug"`
	Module     moduleRef       `json:"module"`
	Version    string          `json:"version"`
	Metadata   json.RawMessage `json:"metadata"`
	Tags       []string        `json:"tags"`
	Supported  bool            `json:"supported"`
	FileURI    string          `json:"file_uri"`
	FileSize   int             `json:"file_size"`
	FileMD5    string          `json:"file_md5"`
	FileSHA256 string          `json:"file_sha256"`
	Downloads  int             `json:"downloads"`
	Readme     string          `json:"readme"`
	Changelog  *string         `json:"changelog"`
	License    string          `json:"license"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
	DeletedAt  *string         `json:"deleted_at"`
}

type releaseLink struct {
	URI     string `json:"uri"`
	Version string `json:"version"`
}

type moduleRecord struct {
	URI            string        `json:"uri"`
	Slug           string        `json:"slug"`
	Name           string        `json:"name"`
	Downloads      int           `json:"downloads"`
	CreatedAt      string        `json:"created_at"`
	UpdatedAt      string        `json:"updated_at"`
	Supported      bool          `json:"supported"`
	Endorsement    *string       `json:"endorsement"`
	ModuleGroup    string        `json:"module_group"`
	Owner          ownerRecord   `json:"owner"`
	CurrentRelease releaseRecord `json:"current_release"`
	Releases       []releaseLink `json:"releases"`
	HomepageURL    string        `json:"homepage_url"`
	IssuesURL      *string       `json:"issues_url"`
}

// pageObject is the Forge's whole pagination object, which every list
// answers before its results (RTFACT-14557).
type pageObject struct {
	Limit    int     `json:"limit"`
	Offset   int     `json:"offset"`
	First    string  `json:"first"`
	Previous *string `json:"previous"`
	Current  string  `json:"current"`
	Next     *string `json:"next"`
	Total    int     `json:"total"`
}

// newPageObject returns the pagination of route's page at offset of limit
// entries in a list of total, whose links keep filter first.
func newPageObject(route, filter string, limit, offset, total int) pageObject {
	link := func(at int) *string {
		s := fmt.Sprintf("%s?%s&limit=%d&offset=%d", route, filter, limit, at)
		return &s
	}
	p := pageObject{
		Limit: limit, Offset: offset, First: *link(0), Current: *link(offset), Total: total,
	}
	if offset > 0 {
		p.Previous = link(max(offset-limit, 0))
	}
	if offset+limit < total {
		p.Next = link(offset + limit)
	}
	return p
}

func (f *cappedForge) record(v string) releaseRecord {
	data := f.archives[v]
	return releaseRecord{
		URI: "/v3/releases/acme-widget-" + v, Slug: "acme-widget-" + v,
		Module: moduleRef{
			URI: "/v3/modules/acme-widget", Slug: "acme-widget", Name: "widget",
			Owner: f.owner(),
		},
		Version: v, Metadata: f.metadata(v), Tags: []string{}, Supported: true,
		FileURI: "/v3/files/acme-widget-" + v + ".tar.gz", FileSize: len(data),
		FileMD5: md5Hex(data), FileSHA256: checksum(data), Downloads: f.downloads(v),
		Readme: "# acme-widget " + v, License: "Apache License",
		CreatedAt: f.dates[v], UpdatedAt: f.dates[v],
	}
}

// metadata is the metadata a remote's record of version v answers: the
// upstream's whole metadata.json, the fields 7.x writes first in its order
// and null where the file has none (#1325).
func (f *cappedForge) metadata(v string) json.RawMessage {
	return json.RawMessage(`{"name":"acme-widget","version":"` + v + `","source":null,` +
		`"author":null,"license":null,"summary":"forge","description":null,` +
		`"project_page":"` + forgeHomepage + `","issues_url":null,"tags":null,"dependencies":[]}`)
}

// moduleRecord is the module as a remote answers it, relaying the Forge's
// module record (upstreamModule), and as a virtual repository over it
// answers it from the releases: their downloads summed, the oldest
// release's created_at and the newest's updated_at, and the newest's owner
// and links.
func (f *cappedForge) moduleRecord() moduleRecord {
	out := moduleRecord{
		URI: "/v3/modules/acme-widget", Slug: "acme-widget", Name: "widget",
		CreatedAt: f.dates[f.versions[len(f.versions)-1]], UpdatedAt: f.dates[f.versions[0]],
		Supported: true, ModuleGroup: "base", Owner: f.owner(),
		CurrentRelease: f.record(f.versions[0]), Releases: []releaseLink{},
		HomepageURL: forgeHomepage,
	}
	for _, v := range f.versions {
		out.Downloads += f.downloads(v)
		out.Releases = append(out.Releases, releaseLink{"/v3/releases/acme-widget-" + v, v})
	}
	return out
}

// answers returns the exact body each route of a remote, or of a virtual
// repository over it, answers for acme-widget once it has read every
// release the Forge holds: the release list a page of 100 at a time, and
// the first, second and last page of 20 it serves when no limit is asked,
// the newest release, the oldest one, which the Forge serves last, the
// module and the module search. "&" is written as it is (#1287).
func (f *cappedForge) answers(t *testing.T) map[string]string {
	t.Helper()
	encode := func(doc any) string {
		var b bytes.Buffer
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		if err := e.Encode(doc); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	type page[T any] struct {
		Pagination pageObject `json:"pagination"`
		Results    []T        `json:"results"`
	}
	newest, oldest := f.versions[0], f.versions[len(f.versions)-1]
	out := map[string]string{
		"/v3/releases/acme-widget-" + newest: encode(f.record(newest)),
		"/v3/releases/acme-widget-" + oldest: encode(f.record(oldest)),
		"/v3/modules/acme-widget":            encode(f.moduleRecord()),
		"/v3/modules?query=acme-widget": encode(page[moduleRecord]{
			newPageObject("/v3/modules", "query=acme-widget", 20, 0, 1),
			[]moduleRecord{f.moduleRecord()},
		}),
	}
	total := len(f.versions)
	releases := func(limit, offset int) string {
		records := []releaseRecord{}
		for _, v := range f.versions[offset:min(offset+limit, total)] {
			records = append(records, f.record(v))
		}
		return encode(page[releaseRecord]{
			newPageObject("/v3/releases", "module=acme-widget", limit, offset, total), records,
		})
	}
	for offset := 0; offset < total; offset += 100 {
		out[fmt.Sprintf("/v3/releases?module=acme-widget&limit=100&offset=%d", offset)] = releases(
			100,
			offset,
		)
	}
	out["/v3/releases?module=acme-widget"] = releases(20, 0)
	for _, offset := range []int{20, (total - 1) / 20 * 20} {
		if offset > 0 && offset < total {
			out[fmt.Sprintf("/v3/releases?module=acme-widget&offset=%d", offset)] = releases(
				20,
				offset,
			)
		}
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

// A Puppet remote asks its upstream for 20 releases a page, without the
// fields no record answers (#1300), and starts the next page after the
// releases a page held, so a module with more releases than the Forge
// serves a page lists, resolves and serves every one of them, through the
// remote and through a virtual repository over it. A page that says it
// serves fewer than 20 is read as complete (#1285).
func TestAccPuppetRemoteReadsEveryForgePage(t *testing.T) {
	const remote = "/artifactory/api/puppet/remote"
	t.Run("the Forge's cap of 100", func(t *testing.T) {
		up := newCappedForge(t, 150, 100, false)
		w := pagedRemote(t, up)
		up.wantAnswers(t, w, remote, "reader")
		want := []string{}
		for offset := 0; offset < 150; offset += 20 {
			want = append(want, fmt.Sprintf("limit=20&offset=%d", offset))
		}
		slices.Sort(want)
		if got := up.pagesAsked(); !slices.Equal(got, want) {
			t.Fatalf("pages asked: %v", got)
		}
		up.mu.Lock()
		excluded := slices.Sorted(maps.Keys(up.excluded))
		up.mu.Unlock()
		if !slices.Equal(excluded, []string{
			"module: current_release releases",
			"releases: reference tasks plans pdk validation_score malware_scan deleted_for",
		}) {
			t.Fatalf("exclude_fields asked: %q", excluded)
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
		up := newCappedForge(t, 150, 10, false)
		up.wantAnswers(t, pagedRemote(t, up), remote, "reader")
		want := []string{}
		for offset := 0; offset < 150; offset += 10 {
			want = append(want, fmt.Sprintf("limit=20&offset=%d", offset))
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

// A module whose release pages carry a REFERENCE.md of 200 KiB a release
// comes to over 16 MiB a page at the Forge's 100, the most metadata a
// remote reads, as puppetlabs-apache's REFERENCE.md of 398,265 bytes in its
// sources would. A remote asks for 20 a page without reference, so it lists
// every release (#1300). The Puppet client's own exclude_fields leaves its
// fields out of the remote's answer, and is not asked of the upstream
// (#1327) [I].
func TestAccPuppetRemoteLargeReleasePages(t *testing.T) {
	const remote = "/artifactory/api/puppet/remote"
	up := newCappedForge(t, 100, 100, false)
	up.reference = 200 << 10
	w := pagedRemote(t, up)
	up.wantAnswers(t, w, remote, "reader")
	const client = "readme,changelog,license,uri,module,tags,supported,file_size," +
		"downloads,created_at,updated_at,deleted_at"
	type trimmed struct {
		Slug       string          `json:"slug"`
		Version    string          `json:"version"`
		Metadata   json.RawMessage `json:"metadata"`
		FileURI    string          `json:"file_uri"`
		FileMD5    string          `json:"file_md5"`
		FileSHA256 string          `json:"file_sha256"`
	}
	results := []trimmed{}
	for _, v := range up.versions[2:4] {
		rec := up.record(v)
		results = append(results, trimmed{
			rec.Slug, rec.Version, rec.Metadata, rec.FileURI, rec.FileMD5, rec.FileSHA256,
		})
	}
	var want bytes.Buffer
	e := json.NewEncoder(&want)
	e.SetEscapeHTML(false)
	if err := e.Encode(struct {
		Pagination pageObject `json:"pagination"`
		Results    []trimmed  `json:"results"`
	}{newPageObject("/v3/releases", "module=acme-widget&sort_by=version&exclude_fields="+
		url.QueryEscape(client), 2, 2, 100), results}); err != nil {
		t.Fatal(err)
	}
	r := w.request("GET", remote+"/v3/releases?module=acme-widget&sort_by=version&exclude_fields="+
		client+"&limit=2&offset=2", "reader", "")
	if r.Code != 200 || r.Body.String() != want.String() {
		t.Fatalf("got %d %s\nwant 200 %s", r.Code, r.Body.String(), want.String())
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if _, ok := up.excluded["releases: reference tasks plans pdk validation_score malware_scan deleted_for"]; !ok ||
		len(up.excluded) != 2 {
		t.Fatalf("exclude_fields asked: %v", up.excluded)
	}
}
