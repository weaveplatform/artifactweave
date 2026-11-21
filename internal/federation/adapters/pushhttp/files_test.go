package pushhttp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitTargetEmptyAQLDoesNotEndReconciliation(t *testing.T) {
	f, _, _, connection := newFixture(t)
	f.items["selected"] = fixtureItem{folder: true}
	for _, path := range []string{"selected/a", "selected/b", "selected/deep/c", "unrelated/keep", "selected-sibling/keep"} {
		f.items[path] = fixtureItem{body: []byte(path)}
	}
	var paths []string
	page := kernel.Page{Limit: 1}
	for attempt := 0; attempt < 5; attempt++ {
		items, next, err := connection.Files(t.Context(), "selected", page)
		if err != nil || len(items) != 1 {
			t.Fatal("permission-filtered AQL hid readable files", items, next, err)
		}
		paths = append(paths, items[0].Path)
		// Deletes change the list between pages; an offset would skip a row.
		if err := connection.Delete(t.Context(), items[0].Path, items[0].Snapshot); err != nil {
			t.Fatal(err)
		}
		if next == "" {
			break
		}
		if next == page.After {
			t.Fatal("cursor failed to advance")
		}
		page.After = next
	}
	if !slices.Equal(paths, []string{"selected/a", "selected/b", "selected/deep/c"}) {
		t.Fatal("scoped seek enumeration", paths)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.items) != 3 || f.items["unrelated/keep"].body == nil || f.items["selected-sibling/keep"].body == nil {
		t.Fatal("reconciliation escaped exact scope")
	}
	if len(f.queries) != 3 || strings.Contains(f.queries[1], ".offset(") || !strings.Contains(f.queries[1], `"$gt":"a"`) {
		t.Fatal("listing did not use keyset seek", f.queries)
	}
}

func TestUnitTargetFullAQLUsesBoundedPageAndBindsCursor(t *testing.T) {
	f, _, _, connection := newFixture(t)
	for _, path := range []string{"a", "b", "c"} {
		f.items[path] = fixtureItem{body: []byte(path)}
	}
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/api/search/aql") {
			return false
		}
		query, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(query), ".limit(2)") || r.Header.Get("Content-Type") != "text/plain" {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		writeJSON(w, map[string]any{"results": []any{
			map[string]any{"repo": "repo", "path": ".", "name": "a", "type": "file"},
			map[string]any{"repo": "repo", "path": ".", "name": "b", "type": "file"},
		}})
		return true
	}
	items, next, err := connection.Files(t.Context(), "", kernel.Page{Limit: 1})
	if err != nil || len(items) != 1 || items[0].Path != "a" || next == "" {
		t.Fatal(items, next, err)
	}
	f.mu.Lock()
	for _, request := range f.requests {
		if strings.Contains(request, "list&") {
			t.Fatal("full page unnecessarily scanned storage")
		}
	}
	f.mu.Unlock()
	if rows, cursor, err := connection.Files(t.Context(), "different", kernel.Page{After: next}); !errors.Is(err, kernel.ErrInvalidCursor) || rows != nil || cursor != "" {
		t.Fatal("cursor escaped original root", rows, cursor, err)
	}
	if rows, cursor, err := connection.Files(t.Context(), "", kernel.Page{After: "invalid!"}); !errors.Is(err, kernel.ErrInvalidCursor) || rows != nil || cursor != "" {
		t.Fatal("malformed cursor accepted", rows, cursor, err)
	}
}

