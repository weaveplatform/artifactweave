package docker_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	dockerhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker/http"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func virtualRegistryWorld(t *testing.T, kind repo.PackageType) *remoteWorld {
	t.Helper()
	w := registryWorld(t)
	w.c.PackageType = kind
	w.update()
	for _, c := range []repo.Config{
		{Key: "first", Class: repo.Local, PackageType: kind},
		{Key: "second", Class: repo.Local, PackageType: kind},
		{Key: "inner", Class: repo.Virtual, PackageType: kind, Repositories: []string{"first", "remote"}},
		{Key: "virtual", Class: repo.Virtual, PackageType: kind, Repositories: []string{"inner", "second", "remote"}},
	} {
		if err := w.rt.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func virtualRegistryConfig(t *testing.T, w *remoteWorld, key string, change func(*repo.Config)) {
	t.Helper()
	c, err := w.rt.Store.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	change(&c)
	if err = w.rt.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func virtualRegistryPush(t *testing.T, w *remoteWorld, key, tag, marker string) ([]byte, []byte) {
	t.Helper()
	manifest, layer := registryImage(t, w, marker)
	config := []byte(`{"os":"linux","architecture":"amd64","config":{"Labels":{"fixture":"` + marker + `"}}}`)
	for _, body := range [][]byte{config, layer} {
		r := w.request("POST", "/v2/"+key+"/team/image/blobs/uploads/?digest="+registryDigest(body), "admin", string(body))
		remoteStatus(t, r, 201)
	}
	r := w.request("PUT", "/v2/"+key+"/team/image/manifests/"+tag, "admin", string(manifest), "Content-Type", layout.MediaTypeOCIManifest)
	remoteStatus(t, r, 201)
	return manifest, layer
}

func TestAccRegistryVirtualPulls(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Docker, repo.OCI, repo.HelmOCI} {
		t.Run(string(kind), func(t *testing.T) {
			w := virtualRegistryWorld(t, kind)
			first, layer := virtualRegistryPush(t, w, "first", "local", "first")
			second, _ := virtualRegistryPush(t, w, "second", "local", "second")
			remote, remoteLayer := registryImage(t, w, "remote")
			registryPut(w, "manifests", "remote", layout.MediaTypeOCIManifest, remote)
			for _, tc := range []struct {
				kind, ref string
				body      []byte
				member    string
			}{
				{"manifests", "local", first, "first"},
				{"manifests", registryDigest(first), first, "first"},
				{"blobs", registryDigest(layer), layer, ""},
				{"manifests", "remote", remote, "remote"},
				{"manifests", registryDigest(remote), remote, "remote"},
				{"blobs", registryDigest(remoteLayer), remoteLayer, ""},
			} {
				for _, method := range []string{"GET", "HEAD"} {
					r := w.request(method, "/v2/virtual/team/image/"+tc.kind+"/"+tc.ref, "admin", "")
					remoteStatus(t, r, 200)
					if method == "GET" && !bytes.Equal(r.Body.Bytes(), tc.body) {
						t.Fatal(tc.kind, tc.ref, "wrong member bytes")
					}
					if method == "HEAD" && r.Body.Len() != 0 {
						t.Fatal("HEAD sent a body")
					}
					if r.Header().Get("Docker-Content-Digest") != registryDigest(tc.body) || r.Header().Get(dockerhttp.RegistryHeader) != tc.member {
						t.Fatal(r.Header())
					}
				}
			}
			if bytes.Equal(first, second) {
				t.Fatal("duplicate fixture did not differ")
			}
			virtualRegistryConfig(t, w, "first", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
			before := len(w.up.Requests())
			remoteStatus(t, w.request("GET", "/v2/virtual/team/image/manifests/local", "admin", ""), 200)
			if len(w.up.Requests()) != before {
				t.Fatal("priority local hit reached upstream")
			}
			r := w.request("GET", "/v2/virtual/team/image/manifests/remote", "admin", "")
			if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), remote) {
				t.Fatal("priority miss stopped remote", r.Code)
			}
		})
	}
}

