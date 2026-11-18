package nugethttp_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

const virtualV2 = "/artifactory/api/nuget/nuget-virtual/"

func newVirtualLegacyFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := newLegacyFixture(t)
	for _, c := range []repo.Config{{Key: "nuget-nested", Class: repo.Virtual, PackageType: repo.NuGet, Repositories: []string{"nuget-local", "nuget-remote"}}, {Key: "nuget-virtual", Class: repo.Virtual, PackageType: repo.NuGet, Repositories: []string{"nuget-nested", "nuget-local"}}} {
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *remoteFixture) legacyVirtualFeed(entries string) {
	for _, p := range []string{"FindPackagesById()", "Packages", "Packages()", "Search()"} {
		f.setXML("/custom/feed/"+p, legacyFeedXML(entries, ""))
	}
}

func TestAccVirtualV2OwnershipQueriesFlagsAndDownloads(t *testing.T) {
	f := newVirtualLegacyFixture(t)
	local := f.push("Acme.Mixed", "1.0.0", "<description>first owner</description>")
	entries := f.legacyPackage("Acme.Mixed", "1.0.0") + f.legacyPackage("Acme.Mixed", "2.0.0") + f.legacyPackage("Acme.Mixed", "3.0.0-beta.1")
	entries = strings.ReplaceAll(entries, "</m:properties>", "<d:RichField>preserved</d:RichField></m:properties>")
	f.legacyVirtualFeed(entries)
	for _, endpoint := range []string{"Packages()", "FindPackagesById()?id=%27Acme.Mixed%27", "Search()?searchTerm=%27Mixed%27&includePrerelease=true"} {
		feed := f.feed(virtualV2+endpoint+func() string {
			if strings.Contains(endpoint, "?") {
				return "&semVerLevel=2.0.0"
			}
			return "?semVerLevel=2.0.0"
		}(), "reader")
		if len(feed.Entries) != 3 || feed.Entries[0].prop(t, "Description") != "first owner" || feed.Entries[1].prop(t, "RichField") != "preserved" ||
			feed.Entries[1].prop(t, "IsLatestVersion") != "true" ||
			feed.Entries[2].prop(t, "IsAbsoluteLatestVersion") != "true" {
			t.Fatal(feedVersions(t, feed))
		}
		for _, e := range feed.Entries {
			if !strings.Contains(e.Content.Src, virtualV2+"Download/") || !strings.Contains(e.ID, virtualV2+"Packages(") {
				t.Fatal(e)
			}
		}
	}
	for _, path := range []string{"", "$metadata", "Packages(Id='Acme.Mixed',Version='1.0')", "Packages(Id='Acme.Mixed',Version='3.0.0-beta.1')"} {
		wantStatus(t, f.do("GET", virtualV2+path, "reader"), 200)
	}
	for _, user := range []string{"reader", "partial"} {
		wantStatus(t, f.do("GET", virtualV2+"Packages()/$count", ""+user), 200)
	}
	r := f.do("GET", virtualV2+"Packages()/$count?semVerLevel=2.0.0", "reader")
	if r.Code != 200 || r.Body.String() != "3" {
		t.Fatal(r.Code, r.Body)
	}
	for _, path := range []string{"Download/Acme.Mixed/1.0.0", "Download/Acme.Mixed/2.0.0"} {
		r = f.do("GET", virtualV2+path, "reader")
		wantStatus(t, r, 200)
		if strings.HasSuffix(path, "1.0.0") && !bytes.Equal(r.Body.Bytes(), local) {
			t.Fatal("wrong first bytes")
		}
	}
	before := len(f.up.Requests())
	wantStatus(t, f.do("GET", virtualV2+"Download/Acme.Mixed/1.0.0", "reader"), 200)
	if len(f.up.Requests()) != before {
		t.Fatal("local winner contacted upstream")
	}
	for _, path := range []string{"Download/Acme.Mixed/2.0.0", "Packages(Id='Acme.Mixed',Version='2.0.0')"} {
		wantStatus(t, f.do("GET", virtualV2+path, "partial"), 404)
	}
	for _, path := range []string{"Packages()?$filter=IsPrerelease", "Packages()?$top=-1", "Packages()?$orderby=Version", "FindPackagesById()?id=bad", "Packages()?$filter=invalid"} {
		wantStatus(t, f.do("GET", virtualV2+path, "reader"), 400)
	}
	for _, path := range []string{"Download/bad/invalid", "Packages(Id='Bad',Version='bad')", "unknown", "GetUpdates()", "a/b/c/d"} {
		wantStatus(t, f.do("GET", virtualV2+path, "reader"), 404)
	}
	for _, method := range []string{"PUT", "POST", "DELETE"} {
		r = f.doBody(method, virtualV2+"%2e%2e/bad", "admin", strings.NewReader("invalid"))
		if r.Code != 501 || !strings.Contains(r.Body.String(), "through the nuget API yet") {
			t.Fatal(r.Code, r.Body)
		}
	}
}

