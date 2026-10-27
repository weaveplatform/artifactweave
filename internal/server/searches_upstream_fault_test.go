package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccSearchUpstreamRealHTTPFailuresAndOffline(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource fakeupstream.Resource
		hard     bool
		expected int
	}{
		{"404", fakeupstream.Resource{Status: 404}, true, 200},
		{"503 soft", fakeupstream.Resource{Status: 503}, false, 200},
		{"503 hard", fakeupstream.Resource{Status: 503}, true, 503},
		{"truncated soft", fakeupstream.Resource{Body: []byte(searchMetadata), Fault: fakeupstream.Fault{TruncateAfter: 32}}, false, 200},
		{"truncated hard", fakeupstream.Resource{Body: []byte(searchMetadata), Fault: fakeupstream.Fault{TruncateAfter: 32}}, true, 502},
		{"drop soft", fakeupstream.Resource{Fault: fakeupstream.Fault{DropConnection: true}}, false, 200},
		{"drop hard", fakeupstream.Resource{Fault: fakeupstream.Fault{DropConnection: true}}, true, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := searchUpstreamWorld(t)
			u.put("/artifactory/libs-local/org/acme/lib/0.5/lib-0.5.jar", "local")
			u.config(func(c *repo.Config) { c.HardFail = &tc.hard })
			tc.resource.Header = http.Header{"Content-Type": {"application/xml"}}
			u.up.Set("/org/acme/lib/maven-metadata.xml", tc.resource)
			wantStatus(
				t,
				u.do(
					"GET",
					"/artifactory/api/search/versions?g=org.acme&a=lib&repos=virt&remote=1",
					"reader",
					"",
				),
				tc.expected,
			)
		})
	}
	u := searchUpstreamWorld(t)
	u.metadata("org/acme/lib", searchMetadata)
	u.config(func(c *repo.Config) { c.Offline = ptr(true) })
	const query = "/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1"
	wantStatus(t, u.do("GET", query, "reader", ""), 404)
	if len(u.up.Requests()) != 0 {
		t.Fatal("explicit offline initiated a probe")
	}
	u.config(func(c *repo.Config) { c.Offline = ptr(false) })
	wantStatus(t, u.do("GET", query, "reader", ""), 200)
	u.config(func(c *repo.Config) { c.Offline = ptr(true) })
	wantStatus(t, u.do("GET", query, "reader", ""), 200)
	if u.up.Count("GET", "/org/acme/lib/maven-metadata.xml") != 1 {
		t.Fatal("offline reused data with an upstream request")
	}
}

func TestAccSearchUpstreamSnapshotFilteringLegacyAndAdmission(t *testing.T) {
	const prefix = `<metadata><groupId>org.acme</groupId><artifactId>lib</artifactId><version>1.0-SNAPSHOT</version><versioning>`
	const suffix = `</versioning></metadata>`
	for _, tc := range []struct {
		name, body, expected string
		status               int
	}{
		{"legacy", `<snapshot><timestamp>20261022.120000</timestamp><buildNumber>3</buildNumber></snapshot>`, "1.0-20261022.120000-3", 200},
		{"nonunique", "", "1.0-SNAPSHOT", 200},
		{"bad timestamp", `<snapshot><timestamp>invalid</timestamp><buildNumber>3</buildNumber></snapshot>`, "", 502},
		{"negative build", `<snapshot><timestamp>20261022.120000</timestamp><buildNumber>-1</buildNumber></snapshot>`, "", 502},
		{"wrong snapshot", `<snapshotVersions><snapshotVersion><extension>jar</extension><value>2.0-20261022.120000-3</value></snapshotVersion></snapshotVersions>`, "", 502},
		{"invalid classifier", `<snapshotVersions><snapshotVersion><extension>jar</extension><classifier>../secret</classifier><value>1.0-20261022.120000-3</value></snapshotVersion></snapshotVersions>`, "", 502},
		{"not integration", `<snapshotVersions><snapshotVersion><extension>jar</extension><value>1.0-unrecognized</value></snapshotVersion></snapshotVersions>`, "", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := searchUpstreamWorld(t)
			u.metadata("org/acme/lib", searchMetadata)
			u.metadata("org/acme/lib/1.0-SNAPSHOT", prefix+tc.body+suffix)
			res := u.do(
				"GET",
				"/artifactory/api/search/latestVersion?g=org.acme&a=lib&repos=up&remote=1&v=1.0-SNAPSHOT",
				"reader",
				"",
			)
			wantStatus(t, res, tc.status)
			if tc.status == 200 && res.Body.String() != tc.expected {
				t.Fatal(res.Body)
			}
			if tc.status != 200 {
				entries, _, err := u.db.RemoteCache().
					List(t.Context(), "up", "org/acme/lib/1.0-SNAPSHOT", store.Page{Limit: 100})
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Blob.Checksums.SHA256 != "" {
						t.Fatal("invalid snapshot metadata admitted")
					}
				}
			}
		})
	}
	u := searchUpstreamWorld(t)
	u.metadata("org/acme/lib", searchMetadata)
	u.metadata("org/acme/lib/1.0-SNAPSHOT", prefix+`<snapshotVersions>`+
		`<snapshotVersion><extension>jar</extension><value>1.0-20261022.120000-2</value></snapshotVersion>`+
		`<snapshotVersion><extension>jar</extension><classifier>sources</classifier><value>1.0-20261022.120000-10</value></snapshotVersion>`+`</snapshotVersions>`+suffix)
	u.config(func(c *repo.Config) { c.ExcludesPattern = ptr("**/*-sources.jar") })
	res := u.do(
		"GET",
		"/artifactory/api/search/latestVersion?g=org.acme&a=lib&repos=up&remote=1&v=1.0-SNAPSHOT",
		"reader",
		"",
	)
	wantStatus(t, res, 200)
	if res.Body.String() != "1.0-20261022.120000-2" {
		t.Fatal("forbidden snapshot won", res.Body)
	}
}