func TestAccRegistryVirtualReadPoliciesAndTokens(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	if err := w.rt.Store.Users().Create(t.Context(), accesstest.User{Name: "limited", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	first, _ := virtualRegistryPush(t, w, "first", "same", "first")
	remote, _ := registryImage(t, w, "remote")
	registryPut(w, "manifests", "same", layout.MediaTypeOCIManifest, remote)
	r := w.request("GET", "/v2/virtual/team/image/manifests/same", "reader", "")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), remote) {
		t.Fatal("unreadable local did not pass over", r.Code, r.Body.String())
	}
	for _, tc := range []struct {
		name   string
		change func(*repo.Config)
	}{
		{"blackout", func(c *repo.Config) { c.BlackedOut = new(true) }},
		{"patterns", func(c *repo.Config) { c.BlackedOut = new(false); c.ExcludesPattern = new("team/**") }},
	} {
		virtualRegistryConfig(t, w, "first", tc.change)
		r = w.request("GET", "/v2/virtual/team/image/manifests/same", "admin", "")
		if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), remote) {
			t.Fatal(tc.name, r.Code, r.Body.String())
		}
	}
	r = w.request("GET", "/v2/virtual/team/image/manifests/local-only", "reader", "")
	if r.Code != 404 || !strings.Contains(r.Body.String(), "MANIFEST_UNKNOWN") {
		t.Fatal(r.Code, r.Body.String())
	}
	virtualRegistryConfig(t, w, "first", func(c *repo.Config) { c.ExcludesPattern = nil })
	token := w.request("GET", "/v2/token?scope=repository:virtual/team/image:pull", "reader", "")
	remoteStatus(t, token, 200)
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(token.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	r = w.request("GET", "/v2/virtual/team/image/manifests/same", "", "", "Authorization", "Bearer "+issued.Token)
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), remote) {
		t.Fatal("virtual-scoped token failed", r.Code, r.Body.String())
	}
	r = w.request("GET", "/v2/virtual/team/image/manifests/same", "limited", "")
	if r.Code != 403 {
		t.Fatal("unreadable virtual", r.Code, r.Body.String())
	}
	virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("team/**") })
	r = w.request("GET", "/v2/virtual/team/image/manifests/same", "admin", "")
	if r.Code != 404 || strings.Contains(r.Body.String(), string(first)) {
		t.Fatal("virtual patterns ignored for admin", r.Code, r.Body.String())
	}
}

func TestAccRegistryVirtualOtherKindsStayUnsupported(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	for _, tc := range []struct{ method, path string }{
		{"PUT", "team/image/manifests/latest"},
		{"DELETE", "team/image/manifests/latest"},
		{"POST", "team/image/blobs/uploads/"},
		{"DELETE", "team/image/blobs/sha256:" + strings.Repeat("a", 64)},
		{"GET", "team/image/tags/list"},
		{"GET", "_catalog"},
		{"GET", "team/image/referrers/sha256:" + strings.Repeat("a", 64)},
	} {
		r := w.request(tc.method, "/v2/virtual/"+tc.path, "admin", "")
		if r.Code != 405 || !strings.Contains(r.Body.String(), "virtual is a virtual repository, which the registry does not serve yet") {
			t.Fatal(tc, r.Code, r.Body.String())
		}
	}
	r := w.request("DELETE", "/artifactory/api/docker/virtual/v2/delete", "admin", `{"dockerRepository":"team/image","tag":"latest"}`, "Content-Type", "application/json")
	if r.Code != 405 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestAccRegistryVirtualDigestLocationCache(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	manifest, _ := registryImage(t, w, "remote")
	w.c.StoreArtifactsLocally = new(false)
	w.update()
	absent := *repo.Defaults(&repo.Config{Key: "absent", Class: repo.Remote, PackageType: repo.Docker, URL: w.up.URL, DockerProjectID: "absent", MissedRetrievalCachePeriodSecs: new(0), StoreArtifactsLocally: new(false)})
	body, err := json.Marshal(absent)
	if err != nil {
		t.Fatal(err)
	}
	remoteStatus(t, w.request("PUT", "/artifactory/api/repositories/absent", "admin", string(body), "Content-Type", "application/json"), 200)
	virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"absent", "remote"} })
	path := "/v2/virtual/team/image/manifests/" + registryDigest(manifest)
	missingPath := "/v2/absent/team/image/manifests/" + registryDigest(manifest)
	w.up.Set(missingPath, fakeupstream.Resource{Status: 403})
	for range 2 {
		r := w.request("GET", path, "admin", "")
		if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), manifest) {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if got := w.up.Count("GET", missingPath); got != 1 {
		t.Fatal("second digest lookup searched a non-holder", got, w.up.Requests())
	}
	virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) { c.IncludesPattern = new("team/**") })
	remoteStatus(t, w.request("GET", path, "admin", ""), 200)
	if got := w.up.Count("GET", missingPath); got != 2 {
		t.Fatal("pattern change retained location", got, w.up.Requests())
	}
	w.up.Set("/v2/team/image/manifests/"+registryDigest(manifest), fakeupstream.Resource{Status: 404})
	r := w.request("GET", path, "admin", "")
	if r.Code != 404 {
		t.Fatal("missing cached holder did not trigger a fresh walk", r.Code, r.Body.String())
	}
}

