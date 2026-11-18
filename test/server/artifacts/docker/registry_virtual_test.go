package docker_test

import (
	"bytes"
	"encoding/json"
	"net/url"
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
	r := w.request("DELETE", "/artifactory/api/docker/virtual/v2/delete", "admin", `{"dockerRepository":"team/image","tag":"latest"}`, "Content-Type", "application/json")
	if r.Code != 400 || !strings.Contains(r.Body.String(), "Unable to delete a manifest from a virtual repository") {
		t.Fatal(r.Code, r.Body.String())
	}
}

func virtualPushWorld(t *testing.T, kind repo.PackageType) *remoteWorld {
	t.Helper()
	w := virtualRegistryWorld(t, kind)
	virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) { c.DefaultDeploymentRepo = "second" })
	return w
}

func virtualCheckDDR(t *testing.T, w *remoteWorld, manifest []byte) {
	t.Helper()
	m, err := layout.ParseManifest(layout.MediaTypeOCIManifest, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range m.BlobDescriptors() {
		hex, _ := layout.SHA256Hex(d.Digest)
		if _, err := layout.FindBlob(t.Context(), w.rt.Store.Items(), "second", "team/image", hex); err != nil {
			t.Fatal("DDR blob absent", d.Digest, err)
		}
	}
}

func TestAccRegistryVirtualPushUploads(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Docker, repo.OCI, repo.HelmOCI} {
		t.Run(string(kind), func(t *testing.T) {
			w := virtualPushWorld(t, kind)
			manifest, layer := registryImage(t, w, "push")
			var parsed layout.Manifest
			if err := json.Unmarshal(manifest, &parsed); err != nil {
				t.Fatal(err)
			}
			config := `{"os":"linux","architecture":"amd64","config":{"Labels":{"fixture":"push"}}}`
			r := w.request("POST", "/v2/virtual/team/image/blobs/uploads/?digest="+parsed.Config.Digest, "admin", config)
			remoteStatus(t, r, 201)
			if !strings.Contains(r.Header().Get("Location"), "/v2/virtual/team/image/blobs/") {
				t.Fatal(r.Header())
			}
			r = w.request("POST", "/v2/virtual/team/image/blobs/uploads/", "admin", "")
			remoteStatus(t, r, 202)
			u, err := url.Parse(r.Header().Get("Location"))
			if err != nil || !strings.HasPrefix(u.Path, "/v2/virtual/") {
				t.Fatal(r.Header(), err)
			}
			remoteStatus(t, w.request("GET", u.Path, "admin", ""), 204)
			r = w.request("PATCH", u.Path, "admin", string(layer[:5]), "Content-Range", "0-4")
			remoteStatus(t, r, 202)
			if !strings.Contains(r.Header().Get("Location"), "/v2/virtual/") {
				t.Fatal(r.Header())
			}
			remoteStatus(t, w.request("PUT", u.Path+"?digest="+registryDigest(layer), "admin", string(layer[5:])), 201)
			remoteStatus(t, w.request("HEAD", "/v2/virtual/team/image/manifests/new", "admin", ""), 404)
			r = w.request("PUT", "/v2/virtual/team/image/manifests/new", "admin", string(manifest), "Content-Type", layout.MediaTypeOCIManifest)
			remoteStatus(t, r, 201)
			if !strings.Contains(r.Header().Get("Location"), "/v2/virtual/team/image/manifests/") {
				t.Fatal(r.Header())
			}
			virtualCheckDDR(t, w, manifest)
			for _, key := range []string{"virtual", "second"} {
				r = w.request("GET", "/v2/"+key+"/team/image/manifests/new", "admin", "")
				if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), manifest) || r.Header().Get(dockerhttp.RegistryHeader) != "second" {
					t.Fatal(key, r.Code, r.Header(), r.Body.String())
				}
			}
			r = w.request("HEAD", "/v2/virtual/team/image/manifests/new", "admin", "")
			if r.Code != 200 || r.Body.Len() != 0 || r.Header().Get(dockerhttp.RegistryHeader) != "second" {
				t.Fatal("post-push HEAD did not name DDR", r.Code, r.Header(), r.Body.String())
			}
			r = w.request("POST", "/v2/virtual/team/image/blobs/uploads/", "admin", "")
			remoteStatus(t, r, 202)
			id := r.Header().Get("Docker-Upload-UUID")
			remoteStatus(t, w.request("DELETE", "/v2/virtual/team/image/blobs/uploads/"+id, "admin", ""), 204)
			remoteStatus(t, w.request("GET", "/v2/virtual/team/image/blobs/uploads/"+id, "admin", ""), 404)
		})
	}
}

