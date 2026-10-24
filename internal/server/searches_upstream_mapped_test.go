package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const mappedVersions = `<metadata><groupId>org.acme</groupId><artifactId>lib</artifactId><versioning><versions><version>1.0</version><version>2.0</version></versions></versioning></metadata>`

func mappedSearchGrant(t *testing.T, u *upstreamSearchWorld, patterns ...string) {
	t.Helper()
	grant := types.Permission{
		Name: "mapped-user",
		Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
				Targets: map[string]types.Target{"up": {Include: patterns}},
			},
		},
	}
	if err := u.db.Permissions().Create(t.Context(), grant); err != nil {
		t.Fatal(err)
	}
}

func mappedSearchLayout(t *testing.T, u *upstreamSearchWorld, name, pattern string) {
	t.Helper()
	doc, err := json.Marshal(map[string]any{"artifactPathPattern": pattern})
	if err != nil {
		t.Fatal(err)
	}
	if err = u.db.Configs().
		Put(t.Context(), types.ConfigDocument{Kind: types.ConfigLayout, Key: name, Document: doc}); err != nil {
		t.Fatal(err)
	}
	u.config(
		func(c *repo.Config) { c.RepoLayoutRef = name; c.RemoteRepoLayoutRef = "maven-2-default" },
	)
}

func TestAccSearchUpstreamMappedMavenMixedFolderAndOffline(t *testing.T) {
	u := searchUpstreamWorld(t)
	u.config(
		func(c *repo.Config) { c.RepoLayoutRef = "gradle-default"; c.RemoteRepoLayoutRef = "maven-2-default" },
	)
	u.metadata("org/acme/lib", mappedVersions)
	u.listing("org/acme/lib", "maven-metadata.xml", "2.0/")
	u.listing("org/acme/lib/2.0", "lib-2.0.jar", "lib-2.0-sources.jar", "lib-2.0.pom")
	const versions = "/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1"
	res := u.do(http.MethodGet, versions, "reader", "")
	wantStatus(t, res, 200)
	if !strings.Contains(res.Body.String(), `"2.0"`) ||
		u.up.Count("GET", "/org/acme/lib/2.0/") != 0 {
		t.Fatal("mapped authoritative version required a guessed artifact", res.Body)
	}
	const gavc = "/artifactory/api/search/gavc?g=org.acme&a=lib&repos=up&specific=true&remote=1"
	res = u.do(http.MethodGet, gavc, "reader", "")
	wantStatus(t, res, 200)
	for _, p := range []string{"/up/org.acme/lib/2.0/lib-2.0.jar", "/up/org.acme/lib/2.0/lib-2.0-sources.jar", "/up/org.acme/lib/ivy-2.0.xml"} {
		if !strings.Contains(res.Body.String(), p) {
			t.Fatal("mixed folder lost canonical path", p, res.Body)
		}
	}
	if strings.Contains(res.Body.String(), "/up/org/acme/") ||
		strings.Contains(res.Body.String(), u.up.URL) ||
		strings.Contains(res.Body.String(), "api/storage") {
		t.Fatal("mapped search leaked upstream or synthetic storage identity", res.Body)
	}
	for _, r := range u.up.Requests() {
		if strings.HasSuffix(r.Path, ".jar") || strings.HasSuffix(r.Path, ".pom") {
			t.Fatal("search fetched binary", r.Path)
		}
	}
	before := len(u.up.Requests())
	u.up.Close()
	for _, query := range []string{versions, gavc} {
		wantStatus(t, u.do(http.MethodGet, query, "reader", ""), 200)
	}
	if len(u.up.Requests()) != before {
		t.Fatal("warm mapped search contacted offline upstream")
	}
	wantStatus(
		t,
		u.do(
			http.MethodGet,
			strings.ReplaceAll(versions, "repos=up&", "repos=up-cache&"),
			"reader",
			"",
		),
		404,
	)
}

