package dockerhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// paginateHeaders is how a listing was cut from the whole sorted list
// before its pages were read on their own (#1402, #1423): the names after
// last, at most n of them, with a Link while more follow; n of zero gives
// an empty page without a Link, and an n that is no count is ignored. The
// paged reads are held to it byte for byte.
func paginateHeaders(header http.Header, r *http.Request, names []string) []string {
	if names == nil {
		// An empty list is [] in JSON, never null.
		names = []string{}
	}
	values := r.URL.Query()
	if last := values.Get("last"); last != "" {
		i, _ := slices.BinarySearchFunc(names, last, layout.Compare)
		if i < len(names) && names[i] == last {
			i++
		}
		names = names[i:]
	}
	n, err := strconv.Atoi(values.Get("n"))
	if err != nil || n < 0 || n >= len(names) {
		return names
	}
	page := names[:n]
	if n > 0 {
		header.Set("Link", nextLink(r, n, page[n-1]))
	}
	return page
}

// remoteTagsBefore is a remote tags list's answer as the registry built it
// before #1423: the whole document decoded, every tag checked, then sorted,
// de-duplicated and cut.
func remoteTagsBefore(
	r *http.Request,
	name, doc string,
	readable func(string) bool,
) (body []byte, link string) {
	var listing struct {
		Tags []string `json:"tags"`
	}
	_ = json.Unmarshal([]byte(doc), &listing)
	tags := make([]string, 0, len(listing.Tags))
	for _, tag := range listing.Tags {
		if readable(tag) {
			tags = append(tags, tag)
		}
	}
	slices.SortFunc(tags, layout.Compare)
	header := http.Header{}
	page := paginateHeaders(header, r, slices.Compact(tags))
	body, _ = json.Marshal(map[string]any{"name": name, "tags": page})
	return body, header.Get("Link")
}

// remoteTagsDoc is an upstream tags list: unsorted, with tags that differ
// only in case, digits that sort as text, and repeats.
func remoteTagsDoc() (string, []string) {
	tags := []string{
		"latest", "B", "a", "A", "b", "10", "9", "1.0", "v1.0", "V1.0", "a-1", "A-1",
		"a", "z", "Z", "latest", "_x", "x_", "beta", "Beta", "BETA",
	}
	for i := range 40 {
		tags = append(tags, fmt.Sprintf("r%02d", (i*7)%40), fmt.Sprintf("R%02d", i))
	}
	doc, _ := json.Marshal(map[string]any{"name": "team/image", "tags": tags})
	return string(doc), tags
}

// readablePolicies are the callers a list is read by: one who may read
// every tag, one who may read none, and ones refused some, the tag a page
// would end on included.
func readablePolicies() map[string]func(string) bool {
	return map[string]func(string) bool{
		"all":  func(string) bool { return true },
		"none": func(string) bool { return false },
		"every third": func(tag string) bool {
			return (len(tag)+int(tag[len(tag)-1]))%3 != 0
		},
		"no lower case": func(tag string) bool { return strings.ToUpper(tag) == tag },
		"no a or r1x": func(tag string) bool {
			return !strings.EqualFold(tag, "a") && !strings.HasPrefix(strings.ToLower(tag), "r1")
		},
	}
}

// remoteTagsQueries are the n and last a list is paged with: no n, n that
// is no count, n from zero past the list's length, and last naming each
// tag, a tag in another case, and names before, between and after the
// tags.
func remoteTagsQueries(tags []string) []string {
	lasts := []string{"", "!", "0", "a0", "BETA", "Latest", "r05", "R05", "r39", "zz", "~"}
	lasts = append(lasts, tags...)
	ns := []string{"", "x", "-1", "+2", "0", "1", "2", "3", "7"}
	for _, n := range []int{len(tags) - 2, len(tags) - 1, len(tags), len(tags) + 1, 1 << 40} {
		ns = append(ns, strconv.Itoa(n))
	}
	var out []string
	for _, last := range lasts {
		for _, n := range ns {
			q := ""
			if n != "" {
				q += "&n=" + n
			}
			if last != "" {
				q += "&last=" + last
			}
			out = append(out, "/v2/remote/team/image/tags/list"+strings.Replace(q, "&", "?", 1))
		}
	}
	return out
}