func TestAccRegistryVirtualPushImports(t *testing.T) {
	for _, mode := range []string{"local", "federated DDR", "remote cache", "remote uncached", "remote streaming"} {
		t.Run(mode, func(t *testing.T) {
			w := virtualPushWorld(t, repo.Docker)
			var manifest, layer []byte
			if mode == "federated DDR" {
				virtualRegistryConfig(t, w, "second", func(c *repo.Config) { c.Class = repo.Federated })
			}
			if mode == "local" || mode == "federated DDR" {
				manifest, layer = virtualRegistryPush(t, w, "first", "base", "base")
			} else {
				manifest, layer = registryImage(t, w, "base")
				if mode == "remote streaming" {
					w.c.StoreArtifactsLocally = new(false)
					w.update()
				}
				if mode == "remote cache" {
					var m layout.Manifest
					if err := json.Unmarshal(manifest, &m); err != nil {
						t.Fatal(err)
					}
					for _, d := range m.BlobDescriptors() {
						remoteStatus(t, w.request("HEAD", "/v2/virtual/team/image/blobs/"+d.Digest, "admin", ""), 200)
					}
					w.c.Offline = new(true)
					w.update()
				}
			}
			before := w.up.Count("GET", "/v2/team/image/blobs/"+registryDigest(layer))
			r := w.request("PUT", "/v2/virtual/team/image/manifests/derived", "admin", string(manifest), "Content-Type", layout.MediaTypeOCIManifest)
			remoteStatus(t, r, 201)
			virtualCheckDDR(t, w, manifest)
			if mode == "remote cache" && w.up.Count("GET", "/v2/team/image/blobs/"+registryDigest(layer)) != before {
				t.Fatal("cached base fetched again")
			}
			remoteStatus(t, w.request("GET", "/v2/second/team/image/blobs/"+registryDigest(layer), "admin", ""), 200)
		})
	}
}

func TestAccRegistryVirtualPushIndexImportsChildren(t *testing.T) {
	for _, source := range []string{"first", "remote"} {
		t.Run(source, func(t *testing.T) {
			w := virtualPushWorld(t, repo.OCI)
			child, _ := registryImage(t, w, "child")
			if source == "first" {
				child, _ = virtualRegistryPush(t, w, "first", "child", "child")
			}
			remoteStatus(t, w.request("HEAD", "/v2/virtual/team/image/manifests/"+registryDigest(child), "admin", ""), 200)
			index := registryJSON(
				t,
				map[string]any{
					"schemaVersion": 2,
					"mediaType":     layout.MediaTypeOCIIndex,
					"manifests":     []layout.Descriptor{{MediaType: layout.MediaTypeOCIManifest, Digest: registryDigest(child), Size: int64(len(child))}},
				},
			)
			r := w.request("PUT", "/v2/virtual/team/image/manifests/multi", "admin", string(index), "Content-Type", layout.MediaTypeOCIIndex)
			remoteStatus(t, r, 201)
			virtualCheckDDR(t, w, child)
			remoteStatus(t, w.request("GET", "/v2/second/team/image/manifests/"+registryDigest(child), "admin", ""), 200)
			remoteStatus(t, w.request("HEAD", "/v2/virtual/team/image/manifests/multi", "admin", ""), 200)
		})
	}
}

func TestAccRegistryVirtualPushPolicies(t *testing.T) {
	w := virtualPushWorld(t, repo.Docker)
	manifest, _ := registryImage(t, w, "patterns")
	for _, key := range []string{"virtual", "second"} {
		virtualRegistryConfig(t, w, key, func(c *repo.Config) { c.IncludesPattern = new("team/image") })
	}
	// The internal upload/manifest filenames fail this exact image pattern.
	virtualRegistryPush(t, w, "virtual", "allowed", "patterns")
	virtualCheckDDR(t, w, manifest)
	r := w.request("PUT", "/artifactory/second/team/image/v1/manifest.json", "admin", "ordinary REST deployment")
	if r.Code != 409 {
		t.Fatal("REST item deploy bypassed the stored path policy", r.Code, r.Body.String())
	}
	for _, key := range []string{"virtual", "second"} {
		c, err := w.rt.Store.Repos().Get(t.Context(), key)
		if err != nil || c.IncludesPattern == nil || *c.IncludesPattern != "team/image" {
			t.Fatal("configuration changed", c, err)
		}
		virtualRegistryConfig(t, w, key, func(c *repo.Config) { c.IncludesPattern = new("other") })
		r := w.request("POST", "/v2/virtual/team/image/blobs/uploads/", "admin", "")
		if r.Code != 409 || !strings.Contains(r.Body.String(), key+":team/image") || strings.Contains(r.Body.String(), "_uploads") {
			t.Fatal(key, r.Code, r.Body.String())
		}
		virtualRegistryConfig(t, w, key, func(c *repo.Config) { c.IncludesPattern = nil })
	}
}

