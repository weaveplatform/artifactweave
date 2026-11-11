package dockerhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// patchRepo changes the configuration of the repository key.
func (w *blobWorld) patchRepo(key string, f func(*repo.Config)) {
	w.t.Helper()
	ctx := context.Background()
	c, err := w.db.Repos().Get(ctx, key)
	if err != nil {
		w.t.Fatal(err)
	}
	f(&c)
	if err := w.db.Repos().Update(ctx, c); err != nil {
		w.t.Fatal(err)
	}
}

// refusal is the body of a download the policy refuses: Artifactory's own
// envelope, a status and a message and no registry code.
type refusal struct {
	Errors []map[string]any `json:"errors"`
}

// expectRefusal checks that rec is the policy's 404 with one of msgs, in
// Artifactory's envelope.
func expectRefusal(t *testing.T, rec *httptest.ResponseRecorder, msgs ...string) {
	t.Helper()
	var body refusal
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json" ||
		json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Errors) != 1 {
		t.Fatalf("status %d, want the policy's 404: %s", rec.Code, rec.Body)
	}
	got := body.Errors[0]
	for _, msg := range msgs {
		if cmp.Equal(got, map[string]any{"status": float64(http.StatusNotFound), "message": msg}) {
			return
		}
	}
	t.Fatalf("refusal %v, want one of %q", got, msgs)
}

const (
	blackedOutMsg = "The repository 'docker-local' is blacked out and cannot serve artifact " +
		"'docker-local:%s'."
	excludedMsg = "The repository 'docker-local' rejected the artifact 'docker-local:%s' due " +
		"to its include/exclude pattern settings."
)

func msg(format, path string) string { return fmt.Sprintf(format, path) }