// tagsResult is a remote read of the tags list doc for a caller readable
// accepts, whose CheckRead counts the tags it is asked about.
func tagsResult(readable func(string) bool, asked map[string]int) source.Result {
	return source.Result{
		CheckRead: func(_ context.Context, path string) error {
			tag := strings.TrimPrefix(path, "team/image/")
			tag = strings.TrimSuffix(tag, "/"+layout.ManifestFile)
			asked[tag]++
			if !readable(tag) {
				return source.ErrDenied
			}
			return nil
		},
	}
}

// tagsQuery is a tags list request on the remote repository the tests read.
var tagsQuery = Request{Kind: KindTags, RepoKey: "remote", Name: "team/image", base: "/v2/remote"}

// tagsStream is a remote read's body, body, of a document whose SHA-256 is sum.
func tagsStream(body io.ReadCloser, sum string) *source.Stream {
	return &source.Stream{
		Body:           body,
		Representation: source.Representation{Checksums: kernel.Checksums{SHA256: sum}},
	}
}

func tagsRequest(target string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	return r.WithContext(kernel.WithPrincipal(r.Context(), kernel.Principal{Name: "reader"}))
}

// Every page of a remote tags list, with its Link, for every caller, is
// byte for byte the page cut from the whole list once it was filtered and
// sorted, whether the list is decoded for the request or read from the
// decoded lists the registry keeps (#1423).
func TestAccRemoteTagsPagesAnswerAsTheWholeList(t *testing.T) {
	doc, tags := remoteTagsDoc()
	sum := strings.Repeat("ab", 32)
	for label, readable := range readablePolicies() {
		kept := New(Options{})
		for _, target := range remoteTagsQueries(tags) {
			req := tagsRequest(target)
			wantBody, wantLink := remoteTagsBefore(req, "remote/team/image", doc, readable)
			for _, g := range []*Registry{{}, kept} {
				w := httptest.NewRecorder()
				g.remoteListing(w, tagsRequest(target), tagsQuery,
					tagsResult(readable, map[string]int{}),
					tagsStream(io.NopCloser(strings.NewReader(doc)), sum))
				h := w.Header()
				if w.Code != http.StatusOK || w.Body.String() != string(wantBody) ||
					h.Get("Link") != wantLink || h.Get("Content-Type") != "application/json" ||
					h.Get("Content-Length") != strconv.Itoa(len(wantBody)) {
					t.Fatalf("%s %s (kept %t): %d %s Link %q %v, want %s Link %q",
						label, target, g == kept, w.Code, w.Body, h.Get("Link"), h,
						wantBody, wantLink)
				}
			}
		}
	}
}

// failingBody is a document no request should read.
type failingBody struct{ t *testing.T }

func (b failingBody) Read([]byte) (int, error) {
	b.t.Fatal("the cached list was read again")
	return 0, io.EOF
}

func (failingBody) Close() error { return nil }

// A later page of a remote tags list reads none of its document, which an
// earlier page decoded, and asks about only the tags from last up to the
// one after its own; a caller refused some of them is asked about those as
// well and no others (#1423).
func TestAccRemoteTagsLaterPageReadsOnlyItsTags(t *testing.T) {
	doc, _ := remoteTagsDoc()
	sum := strings.Repeat("cd", 32)
	g := New(Options{})
	const base = "/v2/remote/team/image/tags/list"
	type served = *httptest.ResponseRecorder
	serve := func(query string, body io.ReadCloser, readable func(string) bool) (served, map[string]int) {
		asked := map[string]int{}
		w := httptest.NewRecorder()
		g.remoteListing(w, tagsRequest(base+query), tagsQuery, tagsResult(readable, asked),
			tagsStream(body, sum))
		return w, asked
	}
	all := func(string) bool { return true }
	first, _ := serve("?n=2", io.NopCloser(strings.NewReader(doc)), all)
	if first.Code != http.StatusOK ||
		first.Body.String() != `{"name":"remote/team/image","tags":["1.0","10"]}` {
		t.Fatalf("first page: %d %s", first.Code, first.Body)
	}
	later, asked := serve("?n=3&last=r05", failingBody{t}, all)
	if later.Code != http.StatusOK ||
		later.Body.String() != `{"name":"remote/team/image","tags":["R06","r06","R07"]}` ||
		later.Header().Get("Link") != `</v2/remote/team/image/tags/list?last=R07&n=3>; rel="next"` {
		t.Fatalf("later page: %d %s %v", later.Code, later.Body, later.Header())
	}
	// Each tag of the page twice, as the list is cut and again before it is
	// sent, and the one after it once, to decide the Link.
	want := map[string]int{"R06": 2, "r06": 2, "R07": 2, "r07": 1}
	if fmt.Sprint(asked) != fmt.Sprint(want) {
		t.Fatalf("asked about %v, want %v", asked, want)
	}
	refused := func(tag string) bool { return tag != "r06" && tag != "r07" }
	page, asked := serve("?n=2&last=r05", failingBody{t}, refused)
	if page.Body.String() != `{"name":"remote/team/image","tags":["R06","R07"]}` ||
		page.Header().Get("Link") != `</v2/remote/team/image/tags/list?last=R07&n=2>; rel="next"` {
		t.Fatalf("refused page: %d %s %v", page.Code, page.Body, page.Header())
	}
	want = map[string]int{"R06": 2, "r06": 1, "R07": 2, "r07": 1, "R08": 1}
	if fmt.Sprint(asked) != fmt.Sprint(want) {
		t.Fatalf("asked about %v, want %v", asked, want)
	}
}