func TestAccRegistryVirtualPushNoDDRAndDelete(t *testing.T) {
	w := virtualPushWorld(t, repo.Docker)
	manifest, layer := virtualRegistryPush(t, w, "first", "keep", "keep")
	for _, target := range []string{"", "missing", "remote", "inner"} {
		virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) { c.DefaultDeploymentRepo = target })
		for _, operation := range []struct{ method, path string }{{"POST", "blobs/uploads/"}, {"PUT", "manifests/new"}, {"GET", "blobs/uploads/abc"}} {
			r := w.request(operation.method, "/v2/virtual/team/image/"+operation.path, "admin", "")
			if r.Code != 405 || r.Header().Get("Allow") != "GET" || !strings.Contains(r.Body.String(), `"code":"UNSUPPORTED"`) ||
				!strings.Contains(r.Body.String(), "No local repository was configured as local deployment repository for the (virtual) virtual repository.") {
				t.Fatal(target, operation, r.Code, r.Header(), r.Body.String())
			}
		}
	}
	for _, path := range []string{"manifests/keep", "manifests/" + registryDigest(manifest), "blobs/" + registryDigest(layer)} {
		r := w.request("DELETE", "/v2/virtual/team/image/"+path, "admin", "")
		if r.Code != 400 || r.Body.Len() != 0 {
			t.Fatal(path, r.Code, r.Body.String())
		}
	}
	remoteStatus(t, w.request("HEAD", "/v2/first/team/image/manifests/keep", "admin", ""), 200)
}

func TestAccRegistryVirtualMounts(t *testing.T) {
	for _, source := range []string{"first", "remote cache", "remote streaming"} {
		t.Run(source, func(t *testing.T) {
			w := virtualPushWorld(t, repo.Docker)
			_, layer := registryImage(t, w, "mount")
			if source == "first" {
				_, layer = virtualRegistryPush(t, w, "first", "base", "mount")
			} else if source == "remote streaming" {
				w.c.StoreArtifactsLocally = new(false)
				w.update()
			} else {
				remoteStatus(t, w.request("HEAD", "/v2/virtual/team/image/blobs/"+registryDigest(layer), "admin", ""), 200)
				w.c.Offline = new(true)
				w.update()
			}
			for _, dest := range []string{"second", "virtual"} {
				path := "/v2/" + dest + "/team/" + dest + "/blobs/uploads/?mount=" + registryDigest(layer) + "&from=virtual/team/image"
				r := w.request("POST", path, "admin", "")
				remoteStatus(t, r, 201)
				if !strings.Contains(r.Header().Get("Location"), "/v2/"+dest+"/") {
					t.Fatal(r.Header())
				}
				remoteStatus(t, w.request("GET", "/v2/second/team/"+dest+"/blobs/"+registryDigest(layer), "admin", ""), 200)
			}
		})
	}
}