func TestAccVirtualV2MixedVersionPagesAndPriorityBeforeFilters(t *testing.T) {
	for _, mode := range []string{"whole", "partial", "priority"} {
		t.Run(mode, func(t *testing.T) {
			f := newVirtualLegacyFixture(t)
			var entries strings.Builder
			for i := range 150 {
				v := fmt.Sprintf("1.0.%d", i)
				if i%2 == 0 {
					f.push("Acme.Mixed", v, "")
				} else {
					entries.WriteString(f.legacyPackage("Acme.Mixed", v))
				}
			}
			entries.WriteString(f.legacyPackage("Acme.Mixed", "2.0.0-beta.1"))
			f.legacyVirtualFeed(entries.String())
			user := "admin"
			if mode == "partial" {
				user = "partial"
			}
			if mode == "priority" {
				f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
			}
			feed := f.feed(virtualV2+"FindPackagesById()?id=%27Acme.Mixed%27&semVerLevel=2.0.0", user)
			if mode == "priority" {
				if len(feed.Entries) != 76 {
					t.Fatal(len(feed.Entries))
				}
				return
			}
			want := 150
			if mode == "whole" {
				want = 151
			}
			if len(feed.Entries) != 100 || len(feed.Links) != 1 || !strings.Contains(feed.Links[0].Href, virtualV2+"FindPackagesById()") {
				t.Fatal(len(feed.Entries), feed.Links)
			}
			for _, e := range feed.Entries {
				if e.prop(t, "IsLatestVersion") == "true" || e.prop(t, "IsAbsoluteLatestVersion") == "true" {
					t.Fatal("latest truncated to page")
				}
			}
			next := strings.TrimPrefix(feed.Links[0].Href, base)
			second := f.feed(next, user)
			if len(second.Entries) != want-100 || second.Entries[49].prop(t, "IsLatestVersion") != "true" {
				t.Fatal(len(second.Entries), want)
			}
			r := f.do("GET", virtualV2+"Packages()/$count?semVerLevel=2.0.0", user)
			if r.Body.String() != fmt.Sprint(want) {
				t.Fatal(r.Code, r.Body)
			}
			limited := f.feed(virtualV2+"Packages()?$top=125&semVerLevel=2.0.0", user)
			if len(limited.Links) != 1 {
				t.Fatal(limited)
			}
			last := f.feed(strings.TrimPrefix(limited.Links[0].Href, base), user)
			if len(last.Entries) != 25 || len(last.Links) != 0 {
				t.Fatal(last)
			}
		})
	}
	for _, mode := range []string{"prerelease", "semver2", "query", "unlisted", "member-pattern", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			f := newVirtualLegacyFixture(t)
			if mode == "unreadable" {
				f.deploy("team/Acme.Mixed.1.0.0.nupkg", nupkg("Acme.Mixed", "1.0.0", "<description>needle</description>"))
			} else {
				f.push("Acme.Mixed", "1.0.0", "<description>needle</description>")
			}
			v := "2.0.0-beta"
			if mode == "semver2" {
				v = "2.0.0+build"
			}
			if mode == "query" || mode == "unlisted" || mode == "member-pattern" || mode == "unreadable" {
				v = "2.0.0"
			}
			entry := f.legacyPackage("Acme.Mixed", v)
			if mode == "unlisted" {
				entry = strings.ReplaceAll(entry, "</m:properties>", "<d:Listed>false</d:Listed></m:properties>")
			}
			f.legacyVirtualFeed(entry)
			f.virtualRepo(t, "nuget-remote", func(c *repo.Config) {
				c.PriorityResolution = repo.Flex(true)
				if mode == "member-pattern" {
					s := "other/**"
					c.IncludesPattern = &s
				}
			})
			user := "reader"
			if mode == "unreadable" {
				user = "teamwriter"
			}
			path := "Search()?searchTerm=%27needle%27"
			if mode == "member-pattern" || mode == "unreadable" {
				path = "Packages()"
			}
			feed := f.feed(virtualV2+path, user)
			want := 0
			if mode == "member-pattern" || mode == "unreadable" {
				want = 1
			}
			if len(feed.Entries) != want {
				t.Fatal(mode, feedVersions(t, feed))
			}
		})
	}
}

