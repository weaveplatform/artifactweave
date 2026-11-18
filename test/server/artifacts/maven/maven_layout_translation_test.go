package maven_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

const (
	layoutMavenPath  = "org/acme/app/1.0/app-1.0.jar"
	layoutIvyPath    = "org.acme/app/1.0/jars/app-1.0.jar"
	layoutGradlePath = "org.acme/app/1.0/app-1.0.jar"
)

func layoutWorld(t *testing.T, from, to string) *remoteWorld {
	t.Helper()
	w := selectionWorld(t)
	selectionConfig(t, w, "virtual", func(c *repo.Config) {
		c.RepoLayoutRef = from
		c.Repositories = []string{"first"}
		c.DefaultDeploymentRepo = "first"
	})
	selectionConfig(t, w, "first", func(c *repo.Config) { c.RepoLayoutRef = to })
	return w
}

func TestAccMavenVirtualLayoutTranslations(t *testing.T) {
	for _, tc := range []struct{ name, from, to, asked, stored string }{
		{"maven to ivy", "maven-2-default", "ivy-default", layoutMavenPath, layoutIvyPath},
		{"ivy to maven", "ivy-default", "maven-2-default", layoutIvyPath, layoutMavenPath},
		{"maven to gradle", "maven-2-default", "gradle-default", layoutMavenPath, layoutGradlePath},
		{"gradle to maven", "gradle-default", "maven-2-default", layoutGradlePath, layoutMavenPath},
		{"ivy to gradle", "ivy-default", "gradle-default", layoutIvyPath, layoutGradlePath},
		{"gradle to ivy", "gradle-default", "ivy-default", layoutGradlePath, layoutIvyPath},
		{"same layout", "maven-2-default", "maven-2-default", layoutMavenPath, layoutMavenPath},
		{"unreadable layout path", "maven-2-default", "ivy-default", "README.txt", "README.txt"},
		{"missing layout", "unavailable", "ivy-default", layoutMavenPath, layoutMavenPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := layoutWorld(t, tc.from, tc.to)
			selectionPut(t, w, "first", tc.stored, "translated bytes", 100)
			remoteStatus(t, w.request("PUT", "/artifactory/first/"+tc.stored+".md5", "admin", pomSums("translated bytes").MD5), 201)
			remoteStatus(t, w.request("PUT", "/artifactory/first/"+tc.stored+".sha512", "admin", pomSums("translated bytes").SHA512), 201)
			for _, method := range []string{"GET", "HEAD"} {
				r := w.request(method, "/artifactory/virtual/"+tc.asked, "admin", "")
				if r.Code != 200 || method == "GET" && r.Body.String() != "translated bytes" || method == "HEAD" && r.Body.Len() != 0 {
					t.Fatal(method, r.Code, r.Body.String())
				}
				if !strings.HasSuffix(tc.stored, r.Header().Get("X-Artifactory-Filename")) {
					t.Fatal(r.Header())
				}
			}
			r := w.request("GET", "/artifactory/virtual/"+tc.asked+".md5", "admin", "")
			if r.Code != 200 || r.Body.String() != pomSums("translated bytes").MD5 {
				t.Fatal(r.Code, r.Body.String())
			}
			// SHA-512 is an existing item-route sidecar, not a vendor helper
			// checksum filter. It must resolve the translated owner file.
			r = w.request("GET", "/artifactory/virtual/"+tc.asked+".sha512", "admin", "")
			if r.Code != 200 || r.Body.String() != pomSums("translated bytes").SHA512 {
				t.Fatal("SHA-512 translated owner", r.Code, r.Body.String())
			}
			trace := w.request("GET", "/artifactory/virtual/"+tc.asked+"?trace", "admin", "")
			if tc.asked != tc.stored && !strings.Contains(trace.Body.String(), "Request path was translated to "+tc.stored+" due to repository layout differences") {
				t.Fatal(trace.Body.String())
			}
			// Storage API and direct reads preserve their existing namespace.
			remoteStatus(t, w.request("GET", "/artifactory/first/"+tc.stored, "admin", ""), 200)
			if tc.asked != tc.stored {
				remoteStatus(t, w.request("GET", "/artifactory/api/storage/virtual/"+tc.asked, "admin", ""), 404)
			}
		})
	}
}

