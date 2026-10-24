package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func searchVirtual(
	t *testing.T,
	u *upstreamSearchWorld,
	key string,
	members []string,
	include, exclude string,
) {
	t.Helper()
	c := repo.Config{
		Key:             key,
		Class:           repo.Virtual,
		PackageType:     repo.Maven,
		Repositories:    members,
		IncludesPattern: ptr(include),
		ExcludesPattern: ptr(exclude),
	}
	if _, err := u.db.Repos().Get(t.Context(), key); err != nil {
		if err = u.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	} else if err = u.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func TestAccSearchUpstreamVirtualRoutes(t *testing.T) {
	u := searchUpstreamWorld(t)
	u.metadata("org/acme/lib", mappedVersions)
	u.listing("org/acme/lib", "1.0/", "2.0/")
	u.listing("org/acme/lib/1.0", "lib-1.0.jar")
	u.listing("org/acme/lib/2.0", "lib-2.0.jar", "lib-2.0-sources.jar")
	searchVirtual(t, u, "denied", []string{"up"}, "elsewhere/**", "")
	searchVirtual(t, u, "inner", []string{"up"}, "org/acme/lib/2.0/**", "**/*-sources.jar")
	searchVirtual(t, u, "virt", []string{"denied", "inner"}, "org/acme/**", "")
	const versions = "/artifactory/api/search/versions?g=org.acme&a=lib&repos=virt&remote=1"
	res := u.do(http.MethodGet, versions, "reader", "")
	wantStatus(t, res, 200)
	if strings.Contains(res.Body.String(), `"1.0"`) ||
		!strings.Contains(res.Body.String(), `"2.0"`) {
		t.Fatal(res.Body)
	}
	const gavc = "/artifactory/api/search/gavc?g=org.acme&a=lib&repos=denied,virt&specific=true&remote=1"
	res = u.do(http.MethodGet, gavc, "reader", "")
	wantStatus(t, res, 200)
	if strings.Contains(res.Body.String(), "1.0") ||
		strings.Contains(res.Body.String(), "sources") ||
		strings.Contains(res.Body.String(), "/denied/") ||
		!strings.Contains(res.Body.String(), "/virt/org/acme/lib/2.0/lib-2.0.jar") {
		t.Fatal(res.Body)
	}
	// A local cache row must not bypass the virtual route on the next query.
	u.put("/artifactory/libs-local/org/acme/lib/1.0/lib-1.0.jar", "local")
	searchVirtual(t, u, "virt", []string{"libs-local", "up"}, "org/acme/lib/2.0/**", "")
	res = u.do(http.MethodGet, versions, "reader", "")
	wantStatus(t, res, 200)
	if strings.Contains(res.Body.String(), `"1.0"`) {
		t.Fatal("local result bypassed route", res.Body)
	}
	before := len(u.up.Requests())
	searchVirtual(t, u, "virt", []string{"up"}, "elsewhere/**", "")
	wantStatus(t, u.do(http.MethodGet, versions, "reader", ""), 404)
	if len(u.up.Requests()) != before {
		t.Fatal("denied virtual scope made an upstream request")
	}
}

func TestAccSearchUpstreamVirtualCurrentMembership(t *testing.T) {
	for _, endpoint := range []string{"versions", "gavc"} {
		for _, noStore := range []bool{false, true} {
			for _, mutation := range []string{"member", "pattern", "class", "delete", "blackout"} {
				t.Run(fmt.Sprintf("%s/%v/%s", endpoint, noStore, mutation), func(t *testing.T) {
					u := searchUpstreamWorld(t)
					u.config(func(c *repo.Config) { c.StoreArtifactsLocally = ptr(!noStore) })
					u.metadata("org/acme/lib", mappedVersions)
					u.listing("org/acme/lib", "2.0/")
					u.listing("org/acme/lib/2.0", "lib-2.0.jar")
					searchVirtual(t, u, "inner", []string{"up"}, "**/*", "")
					searchVirtual(t, u, "virt", []string{"inner"}, "**/*", "")
					u.late = func(source.Request) {
						u.late = nil
						c, err := u.db.Repos().Get(t.Context(), "inner")
						if err != nil {
							t.Fatal(err)
						}
						switch mutation {
						case "member":
							c.Repositories = nil
						case "pattern":
							c.ExcludesPattern = ptr("org/acme/**")
						case "blackout":
							c.BlackedOut = ptr(true)
						case "class":
							c.Class = repo.Local
							c.Repositories = nil
						case "delete":
							if err = u.db.Repos().Delete(t.Context(), "inner"); err != nil {
								t.Fatal(err)
							}
							return
						}
						if err = u.db.Repos().Update(t.Context(), c); err != nil {
							t.Fatal(err)
						}
					}
					res := u.do(
						http.MethodGet,
						"/artifactory/api/search/"+endpoint+"?g=org.acme&a=lib&repos=virt&remote=1&specific=true",
						"reader",
						"",
					)
					wantStatus(t, res, 503)
					if strings.Contains(res.Body.String(), `"2.0"`) ||
						strings.Contains(res.Body.String(), "downloadUri") {
						t.Fatal("changed route disclosed result", res.Body)
					}
				})
			}
		}
	}
}

func TestAccSearchUpstreamGavcNonMavenFlagOff(t *testing.T) {
	u := searchUpstreamWorld(t)
	u.config(func(c *repo.Config) {
		c.RepoLayoutRef = "ivy-default"
		c.RemoteRepoLayoutRef = "ivy-default"
		c.ListRemoteFolderItems = repo.Flex(false)
	})
	u.listing("org.acme/lib", "2.0/")
	u.listing("org.acme/lib/2.0", "jars/")
	u.listing("org.acme/lib/2.0/jars", "lib-2.0.jar")
	const query = "?g=org.acme&a=lib&repos=up&remote=1"
	wantStatus(t, u.do(http.MethodGet, "/artifactory/api/search/versions"+query, "reader", ""), 404)
	if len(u.up.Requests()) != 0 {
		t.Fatal("non-Maven versions ignored browse flag")
	}
	res := u.do(http.MethodGet, "/artifactory/api/search/gavc"+query+"&specific=true", "reader", "")
	wantStatus(t, res, 200)
	if !strings.Contains(res.Body.String(), "/up/org.acme/lib/2.0/jars/lib-2.0.jar") {
		t.Fatal(res.Body)
	}
}

func TestAccSearchUpstreamRouteBoundsAndDirectAuthority(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(fmt.Sprint(cycle), func(t *testing.T) {
			u := searchUpstreamWorld(t)
			members := make([]string, 1001)
			for i := range members {
				members[i] = "up"
			}
			if cycle {
				members = []string{"virt"}
			}
			searchVirtual(t, u, "virt", members, "**/*", "")
			for _, endpoint := range []string{"versions", "gavc"} {
				wantStatus(
					t,
					u.do(
						http.MethodGet,
						"/artifactory/api/search/"+endpoint+"?g=org.acme&a=lib&repos=virt&remote=1&specific=true",
						"reader",
						"",
					),
					502,
				)
			}
			if len(u.up.Requests()) != 0 {
				t.Fatal("invalid route graph contacted upstream")
			}
		})
	}
	u := searchUpstreamWorld(t)
	u.metadata("org/acme/lib", mappedVersions)
	searchVirtual(t, u, "virt", []string{"up"}, "other/**", "")
	const query = "/artifactory/api/search/versions?g=org.acme&a=lib&remote=1"
	// Unscoped search has direct member authority; an unrelated virtual cannot
	// impose its restrictions on a separately requested physical repository.
	for _, repos := range []string{"", "&repos=virt,up", "&repos=missing,up"} {
		res := u.do(http.MethodGet, query+repos, "reader", "")
		wantStatus(t, res, 200)
		if !strings.Contains(res.Body.String(), `"2.0"`) {
			t.Fatal(res.Body)
		}
	}
	wantStatus(t, u.do(http.MethodGet, query, "user", ""), 404)
}

func TestAccSearchUpstreamVirtualBlackout(t *testing.T) {
	u := searchUpstreamWorld(t)
	u.metadata("org/acme/lib", mappedVersions)
	searchVirtual(t, u, "virt", []string{"up"}, "**/*", "")
	c, err := u.db.Repos().Get(t.Context(), "virt")
	if err != nil {
		t.Fatal(err)
	}
	c.BlackedOut = ptr(true)
	if err = u.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	const query = "/artifactory/api/search/versions?g=org.acme&a=lib&remote=1&repos="
	wantStatus(t, u.do(http.MethodGet, query+"virt", "reader", ""), 404)
	if len(u.up.Requests()) != 0 {
		t.Fatal("blacked-out route contacted upstream")
	}
	searchVirtual(t, u, "permitted", []string{"up"}, "**/*", "")
	for _, key := range []string{"virt,permitted", "virt,up"} {
		wantStatus(t, u.do(http.MethodGet, query+key, "reader", ""), 200)
	}
}
