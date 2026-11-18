package npm

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

func virtualFixture(key string, priority bool, versions map[string]time.Time, tags map[string]string) virtual.Part[VirtualPackage] {
	doc := VirtualPackage{
		Name:     "pkg",
		Fields:   map[string]json.RawMessage{"readme": json.RawMessage(`"` + key + `"`)},
		Versions: map[string]VirtualVersion{},
		Tags:     tags,
		Times:    map[string]json.RawMessage{},
	}
	for version, published := range versions {
		raw, _ := json.Marshal(
			map[string]any{
				"name":       "pkg",
				"version":    version,
				"deprecated": true,
				"unknown":    key,
				"dist":       map[string]any{"shasum": key, "integrity": key, "signatures": []any{map[string]string{"sig": key}}},
			},
		)
		doc.Versions[version] = VirtualVersion{Manifest: raw, Published: published}
		at, _ := json.Marshal(published.Format(time.RFC3339Nano))
		doc.Times[version] = at
	}
	return virtual.Part[VirtualPackage]{Member: repo.Config{Key: key, Class: repo.Local, PriorityResolution: repo.Flex(priority)}, Doc: doc}
}

func TestUnitNPMVirtualMerge(t *testing.T) {
	one, two, three := time.Unix(100, 0).UTC(), time.Unix(200, 0).UTC(), time.Unix(300, 0).UTC()
	first := virtualFixture("first", false, map[string]time.Time{"1.0.0": two, "4.0.0-beta.1": three}, map[string]string{"latest": "1.0.0", "custom": "1.0.0"})
	second := virtualFixture(
		"second",
		false,
		map[string]time.Time{"1.0.0": three, "3.0.0": one},
		map[string]string{"latest": "1.0.0", "custom": "3.0.0", "other": "3.0.0", "hidden": "missing"},
	)
	first.Doc.Times["created"], first.Doc.Times["modified"] = json.RawMessage(`"2020-01-01T00:00:00Z"`), json.RawMessage(`"2021-01-01T00:00:00Z"`)
	second.Doc.Times["created"], second.Doc.Times["modified"] = json.RawMessage(`"2019-01-01T00:00:00Z"`), json.RawMessage(`"2022-01-01T00:00:00Z"`)
	for _, tc := range []struct {
		name           string
		settings       Settings
		latest, custom string
	}{
		{"defaults", Settings{}, "3.0.0", "1.0.0"},
		{"publish latest", Settings{TagLatestByPublish: true}, "4.0.0-beta.1", "1.0.0"},
		{"tag versions", Settings{MergeDistTagsByVersion: true}, "3.0.0", "3.0.0"},
		{"both", Settings{TagLatestByPublish: true, MergeDistTagsByVersion: true}, "4.0.0-beta.1", "3.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := (Merger{Settings: tc.settings}).Merge(t.Context(), []virtual.Part[VirtualPackage]{first, second})
			if err != nil || len(out.Versions) != 3 || out.Tags["latest"] != tc.latest || out.Tags["custom"] != tc.custom || out.Tags["other"] != "3.0.0" {
				t.Fatal(out, err)
			}
			if _, ok := out.Tags["hidden"]; ok {
				t.Fatal("unreadable tag included")
			}
			if !reflect.DeepEqual(out.Versions["1.0.0"].Manifest, first.Doc.Versions["1.0.0"].Manifest) || out.Versions["1.0.0"].Member != "first" ||
				!reflect.DeepEqual(out.Times["1.0.0"], first.Doc.Times["1.0.0"]) {
				t.Fatal("version ownership was split", out)
			}
			if string(out.Fields["readme"]) != `"first"` || string(out.Times["created"]) != `"2019-01-01T00:00:00Z"` || string(out.Times["modified"]) != `"2022-01-01T00:00:00Z"` {
				t.Fatal(out)
			}
			out.Fields["readme"][0] = 'x'
			if string(first.Doc.Fields["readme"]) != `"first"` {
				t.Fatal("merge mutated input JSON")
			}
		})
	}
}

func TestUnitNPMVirtualPriority(t *testing.T) {
	for _, hit := range []bool{true, false} {
		t.Run(map[bool]string{true: "hit", false: "miss"}[hit], func(t *testing.T) {
			versions := map[string]time.Time{}
			if hit {
				versions["1.0.0"] = time.Time{}
			}
			parts := []virtual.Part[VirtualPackage]{virtualFixture("ordinary", false, map[string]time.Time{"2.0.0": {}}, nil), virtualFixture("priority", true, versions, nil)}
			out, err := (Merger{}).Merge(t.Context(), parts)
			want := "2.0.0"
			if hit {
				want = "1.0.0"
			}
			if err != nil || len(out.Versions) != 1 || out.Tags["latest"] != want {
				t.Fatal(out, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Merger{}).Merge(ctx, []virtual.Part[VirtualPackage]{virtualFixture("first", false, map[string]time.Time{"1.0.0": {}}, nil)}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if out, err := (Merger{}).Merge(t.Context(), nil); err != nil || len(out.Versions) != 0 {
		t.Fatal(out, err)
	}
}

func TestUnitNPMVirtualEquivalentVersionsDeterministic(t *testing.T) {
	first := virtualFixture("first", false, map[string]time.Time{"1.0.0+second": {}, "1.0.0+first": {}}, nil)
	second := virtualFixture("second", false, map[string]time.Time{"1.0.0": {}}, nil)
	for range 100 {
		out, err := (Merger{}).Merge(t.Context(), []virtual.Part[VirtualPackage]{first, second})
		if err != nil || out.Tags["latest"] != "1.0.0+first" || len(out.Versions) != 3 {
			t.Fatal(out, err)
		}
	}
}