func TestAccMavenVirtualLayoutFallbackAndNoFallback(t *testing.T) {
	w := layoutWorld(t, "maven-2-default", "gradle-default")
	for _, p := range []string{layoutMavenPath, "org/acme/app/[RELEASE]/app-[RELEASE].jar", "org/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", "org/acme/app/1.0-SNAPSHOT/app-1.0-20261118.120000-1.jar"} {
		selectionPut(t, w, "first", p, "original bytes", 100)
		want := 200
		if strings.Contains(p, "SNAPSHOT") {
			want = 404
		}
		r := w.request("GET", "/artifactory/virtual/"+p, "admin", "")
		if r.Code != want || want == 200 && r.Body.String() != "original bytes" {
			t.Fatal(p, r.Code, r.Body.String())
		}
		trace := w.request("GET", "/artifactory/virtual/"+p+"?trace", "admin", "").Body.String()
		if strings.Contains(trace, "Unable to find the resource in the translated path - retrying with the original") != (want == 200) {
			t.Fatal(trace)
		}
	}
	// A custom metadata filename carries valid module values, so metadata can
	// translate here even though ordinary Maven metadata has no module match.
	for _, tc := range []struct{ name, pattern string }{{"metadata-root", "[orgPath]/[module]/[baseRev]/maven-metadata.xml"}, {"metadata-member", "mapped/[org]/[module]/[baseRev]/maven-metadata.xml"}} {
		if err := w.rt.Store.Configs().
			Put(t.Context(), configdoc.ConfigDocument{Kind: configdoc.ConfigLayout, Key: tc.name, Document: json.RawMessage(`{"artifactPathPattern":` + strconvQuote(tc.pattern) + `}`)}); err != nil {
			t.Fatal(err)
		}
	}
	selectionConfig(t, w, "virtual", func(c *repo.Config) { c.RepoLayoutRef = "metadata-root" })
	selectionConfig(t, w, "first", func(c *repo.Config) { c.RepoLayoutRef = "metadata-member" })
	const original = "org/acme/app/1.0/maven-metadata.xml"
	const mapped = "mapped/org.acme/app/1.0/maven-metadata.xml"
	selectionPut(t, w, "first", original, virtualMetadata("1.0"), 100)
	remoteStatus(t, w.request("GET", "/artifactory/virtual/"+original, "admin", ""), 404)
	selectionPut(t, w, "first", mapped, virtualMetadata("2.0"), 100)
	r := w.request("GET", "/artifactory/virtual/"+original, "admin", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "<version>2.0</version>") || strings.Contains(r.Body.String(), "<version>1.0</version>") {
		t.Fatal(r.Code, r.Body.String())
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestAccMavenVirtualLayoutNestedPoliciesAndReads(t *testing.T) {
	w := layoutWorld(t, "maven-2-default", "ivy-default")
	selectionPut(t, w, "first", layoutIvyPath, "first bytes", 100)
	inner := *repo.Defaults(&repo.Config{Key: "inner", Class: repo.Virtual, PackageType: repo.Maven, RepoLayoutRef: "gradle-default", Repositories: []string{"first"}, IncludesPattern: new("org.acme/**")})
	if err := w.rt.Store.Repos().Create(t.Context(), inner); err != nil {
		t.Fatal(err)
	}
	selectionConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"inner"}; c.IncludesPattern = new("org/acme/**") })
	selectionConfig(t, w, "first", func(c *repo.Config) { c.IncludesPattern = new("org.acme/**") })
	r := w.request("GET", "/artifactory/virtual/"+layoutMavenPath, "admin", "")
	if r.Code != 200 || r.Body.String() != "first bytes" {
		t.Fatal(r.Code, r.Body.String())
	}
	trace := w.request("GET", "/artifactory/virtual/"+layoutMavenPath+"?trace", "admin", "").Body.String()
	if !strings.Contains(trace, "Resource was translated to '"+layoutGradlePath+"' in order to search within nested virtual 'inner'") {
		t.Fatal(trace)
	}
	selectionConfig(t, w, "inner", func(c *repo.Config) { c.IncludesPattern = new("org/acme/**") })
	remoteStatus(t, w.request("GET", "/artifactory/virtual/"+layoutMavenPath, "admin", ""), 404)
	selectionConfig(t, w, "inner", func(c *repo.Config) { c.IncludesPattern = new("org.acme/**") })
	selectionConfig(t, w, "first", func(c *repo.Config) { c.BlackedOut = new(true) })
	// Virtual member policy precedes Read, including translated attempts.
	r = w.request("GET", "/artifactory/virtual/"+layoutMavenPath, "reader", "")
	if r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	trace = w.request("GET", "/artifactory/virtual/"+layoutMavenPath+"?trace", "reader", "").Body.String()
	if !strings.Contains(trace, "is blacked out") || strings.Contains(trace, "Request is forbidden by") {
		t.Fatal(trace)
	}
	selectionConfig(t, w, "first", func(c *repo.Config) { c.BlackedOut = new(false); c.ExcludesPattern = new("**/*.jar") })
	r = w.request("GET", "/artifactory/virtual/"+layoutMavenPath, "reader", "")
	if r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	trace = w.request("GET", "/artifactory/virtual/"+layoutMavenPath+"?trace", "reader", "").Body.String()
	if !strings.Contains(trace, "include/exclude") || strings.Contains(trace, "Request is forbidden by") {
		t.Fatal(trace)
	}
	selectionConfig(t, w, "first", func(c *repo.Config) { c.ExcludesPattern = nil })
	selectionConfig(t, w, "second", func(c *repo.Config) { c.RepoLayoutRef = "gradle-default" })
	selectionPut(t, w, "second", layoutGradlePath, "second bytes", 200)
	selectionConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"inner", "second"} })
	selectionConfig(t, w, "first", func(c *repo.Config) { c.ExcludesPattern = new("**/*.jar") })
	r = w.request("GET", "/artifactory/virtual/"+layoutMavenPath, "admin", "")
	if r.Code != 200 || r.Body.String() != "second bytes" {
		t.Fatal(r.Code, r.Body.String())
	}
	// The addressed virtual still checks its original path, including admins.
	selectionConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("org/acme/**") })
	remoteStatus(t, w.request("GET", "/artifactory/virtual/"+layoutMavenPath, "admin", ""), 404)
}

