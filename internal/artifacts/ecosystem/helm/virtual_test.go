package helm_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitHelmVirtualIndexMerge(t *testing.T) {
	a := helm.Index{Generated: "2026-11-17T10:00:00Z", Entries: map[string][]helm.ChartVersion{
		"web": {{Chart: helm.Chart{Name: "web", Version: "1.0.0", Annotations: map[string]string{"owner": "first"}}, Digest: "first", URLs: []string{"folder/a%20chart.tgz"}}},
	}}
	b := helm.Index{Generated: "2026-11-17T11:00:00Z", Entries: map[string][]helm.ChartVersion{
		"web": {
			{Chart: helm.Chart{Name: "web", Version: "1.0.0"}, Digest: "second", URLs: []string{"other.tgz"}},
			{Chart: helm.Chart{Name: "web", Version: "2.0.0"}, URLs: []string{"web-2.tgz"}},
		},
		"remote": {{Chart: helm.Chart{Name: "remote", Version: "1.0.0"}, URLs: []string{"remote.tgz"}}},
		"external": {
			{
				Chart: helm.Chart{Name: "external", Version: "1.0.0"},
				URLs:  []string{"_external/https/up/file.tgz", "local://repo/file.tgz", "https://other/file.tgz", "//other/file.tgz", "%invalid", "?secret=token", "#fragment", ""},
			},
		},
	}}
	for _, relative := range []bool{true, false} {
		out, err := (helm.Merger{Settings: helm.Settings{RelativeURLs: &relative}, Base: "https://proxy.example/prefix/", Key: "charts"}).Merge(
			t.Context(),
			[]virtual.Part[helm.Index]{{Doc: a}, {Doc: b}},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Entries) != 2 || len(out.Entries["web"]) != 2 || out.Entries["web"][0].Version != "2.0.0" || out.Entries["web"][1].Digest != "first" ||
			out.Generated != b.Generated {
			t.Fatal(out)
		}
		want := "folder/a%20chart.tgz"
		if !relative {
			want = "https://proxy.example/prefix/artifactory/charts/" + want
		}
		if out.Entries["web"][1].URLs[0] != want {
			t.Fatal(out.Entries)
		}
		if a.Entries["web"][0].URLs[0] != "folder/a%20chart.tgz" {
			t.Fatal("member mutated")
		}
		out.Entries["web"][1].Annotations["owner"] = "changed"
		if a.Entries["web"][0].Annotations["owner"] != "first" {
			t.Fatal("annotation map aliased")
		}
	}
	if _, err := (helm.Merger{}).Merge(t.Context(), nil); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal(err)
	}
	out, err := (helm.Merger{}).Merge(t.Context(), []virtual.Part[helm.Index]{{Doc: helm.Index{}}})
	if err != nil || out.Entries == nil || len(out.Entries) != 0 {
		t.Fatal(out, err)
	}
}

func TestUnitHelmNamespacedAndExternalURLs(t *testing.T) {
	for _, relative := range []bool{true, false} {
		for _, namespaces := range []bool{true, false} {
			t.Run(fmt.Sprintf("relative=%t/namespaces=%t", relative, namespaces), func(t *testing.T) {
				member := repo.Config{Key: "upstream", Class: repo.Remote}
				index := helm.Index{
					Entries: map[string][]helm.ChartVersion{
						"web": {{Chart: helm.Chart{Name: "web", Version: "1.0.0"}, URLs: []string{"nested/a%20chart.tgz", "_external/https/charts.example/releases/web.tgz"}}},
					},
				}
				out, err := (helm.Merger{Settings: helm.Settings{RelativeURLs: &relative}, UseNamespaces: namespaces, Base: "https://proxy.example/prefix", Key: "v"}).Merge(
					t.Context(),
					[]virtual.Part[helm.Index]{{Member: member, Doc: index}},
				)
				if err != nil {
					t.Fatal(err)
				}
				prefix := ""
				if namespaces {
					prefix = "upstream/"
				}
				want := []string{prefix + "nested/a%20chart.tgz", prefix + "_external/https/charts.example/releases/web.tgz"}
				if !relative {
					want[0] = "https://proxy.example/prefix/artifactory/v/" + want[0]
					want[1] = "https://proxy.example/prefix/artifactory/api/helm/v/" + want[1]
				}
				if !slices.Equal(out.Entries["web"][0].URLs, want) {
					t.Fatal(out.Entries, want)
				}
			})
		}
	}
}

func TestUnitHelmNamespacePath(t *testing.T) {
	members := []virtual.Member{{Repo: repo.Config{Key: "first"}}, {Repo: repo.Config{Key: "remote"}}}
	for _, tc := range []struct {
		name, path, member, actual string
		enabled, named             bool
	}{
		{"off", "first/web.tgz", "", "first/web.tgz", false, false},
		{"member", "first/web.tgz", "first", "web.tgz", true, true},
		{"remote", "remote/sub/web.tgz", "remote", "sub/web.tgz", true, true},
		{"folder", "other/web.tgz", "", "other/web.tgz", true, false},
		{"root", "first", "first", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			member, actual, named := helm.NamespacePath(repo.Config{UseNamespaces: &tc.enabled}, members, tc.path)
			if member != tc.member || actual != tc.actual || named != tc.named {
				t.Fatal(member, actual, named)
			}
		})
	}
}

func TestUnitHelmInvalidationRunsOnlyAfterCommit(t *testing.T) {
	w := newWorld(t)
	changes := 0
	w.items.Formats = (ecosystem.Settings{Helm: helm.Settings{IndexChanged: func(ctx context.Context, key string) {
		if key != w.c.Key {
			t.Errorf("changed %s", key)
		}
		if _, err := w.st.Repos().Get(ctx, key); err != nil {
			t.Errorf("callback cannot read committed data: %v", err)
		}
		changes++
	}}}).For
	err := w.st.Tx(t.Context(), func(ctx context.Context) error {
		_, err := w.items.Deploy(ctx, item.Deploy{Target: item.Target{Repo: w.c, Path: "web-1.0.0.tgz"}, Body: bytes.NewReader(chartTgz("web", "1.0.0"))})
		if err != nil {
			return err
		}
		if changes != 0 {
			t.Error("invalidated before commit")
		}
		return kernel.ErrConflict
	})
	if !errors.Is(err, kernel.ErrConflict) || changes != 0 {
		t.Fatal(err, changes)
	}
	w.deploy("web-1.0.0.tgz", chartTgz("web", "1.0.0"))
	if changes != 1 {
		t.Fatal(changes)
	}
	w.remove("web-1.0.0.tgz")
	if changes != 2 {
		t.Fatal(changes)
	}
}