func TestUnitTargetIncompleteOrUntrustedListsReturnNoPartialPage(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"truncated", `{"files":[{"uri":"/a","folder":false}]`},
		{"not object", `[]`},
		{"empty response", ``},
		{"broken field", `{"extra":`},
		{"broken entry", `{"files":[{"uri":`},
		{"broken array", `{"files":[`},
		{"trailing", `{"files":[{"uri":"/a","folder":false}]} secret`},
		{"outside", `{"files":[{"uri":"/a","folder":false},{"uri":"/../outside","folder":false}]}`},
		{"missing files", `{}`},
		{"null files", `{"files":null}`},
		{"duplicate files", `{"files":[],"files":[]}`},
		{"folder", `{"files":[{"uri":"/folder","folder":true}]}`},
		{"missing type", `{"files":[{"uri":"/a"}]}`},
		{"duplicate item", `{"files":[{"uri":"/a","folder":false},{"uri":"/a","folder":false}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, _, _, connection := newFixture(t)
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Query().Has("list") {
					_, _ = io.WriteString(w, test.body)
					return true
				}
				return false
			}
			rows, next, err := connection.Files(t.Context(), "", kernel.Page{Limit: 1})
			if !errors.Is(err, port.PushTargetInvalid) || rows != nil || next != "" {
				t.Fatal("partial or unsafe list accepted", rows, next, err)
			}
		})
	}
}

func TestUnitTargetUnorderedLargeStorageListIsBoundedAndExhaustive(t *testing.T) {
	f, _, _, connection := newFixture(t)
	expected := make([]string, 0, 137)
	for index := 0; index < 137; index++ {
		name := "file-" + strconv.Itoa(index)
		f.items[name] = fixtureItem{body: []byte(name)}
		expected = append(expected, name)
	}
	slices.Sort(expected)
	var observed []string
	page := kernel.Page{Limit: 27}
	for attempt := 0; attempt < 10; attempt++ {
		items, next, err := connection.Files(t.Context(), "", page)
		if err != nil || len(items) > 27 {
			t.Fatal("bounded storage scan", len(items), err)
		}
		for _, item := range items {
			observed = append(observed, item.Path)
		}
		if next == "" {
			break
		}
		page.After = next
	}
	if !slices.Equal(expected, observed) {
		t.Fatal("unordered scan missed or duplicated a key", len(observed), len(expected))
	}
}

func TestUnitTargetRejectsAQLNamespaceOrderAndShape(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"wrong repository", `{"results":[{"repo":"other","path":".","name":"a","type":"file"}]}`},
		{"out of scope", `{"results":[{"repo":"repo","path":".","name":"outside","type":"file"}]}`},
		{"folder", `{"results":[{"repo":"repo","path":"selected","name":"a","type":"folder"}]}`},
		{"duplicate", `{"results":[{"repo":"repo","path":"selected","name":"a","type":"file"},{"repo":"repo","path":"selected","name":"a","type":"file"}]}`},
		{"missing result", `{}`},
		{"null result", `{"results":null}`},
		{"truncated", `{"results":[]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, _, _, connection := newFixture(t)
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.HasSuffix(r.URL.Path, "/api/search/aql") {
					_, _ = io.WriteString(w, test.body)
					return true
				}
				return false
			}
			rows, next, err := connection.Files(t.Context(), "selected", kernel.Page{Limit: 1})
			if !errors.Is(err, port.PushTargetInvalid) || rows != nil || next != "" {
				t.Fatal("unsafe AQL page accepted", rows, next, err)
			}
		})
	}
}

func TestUnitTargetFileRootAndCappedAQLRequireStorageVerification(t *testing.T) {
	f, _, _, connection := newFixture(t)
	f.items["file"] = fixtureItem{body: []byte("content")}
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/api/search/aql") {
			writeJSON(w, map[string]any{"results": []any{}, "range": map[string]any{"notification": "hard limit reached"}})
			return true
		}
		return false
	}
	items, next, err := connection.Files(t.Context(), "file", kernel.Page{Limit: 1})
	if err != nil || len(items) != 1 || items[0].Path != "file" || next != "" {
		t.Fatal(items, next, err)
	}
	encoded, err := json.Marshal(fileCursor{Repository: "repo", Root: "file", Last: keyFor("file")})
	if err != nil {
		t.Fatal(err)
	}
	items, next, err = connection.Files(t.Context(), "file", kernel.Page{Limit: 1, After: base64.RawURLEncoding.EncodeToString(encoded)})
	if err != nil || len(items) != 0 || next != "" {
		t.Fatal("exact file root repeated after cursor", items, next, err)
	}
}

func TestUnitTargetFullButCappedAQLStillVerifiesStorage(t *testing.T) {
	f, _, _, connection := newFixture(t)
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/api/search/aql") {
			writeJSON(w, map[string]any{"results": []any{
				map[string]any{"repo": "repo", "path": ".", "name": "a", "type": "file"},
				map[string]any{"repo": "repo", "path": ".", "name": "b", "type": "file"},
			}, "range": map[string]any{"notification": "AQL query reached the search hard limit"}})
			return true
		}
		if r.URL.Query().Has("list") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	}
	items, next, err := connection.Files(t.Context(), "", kernel.Page{Limit: 1})
	if !errors.Is(err, errUnavailable) || items != nil || next != "" {
		t.Fatal("hard-limit notification bypassed storage verification", items, next, err)
	}
}
