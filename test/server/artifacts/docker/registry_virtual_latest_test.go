package docker_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func virtualTagModified(t *testing.T, w *remoteWorld, key, tag string, at time.Time) {
	t.Helper()
	it, err := w.rt.Store.Items().Get(t.Context(), key, false, "team/image/"+tag+"/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	it.Modified = at
	if _, err = w.rt.Store.Items().Put(t.Context(), it); err != nil {
		t.Fatal(err)
	}
}

func TestAccRegistryVirtualTimestamp(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Docker, repo.OCI, repo.HelmOCI} {
		t.Run(string(kind), func(t *testing.T) {
			w := virtualRegistryWorld(t, kind)
			w.c.MissedRetrievalCachePeriodSecs = new(0)
			w.update()
			first, _ := virtualRegistryPush(t, w, "first", "latest", "first")
			second, _ := virtualRegistryPush(t, w, "second", "latest", "second")
			at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
			virtualTagModified(t, w, "first", "latest", at)
			virtualTagModified(t, w, "second", "latest", at.Add(time.Hour))
			check := func(ref string, want []byte) {
				t.Helper()
				for _, method := range []string{"GET", "HEAD"} {
					got := w.request(method, "/v2/virtual/team/image/manifests/"+ref, "admin", "")
					remoteStatus(t, got, 200)
					if got.Header().Get("Docker-Content-Digest") != registryDigest(want) || method == "GET" && !bytes.Equal(got.Body.Bytes(), want) {
						t.Fatal(method, got.Header(), got.Body.String())
					}
				}
			}
			check("latest", first)
			virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) { c.ResolveDockerTagsByTimestamp = new(true) })
			check("latest", second)
			check(registryDigest(first), first)
			virtualTagModified(t, w, "first", "latest", at.Add(time.Hour))
			check("latest", first)
			virtualRegistryConfig(t, w, "second", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true); c.Class = repo.Federated })
			check("latest", second)
			virtualRegistryConfig(t, w, "second", func(c *repo.Config) { c.ExcludesPattern = new("team/**") })
			check("latest", first)
			// reader is admitted by the remote, but neither local may disclose a tag.
			remoteStatus(t, w.request("GET", "/v2/virtual/team/image/manifests/latest", "reader", ""), 404)
			virtualRegistryConfig(t, w, "first", func(c *repo.Config) { c.BlackedOut = new(true) })
			remoteManifest, _ := registryImage(t, w, "remote-only")
			registryPut(w, "manifests", "latest", layout.MediaTypeOCIManifest, remoteManifest)
			check("latest", remoteManifest)
		})
	}
}

func TestAccRegistryVirtualTimestampRemoteErrors(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			w := virtualRegistryWorld(t, repo.Docker)
			other := fakeupstream.New(t, fakeupstream.Options{})
			other.Set("/v2/team/image/manifests/latest", fakeupstream.Resource{Status: status, Body: []byte("remote-owned refusal")})
			bad := *repo.Defaults(&repo.Config{Key: "bad", Class: repo.Remote, PackageType: repo.Docker, URL: other.URL, PriorityResolution: repo.Flex(true)})
			if err := w.rt.Store.Repos().Create(t.Context(), bad); err != nil {
				t.Fatal(err)
			}
			virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) {
				c.Repositories = []string{"bad", "remote"}
				c.ResolveDockerTagsByTimestamp = new(true)
			})
			manifest, _ := registryImage(t, w, "fallback")
			registryPut(w, "manifests", "latest", layout.MediaTypeOCIManifest, manifest)
			got := w.request("GET", "/v2/virtual/team/image/manifests/latest", "admin", "")
			remoteStatus(t, got, 200)
			if !bytes.Equal(got.Body.Bytes(), manifest) {
				t.Fatal("wrong remote")
			}
		})
	}
}

func virtualReferrerPush(t *testing.T, w *remoteWorld, key, tag, subject, kind string) []byte {
	t.Helper()
	body, _ := virtualRegistryPush(t, w, key, tag, key+tag)
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	m["subject"] = layout.Descriptor{MediaType: layout.MediaTypeOCIManifest, Digest: subject, Size: 1}
	m["artifactType"] = kind
	m["annotations"] = map[string]string{"origin": key}
	body = registryJSON(t, m)
	remoteStatus(t, w.request("PUT", "/v2/"+key+"/team/image/manifests/"+tag, "admin", string(body), "Content-Type", layout.MediaTypeOCIManifest), 201)
	return body
}