// A pull the download policy refuses answers 404 with the policy's message
// in Artifactory's envelope, under no registry code, as the Docker client
// showed it for 7.x (RTFACT-27234), GET and HEAD alike. A blacked-out
// repository refuses before the item is looked for and names the file the
// pull reads; patterns refuse the item found. A manifest or blob that is
// missing keeps its registry code.
func TestAccPullPolicy(t *testing.T) {
	w := newBlobWorld(t)
	w.uploadBlobs("team/app", imageConfig, "layer")
	body := image("layer")
	expect(t, w.do(http.MethodPut, "/v2/docker-local/team/app/manifests/v1", body,
		ociManifest), http.StatusCreated, "")
	manifestDigest, layer := digestOf(body), digestOf("layer")
	accept := map[string]string{"Accept": ociManifest["Content-Type"]}
	refused := func(target string, msgs ...string) {
		t.Helper()
		expectRefusal(t, w.do(http.MethodGet, target, "", accept), msgs...)
		if head := w.do(http.MethodHead, target, "", accept); head.Code != http.StatusNotFound {
			t.Fatalf("HEAD %s: %d", target, head.Code)
		}
	}
	const v2 = "/v2/docker-local/team/app/"

	excluded, empty, on, off := "team/**", "", true, false
	w.patchRepo("docker-local", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	refused(v2+"manifests/v1", msg(excludedMsg, "team/app/v1/manifest.json"))
	// A pull by digest checks the copy it finds, here the tag's.
	refused(v2+"manifests/"+manifestDigest, msg(excludedMsg, "team/app/v1/manifest.json"))
	// Both copies of the layer are excluded; the refusal names the one
	// found first.
	refused(v2+"blobs/"+layer,
		msg(excludedMsg, "team/app/v1/sha256__"+layer[7:]),
		msg(excludedMsg, "team/app/_uploads/sha256__"+layer[7:]))
	expect(t, w.do(http.MethodGet, v2+"manifests/v9", "", accept),
		http.StatusNotFound, CodeManifestUnknown)
	expect(t, w.do(http.MethodGet, v2+"blobs/"+digestOf("absent"), "", nil),
		http.StatusNotFound, CodeBlobUnknown)

	w.patchRepo("docker-local", func(c *repo.Config) {
		c.ExcludesPattern = &empty
		c.BlackedOut = &on
	})
	const prefixed = "/artifactory/api/docker/docker-local/v2/team/app/"
	absent := digestOf("absent")
	for target, path := range map[string]string{
		v2 + "manifests/v1":       "team/app/v1/manifest.json",
		prefixed + "manifests/v1": "team/app/v1/manifest.json",
		// Whether or not the tag exists.
		v2 + "manifests/v9":                "team/app/v9/manifest.json",
		v2 + "manifests/" + manifestDigest: "team/app/sha256:" + manifestDigest[7:] + "/manifest.json",
		v2 + "blobs/" + layer:              "team/app/_uploads/sha256__" + layer[7:],
		v2 + "blobs/" + absent:             "team/app/_uploads/sha256__" + absent[7:],
		// A reference that names no file leaves the image.
		v2 + "manifests/_uploads":        "team/app",
		v2 + "blobs/sha512:" + layer[7:]: "team/app",
	} {
		refused(target, msg(blackedOutMsg, path))
	}

	w.patchRepo("docker-local", func(c *repo.Config) { c.BlackedOut = &off })
	expect(t, w.do(http.MethodGet, v2+"manifests/v1", "", accept), http.StatusOK, "")
	expect(t, w.do(http.MethodGet, v2+"blobs/"+layer, "", nil), http.StatusOK, "")
}

// A layer that two tags share is served from a copy the repository's
// patterns accept although the other copy's folder is excluded
// (RTFACT-30134); with every copy excluded the pull is refused.
func TestAccBlobFromAnAcceptedCopy(t *testing.T) {
	w := newBlobWorld(t)
	d := digestOf("shared")
	for _, tag := range []string{"v1", "v2"} {
		w.deploy("docker-local", "team/app/"+tag+"/sha256__"+d[7:], "shared")
	}
	target := "/v2/docker-local/team/app/blobs/" + d
	for _, excluded := range []string{"team/app/v1/**", "team/app/v2/**"} {
		w.patchRepo("docker-local", func(c *repo.Config) { c.ExcludesPattern = &excluded })
		rec := w.do(http.MethodGet, target, "", nil)
		if rec.Code != http.StatusOK || rec.Body.String() != "shared" {
			t.Fatalf("%s excluded: %d %s", excluded, rec.Code, rec.Body)
		}
		expect(t, w.do(http.MethodHead, target, "", nil), http.StatusOK, "")
	}
	both := "team/app/v1/**,team/app/v2/**"
	w.patchRepo("docker-local", func(c *repo.Config) { c.ExcludesPattern = &both })
	expectRefusal(t, w.do(http.MethodGet, target, "", nil),
		msg(excludedMsg, "team/app/v1/sha256__"+d[7:]),
		msg(excludedMsg, "team/app/v2/sha256__"+d[7:]))
}

// A mount from another repository that would not serve the blob to a pull,
// blacked out or with no copy its patterns accept, opens an upload session,
// as a mount from a source the caller cannot read does. A mount within the
// repository is left to the write's own check.
func TestAccMountFromARefusingSource(t *testing.T) {
	d := digestOf("shared")
	mount := func(w *blobWorld, target, from string) *httptest.ResponseRecorder {
		return w.do(http.MethodPost, target+"blobs/uploads/?mount="+d+"&from="+from, "", nil)
	}
	excludeAll, excludeV1, on := "base/**", "base/v1/**", true
	for name, c := range map[string]struct {
		source func(*repo.Config)
		status int
	}{
		"accepted":    {func(*repo.Config) {}, http.StatusCreated},
		"blacked out": {func(c *repo.Config) { c.BlackedOut = &on }, http.StatusAccepted},
		"every copy excluded": {
			func(c *repo.Config) { c.ExcludesPattern = &excludeAll }, http.StatusAccepted,
		},
		"one copy excluded": {
			func(c *repo.Config) { c.ExcludesPattern = &excludeV1 }, http.StatusCreated,
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newBlobWorld(t)
			for _, tag := range []string{"v1", "v2"} {
				w.deploy("oci-local", "base/"+tag+"/sha256__"+d[7:], "shared")
			}
			w.patchRepo("oci-local", c.source)
			expect(t, mount(w, "/v2/docker-local/app/", "oci-local/base"), c.status, "")
			_, err := w.db.Items().Get(
				context.Background(), "docker-local", false, "app/_uploads/sha256__"+d[7:],
			)
			if mounted := err == nil; mounted != (c.status == http.StatusCreated) {
				t.Fatalf("mounted %v, err %v", mounted, err)
			}
		})
	}
	w := newBlobWorld(t)
	w.deploy("docker-local", "base/v1/sha256__"+d[7:], "shared")
	w.patchRepo("docker-local", func(c *repo.Config) { c.BlackedOut = &on })
	expect(t, mount(w, "/artifactory/api/docker/docker-local/v2/app/", "base"),
		http.StatusNotFound, CodeNameUnknown)
}