func TestAccSearchUpstreamBoundsAndFinalDisclosureFence(t *testing.T) {
	t.Run("metadata requests", func(t *testing.T) {
		u := searchUpstreamWorld(t)
		var versions strings.Builder
		for n := 0; n < 101; n++ {
			v := fmt.Sprintf("%d-SNAPSHOT", n)
			fmt.Fprintf(&versions, "<version>%s</version>", v)
			u.metadata("org/acme/lib/"+v, `<metadata><version>`+v+`</version></metadata>`)
		}
		u.metadata(
			"org/acme/lib",
			`<metadata><versioning><versions>`+versions.String()+`</versions></versioning></metadata>`,
		)
		wantStatus(
			t,
			u.do(
				"GET",
				"/artifactory/api/search/latestVersion?g=org.acme&a=lib&repos=up&remote=1&v=*",
				"reader",
				"",
			),
			502,
		)
		if len(u.up.Requests()) != 100 {
			t.Fatal("unbounded metadata requests", len(u.up.Requests()))
		}
	})
	t.Run("metadata records", func(t *testing.T) {
		u := searchUpstreamWorld(t)
		u.metadata(
			"org/acme/lib",
			`<metadata><versioning><versions>`+strings.Repeat(
				"<version>1.0</version>",
				10001,
			)+`</versions></versioning></metadata>`,
		)
		wantStatus(
			t,
			u.do(
				"GET",
				"/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1",
				"reader",
				"",
			),
			502,
		)
	})
	t.Run("listing depth", func(t *testing.T) {
		u := searchUpstreamWorld(t)
		u.config(
			func(c *repo.Config) { c.RepoLayoutRef = "ivy-default"; c.ListRemoteFolderItems = repo.Flex(true) },
		)
		folder := "org.acme/lib"
		for n := 0; n < 33; n++ {
			u.listing(folder, "child/")
			folder += "/child"
		}
		wantStatus(
			t,
			u.do(
				"GET",
				"/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1",
				"reader",
				"",
			),
			502,
		)
		if len(u.up.Requests()) > 32 {
			t.Fatal("depth bound was not applied")
		}
	})
	t.Run("final source fence", func(t *testing.T) {
		u := searchUpstreamWorld(t)
		u.metadata("org/acme/lib", searchMetadata)
		original := u.svc.Revalidate
		calls := 0
		u.svc.Revalidate = func(ctx context.Context, q source.Request, p source.Permit) error {
			if q.Resource.PermissionPath == "org/acme/lib/2.0" {
				calls++
				if calls > 1 {
					return source.ErrDenied
				}
			}
			return original(ctx, q, p)
		}
		wantStatus(
			t,
			u.do(
				"GET",
				"/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1",
				"reader",
				"",
			),
			403,
		)
	})
}