func TestAccRegistryVirtualPushOnlyPermissions(t *testing.T) {
	w := virtualPushWorld(t, repo.Docker)
	manifest, layer := virtualRegistryPush(t, w, "first", "hidden", "hidden")
	remoteStatus(t, w.request("HEAD", "/v2/virtual/team/image/manifests/hidden", "reader", ""), 404)
	if err := w.rt.Store.Users().Create(t.Context(), accesstest.User{Name: "pusher", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.rt.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "push-ddr", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"pusher": accessapi.ActionWrite}}, Targets: map[string]accesstest.Target{"second": {Include: []string{"team/**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	token := w.request("GET", "/v2/token?scope=repository:virtual/team/image:pull,push,delete", "pusher", "")
	remoteStatus(t, token, 200)
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(token.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	verified, err := w.rt.Tokens.Verify(t.Context(), issued.Token)
	if err != nil || verified.Registry == nil || len(verified.Registry.Grants) != 1 || strings.Join(verified.Registry.Grants[0].Actions, ",") != "push" ||
		verified.Registry.Grants[0].Name != "virtual/team/image" {
		t.Fatal(verified, err)
	}
	for _, auth := range []struct{ user, token string }{{"pusher", ""}, {"", issued.Token}} {
		headers := []string{}
		if auth.token != "" {
			headers = []string{"Authorization", "Bearer " + auth.token}
		}
		for _, ref := range []string{"manifests/hidden", "manifests/new", "blobs/" + registryDigest(layer)} {
			r := w.request("HEAD", "/v2/virtual/team/image/"+ref, auth.user, "", headers...)
			remoteStatus(t, r, 404)
			remoteStatus(t, w.request("GET", "/v2/virtual/team/image/"+ref, auth.user, "", headers...), 403)
		}
		remoteStatus(t, w.request("POST", "/v2/virtual/team/image/blobs/uploads/?digest="+registryDigest(layer), auth.user, string(layer), headers...), 201)
	}
	// Read-ineligible source config is not copied just because its digest exists.
	r := w.request("PUT", "/v2/virtual/team/image/manifests/new", "pusher", string(manifest), "Content-Type", layout.MediaTypeOCIManifest)
	if r.Code != 400 || !strings.Contains(r.Body.String(), "MANIFEST_BLOB_UNKNOWN") {
		t.Fatal(r.Code, r.Body.String())
	}
	config := `{"os":"linux","architecture":"amd64","config":{"Labels":{"fixture":"hidden"}}}`
	remoteStatus(t, w.request("POST", "/v2/virtual/team/image/blobs/uploads/?digest="+registryDigest([]byte(config)), "", config, "Authorization", "Bearer "+issued.Token), 201)
	remoteStatus(
		t,
		w.request("PUT", "/v2/virtual/team/image/manifests/new", "", string(manifest), "Authorization", "Bearer "+issued.Token, "Content-Type", layout.MediaTypeOCIManifest),
		201,
	)
	virtualCheckDDR(t, w, manifest)
	remoteStatus(t, w.request("GET", "/v2/virtual/team/image/manifests/new", "", "", "Authorization", "Bearer "+issued.Token), 403)
	other, _ := virtualRegistryPush(t, w, "virtual", "other", "other")
	remoteStatus(t, w.request("PUT", "/v2/virtual/team/image/manifests/new", "pusher", string(other), "Content-Type", layout.MediaTypeOCIManifest), 403)
	r = w.request("HEAD", "/v2/second/team/image/manifests/new", "admin", "")
	if r.Code != 200 || r.Header().Get("Docker-Content-Digest") != registryDigest(manifest) {
		t.Fatal("push-only overwrite changed the manifest", r.Code, r.Header())
	}
	remoteStatus(t, w.request("POST", "/v2/second/team/image/blobs/uploads/?digest="+registryDigest(layer), "pusher", string(layer)), 201)
	remoteStatus(t, w.request("POST", "/v2/virtual/team/image/blobs/uploads/", "reader", ""), 403)
	remoteStatus(t, w.request("POST", "/v2/second/team/image/blobs/uploads/", "", "", "Authorization", "Bearer "+issued.Token), 401)
}

func TestAccRegistryVirtualPushImportPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*repo.Config)
	}{
		{"offline", func(c *repo.Config) { c.Offline = new(true) }},
		{"blackout", func(c *repo.Config) { c.BlackedOut = new(true) }},
		{"patterns", func(c *repo.Config) { c.ExcludesPattern = new("team/**") }},
		{"upstream refusal", func(*repo.Config) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := virtualPushWorld(t, repo.Docker)
			manifest, layer := registryImage(t, w, "refused")
			tc.change(&w.c)
			w.update()
			if tc.name == "upstream refusal" {
				w.up.Set("/v2/team/image/blobs/"+registryDigest(layer), fakeupstream.Resource{Status: 403})
			}
			r := w.request("PUT", "/v2/virtual/team/image/manifests/new", "admin", string(manifest), "Content-Type", layout.MediaTypeOCIManifest)
			if r.Code != 400 || !strings.Contains(r.Body.String(), "MANIFEST_BLOB_UNKNOWN") || !strings.Contains(r.Body.String(), registryDigest(layer)) {
				t.Fatal(r.Code, r.Body.String())
			}
			remoteStatus(t, w.request("HEAD", "/v2/second/team/image/manifests/new", "admin", ""), 404)
			if tc.name != "upstream refusal" && len(w.up.Requests()) != 0 {
				t.Fatal("refused source used network", w.up.Requests())
			}
		})
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
