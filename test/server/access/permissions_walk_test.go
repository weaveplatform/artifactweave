package access_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
)

// TestAccAccessPermissionsWalkVisitsEachOnce follows the Access v2
// permissions list's cursor from the first page to the last, at several
// page sizes, with and without a principal filter and expand, and checks
// that the walk visits exactly what one page of the whole list holds, each
// permission once and in order. The list pages in the store (#1408), so
// the walk is what proves a page boundary neither drops nor repeats a
// permission. A token with system:permissions:r walks it as an
// administrator does; a MANAGE holder and an anonymous caller are refused
// as before.
func TestAccAccessPermissionsWalkVisitsEachOnce(t *testing.T) {
	h := statusFixture(t, true, allowBasicTokens)
	for i := range 30 {
		var users, groups string
		if i%3 == 0 {
			users = `"user":["READ"]`
		}
		if i%4 == 0 {
			groups = `"readers":["READ"]`
		}
		body := fmt.Sprintf(`{"name":"walk-%02d","resources":{"artifact":{`+
			`"actions":{"users":{%s},"groups":{%s}},`+
			`"targets":{"perm-local":{"include_patterns":["**"]}}}}}`, i, users, groups)
		mustStatus(t, do(t, h, http.MethodPost, permAcc, adminJSON(), body), 201, "create")
	}
	created := mintAdmin(t, h, `{"username":"user","scope":"system:permissions:r"}`)
	tokenValue, _ := created["access_token"].(string)
	callers := map[string]map[string]string{
		"admin":  basic("admin", "pw"),
		"reader": bearerAuth(tokenValue),
	}
	type page struct {
		Permissions []json.RawMessage `json:"permissions"`
		Cursor      string            `json:"cursor"`
	}
	get := func(t *testing.T, as map[string]string, query string) page {
		t.Helper()
		rec := do(t, h, http.MethodGet, permAcc+query, as, "")
		mustStatus(t, rec, 200, query)
		var p page
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("%s: %v %s", query, err, rec.Body)
		}
		return p
	}
	name := func(t *testing.T, raw json.RawMessage) string {
		t.Helper()
		var v struct{ Name string }
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		return v.Name
	}
	for _, filter := range []string{
		"",
		"&user=user",
		"&user=USER&include_inherited_groups=false",
		"&group=readers",
		"&user=user&group=readers&expand=true",
	} {
		for who, as := range callers {
			whole := get(t, as, "?limit=99999"+filter)
			if whole.Cursor != "" || len(whole.Permissions) == 0 {
				t.Fatalf("%s %q: the whole list %+v", who, filter, whole)
			}
			for _, limit := range []int{1, 2, 7} {
				var walked []json.RawMessage
				cursor := ""
				for range len(whole.Permissions) + 1 {
					p := get(t, as, fmt.Sprintf("?limit=%d%s%s", limit, filter, cursor))
					if len(p.Permissions) > limit {
						t.Fatalf("%s %q: %d permissions at limit %d", who, filter, len(p.Permissions), limit)
					}
					walked = append(walked, p.Permissions...)
					if p.Cursor == "" {
						break
					}
					if last := name(t, p.Permissions[len(p.Permissions)-1]); p.Cursor != last {
						t.Fatalf("%s %q: cursor %q after %q", who, filter, p.Cursor, last)
					}
					cursor = "&cursor=" + url.QueryEscape(p.Cursor)
				}
				if !slices.EqualFunc(walked, whole.Permissions, func(a, b json.RawMessage) bool {
					return string(a) == string(b)
				}) {
					t.Errorf("%s %q at limit %d walked %d permissions; the whole list has %d",
						who, filter, limit, len(walked), len(whole.Permissions))
				}
			}
		}
	}
	mustStatus(t, do(t, h, http.MethodGet, permAcc+"?limit=2", basic("user", "pw"), ""), 403,
		"a MANAGE holder's list")
	mustStatus(t, do(t, h, http.MethodGet, permAcc+"?limit=2", nil, ""), 401, "an anonymous list")
}