func TestAccRegistryVirtualReferrers(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Docker, repo.OCI, repo.HelmOCI} {
		t.Run(string(kind), func(t *testing.T) {
			w := virtualRegistryWorld(t, kind)
			subject := registryDigest([]byte("subject"))
			sig := virtualReferrerPush(t, w, "first", "sig", subject, "application/vnd.example.sig")
			sbom := virtualReferrerPush(t, w, "second", "sbom", subject, "application/vnd.example.sbom")
			// Duplicate digest in a second member is returned once; existing blobs stay valid.
			for _, blob := range []string{"firstsig"} {
				virtualRegistryPush(t, w, "second", "duplicate-blobs", blob)
			}
			remoteStatus(t, w.request("PUT", "/v2/second/team/image/manifests/duplicate", "admin", string(sig), "Content-Type", layout.MediaTypeOCIManifest), 201)
			read := func(key, as, query string) []layout.Descriptor {
				t.Helper()
				r := w.request("GET", "/v2/"+key+"/team/image/referrers/"+subject+query, as, "")
				remoteStatus(t, r, 200)
				var m struct {
					Manifests []layout.Descriptor `json:"manifests"`
				}
				if err := json.Unmarshal(r.Body.Bytes(), &m); err != nil {
					t.Fatal(err)
				}
				if query != "" && r.Header().Get("OCI-Filters-Applied") != "artifactType" {
					t.Fatal(r.Header())
				}
				return m.Manifests
			}
			refs := read("virtual", "admin", "")
			if len(refs) != 2 || refs[0].Digest != registryDigest(sig) || refs[1].Digest != registryDigest(sbom) || refs[0].Annotations["origin"] != "first" {
				t.Fatal(refs)
			}
			if refs = read("virtual", "admin", "?artifactType=application/vnd.example.sbom"); len(refs) != 1 || refs[0].Digest != registryDigest(sbom) {
				t.Fatal(refs)
			}
			if refs = read("first", "admin", ""); len(refs) != 1 || refs[0].Digest != registryDigest(sig) {
				t.Fatal(refs)
			}
			// A remote-only reader sees an empty union, even after a remote referrer cache is primed.
			registryPut(
				w,
				"referrers",
				subject,
				layout.MediaTypeOCIIndex,
				registryJSON(t, map[string]any{"schemaVersion": 2, "mediaType": layout.MediaTypeOCIIndex, "manifests": refs}),
			)
			remoteStatus(t, w.request("GET", "/v2/remote/team/image/referrers/"+subject, "reader", ""), 200)
			if refs = read("virtual", "reader", ""); len(refs) != 0 {
				t.Fatal("unreadable local disclosure", refs)
			}
			if err := w.rt.Store.Permissions().
				Create(t.Context(), accesstest.Permission{Name: "first-referrers", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"reader": accessapi.ActionRead}}, Targets: map[string]accesstest.Target{"first": {Include: []string{"team/image/**"}}}}}}); err != nil {
				t.Fatal(err)
			}
			if refs = read("virtual", "reader", ""); len(refs) != 1 || refs[0].Digest != registryDigest(sig) {
				t.Fatal(refs)
			}
			before := len(w.up.Requests())
			if err := w.rt.Store.Users().Create(t.Context(), accesstest.User{Name: "none", PasswordHash: support.CheapHash}); err != nil {
				t.Fatal(err)
			}
			// An authenticated user with no image Read is refused before any member read.
			r := w.request("GET", "/v2/virtual/team/image/referrers/"+subject, "none", "")
			if r.Code != 403 {
				t.Fatal(r.Code, r.Body.String())
			}
			if len(w.up.Requests()) != before {
				t.Fatal("referrers reached origin")
			}
			r = w.request("GET", "/v2/virtual/team/image/referrers/sha256:"+strings.Repeat("a", 64), "admin", "")
			remoteStatus(t, r, 200)
			if !strings.Contains(r.Body.String(), `"manifests":[]`) {
				t.Fatal(r.Body.String())
			}
		})
	}
}