// The catalog leaves out an image whose folder its repository's include and
// exclude patterns reject, before the page is cut, and keeps one they reject
// only some tags of (RTFACT-26524).
func TestAccCatalogFollowsPatterns(t *testing.T) {
	w := newBlobWorld(t)
	for _, name := range []string{"atest", "busybox", "ctest", "team/app"} {
		w.pushTags(name, "layer-"+name, "v1", "v2")
	}
	w.deploy("oci-local", "tools/v1/manifest.json", image("x"))
	catalog := func(target string) []string {
		t.Helper()
		return list(t, w.do(http.MethodGet, target, "", nil)).Repositories
	}
	includes, excludes, tag := "b*/**,c*/**", "team/**", "team/app/v1/**"
	for _, c := range []struct {
		name               string
		patch              func(*repo.Config)
		local, registry    []string
		firstPage, nextURL string
	}{
		{
			"includes", func(c *repo.Config) { c.IncludesPattern = &includes },
			[]string{"busybox", "ctest"},
			[]string{"docker-local/busybox", "docker-local/ctest", "oci-local/tools"},
			"busybox", `</v2/docker-local/_catalog?last=busybox&n=1>; rel="next"`,
		},
		{
			"excludes", func(c *repo.Config) { c.ExcludesPattern = &excludes },
			[]string{"atest", "busybox", "ctest"},
			[]string{"docker-local/atest", "docker-local/busybox", "docker-local/ctest", "oci-local/tools"},
			"atest", `</v2/docker-local/_catalog?last=atest&n=1>; rel="next"`,
		},
		{
			"one tag excluded", func(c *repo.Config) { c.ExcludesPattern = &tag },
			[]string{"atest", "busybox", "ctest", "team/app"},
			[]string{
				"docker-local/atest", "docker-local/busybox", "docker-local/ctest",
				"docker-local/team/app", "oci-local/tools",
			},
			"atest", `</v2/docker-local/_catalog?last=atest&n=1>; rel="next"`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			w.patchRepo("docker-local", func(cfg *repo.Config) {
				cfg.IncludesPattern, cfg.ExcludesPattern = nil, nil
				c.patch(cfg)
			})
			for _, target := range []string{
				"/v2/docker-local/_catalog", "/artifactory/api/docker/docker-local/v2/_catalog",
			} {
				if diff := cmp.Diff(c.local, catalog(target)); diff != "" {
					t.Errorf("%s: %s", target, diff)
				}
			}
			if diff := cmp.Diff(c.registry, catalog("/v2/_catalog")); diff != "" {
				t.Errorf("registry-wide: %s", diff)
			}
			rec := w.do(http.MethodGet, "/v2/docker-local/_catalog?n=1", "", nil)
			if got := list(t, rec).Repositories; !slices.Equal(got, []string{c.firstPage}) ||
				rec.Header().Get("Link") != c.nextURL {
				t.Errorf("first page %v, Link %q", got, rec.Header().Get("Link"))
			}
		})
	}
}