// A document that does not decode answers the remote's invalid result, and
// again on the next request, which decodes it again: only lists that decode
// are kept. A stream without a SHA-256 is decoded for each request.
func TestAccRemoteTagsKeepOnlyDecodedListsWithADigest(t *testing.T) {
	g := New(Options{})
	serve := func(doc, sum string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		g.remoteListing(w, tagsRequest("/v2/remote/team/image/tags/list"), tagsQuery,
			source.Result{}, tagsStream(io.NopCloser(strings.NewReader(doc)), sum))
		return w
	}
	bad := strings.Repeat("ef", 32)
	for range 2 {
		if w := serve(`{"tags":`, bad); w.Code != http.StatusBadGateway {
			t.Fatalf("invalid document: %d %s", w.Code, w.Body)
		}
	}
	const sorted = `{"name":"remote/team/image","tags":["a","b"]}`
	if w := serve(`{"tags":["b","a"]}`, bad); w.Body.String() != sorted {
		t.Fatalf("decoded after a failure: %s", w.Body)
	}
	for _, doc := range []string{`{"tags":["c"]}`, `{"tags":["d"]}`} {
		if w := serve(doc, ""); !strings.Contains(w.Body.String(), doc[9:12]) {
			t.Fatalf("no digest: %s for %s", w.Body, doc)
		}
	}
}

// The decoded lists are evicted least recently read first once their
// sizes pass the budget; a list over the budget, an empty key and a nil
// cache keep nothing.
func TestUnitTagCache(t *testing.T) {
	list := func(tags ...string) *sortedTags { return packTags(tags) }
	one := list("a", "bb")           // 3 bytes and two offsets: 11
	c := newTagCache(2 * one.size()) // room for two such lists
	c.put("one", one)
	c.put("two", list("c", "dd"))
	if _, ok := c.get("one"); !ok {
		t.Fatal("one evicted early")
	}
	c.put("three", list("e", "ff"))
	if _, ok := c.get("two"); ok {
		t.Fatal("two, the least recently read, kept")
	}
	for _, key := range []string{"one", "three"} {
		if _, ok := c.get(key); !ok {
			t.Fatalf("%s evicted", key)
		}
	}
	c.put("one", list("x"))
	if got, _ := c.get("one"); got.len() != 2 || got.at(1) != "bb" {
		t.Fatalf("a second put replaced the list: %v", got)
	}
	c.put("big", list("a", "b", "c", "d", "e", "f"))
	if _, ok := c.get("big"); ok {
		t.Fatal("a list over the budget kept")
	}
	c.put("", one)
	if _, ok := c.get(""); ok {
		t.Fatal("an empty key kept")
	}
	var none *tagCache
	none.put("one", one)
	if _, ok := none.get("one"); ok {
		t.Fatal("a nil cache kept a list")
	}
	if c.used != 2*one.size() || len(c.entries) != 2 || c.order.Len() != 2 {
		t.Fatalf("accounting: %d bytes, %d entries", c.used, len(c.entries))
	}
}

// The packed list reads back the tags it was given, empty or not.
func TestUnitPackTags(t *testing.T) {
	for _, tags := range [][]string{nil, {"a"}, {"A", "a", "b10", "b9"}} {
		p := packTags(tags)
		got := make([]string, 0, p.len())
		for i := range p.len() {
			got = append(got, p.at(i))
		}
		if !slices.Equal(got, tags) && (len(got) != 0 || len(tags) != 0) {
			t.Fatalf("%q read back as %q", tags, got)
		}
	}
}