func TestAccMavenVirtualLayoutWritesAndStorageFault(t *testing.T) {
	w := layoutWorld(t, "maven-2-default", "ivy-default")
	r := w.request("PUT", "/artifactory/virtual/"+layoutMavenPath, "admin", "unaltered deploy")
	remoteStatus(t, r, 201)
	if _, err := w.rt.Store.Items().Get(t.Context(), "first", false, layoutMavenPath); err != nil {
		t.Fatal(err)
	}
	if _, err := w.rt.Store.Items().Get(t.Context(), "first", false, layoutIvyPath); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("translated write", err)
	}
	// A layout-store failure is a server fault, never a mapping miss/retry.
	renameTable(t, w, "config_documents", "config_documents_held")
	defer renameTable(t, w, "config_documents_held", "config_documents")
	r = w.request(http.MethodGet, "/artifactory/virtual/"+layoutMavenPath, "admin", "")
	if r.Code != 500 || !strings.Contains(r.Body.String(), "internal error") {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestAccMavenVirtualLayoutActualPathPermissionAndRefusal(t *testing.T) {
	w := layoutWorld(t, "maven-2-default", "ivy-default")
	selectionConfig(t, w, "second", func(c *repo.Config) { c.RepoLayoutRef = "gradle-default" })
	selectionPut(t, w, "first", layoutIvyPath, "unreadable first", 100)
	selectionPut(t, w, "second", layoutGradlePath, "readable second", 200)
	selectionConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"first", "second"} })
	if err := w.rt.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "translated-read", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"reader": accessapi.ActionRead}}, Targets: map[string]accesstest.Target{"second": {Include: []string{layoutGradlePath}}}}}}); err != nil {
		t.Fatal(err)
	}
	r := w.request("GET", "/artifactory/virtual/"+layoutMavenPath, "reader", "")
	if r.Code != 200 || r.Body.String() != "readable second" {
		t.Fatal(r.Code, r.Body.String())
	}
	selectionConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"first"} })
	selectionPut(t, w, "first", layoutMavenPath, "original fallback trap", 100)
	// Each spelling independently applies policy: a refused translated spelling
	// may legitimately fall back to an accepted original file.
	selectionConfig(t, w, "first", func(c *repo.Config) { c.IncludesPattern = new("org/acme/**") })
	remoteStatus(t, w.request("GET", "/artifactory/virtual/"+layoutMavenPath, "admin", ""), 200)
	trace := w.request("GET", "/artifactory/virtual/"+layoutMavenPath+"?trace", "admin", "").Body.String()
	if !strings.Contains(trace, "retrying with the original") {
		t.Fatal(trace)
	}
}