func TestAccSearchUpstreamConfiguredCacheNameStillFetches(t *testing.T) {
	u := searchUpstreamWorld(t)
	c, err := u.db.Repos().Get(t.Context(), "up")
	if err != nil {
		t.Fatal(err)
	}
	c.Key = "real-cache"
	if err = u.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	u.metadata("org/acme/lib", searchMetadata)
	wantStatus(
		t,
		u.do(
			"GET",
			"/artifactory/api/search/versions?g=org.acme&a=lib&repos=real-cache&remote=1",
			"reader",
			"",
		),
		200,
	)
	if len(u.up.Requests()) != 1 {
		t.Fatal("real repository name was treated as an alias")
	}
	// Explicit cached item discovery still returns that artifact for remote=0.
	_, err = u.db.Items().
		Put(t.Context(), types.Item{RepoKey: "up", Cached: true, Path: "org/acme/lib/0.1", Name: "lib-0.1.jar"})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(
		t,
		u.do(
			"GET",
			"/artifactory/api/search/versions?g=org.acme&a=lib&repos=up-cache&remote=1",
			"reader",
			"",
		),
		200,
	)
}

func TestAccSearchUpstreamCustomLayoutsAndGroupMetadata(t *testing.T) {
	for _, name := range []string{"gradle-default", "custom-search"} {
		t.Run(name, func(t *testing.T) {
			u := searchUpstreamWorld(t)
			if name == "custom-search" {
				if err := u.db.Configs().Put(t.Context(), types.ConfigDocument{
					Kind: types.ConfigLayout,
					Key:  name,
					Document: json.RawMessage(
						`{"artifactPathPattern":"[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"}`,
					),
				}); err != nil {
					t.Fatal(err)
				}
			}
			u.config(
				func(c *repo.Config) { c.RepoLayoutRef = name; c.ListRemoteFolderItems = repo.Flex(true) },
			)
			u.listing("org.acme/lib", "5.0/")
			u.listing("org.acme/lib/5.0", "lib-5.0.jar")
			res := u.do(
				"GET",
				"/artifactory/api/search/latestVersion?g=org.acme&a=lib&repos=up&remote=1",
				"reader",
				"",
			)
			wantStatus(t, res, 200)
			if res.Body.String() != "5.0" {
				t.Fatal(res.Body)
			}
		})
	}
	u := searchUpstreamWorld(t)
	u.listing("", "org/")
	u.listing("org", "maven-metadata.xml", "acme/")
	u.metadata(
		"org",
		`<metadata><groupId>org</groupId><plugins><plugin><prefix>root</prefix></plugin></plugins></metadata>`,
	)
	u.listing("org/acme", "maven-metadata.xml", "lib/")
	u.metadata(
		"org/acme",
		`<metadata><groupId>org.acme</groupId><plugins><plugin><prefix>example</prefix></plugin></plugins></metadata>`,
	)
	u.listing("org/acme/lib", "maven-metadata.xml")
	u.metadata("org/acme/lib", searchMetadata)
	res := u.do(
		"GET",
		"/artifactory/api/search/versions?g=*&a=*&repos=up&remote=1",
		"reader",
		"",
	)
	wantStatus(t, res, 200)
	if !strings.Contains(res.Body.String(), `"2.0"`) {
		t.Fatal("group metadata hid modules", res.Body)
	}
}

func TestAccSearchUpstreamAssumedOfflineAndRecovery(t *testing.T) {
	for _, hard := range []bool{false, true} {
		t.Run(fmt.Sprint("hardFail=", hard), func(t *testing.T) {
			u := searchUpstreamWorld(t)
			u.config(func(c *repo.Config) {
				c.HardFail = &hard
				c.AssumedOfflinePeriodSecs = ptr(5)
			})
			u.put("/artifactory/libs-local/org/acme/lib/0.5/lib-0.5.jar", "local")
			u.metadata("org/acme/lib", searchMetadata)
			const query = "/artifactory/api/search/versions?g=org.acme&a=lib&repos=virt&remote=1"
			wantStatus(t, u.do("GET", query, "reader", ""), 200)
			u.clock.Advance(3 * time.Second)
			u.up.Set("/org/acme/lib/maven-metadata.xml", fakeupstream.Resource{
				Fault: fakeupstream.Fault{DropConnection: true},
			})
			status := 200
			if hard {
				status = 502
			}
			for range 10 {
				wantStatus(t, u.do("GET", query, "reader", ""), status)
			}
			state, err := u.db.RemoteCache().State(t.Context(), "up")
			if err != nil || state.ConnectionFailures != 10 ||
				!state.OfflineUntil.After(u.clock.Now()) {
				t.Fatal("search did not use persisted circuit state", state, err)
			}
			requests := len(u.up.Requests())
			res := u.do("GET", query, "reader", "")
			wantStatus(t, res, status)
			if len(u.up.Requests()) != requests {
				t.Fatal("assumed-offline search initiated a probe")
			}
			if !hard &&
				(!strings.Contains(res.Body.String(), `"2.0"`) || !strings.Contains(res.Body.String(), `"0.5"`)) {
				t.Fatal("soft failure lost local or admitted metadata", res.Body)
			}
			u.clock.Advance(5 * time.Second)
			u.metadata("org/acme/lib", strings.ReplaceAll(searchMetadata, "2.0", "3.0"))
			res = u.do("GET", query, "reader", "")
			wantStatus(t, res, 200)
			if !strings.Contains(res.Body.String(), `"3.0"`) || len(u.up.Requests()) != requests+1 {
				t.Fatal("search did not recover through one probe", res.Body)
			}
		})
	}
}

