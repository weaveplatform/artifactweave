package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestLatestVersionByProperties(t *testing.T) {
	w := newItemWorld(t, true)
	for _, p := range []string{
		"libs-local/app/1.0/app.zip;version=1.0;os=linux",
		"libs-local/app/1.2/app.zip;version=1.2;os=win",
		"libs-local/app/1.10/app.zip;version=1.10;os=linux",
		"libs-local/app/1.10/docs.zip;version=1.10",
		"libs-local/app/1.10/notes.txt",
		"libs-local/other/x.zip;Version=9",
		"other-local/app/2.0/app.zip;version=2.0;version=1.9",
	} {
		w.put("/artifactory/"+p, p)
	}
	const q = "/artifactory/api/versions/"
	type result struct {
		Version   string `json:"version"`
		Artifacts []struct {
			URI string `json:"uri"`
		} `json:"artifacts"`
	}
	latest := func(path, as string) result {
		t.Helper()
		rec := w.do("GET", q+path, as, "")
		wantStatus(t, rec, http.StatusOK)
		var res result
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Artifacts == nil {
			t.Fatalf("%s: %v %s", path, err, rec.Body)
		}
		return res
	}
	// Maven order, and only the lowercase key counts.
	if res := latest("libs-local/app", "admin"); res.Version != "1.10" || len(res.Artifacts) != 0 {
		t.Fatalf("latest %+v", res)
	}
	res := latest("libs-local/app/?listFiles=1", "admin")
	if res.Version != "1.10" || len(res.Artifacts) != 2 ||
		res.Artifacts[0].URI != "https://reg.example/artifactory/libs-local/app/1.10/app.zip" {
		t.Fatalf("listed %+v", res)
	}
	// Further parameters are properties the files must carry.
	if res := latest("libs-local/app?os=linux&listFiles=1", "admin"); res.Version != "1.10" ||
		len(res.Artifacts) != 1 {
		t.Fatalf("linux %+v", res)
	}
	if res := latest("libs-local/app?os=win,mac", "admin"); res.Version != "1.2" {
		t.Fatalf("win %+v", res)
	}
	// _any searches every repository or path, and a file's every value
	// counts; a virtual key searches its members.
	for path, want := range map[string]string{
		"_any/_any": "2.0", "virt/app": "2.0", "_any/app/1.0": "1.0", "libs-local/_any": "1.10",
	} {
		if res := latest(path, "admin"); res.Version != want {
			t.Errorf("%s: %+v, want %s", path, res, want)
		}
	}
	if res := latest("_any/_any", "deployer"); res.Version != "1.10" {
		t.Fatalf("deployer %+v", res)
	}
	for _, path := range []string{"libs-local/none", "nope/app", "libs-local/app?os=bsd", "libs-local/ap"} {
		wantStatus(t, w.do("GET", q+path, "admin", ""), http.StatusNotFound)
	}
	wantStatus(t, w.do("GET", q+"_any/_any", "user", ""), http.StatusNotFound)
	wantStatus(t, w.do("GET", q+"_any/_any?listFiles=2", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"_any/_any?=x", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"_any/_any?os=%zz", "admin", ""), http.StatusBadRequest)
	wantStatus(t, w.do("GET", q+"_any/_any", "", ""), http.StatusUnauthorized)
}

// TestLatestVersionByPropertiesStoreErrors: a failing store answers 500.
func TestLatestVersionByPropertiesStoreErrors(t *testing.T) {
	for name, wrap := range map[string]func(store.Store) store.Store{
		"search": func(s store.Store) store.Store { return failingSearchStore{s} },
		"repositories": func(s store.Store) store.Store {
			return faultyStore{Store: s.(*inmem.Store), fail: map[string]bool{"list": true}}
		},
	} {
		w := searchWorld(t, false, func(c *Config) { c.Store = wrap(c.Store) })
		rec := w.do("GET", "/artifactory/api/versions/_any/_any", "admin", "")
		if rec.Code != http.StatusInternalServerError ||
			strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