func TestAccMavenVirtualLayoutRemoteMapping(t *testing.T) {
	for _, store := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream", true: "cache"}[store], func(t *testing.T) {
			w := layoutWorld(t, "ivy-default", "gradle-default")
			selectionConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"remote"} })
			w.c.RepoLayoutRef = "gradle-default"
			w.c.RemoteRepoLayoutRef = "maven-2-default"
			w.c.StoreArtifactsLocally = &store
			w.c.IncludesPattern = new("org.acme/**")
			w.update()
			body := sourceJAR(t, "two independent layout mappings")
			mavenSource(w, layoutMavenPath, body, "application/java-archive")
			for range 2 {
				r := w.request("GET", "/artifactory/virtual/"+layoutIvyPath, "reader", "")
				if r.Code != 200 || r.Body.String() != string(body) {
					t.Fatal(r.Code, r.Body.String(), w.up.Requests())
				}
			}
			if n := w.up.Count("GET", "/"+layoutMavenPath); n < 1 {
				t.Fatal(w.up.Requests())
			}
			if w.up.Count("", "/"+layoutIvyPath) != 0 || w.up.Count("", "/"+layoutGradlePath) != 0 {
				t.Fatal("wrong source path", w.up.Requests())
			}
			if store {
				if _, err := w.rt.Store.Items().Get(t.Context(), "remote", true, layoutGradlePath); err != nil {
					t.Fatal("wrong canonical cache identity", err)
				}
			}
		})
	}
}

func TestAccMavenVirtualLayoutSnapshotSelection(t *testing.T) {
	w := layoutWorld(t, "maven-2-default", "ivy-default")
	selectionConfig(t, w, "second", func(c *repo.Config) { c.RepoLayoutRef = "ivy-default" })
	selectionConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"first", "second"} })
	for _, tc := range []struct{ name, asked, stored, want string }{
		{"nonunique newest", "org/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", "org.acme/app/1.0-SNAPSHOT/jars/app-1.0-SNAPSHOT.jar", "second snapshot"},
		{"unique exact first", "org/acme/app/1.0-SNAPSHOT/app-1.0-20261118.120000-1.jar", "org.acme/app/1.0-SNAPSHOT/jars/app-1.0-20261118.120000-1.jar", "first snapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectionPut(t, w, "first", tc.stored, "first snapshot", 100)
			selectionPut(t, w, "second", tc.stored, "second snapshot", 200)
			r := w.request("GET", "/artifactory/virtual/"+tc.asked, "admin", "")
			if r.Code != 200 || r.Body.String() != tc.want {
				t.Fatal(r.Code, r.Body.String())
			}
			trace := w.request("GET", "/artifactory/virtual/"+tc.asked+"?trace", "admin", "").Body.String()
			if strings.Contains(trace, "retrying with the original") {
				t.Fatal(trace)
			}
			if tc.name == "unique exact first" && (!strings.Contains(trace, "Selecting a unique snapshot") || strings.Contains(trace, "Searching for the resource within second")) {
				t.Fatal(trace)
			}
		})
	}
}