func TestAccRegistryVirtualNarrowMemberPermission(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	virtualRegistryPush(t, w, "first", "private", "first")
	if err := w.rt.Store.Users().Create(t.Context(), accesstest.User{Name: "narrow", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.rt.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "narrow", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"narrow": accessapi.ActionRead}}, Targets: map[string]accesstest.Target{"first": {Include: []string{"team/image/public/**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	direct := w.request("GET", "/v2/first/team/image/manifests/private", "narrow", "")
	if direct.Code != 403 {
		t.Fatal(direct.Code, direct.Body.String())
	}
	r := w.request("GET", "/v2/virtual/team/image/manifests/private", "narrow", "")
	if r.Code == 200 {
		t.Fatal("virtual widened a descendant-only permission", r.Body.String())
	}
}

func TestAccRegistryVirtualParentManifestLists(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	first, _ := virtualRegistryPush(t, w, "first", "child", "first")
	second, _ := virtualRegistryPush(t, w, "second", "child", "second")
	var parents []string
	for i, key := range []string{"first", "second"} {
		child := first
		if i == 1 {
			child = second
		}
		body := registryJSON(
			t,
			map[string]any{
				"schemaVersion": 2,
				"mediaType":     layout.MediaTypeOCIIndex,
				"manifests":     []layout.Descriptor{{MediaType: layout.MediaTypeOCIManifest, Digest: registryDigest(child), Size: int64(len(child))}},
			},
		)
		parents = append(parents, registryDigest(body))
		remoteStatus(t, w.request("PUT", "/v2/"+key+"/team/image/manifests/parent", "admin", string(body), "Content-Type", layout.MediaTypeOCIIndex), 201)
	}
	path := "/artifactory/api/docker/virtual/v2/parentManifests"
	body := `{"dockerRepository":"team/image","tag":"child"}`
	r := w.request("POST", path, "admin", body, "Content-Type", "application/json")
	if r.Code != 200 || !strings.Contains(r.Body.String(), parents[0]) || strings.Contains(r.Body.String(), parents[1]) {
		t.Fatal(r.Code, r.Body.String())
	}
	virtualRegistryConfig(t, w, "first", func(c *repo.Config) { c.BlackedOut = new(true) })
	r = w.request("POST", path, "admin", body, "Content-Type", "application/json")
	if r.Code != 200 || !strings.Contains(r.Body.String(), parents[1]) || strings.Contains(r.Body.String(), parents[0]) {
		t.Fatal(r.Code, r.Body.String())
	}
	r = w.request("POST", path, "reader", body, "Content-Type", "application/json")
	if r.Code != 404 {
		t.Fatal("unreadable members leaked parent lists", r.Code, r.Body.String())
	}
}

func TestAccRegistryVirtualRechecksCachedMemberAuthority(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	manifest, _ := virtualRegistryPush(t, w, "first", "private", "first")
	path := "/v2/virtual/team/image/manifests/" + registryDigest(manifest)
	remoteStatus(t, w.request("GET", path, "admin", ""), 200)
	w.up.Set("/v2/team/image/manifests/"+registryDigest(manifest), fakeupstream.Resource{Status: 404})
	r := w.request("GET", path, "reader", "")
	if r.Code != 404 || !strings.Contains(r.Body.String(), "MANIFEST_UNKNOWN") {
		t.Fatal("digest hint bypassed member permission", r.Code, r.Body.String())
	}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/v2/virtual/team/image/manifests/sha256:wrong", 404},
		{"/v2/virtual/team/image/blobs/sha256:wrong", 400},
	} {
		r = w.request("GET", tc.path, "admin", "")
		if r.Code != tc.status {
			t.Fatal(r.Code, r.Body.String())
		}
	}
}

func TestAccRegistryVirtualRemoteCacheFreshness(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	first, _ := registryImage(t, w, "first")
	registryPut(w, "manifests", "moving", layout.MediaTypeOCIManifest, first)
	path := "/v2/virtual/team/image/manifests/moving"
	r := w.request("GET", path, "reader", "")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), first) {
		t.Fatal(r.Code, r.Body.String())
	}
	before := w.up.Count("GET", "/v2/team/image/manifests/moving")
	r = w.request("GET", path, "reader", "")
	if r.Code != 200 || w.up.Count("GET", "/v2/team/image/manifests/moving") != before {
		t.Fatal("fresh tag fetched upstream", r.Code)
	}
	second, _ := registryImage(t, w, "second")
	registryPut(w, "manifests", "moving", layout.MediaTypeOCIManifest, second)
	w.c.RetrievalCachePeriodSecs = new(0)
	w.update()
	r = w.request("GET", path, "reader", "")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), second) || w.up.Count("GET", "/v2/team/image/manifests/moving") <= before {
		t.Fatal("expired tag did not use source refresh", r.Code, r.Body.String())
	}
	before = len(w.up.Requests())
	r = w.request("GET", path, "reader", "", "X-Artifactory-Originated", "another-node")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), second) || len(w.up.Requests()) != before {
		t.Fatal("origin-restricted cache read fetched", r.Code, r.Body.String())
	}
	w.c.Offline = new(true)
	w.update()
	r = w.request("GET", path, "reader", "")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), second) || len(w.up.Requests()) != before {
		t.Fatal("offline cache read fetched", r.Code, r.Body.String())
	}
}

func TestAccRegistryVirtualRemoteParentManifestLists(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	child, _ := registryImage(t, w, "remote-child")
	registryPut(w, "manifests", "child", layout.MediaTypeOCIManifest, child)
	parent := registryJSON(
		t,
		map[string]any{
			"schemaVersion": 2,
			"mediaType":     layout.MediaTypeOCIIndex,
			"manifests":     []layout.Descriptor{{MediaType: layout.MediaTypeOCIManifest, Digest: registryDigest(child), Size: int64(len(child))}},
		},
	)
	registryPut(w, "manifests", "parent", layout.MediaTypeOCIIndex, parent)
	remoteStatus(t, w.request("GET", "/v2/remote/team/image/manifests/parent", "reader", ""), 200)
	body := `{"dockerRepository":"team/image","tag":"child"}`
	r := w.request("POST", "/artifactory/api/docker/virtual/v2/parentManifests", "reader", body, "Content-Type", "application/json")
	if r.Code != 200 || !strings.Contains(r.Body.String(), registryDigest(parent)) || !strings.Contains(r.Body.String(), "parent") {
		t.Fatal(r.Code, r.Body.String())
	}
}
