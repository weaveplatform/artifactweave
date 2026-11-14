package conan_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A Conan repository's v1 read says enableConanSupport is true whether the
// create sent it true, false or not at all, as 7.x builds the flag from the
// package type; the v2 read and the configurations list leave it out, as
// 7.x's type-specific serializer does, and keep forceConanAuthentication
// (#1440).
func TestAccConanEnableConanSupportReads(t *testing.T) {
	w := newItemWorld(t, false)
	creates := map[string]string{
		"conan-unsent": `{"rclass":"local","packageType":"conan"}`,
		"conan-false":  `{"rclass":"local","packageType":"conan","enableConanSupport":false}`,
		"conan-true":   `{"rclass":"local","packageType":"conan","enableConanSupport":true}`,
		"conan-remote": `{"rclass":"remote","packageType":"conan","url":"https://up.example"}`,
		"conan-virtual": `{"rclass":"virtual","packageType":"conan","repositories":["conan-unsent"],` +
			`"enableConanSupport":false}`,
	}
	for _, key := range []string{"conan-unsent", "conan-false", "conan-true", "conan-remote", "conan-virtual"} {
		rec := w.do(http.MethodPut, "/artifactory/api/repositories/"+key, "admin", creates[key],
			"Content-Type", "application/json")
		if rec.Code != http.StatusOK {
			t.Fatalf("create %s: %d %s", key, rec.Code, rec.Body)
		}
	}
	read := func(path string) map[string]any {
		t.Helper()
		rec := w.do(http.MethodGet, path, "admin", "")
		var doc map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &doc) != nil {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
		return doc
	}
	for key := range creates {
		v1 := read("/artifactory/api/repositories/" + key)
		if got, ok := v1["enableConanSupport"]; !ok || got != true {
			t.Errorf("v1 %s: enableConanSupport %v (present %v), want true", key, got, ok)
		}
		v2 := read("/artifactory/api/v2/repositories/" + key)
		if got, ok := v2["enableConanSupport"]; ok {
			t.Errorf("v2 %s: enableConanSupport %v, want none", key, got)
		}
		// A virtual repository is created without forceConanAuthentication,
		// a gap of the virtual class that this test does not cover.
		if got := v2["forceConanAuthentication"]; key != "conan-virtual" && got != false {
			t.Errorf("v2 %s: forceConanAuthentication %v, want false", key, got)
		}
	}
	const configurations = "/artifactory/api/repositories/configurations?packageType=conan"
	rec := w.do(http.MethodGet, configurations, "admin", "")
	var list map[string][]map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("configurations: %d %s", rec.Code, rec.Body)
	}
	seen := 0
	for _, group := range list {
		for _, c := range group {
			seen++
			if got, ok := c["enableConanSupport"]; ok {
				t.Errorf("configurations %v: enableConanSupport %v, want none", c["key"], got)
			}
		}
	}
	if seen != len(creates) {
		t.Errorf("configurations listed %d Conan repositories, want %d", seen, len(creates))
	}
}