func TestAccSearchUpstreamGavcCurrentFileAuthority(t *testing.T) {
	for _, noStore := range []bool{false, true} {
		t.Run(fmt.Sprint("no-store=", noStore), func(t *testing.T) {
			u := searchUpstreamWorld(t)
			u.config(func(c *repo.Config) { c.StoreArtifactsLocally = ptr(!noStore) })
			u.listing("org/acme/lib", "2.0/")
			u.listing("org/acme/lib/2.0", "lib-2.0.jar", "lib-2.0-sources.jar", "lib-2.0.pom")
			grant := types.Permission{
				Name: "search-file",
				Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
					Actions: types.Grants{
						Users: map[string]types.Actions{"user": types.ActionRead},
					},
					Targets: map[string]types.Target{
						"up": {Include: []string{"org/acme/lib/2.0/lib-2.0.jar"}},
					},
				}},
			}
			if err := u.db.Permissions().Create(t.Context(), grant); err != nil {
				t.Fatal(err)
			}
			const query = "/artifactory/api/search/gavc?g=org.acme&a=lib&repos=up&specific=true"
			res := u.do("GET", query, "user", "")
			wantStatus(t, res, 200)
			if !strings.Contains(res.Body.String(), "/up/org/acme/lib/2.0/lib-2.0.jar") ||
				strings.Contains(
					res.Body.String(),
					"-sources",
				) || strings.Contains(res.Body.String(), ".pom") {
				t.Fatal("GAVC did not apply exact file authority", res.Body)
			}
			original := u.svc.Revalidate
			checks := 0
			u.svc.Revalidate = func(ctx context.Context, q source.Request, p source.Permit) error {
				if q.Resource.Path == "org/acme/lib/2.0/lib-2.0.jar" {
					checks++
					// ListFolder checks visibility and its own completed page. The
					// search must check again after all folders have been collected.
					if checks == 3 {
						if err := u.db.Permissions().Delete(ctx, grant.Name); err != nil {
							t.Fatal(err)
						}
					}
				}
				return original(ctx, q, p)
			}
			res = u.do("GET", query, "user", "")
			wantStatus(t, res, 403)
			if strings.Contains(res.Body.String(), "lib-2.0.jar") {
				t.Fatal("final permission revocation leaked a GAVC path", res.Body)
			}
			for _, request := range u.up.Requests() {
				if strings.HasSuffix(request.Path, ".jar") ||
					strings.HasSuffix(request.Path, ".pom") {
					t.Fatal("GAVC fetched package bytes", request.Path)
				}
			}
		})
	}
}

func TestAccSearchUpstreamListingFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource fakeupstream.Resource
		hard     bool
		expected int
	}{
		{"missing", fakeupstream.Resource{Status: 404}, true, 200},
		{"unavailable soft", fakeupstream.Resource{Status: 503}, false, 200},
		{"unavailable hard", fakeupstream.Resource{Status: 503}, true, 503},
		{"unsupported", fakeupstream.Resource{Body: []byte("<html><h1>Welcome</h1></html>")}, false, 501},
		{"forbidden", fakeupstream.Resource{Status: 403}, false, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := searchUpstreamWorld(t)
			u.put("/artifactory/libs-local/org/acme/lib/0.5/lib-0.5.jar", "local")
			u.config(func(c *repo.Config) {
				c.RepoLayoutRef = "ivy-default"
				c.ListRemoteFolderItems = repo.Flex(true)
				c.HardFail = &tc.hard
			})
			tc.resource.Header = http.Header{"Content-Type": {"text/html"}}
			u.up.Set("/org.acme/lib/", tc.resource)
			for _, endpoint := range []string{"versions", "gavc"} {
				res := u.do("GET", "/artifactory/api/search/"+endpoint+
					"?g=org.acme&a=lib&repos=virt&remote=1&specific=true", "reader", "")
				wantStatus(t, res, tc.expected)
				if tc.expected == 200 && !strings.Contains(res.Body.String(), "0.5") {
					t.Fatal("partial upstream failure lost local artifact", res.Body)
				}
			}
		})
	}
}
