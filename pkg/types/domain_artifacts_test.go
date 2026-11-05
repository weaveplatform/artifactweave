package types_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitItemFullPath(t *testing.T) {
	cases := []struct {
		in           string
		parent, name string
		depth        int
	}{
		{"a.jar", ".", "a.jar", 1},
		{"/org/acme/a.jar", "org/acme", "a.jar", 3},
		{"org/acme/", "org", "acme", 2},
	}
	for _, c := range cases {
		it := types.Item{Path: c.parent, Name: c.name}
		if want := strings.Trim(c.in, "/"); it.FullPath() != want || it.StoragePath() != "/"+want {
			t.Errorf("%q full path %q storage %q", c.in, it.FullPath(), it.StoragePath())
		}
	}
	if (types.Item{Name: "x"}).FullPath() != "x" {
		t.Error("empty parent is the root")
	}
	root := types.RootItem("r", true)
	if !root.IsRoot() || !root.Folder || !root.Cached || root.FullPath() != "" ||
		root.StoragePath() != "/" {
		t.Errorf("root record %+v: full path %q", root, root.FullPath())
	}
	if (types.Item{Path: types.RootPath, Name: "x"}).IsRoot() {
		t.Error("an item at the root is not the root")
	}
}

func TestUnitRepoKeyAliasing(t *testing.T) {
	if k, c := types.ResolveRepoKey("maven-remote-cache"); k != "maven-remote" || !c {
		t.Errorf("%q %v", k, c)
	}
	if k, c := types.ResolveRepoKey("libs"); k != "libs" || c {
		t.Errorf("%q %v", k, c)
	}
	if k, c := types.ResolveRepoKey("-cache"); k != "-cache" || c {
		t.Errorf("a bare suffix is not an alias: %q %v", k, c)
	}
	if (types.Item{RepoKey: "r", Cached: true}).ReportedRepoKey() != "r-cache" ||
		(types.Item{RepoKey: "r"}).ReportedRepoKey() != "r" {
		t.Error("ReportedRepoKey")
	}
}

func TestUnitProperties(t *testing.T) {
	p := types.Properties{}
	p.Set("build.name", "b", "a", "b")
	p.Add("build.name", "c", "a")
	p.Set("release", "1")
	if diff := cmp.Diff([]string{"a", "b", "c"}, p["build.name"]); diff != "" {
		t.Error(diff)
	}
	if !p.Has("release", "1") || !p.Has("release", "") || p.Has("release", "2") ||
		p.Has("missing", "") {
		t.Error("Has")
	}
	if diff := cmp.Diff([]string{"build.name", "release"}, p.Keys()); diff != "" {
		t.Error(diff)
	}
	c := p.Clone()
	c["release"][0] = "changed"
	if p["release"][0] != "1" {
		t.Error("Clone is shallow")
	}
	if !p.Equal(p.Clone()) || p.Equal(c) || p.Equal(nil) ||
		!types.Properties(nil).Equal(types.Properties{}) {
		t.Error("Equal")
	}
	if err := types.ValidatePropertyKey("build.number"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"", "a=b", "a;b", "a,b", "a|b", "a b", "a\tb", "a/b", `a"b`} {
		if err := types.ValidatePropertyKey(bad); !errors.Is(err, types.ErrInvalidPropertyKey) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// The key limit counts characters: "é" is two bytes.
	if err := types.ValidatePropertyKey(strings.Repeat("é", types.PropertyKeyMax)); err != nil {
		t.Error(err)
	}
	if err := types.ValidatePropertyKey(
		strings.Repeat("é", types.PropertyKeyMax+1),
	); !errors.Is(
		err,
		types.ErrPropertyKeyTooLong,
	) ||
		!errors.Is(err, types.ErrInvalidPropertyKey) {
		t.Errorf("a long key: %v", err)
	}
}

func TestUnitTruncatePropertyValue(t *testing.T) {
	// "é" is two UTF-8 bytes and one UTF-16 code unit; "😀" is four bytes
	// and a surrogate pair.
	cases := []struct {
		name, in, want string
		limit          int
	}{
		{"short", "abc", "abc", 3},
		{"bytes over, units within", strings.Repeat("é", 3), strings.Repeat("é", 3), 3},
		{"cut by code units", strings.Repeat("é", 5), strings.Repeat("é", 3), 3},
		{"pair fits", "a😀", "a😀", 3},
		{"pair would straddle", "ab😀", "ab", 3},
		{"pair after the limit", "abc😀", "abc", 3},
	}
	for _, c := range cases {
		if got := types.TruncatePropertyValue(c.in, c.limit); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	long := strings.Repeat("é", types.PropertyValueMax+1)
	if got := types.TruncatePropertyValue(
		long,
		types.PropertyValueMax,
	); got != long[:2*types.PropertyValueMax] {
		t.Errorf("PropertyValueMax: kept %d bytes", len(got))
	}
}

func TestUnitTrashRecordDocument(t *testing.T) {
	rec := types.TrashRecord{
		Item: types.Item{
			ID:      7,
			RepoKey: "libs",
			Path:    ".",
			Name:    "a.jar",
			Depth:   1,
			Size:    3,
			Actual: types.Checksums{
				SHA256: "s",
				SHA1:   "h",
				MD5:    "m",
			},
			Created:  t0,
			Modified: t0,
			Updated:  t0,
		},
		Properties: types.Properties{"k": {"v"}},
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var back types.TrashRecord
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(rec, back); diff != "" {
		t.Error(diff)
	}
	e := types.TrashEntry{DeletedAt: t0}
	if e.Expired(t0.Add(time.Hour), 2*time.Hour) || !e.Expired(t0.Add(2*time.Hour), 2*time.Hour) {
		t.Error("TrashEntry.Expired")
	}
}

func TestUnitArchivedItemExpired(t *testing.T) {
	a := types.ArchivedItem{ExpiresAt: t0}
	if a.Expired(t0.Add(-time.Second)) || !a.Expired(t0) || (types.ArchivedItem{}).Expired(t0) {
		t.Error("Expired")
	}
}