func TestAccMavenVirtualLayoutCopyMoveControls(t *testing.T) {
	w := layoutWorld(t, "maven-2-default", "maven-2-default")
	const pattern = `{"artifactPathPattern":"dest/[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]","folderIntegrationRevisionRegExp":"SNAPSHOT","fileIntegrationRevisionRegExp":"SNAPSHOT|(?:(?:[0-9]{8}.[0-9]{6})-(?:[0-9]+))"}`
	if err := w.rt.Store.Configs().Put(t.Context(), configdoc.ConfigDocument{Kind: configdoc.ConfigLayout, Key: "copy-layout", Document: json.RawMessage(pattern)}); err != nil {
		t.Fatal(err)
	}
	selectionConfig(t, w, "second", func(c *repo.Config) { c.RepoLayoutRef = "copy-layout" })
	selectionPut(t, w, "first", layoutMavenPath, "copy control", 100)
	remoteStatus(t, w.request("POST", "/artifactory/api/copy/first/"+layoutMavenPath+"?to=/second/&suppressLayouts=0", "admin", ""), 200)
	r := w.request("GET", "/artifactory/second/dest/"+layoutMavenPath, "admin", "")
	if r.Code != 200 || r.Body.String() != "copy control" {
		t.Fatal(r.Code, r.Body.String())
	}
	remoteStatus(t, w.request("POST", "/artifactory/api/move/second/dest/"+layoutMavenPath+"?to=/first/&suppressLayouts=0", "admin", ""), 200)
	remoteStatus(t, w.request("GET", "/artifactory/second/dest/"+layoutMavenPath, "admin", ""), 404)
	r = w.request("GET", "/artifactory/first/"+layoutMavenPath, "admin", "")
	if r.Code != 200 || r.Body.String() != "copy control" {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestAccMavenVirtualLayoutResolvedReadPermission(t *testing.T) {
	for _, tc := range []struct{ name, to, asked, stored, grant string }{
		{"same release", "maven-2-default", "org/acme/app/[RELEASE]/app-[RELEASE].jar", layoutMavenPath, "org/acme/app/**/app-1.0.jar"},
		{"same snapshot", "maven-2-default", "org/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", "org/acme/app/1.0-SNAPSHOT/app-1.0-20261118.120000-1.jar", "org/acme/app/**/app-1.0-20261118.120000-1.jar"},
		{"translated release", "ivy-default", "org/acme/app/[RELEASE]/app-[RELEASE].jar", layoutIvyPath, "org.acme/app/**/app-1.0.jar"},
		{"translated unique snapshot", "ivy-default", "org/acme/app/1.0-SNAPSHOT/app-1.0-20261118.120000-1.jar", "org.acme/app/1.0-SNAPSHOT/jars/app-1.0-20261118.120000-1.jar", "org.acme/app/**/app-1.0-20261118.120000-1.jar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := layoutWorld(t, "maven-2-default", tc.to)
			selectionPut(t, w, "first", tc.stored, "resolved readable bytes", 100)
			if err := w.rt.Store.Permissions().
				Create(t.Context(), accesstest.Permission{Name: "resolved-read", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"reader": accessapi.ActionRead}}, Targets: map[string]accesstest.Target{"first": {Include: []string{tc.grant}}}}}}); err != nil {
				t.Fatal(err)
			}
			r := w.request("GET", "/artifactory/virtual/"+tc.asked, "reader", "")
			if r.Code != 200 || r.Body.String() != "resolved readable bytes" {
				t.Fatal(r.Code, r.Body.String())
			}
		})
	}
}

func TestAccMavenVirtualLayoutRemoteDescriptorHeaders(t *testing.T) {
	for _, store := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream", true: "cache"}[store], func(t *testing.T) {
			w := layoutWorld(t, "ivy-default", "maven-2-default")
			selectionConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"remote"} })
			w.c.StoreArtifactsLocally = &store
			w.update()
			const asked = "org.acme/app/1.0/ivys/ivy-1.0.xml"
			const actual = "org/acme/app/1.0/app-1.0.xml"
			const body = `<ivy-module version="2.0"/>`
			mavenSource(w, actual, []byte(body), "application/xml")
			for _, method := range []string{"GET", "HEAD"} {
				r := w.request(method, "/artifactory/virtual/"+asked, "reader", "")
				if r.Code != 200 || method == "GET" && r.Body.String() != body || r.Header().Get("X-Artifactory-Filename") != "app-1.0.xml" {
					t.Fatal(method, r.Code, r.Header(), r.Body.String())
				}
			}
		})
	}
}