func TestAccVirtualV2FirstFilteredOwnerCannotBeReplaced(t *testing.T) {
	for _, mode := range []string{"unlisted", "query"} {
		t.Run(mode, func(t *testing.T) {
			f := newVirtualLegacyFixture(t)
			first := f.legacyPackage("Acme.Mixed", "1.0.0")
			second := first
			if mode == "unlisted" {
				first = strings.ReplaceAll(first, "</m:properties>", "<d:Listed>false</d:Listed></m:properties>")
			} else {
				second = strings.ReplaceAll(second, "Remote package", "needle")
			}
			f.legacyVirtualFeed(first)
			c, err := f.db.Repos().Get(t.Context(), "nuget-remote")
			if err != nil {
				t.Fatal(err)
			}
			c.Key = "nuget-second"
			c.FeedContextPath = new("second")
			if err = f.db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			f.setXML("/second/Packages", legacyFeedXML(second, ""))
			f.virtualRepo(t, "nuget-virtual", func(c *repo.Config) { c.Repositories = []string{"nuget-remote", "nuget-second"} })
			path := "Packages()"
			if mode == "query" {
				path = "Search()?searchTerm=%27needle%27"
			}
			feed := f.feed(virtualV2+path, "admin")
			if len(feed.Entries) != 0 {
				t.Fatal(feed)
			}
		})
	}
}

type v2StatHookStore struct {
	pkgroute.Store
	hook func()
}

func (s v2StatHookStore) Items() port.ItemStore {
	return v2StatHookItems{ItemStore: s.Store.Items(), hook: s.hook}
}

type v2StatHookItems struct {
	port.ItemStore
	hook func()
}

func (s v2StatHookItems) Stats(ctx context.Context, id int64) (model.ItemStats, error) {
	s.hook()
	return s.ItemStore.Stats(ctx, id)
}

func TestAccVirtualV2FencesOnlyRetainedSourcesAfterRendering(t *testing.T) {
	for _, mode := range []string{"unused-denied", "retained-denied", "storage"} {
		t.Run(mode, func(t *testing.T) {
			f := newVirtualLegacyFixture(t)
			f.push("Acme.Mixed", "1.0.0", "")
			f.legacyVirtualFeed(f.legacyPackage("Acme.Mixed", "1.0.0"))
			revoked := false
			if mode == "retained-denied" {
				f.legacyVirtualFeed(f.legacyPackage("Acme.Mixed", "2.0.0"))
			}
			f.opts.Store = v2StatHookStore{Store: f.db, hook: func() { revoked = true }}
			f.mount(t, base)
			f.svc.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
				if revoked && strings.HasSuffix(q.Resource.Path, ".nupkg") {
					if mode == "storage" {
						return fmt.Errorf("%w: %w", format.ErrStorageFault, errBroken)
					}
					return source.ErrDenied
				}
				return nil
			}
			path := "Packages()"
			if mode == "storage" {
				f.legacyVirtualFeed(f.legacyPackage("Acme.Mixed", "2.0.0"))
			}
			r := f.do("GET", virtualV2+path, "reader")
			want := 200
			if mode == "retained-denied" {
				want = 404
			}
			if mode == "storage" {
				want = 500
			}
			if r.Code != want {
				t.Fatal(r.Code, r.Body)
			}
		})
	}
}

