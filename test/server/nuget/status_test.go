package nuget_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func nugetStatusFixture(t *testing.T, anonymous bool) http.Handler {
	t.Helper()
	var blobs blob.Store
	h, db, _ := nugetServer(t, func(cfg *server.Config) {
		blobs = cfg.Blobs
		for _, c := range []repo.Config{
			{Key: "libs-local", Class: repo.Local},
			{Key: "nuget-remote", Class: repo.Remote, PackageType: repo.NuGet, URL: "https://up.example"},
			{
				Key: "nuget-virtual", Class: repo.Virtual, PackageType: repo.NuGet,
				Repositories: []string{"nuget-local"}, DefaultDeploymentRepo: "nuget-local",
			},
		} {
			if err := cfg.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
		if err := cfg.Store.Users().Create(t.Context(), types.User{
			Name: "nugetreader", PasswordHash: cheapHash,
		}); err != nil {
			t.Fatal(err)
		}
		if err := cfg.Store.Permissions().Create(t.Context(), types.Permission{
			Name: "nuget-read",
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					"nugetreader": types.ActionRead,
				}},
				Targets: map[string]types.Target{"nuget-local": {Include: []string{"**"}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
		if anonymous {
			if err := cfg.Store.Settings().
				Put(t.Context(), types.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
		}
	})
	items := &item.Service{Store: db, Blobs: blobs}
	for _, entry := range []struct {
		key, path string
		body      []byte
	}{
		{"nuget-local", "Status.Pkg.1.0.0.nupkg", nugetPackage("Status.Pkg", "1.0.0", "")},
		{"nuget-local", "Status.Pkg.2.0.0-rc.1.nupkg", nugetPackage("Status.Pkg", "2.0.0-rc.1", "")},
		{"nuget-local", "Delete.Me.1.0.0.nupkg", nugetPackage("Delete.Me", "1.0.0", "")},
		{"nuget-local", "Delete.Me.1.0.0.snupkg", nugetSymbolsPackage("Delete.Me", "1.0.0")},
		{"nuget-local", "Debug.1.0.0.snupkg", nugetPDBPackage("Debug", nugetPortablePDB())},
		{"nuget-forced", "Debug.1.0.0.snupkg", nugetPDBPackage("Debug", nugetPortablePDB())},
	} {
		deployNuGet(t, db, items, entry.key, entry.path, entry.body)
	}
	q, err := work.New(db, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := items.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"nuget-local", "nuget-forced"} {
		if err := q.EnsureFresh(context.Background(), nuget.KindSymbols, key); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func TestNuGetStatusTable(t *testing.T) {
	off := nugetStatusFixture(t, false)
	on := nugetStatusFixture(t, true)
	for _, c := range nugetStatusRows() {
		h := off
		var headers map[string]string
		switch c.as {
		case "anonymous":
			h = on
		case "admin", "user":
			headers = basic(c.as, "pw")
		default:
			if user, password, ok := strings.Cut(c.as, ":"); ok {
				headers = basic(user, password)
			}
		}
		body := c.body
		if body != "" {
			if headers == nil {
				headers = map[string]string{}
			}
			headers["Content-Type"], body = contentTypeOf(body)
		}
		rec := do(t, h, c.method(), c.path, headers, body)
		if rec.Code != c.want {
			t.Errorf("%s %s as %q: status %d, want %d: %s",
				c.method(), c.path, c.as, rec.Code, c.want, rec.Body)
		}
	}
}
