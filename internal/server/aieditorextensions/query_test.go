package aieditorextensions

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnitGalleryQueryGrammarAndCacheSeparation(t *testing.T) {
	t.Parallel()
	makeRequest := func(body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/extensionquery", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json; charset=utf-8")
		return r
	}
	exact := `{"filters":[{"criteria":[{"filterType":7,"value":"MS-VSCode.HexEditor"},{"filterType":8,"value":"Microsoft.VisualStudio.Code"}],"pageNumber":1,"pageSize":1}],"assetTypes":[],"flags":950}`
	q, err := parseQuery(makeRequest(exact))
	if err != nil || q.Publisher != "ms-vscode" || q.Name != "hexeditor" ||
		!bytes.Contains(q.Body, []byte("ms-vscode.hexeditor")) {
		t.Fatalf("exact query: %+v %v", q, err)
	}
	search := `{"filters":[{"criteria":[{"filterType":10,"value":"hex editor"},{"filterType":8,"value":"Microsoft.VisualStudio.Code"}],"pageNumber":1,"pageSize":10}],"assetTypes":[],"flags":950}`
	s, err := parseQuery(makeRequest(search))
	if err != nil || s.SearchText != "hex editor" || s.Path == q.Path {
		t.Fatalf("search query: %+v %v", s, err)
	}
	for _, tc := range []struct{ name, body string }{{"empty", ""}, {"unknown-field", strings.Replace(exact, `"flags":950`, `"unexpected":true,"flags":950`, 1)}, {"duplicate-filter", strings.Replace(exact, `{"filterType":7,"value":"MS-VSCode.HexEditor"}`, `{"filterType":7,"value":"MS-VSCode.HexEditor"},{"filterType":7,"value":"other.name"}`, 1)}, {"missing-target", strings.Replace(exact, `,{"filterType":8,"value":"Microsoft.VisualStudio.Code"}`, "", 1)}, {"wrong-target", strings.Replace(exact, "Microsoft.VisualStudio.Code", "Other", 1)}, {"unknown-filter", strings.Replace(exact, "\"filterType\":7", "\"filterType\":99", 1)}, {"bad-id", strings.Replace(exact, "MS-VSCode.HexEditor", "../other", 1)}, {"bad-page", strings.Replace(exact, `"pageSize":1`, `"pageSize":500`, 1)}, {"bad-asset-type", strings.Replace(exact, `"assetTypes":[]`, `"assetTypes":["../evil"]`, 1)}, {"trailing-json", exact + `{}`}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseQuery(makeRequest(tc.body)); err == nil {
				t.Fatal("invalid gallery query admitted")
			}
		})
	}
	tooBig := makeRequest(strings.Repeat("x", maxQueryBody+1))
	if _, err := parseQuery(tooBig); err == nil {
		t.Fatal("oversized body admitted")
	}
	query := makeRequest(exact)
	query.URL.RawQuery = "secret=1"
	if _, err := parseQuery(query); err == nil {
		t.Fatal("query string admitted")
	}
	media := makeRequest(exact)
	media.Header.Set("Content-Type", "text/plain")
	if _, err := parseQuery(media); err == nil {
		t.Fatal("wrong content type admitted")
	}
}

func TestUnitGalleryAssetQueryWhitelist(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "redirect=true", "install=true", "redirect=true&install=true"} {
		if !validAssetQuery(raw) {
			t.Errorf("rejected %q", raw)
		}
	}
	for _, raw := range []string{"redirect=false", "install=no", "token=secret", "redirect=true&redirect=true", "install=true&token=secret", "%zz"} {
		if validAssetQuery(raw) {
			t.Errorf("admitted %q", raw)
		}
	}
}