func TestAccVirtualV2StorageFaultsAndNoReadableHolders(t *testing.T) {
	for _, mode := range []string{"find", "versions", "metadata", "list", "page", "item", "stats"} {
		t.Run(mode, func(t *testing.T) {
			f := newVirtualLegacyFixture(t)
			f.push("Acme.Mixed", "1.0.0", "")
			f.opts.Store = failingStore{Store: f.db, failing: mode}
			f.mount(t, base)
			paths := []string{"Packages()", "Packages()/$count", "Packages()?$filter=IsLatestVersion", "FindPackagesById()?id=%27Acme.Mixed%27"}
			if mode == "find" {
				paths = []string{"FindPackagesById()?id=%27Acme.Mixed%27", "Packages(Id='Acme.Mixed',Version='1.0.0')", "Download/Acme.Mixed/1.0.0"}
			}
			if mode == "page" {
				paths = []string{"Packages()"}
			}
			if mode == "item" {
				paths = []string{"Download/Acme.Mixed/1.0.0"}
			}
			if mode == "stats" {
				paths = []string{"Packages()", "Packages(Id='Acme.Mixed',Version='1.0.0')"}
			}
			if mode == "list" {
				paths = paths[:3]
			}
			for _, path := range paths {
				r := f.do("GET", virtualV2+path, "admin")
				if r.Code != 500 {
					t.Fatal(path, r.Code, r.Body)
				}
			}
		})
	}
	f := newVirtualLegacyFixture(t)
	f.push("Acme.Mixed", "1.0.0", "")
	for _, path := range []string{"Packages(Id='Acme.Mixed',Version='1.0.0')", "Download/Acme.Mixed/1.0.0"} {
		wantStatus(t, f.do("GET", virtualV2+path, "nobody"), 404)
	}
	f.virtualRepo(t, "nuget-virtual", func(c *repo.Config) { c.ForceNugetAuthentication = new(true) })
	for _, method := range []string{"GET", "PUT", "DELETE", "POST"} {
		r := f.doBody(method, virtualV2+"%2e%2e/bad", "anonymous", strings.NewReader("malformed"))
		if r.Code != 401 || r.Header().Get("WWW-Authenticate") == "" {
			t.Fatal(method, r.Code, r.Header(), r.Body)
		}
	}
}

func TestAccVirtualV2StaleHostedAndMissingRemoteBinariesFallThrough(t *testing.T) {
	for _, mode := range []string{"missing-hosted", "folder-hosted", "missing-remote"} {
		t.Run(mode, func(t *testing.T) {
			f := newVirtualLegacyFixture(t)
			entry := f.legacyPackage("Acme.Mixed", "1.0.0")
			f.legacyVirtualFeed(entry)
			if mode != "missing-remote" {
				it := f.deploy("Acme.Mixed.1.0.0.nupkg", nupkg("Acme.Mixed", "1.0.0", "<description>stale</description>"))
				if _, err := f.db.Items().Delete(t.Context(), "nuget-local", false, it.FullPath()); err != nil {
					t.Fatal(err)
				}
				if mode == "folder-hosted" {
					folder := model.Item{RepoKey: "nuget-local", Name: "Acme.Mixed.1.0.0.nupkg", Folder: true}
					if _, err := f.db.Items().Create(t.Context(), folder); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				f.up.Set("/custom/packages/Acme.Mixed/1.0.0", fakeupstream.Resource{Status: 403})
				body := nupkg("Acme.Mixed", "1.0.0", "")
				f.up.Set("/second/packages/Acme.Mixed/1.0.0", fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}})
				c, err := f.db.Repos().Get(t.Context(), "nuget-remote")
				if err != nil {
					t.Fatal(err)
				}
				c.Key = "nuget-second"
				c.FeedContextPath = new("second/feed")
				c.DownloadContextPath = new("second/packages")
				if err = f.db.Repos().Create(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				f.setXML("/second/feed/FindPackagesById()", legacyFeedXML(legacyEntryXML("Acme.Mixed", "1.0.0", f.up.URL+"/second/packages/Acme.Mixed/1.0.0", body), ""))
				f.virtualRepo(t, "nuget-virtual", func(c *repo.Config) { c.Repositories = []string{"nuget-remote", "nuget-second"} })
			}
			r := f.do("GET", virtualV2+"Download/Acme.Mixed/1.0.0", "admin")
			wantStatus(t, r, 200)
			if !bytes.Equal(r.Body.Bytes(), nupkg("Acme.Mixed", "1.0.0", "")) {
				t.Fatal("fall-through returned stale/folder bytes")
			}
		})
	}
}

