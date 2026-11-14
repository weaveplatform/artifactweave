package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// RunRepoPages runs the contract of RepoStore.Page, the window Get
// Repositories answers (#1408). Each adapter's tests call it next to Run;
// the SQL adapter's on PostgreSQL too, since each engine plans the filter,
// the order and the window in its own way.
func RunRepoPages(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("RepoPages", func(t *testing.T) { testRepoPages(t, newStore(t).Repos()) })
}

// repoPageWorld is stored in reverse key order, so a store that answered in
// insertion order would fail every case.
var repoPageWorld = []repo.Config{
	{Key: "f-local", Class: repo.Local, PackageType: repo.Docker, Description: "images"},
	{Key: "e-local", Class: repo.Local, PackageType: repo.RPM},
	{Key: "d-virtual", Class: repo.Virtual, PackageType: repo.NPM, ProjectKey: "p"},
	{Key: "c-remote", Class: repo.Remote, PackageType: repo.Maven, URL: "https://repo.example/m2"},
	{Key: "b-fed", Class: repo.Federated, PackageType: repo.Maven, ProjectKey: "p"},
	{Key: "a-local", Class: repo.Local, PackageType: repo.Generic, Description: "first"},
}

func testRepoPages(t *testing.T, r port.RepoStore) {
	ctx := context.Background()
	for _, c := range repoPageWorld {
		storekit.Must(t, r.Create(ctx, c))
	}
	every := []string{"a-local", "b-fed", "c-remote", "d-virtual", "e-local", "f-local"}
	type want struct {
		keys  []string
		more  bool
		total int
	}
	for _, c := range []struct {
		name string
		l    port.RepoListing
		want want
	}{
		{"whole list", port.RepoListing{}, want{keys: every}},
		{"first window", port.RepoListing{Limit: 2, Count: true}, want{every[:2], true, 6}},
		{"middle window", port.RepoListing{Offset: 1, Limit: 2, Count: true}, want{every[1:3], true, 6}},
		{"last window", port.RepoListing{Offset: 4, Limit: 2, Count: true}, want{every[4:], false, 6}},
		{"short last window", port.RepoListing{Offset: 5, Limit: 2, Count: true}, want{every[5:], false, 6}},
		{"past the end", port.RepoListing{Offset: 6, Limit: 2, Count: true}, want{nil, false, 6}},
		{"far past the end", port.RepoListing{Offset: 100, Limit: 2, Count: true}, want{nil, false, 6}},
		{"window without a count", port.RepoListing{Offset: 2, Limit: 3}, want{every[2:5], true, 0}},
		{"limit past the list", port.RepoListing{Limit: 1000, Count: true}, want{every, false, 6}},
		{
			"class",
			port.RepoListing{Class: repo.Local, Limit: 2, Count: true},
			want{[]string{"a-local", "e-local"}, true, 3},
		},
		{"project", port.RepoListing{ProjectKey: "p"}, want{keys: []string{"b-fed", "d-virtual"}}},
		{
			"package types",
			port.RepoListing{PackageTypes: []repo.PackageType{repo.Maven, repo.RPM}},
			want{keys: []string{"b-fed", "c-remote", "e-local"}},
		},
		{
			"no package type",
			port.RepoListing{PackageTypes: []repo.PackageType{}, Limit: 5, Count: true},
			want{nil, false, 0},
		},
		{"visible to every set", port.RepoListing{Visible: []port.RepoSet{{Every: true}}}, want{keys: every}},
		{
			"visible to no set",
			port.RepoListing{Visible: []port.RepoSet{{}}, Limit: 5, Count: true},
			want{nil, false, 0},
		},
		{"visible by class or key", port.RepoListing{Visible: []port.RepoSet{
			{Classes: []repo.Class{repo.Remote}, Keys: []string{"a-local", "gone-local"}},
		}}, want{keys: []string{"a-local", "c-remote"}}},
		{"visible by class and key", port.RepoListing{Visible: []port.RepoSet{
			{Classes: []repo.Class{repo.Local, repo.Federated}},
			{Keys: []string{"a-local", "b-fed", "c-remote"}},
			{Every: true},
		}, Limit: 1, Offset: 1, Count: true}, want{[]string{"b-fed"}, false, 2}},
		{"every filter", port.RepoListing{
			Class: repo.Federated, ProjectKey: "p", PackageTypes: []repo.PackageType{repo.Maven},
			Visible: []port.RepoSet{{Keys: []string{"b-fed"}}}, Limit: 1, Count: true,
		}, want{[]string{"b-fed"}, false, 1}},
	} {
		got := storekit.Get[port.RepoPage](t)(r.Page(ctx, c.l))
		keys := make([]string, 0, len(got.Repos))
		for _, s := range got.Repos {
			keys = append(keys, s.Key)
		}
		if len(c.want.keys) == 0 {
			c.want.keys = []string{}
		}
		storekit.Same(t, c.name, fmt.Sprint(c.want.keys, c.want.more, c.want.total),
			fmt.Sprint(keys, got.More, got.Total))
	}
	// A summary carries what the list shows, and never the remote's URL or
	// any other part of its configuration.
	got := storekit.Get[port.RepoPage](t)(r.Page(ctx, port.RepoListing{Limit: 6}))
	storekit.Same(t, "summaries", []port.RepoSummary{
		{Key: "a-local", Class: repo.Local, PackageType: repo.Generic, Description: "first"},
		{Key: "b-fed", Class: repo.Federated, PackageType: repo.Maven},
		{Key: "c-remote", Class: repo.Remote, PackageType: repo.Maven},
		{Key: "d-virtual", Class: repo.Virtual, PackageType: repo.NPM},
		{Key: "e-local", Class: repo.Local, PackageType: repo.RPM},
		{Key: "f-local", Class: repo.Local, PackageType: repo.Docker, Description: "images"},
	}, got.Repos)
}