func TestAccMavenVirtualLayoutRefusedAttemptsRetryOriginal(t *testing.T) {
	for _, holder := range []bool{false, true} {
		for _, original := range []bool{false, true} {
			for _, readable := range []bool{false, true} {
				name := fmt.Sprintf("translated-holder=%t/original-holder=%t/original-readable=%t", holder, original, readable)
				t.Run(name, func(t *testing.T) {
					w := layoutWorld(t, "maven-2-default", "ivy-default")
					if original {
						selectionPut(t, w, "first", layoutMavenPath, "authorized original", 100)
					}
					if holder {
						selectionPut(t, w, "first", layoutIvyPath, "denied translated holder", 100)
					}
					includes := []string{layoutIvyPath + "/child"}
					if readable {
						includes = append(includes, layoutMavenPath)
					}
					if err := w.rt.Store.Permissions().
						Create(t.Context(), accesstest.Permission{Name: "separate-attempts", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"reader": accessapi.ActionRead}}, Targets: map[string]accesstest.Target{"first": {Include: includes}}}}}); err != nil {
						t.Fatal(err)
					}
					r := w.request("GET", "/artifactory/virtual/"+layoutMavenPath, "reader", "")
					want := 403
					if readable {
						want = 404
						if original {
							want = 200
						}
					}
					if r.Code != want || want == 200 && r.Body.String() != "authorized original" {
						t.Fatal(r.Code, r.Body.String(), want)
					}
					trace := w.request("GET", "/artifactory/virtual/"+layoutMavenPath+"?trace", "reader", "").Body.String()
					if !strings.Contains(trace, "retrying with the original") {
						t.Fatal(trace)
					}
					if readable && strings.Contains(trace, "Request is forbidden by") {
						t.Fatal("translated denial leaked into final member result", trace)
					}
					if !readable && !strings.Contains(r.Body.String(), "first:"+layoutMavenPath) {
						t.Fatal("wrong final denial path", r.Body.String())
					}
				})
			}
		}
	}
}

func TestAccMavenVirtualLayoutRemoteMissAndFailure(t *testing.T) {
	for _, status := range []int{404, 403, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			w := layoutWorld(t, "maven-2-default", "gradle-default")
			selectionConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"remote"} })
			w.c.RepoLayoutRef = "gradle-default"
			w.update()
			body := sourceJAR(t, "original remote bytes")
			mavenSource(w, layoutMavenPath, body, "application/java-archive")
			w.up.Set("/"+layoutGradlePath, fakeupstream.Resource{Status: status})
			r := w.request("GET", "/artifactory/virtual/"+layoutMavenPath, "reader", "")
			if r.Code != 200 || r.Body.String() != string(body) {
				t.Fatal(r.Code, r.Body.String(), w.up.Requests())
			}
			if w.up.Count("GET", "/"+layoutMavenPath) != 1 {
				t.Fatal("wrong retry classification", w.up.Requests())
			}
		})
	}
}

func TestAccMavenVirtualLayoutPropertyFallbackAndFormatSkip(t *testing.T) {
	w := layoutWorld(t, "maven-2-default", "ivy-default")
	remoteStatus(t, w.request("PUT", "/artifactory/first/"+layoutIvyPath+";tier=bronze", "admin", "translated property mismatch"), 201)
	remoteStatus(t, w.request("PUT", "/artifactory/first/"+layoutMavenPath+";tier=gold", "admin", "original property match"), 201)
	r := w.request("GET", "/artifactory/virtual/"+layoutMavenPath+";tier=gold", "admin", "")
	if r.Code != 200 || r.Body.String() != "original property match" {
		t.Fatal(r.Code, r.Body.String())
	}
	trace := w.request("GET", "/artifactory/virtual/"+layoutMavenPath+";tier=gold?trace", "admin", "").Body.String()
	if !strings.Contains(trace, "properties conflict") || !strings.Contains(trace, "retrying with the original") {
		t.Fatal(trace)
	}
	const release = "org/acme/app/[RELEASE]/app-[RELEASE].jar"
	selectionPut(t, w, "first", release, "release original trap", 100)
	selectionConfig(t, w, "first", func(c *repo.Config) { c.HandleReleases = new(false) })
	remoteStatus(t, w.request("GET", "/artifactory/virtual/"+release, "admin", ""), 404)
	trace = w.request("GET", "/artifactory/virtual/"+release+"?trace", "admin", "").Body.String()
	if !strings.Contains(trace, "Skipping first - doesn't handle releases") || strings.Contains(trace, "retrying with the original") {
		t.Fatal(trace)
	}
}