func TestAccVirtualV2AuthorityAndSourcePolicyPrecedePriority(t *testing.T) {
	for _, mode := range []string{"denied", "storage", "local-pattern", "count-fence", "entry-fence"} {
		t.Run(mode, func(t *testing.T) {
			f := newVirtualLegacyFixture(t)
			f.push("Acme.Mixed", "1.0.0", "")
			f.legacyVirtualFeed(f.legacyPackage("Acme.Mixed", "2.0.0"))
			f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
			if mode == "local-pattern" {
				f.virtualRepo(t, "nuget-remote", func(c *repo.Config) { c.PriorityResolution = repo.Flex(false) })
				f.virtualRepo(t, "nuget-local", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true); c.IncludesPattern = new("other/**") })
			}
			checks := 0
			f.svc.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
				if !strings.HasSuffix(q.Resource.Path, ".nupkg") {
					return nil
				}
				checks++
				if mode == "storage" {
					return fmt.Errorf("%w: %w", format.ErrStorageFault, errBroken)
				}
				if mode == "denied" || (mode == "count-fence" || mode == "entry-fence") && checks > 1 {
					return source.ErrDenied
				}
				return nil
			}
			path := "Packages()"
			want := 200
			if mode == "storage" {
				path = "Download/Acme.Mixed/2.0.0"
				want = 500
			}
			if mode == "count-fence" {
				path = "Packages()/$count"
				want = 404
			}
			if mode == "entry-fence" {
				path = "Packages(Id='Acme.Mixed',Version='2.0.0')"
				want = 404
			}
			r := f.do("GET", virtualV2+path, "reader")
			wantStatus(t, r, want)
			if mode == "denied" && !strings.Contains(r.Body.String(), "1.0.0") {
				t.Fatal("unreadable priority source shadowed ordinary holder", r.Body)
			}
			if mode == "local-pattern" && (strings.Contains(r.Body.String(), "1.0.0") || !strings.Contains(r.Body.String(), "2.0.0")) {
				t.Fatal(r.Body)
			}
		})
	}
	f := newVirtualLegacyFixture(t)
	wantStatus(t, f.do("GET", virtualV2+"FindPackagesById()?id=%27bad!%27", "admin"), 400)
	wantStatus(t, f.do("GET", virtualV2+"Download/Acme.Mixed/%2e%2e", "admin"), 404)
}

func TestAccVirtualV2CountersBelongToTheSupplyingVersion(t *testing.T) {
	f := newVirtualLegacyFixture(t)
	it := f.deploy("Acme.Mixed.1.0.0.nupkg", nupkg("Acme.Mixed", "1.0.0", ""))
	f.legacyVirtualFeed(f.legacyPackage("Acme.Mixed", "1.0.0"))
	if err := f.db.Items().RecordDownload(t.Context(), it.ID, port.Download{By: "reader", At: f.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	r := f.do("GET", virtualV2+"Packages()", "reader")
	wantStatus(t, r, 200)
	if strings.Contains(r.Body.String(), "<d:DownloadCount") || strings.Contains(r.Body.String(), "9999") || !strings.Contains(r.Body.String(), ">1</d:VersionDownloadCount>") {
		t.Fatal(r.Body)
	}
}