func TestAccSearchUpstreamMappedCanonicalPermissionAndCurrentFences(t *testing.T) {
	for _, noStore := range []bool{false, true} {
		t.Run(fmt.Sprint(noStore), func(t *testing.T) {
			u := searchUpstreamWorld(t)
			u.config(func(c *repo.Config) {
				c.RepoLayoutRef = "gradle-default"
				c.RemoteRepoLayoutRef = "maven-2-default"
				c.StoreArtifactsLocally = ptr(!noStore)
			})
			u.metadata("org/acme/lib", mappedVersions)
			mappedSearchGrant(t, u, "org.acme/lib/2.0/*.jar")
			const query = "/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1"
			res := u.do(http.MethodGet, query, "user", "")
			wantStatus(t, res, 200)
			if strings.Contains(res.Body.String(), `"1.0"`) ||
				!strings.Contains(res.Body.String(), `"2.0"`) {
				t.Fatal("upstream path replaced canonical folder authority", res.Body)
			}
			u.late = func(source.Request) {
				u.late = nil
				if err := u.db.Permissions().Delete(t.Context(), "mapped-user"); err != nil {
					t.Fatal(err)
				}
			}
			wantStatus(t, u.do(http.MethodGet, query, "user", ""), 403)
		})
	}
	for _, mutation := range []string{"layout", "source", "credential"} {
		t.Run(mutation, func(t *testing.T) {
			u := searchUpstreamWorld(t)
			u.config(func(c *repo.Config) {
				c.RepoLayoutRef = "gradle-default"
				c.RemoteRepoLayoutRef = "maven-2-default"
				c.StoreArtifactsLocally = ptr(false)
			})
			u.metadata("org/acme/lib", mappedVersions)
			u.late = func(source.Request) {
				u.late = nil
				if mutation == "layout" {
					if err := u.db.Configs().
						Put(t.Context(), types.ConfigDocument{Kind: types.ConfigLayout, Key: "gradle-default", Document: json.RawMessage(`{"artifactPathPattern":"new/[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"}`)}); err != nil {
						t.Fatal(err)
					}
				} else {
					u.config(func(c *repo.Config) {
						if mutation == "source" {
							c.URL += "/new"
						} else {
							c.Password = "rotated"
						}
					})
				}
			}
			wantStatus(
				t,
				u.do(
					http.MethodGet,
					"/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1",
					"reader",
					"",
				),
				503,
			)
		})
	}
}

func TestAccSearchUpstreamMappedFilenameVersionUsesActualFiles(t *testing.T) {
	u := searchUpstreamWorld(t)
	mappedSearchLayout(
		t,
		u,
		"filename-version",
		"[org]/[module]/[module]-[baseRev](-[classifier]).[ext]",
	)
	u.metadata("org/acme/lib", mappedVersions)
	u.listing("org/acme/lib/1.0", "lib-1.0.jar")
	u.listing("org/acme/lib/2.0", "lib-2.0.jar", "lib-2.0-sources.jar")
	mappedSearchGrant(t, u, "org.acme/lib/lib-2.0-sources.jar")
	res := u.do(
		http.MethodGet,
		"/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1",
		"user",
		"",
	)
	wantStatus(t, res, 200)
	if strings.Contains(res.Body.String(), `"1.0"`) ||
		!strings.Contains(res.Body.String(), `"2.0"`) {
		t.Fatal("filename version fabricated directory authority", res.Body)
	}
	if u.up.Count("GET", "/org/acme/lib/2.0/") != 1 {
		t.Fatal("filename version skipped actual-file evidence")
	}
	for _, r := range u.up.Requests() {
		if strings.HasSuffix(r.Path, ".jar") || strings.HasSuffix(r.Path, ".pom") {
			t.Fatal("version evidence fetched a binary", r.Path)
		}
	}
}

func TestAccSearchUpstreamMappedRootRequiresFullGrant(t *testing.T) {
	u := searchUpstreamWorld(t)
	mappedSearchLayout(t, u, "filename-module", "[org]/[module]-[baseRev].[ext]")
	u.metadata("org/acme/lib", mappedVersions)
	u.listing("org/acme/lib/2.0", "lib-2.0.jar")
	mappedSearchGrant(t, u, "org.acme/lib-2.0.jar")
	const query = "/artifactory/api/search/versions?g=org.acme&a=lib&repos=up&remote=1"
	wantStatus(t, u.do(http.MethodGet, query, "user", ""), 404)
	if len(u.up.Requests()) != 0 {
		t.Fatal("partial grant initiated root discovery")
	}
	res := u.do(http.MethodGet, query, "reader", "")
	wantStatus(t, res, 200)
	if !strings.Contains(res.Body.String(), `"2.0"`) {
		t.Fatal(res.Body)
	}
}

func TestAccSearchUpstreamMappedWildcardMetadata(t *testing.T) {
	u := searchUpstreamWorld(t)
	u.config(
		func(c *repo.Config) { c.RepoLayoutRef = "gradle-default"; c.RemoteRepoLayoutRef = "maven-2-default" },
	)
	u.listing("org", "acme/")
	u.listing("org/acme", "lib/")
	u.listing("org/acme/lib", "maven-metadata.xml", "2.0/")
	u.metadata("org/acme/lib", mappedVersions)
	res := u.do(
		http.MethodGet,
		"/artifactory/api/search/versions?g=org.*&a=*&repos=up&remote=1",
		"reader",
		"",
	)
	wantStatus(t, res, 200)
	if !strings.Contains(res.Body.String(), `"2.0"`) {
		t.Fatal(res.Body)
	}
	if u.up.Count("GET", "/org/acme/lib/2.0/") != 0 {
		t.Fatal("metadata discovery descended into artifact folders")
	}
}
