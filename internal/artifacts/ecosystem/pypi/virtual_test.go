package pypi_test

import (
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pypi"
)

func TestUnitPyPIVirtualMerge(t *testing.T) {
	first := pypi.Project[string]{
		Name: "demo",
		Releases: []pypi.Release[string]{
			{Version: "1.0", Info: "first info", Files: []pypi.Distribution[string]{{Name: "demo-1.0.whl", Path: "first/demo-1.0.whl", Value: "first hash", Info: "first info"}}},
		},
	}
	second := pypi.Project[string]{
		Name: "demo",
		Releases: []pypi.Release[string]{
			{
				Version: "1.0",
				Info:    "second info",
				Files: []pypi.Distribution[string]{
					{Name: "demo-1.0.whl", Path: "second/demo-1.0.whl", Value: "second hash", Info: "second info"},
					{Name: "demo-1.0.tar.gz", Path: "second/demo-1.0.tar.gz", Value: "sdist hash", Info: "second info"},
				},
			},
			{Version: "2.0", Files: []pypi.Distribution[string]{{Name: "demo-2.0.whl", Path: "second/demo-2.0.whl", Value: "new hash"}}},
		},
	}
	for _, tc := range []struct {
		name                    string
		priority, empty, filter bool
		files                   int
		hash, info              string
	}{
		{name: "union and first filename", files: 3, hash: "first hash", info: "first info"},
		{name: "priority", priority: true, files: 3, hash: "second hash", info: "second info"},
		{name: "missing priority falls through", priority: true, empty: true, files: 1, hash: "first hash", info: "first info"},
		{name: "virtual patterns", filter: true, files: 3, hash: "second hash", info: "second info"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := second
			if tc.empty {
				other.Releases = nil
			}
			parts := []virtual.Part[pypi.Project[string]]{
				{Member: repo.Config{Key: "first"}, Doc: first},
				{Member: repo.Config{Key: "second", PriorityResolution: repo.Flex(tc.priority)}, Doc: other},
			}
			m := pypi.Merger[string]{}
			if tc.filter {
				m.Allowed = func(path string) bool { return path != "first/demo-1.0.whl" }
			}
			got, err := m.Merge(t.Context(), parts)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			hash := ""
			for _, rel := range got.Releases {
				for _, f := range rel.Files {
					count++
					if f.Name == "demo-1.0.whl" {
						hash = f.Value
					}
				}
			}
			if count != tc.files || hash != tc.hash || got.Releases[0].Info != tc.info {
				t.Fatal(got, count, hash)
			}
			if first.Releases[0].Files[0].Value != "first hash" {
				t.Fatal("input mutated")
			}
		})
	}
}

func TestUnitPyPIVirtualProjectList(t *testing.T) {
	got, err := (pypi.ListMerger{}).Merge(t.Context(), []virtual.Part[[]string]{{Doc: []string{"Demo_Name", "a"}}, {Doc: []string{"demo-name", "z"}}})
	if err != nil || !slices.Equal(got, []string{"a", "demo-name", "z"}) {
		t.Fatal(got, err)
	}
	empty, err := (pypi.Merger[string]{}).Merge(t.Context(), nil)
	if err != nil || len(empty.Releases) != 0 {
		t.Fatal(empty, err)
	}
}

func TestUnitPyPIVirtualInfoFollowsSurvivingOwner(t *testing.T) {
	at := func(n int64) time.Time { return time.Unix(n, 0) }
	first := pypi.Project[string]{Name: "demo", Releases: []pypi.Release[string]{{Version: "1.0", Files: []pypi.Distribution[string]{
		{Name: "excluded", Path: "first/excluded", Value: "secret", Info: "excluded info", Uploaded: at(0)},
		{Name: "later", Path: "first/later", Value: "later", Info: "later info", Uploaded: at(200)},
		{Name: "earlier", Path: "first/earlier", Value: "earlier", Info: "earlier info", Uploaded: at(100)},
	}}}}
	second := pypi.Project[string]{
		Name: "demo",
		Releases: []pypi.Release[string]{
			{Version: "1.0", Files: []pypi.Distribution[string]{{Name: "earlier", Path: "second/earlier", Value: "losing duplicate", Info: "loser info", Uploaded: at(1)}}},
		},
	}
	got, err := (pypi.Merger[string]{Allowed: func(p string) bool { return p != "first/excluded" }}).Merge(
		t.Context(),
		[]virtual.Part[pypi.Project[string]]{{Doc: first}, {Doc: second}},
	)
	if err != nil || got.Releases[0].Info != "earlier info" || got.Releases[0].Owner != "earlier" {
		t.Fatal(got, err)
	}
}
