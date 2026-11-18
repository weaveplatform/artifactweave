package terraformhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

func TestAccVirtualProviderMissingPlatformHintsAndMirrorFallback(t *testing.T) {
	f := newProviderFixture(t)
	virtualMembers(t, f.fixture, "remote")
	f.json("/registry/providers/hashicorp/null/versions", map[string]any{"versions": []remoteProviderVersion{{Version: "1.0.0", Protocols: []string{"5.0"}}}})
	response := f.get("reader", virtualProviderBase+"versions")
	expect(t, response, 200)
	var doc struct {
		Versions []remoteProviderVersion `json:"versions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Versions) != 1 || len(doc.Versions[0].Platforms) != 0 {
		t.Fatal("invented unobserved platforms", doc)
	}
	host, _ := url.Parse(f.up.URL)
	mirror := f.get("reader", prefix+"virtual/providers/"+host.Host+"/hashicorp/null/1.0.0.json")
	expect(t, mirror, 200)
	archives := decode(t, mirror.Body.Bytes())["archives"].(map[string]any)
	if len(archives) != 1 || archives["linux_amd64"] == nil {
		t.Fatal(archives)
	}
	missing := f.up.Count("GET", "/registry/providers/hashicorp/null/1.0.0/download/darwin/arm64")
	if missing != 1 {
		t.Fatal("missing platform was not tested exactly once", missing)
	}
}

func TestAccVirtualProviderPlatformDiscoveryIsBounded(t *testing.T) {
	f := newProviderFixture(t)
	virtualMembers(t, f.fixture, "remote")
	versions := []remoteProviderVersion{}
	for i := 1; i <= 19; i++ {
		versions = append(versions, remoteProviderVersion{Version: fmt.Sprintf("1.%d.0", i), Protocols: []string{"5.0"}})
	}
	f.json("/registry/providers/hashicorp/null/versions", map[string]any{"versions": versions})
	response := f.get("reader", virtualProviderBase+"versions")
	expect(t, response, 404)
	lookups := 0
	for _, request := range f.up.Requests() {
		if strings.Contains(request.Path, "/download/") {
			lookups++
		}
		if strings.HasPrefix(request.Path, "/storage/") {
			t.Fatal("discovery prefetched archive bytes")
		}
	}
	if lookups != moduleDiscoveryLimit {
		t.Fatalf("got %d platform reads; want existing bound %d", lookups, moduleDiscoveryLimit)
	}
}

func TestAccVirtualProviderMissingMirrorResourcesAndFilePolicy(t *testing.T) {
	for _, mode := range []string{"missing-index", "missing-version", "file-policy", "storage-fault"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := virtualProviderFixture(t)
			patchRepo(t, f.fixture, "remote", func(c *repo.Config) { c.TerraformRegistryURL = f.up.URL + "/smart" })
			root := "/smart/providers/private.example/hashicorp/null/"
			if mode != "missing-index" {
				f.json(root+"index.json", map[string]any{"versions": map[string]any{"1.0.0": map[string]any{}, "9.0.0": map[string]any{}}})
			}
			if mode == "file-policy" {
				f.json(
					root+"1.0.0.json",
					map[string]any{
						"archives": map[string]remoteMirrorArchive{"linux_amd64": {URL: "/storage/provider.zip", Hashes: []string{"zh:" + f.metadata["shasum"].(string)}}},
					},
				)
				patchRepo(t, f.fixture, "remote", func(c *repo.Config) { c.ExcludesPattern = new("storage/**") })
			}
			if mode == "storage-fault" {
				f.svc.HTTP = providerRotationBackend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
					return source.Result{}, errors.Join(format.ErrStorageFault, errors.New("metadata store failed"))
				})
			}
			response := f.get("admin", prefix+"virtual/providers/private.example/hashicorp/null/index.json")
			if mode == "storage-fault" {
				expect(t, response, 500)
				return
			}
			expect(t, response, 200)
			versions := decode(t, response.Body.Bytes())["versions"].(map[string]any)
			if len(versions) != 1 || versions["2.0.0"] == nil {
				t.Fatal("missing/refused mirror shadowed a hosted contribution", versions)
			}
		})
	}
}

func TestAccVirtualProviderLateOwnerRefusalSelectsSurvivingDuplicate(t *testing.T) {
	f, _ := virtualProviderFixture(t)
	f.json(
		"/registry/providers/hashicorp/null/versions",
		map[string]any{"versions": []remoteProviderVersion{{Version: "2.0.0", Protocols: []string{"5.0"}, Platforms: []remotePlatform{{OS: "darwin", Arch: "arm64"}}}}},
	)
	backend := f.svc.HTTP
	var once sync.Once
	f.svc.HTTP = providerRotationBackend(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
		once.Do(func() {
			patchRepo(t, f.fixture, "provider", func(c *repo.Config) { c.TerraformType = repo.TerraformModule })
		})
		return backend.Read(ctx, q, p)
	})
	response := f.get("admin", virtualProviderBase+"versions")
	expect(t, response, 200)
	var doc struct {
		Versions []remoteProviderVersion `json:"versions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Versions) != 1 || len(doc.Versions[0].Platforms) != 1 || doc.Versions[0].Platforms[0].OS != "darwin" || doc.Versions[0].Protocols[0] != "5.0" {
		t.Fatal("late owner survived or duplicate record mixed", doc)
	}
}

func TestAccVirtualProviderOnlyRetainedSourcesFenceListing(t *testing.T) {
	for _, mode := range []string{"retained-refusal", "retained-fault", "unused-fault"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := virtualProviderFixture(t)
			if mode == "unused-fault" {
				f.json(
					"/registry/providers/hashicorp/null/versions",
					map[string]any{"versions": []remoteProviderVersion{{Version: "2.0.0", Protocols: []string{"5.0"}, Platforms: []remotePlatform{{OS: "linux", Arch: "amd64"}}}}},
				)
			}
			backend := f.svc.HTTP
			calls := 0
			f.svc.HTTP = providerRotationBackend(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
				result, err := backend.Read(ctx, q, p)
				if err == nil && strings.HasSuffix(q.Resource.Path, "/versions") {
					previous := result.Revalidate
					result.Revalidate = func(ctx context.Context) error {
						calls++
						if calls > 1 {
							if mode == "retained-refusal" {
								return source.ErrDenied
							}
							return errors.Join(format.ErrStorageFault, errors.New("late metadata storage fault"))
						}
						if previous != nil {
							return previous(ctx)
						}
						return nil
					}
				}
				return result, err
			})
			response := f.get("admin", virtualProviderBase+"versions")
			if mode == "retained-fault" {
				expect(t, response, 500)
				return
			}
			expect(t, response, 200)
			if strings.Contains(response.Body.String(), `"1.0.0"`) {
				t.Fatal("late refused owner remained visible", response.Body.String())
			}
			if !strings.Contains(response.Body.String(), `"2.0.0"`) {
				t.Fatal("hosted survivor disappeared", response.Body.String())
			}
			if mode == "unused-fault" && calls != 1 {
				t.Fatal("unused losing source reached delivery fence", calls)
			}
		})
	}
}

func TestAccVirtualProviderManifestVisibilityKeepsMetadataConsistent(t *testing.T) {
	for _, scope := range []string{"virtual", "provider"} {
		t.Run(scope, func(t *testing.T) {
			f, _ := virtualProviderFixture(t)
			virtualMembers(t, f.fixture, "provider")
			patchRepo(t, f.fixture, scope, func(c *repo.Config) { c.ExcludesPattern = new("**/*_manifest.json") })
			versions := f.get("admin", virtualProviderBase+"versions")
			expect(t, versions, 200)
			var listed struct {
				Versions []remoteProviderVersion `json:"versions"`
			}
			if err := json.Unmarshal(versions.Body.Bytes(), &listed); err != nil {
				t.Fatal(err)
			}
			if len(listed.Versions) != 1 || listed.Versions[0].Protocols[0] != "5.0" {
				t.Fatal(listed)
			}
			metadata := f.get("admin", virtualProviderBase+"2.0.0/download/linux/amd64")
			expect(t, metadata, 200)
			var selected struct {
				Protocols []string `json:"protocols"`
			}
			if err := json.Unmarshal(metadata.Body.Bytes(), &selected); err != nil {
				t.Fatal(err)
			}
			if len(selected.Protocols) != 1 || selected.Protocols[0] != "5.0" {
				t.Fatal("excluded manifest metadata escaped through descriptor", selected)
			}
			direct := f.get("admin", prefix+"provider/v1/providers/hashicorp/null/2.0.0/download/linux/amd64")
			expect(t, direct, 200)
			if !strings.Contains(direct.Body.String(), `"6.0"`) {
				t.Fatal("changed existing direct lookup semantics", direct.Body.String())
			}
		})
	}
}

func TestAccVirtualProviderRemoteAdmissionPrecedesNetwork(t *testing.T) {
	for _, reason := range []string{"read", "metadata-policy"} {
		t.Run(reason, func(t *testing.T) {
			f := newProviderFixture(t)
			virtualMembers(t, f.fixture, "remote")
			if reason == "read" {
				permission, err := f.db.Permissions().Get(t.Context(), "remote-read")
				if err != nil {
					t.Fatal(err)
				}
				resource := permission.Resources[api.ResourceArtifact]
				resource.Targets["remote"] = accesstest.Target{Include: []string{"storage/**"}}
				permission.Resources[api.ResourceArtifact] = resource
				if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
					t.Fatal(err)
				}
			} else {
				patchRepo(t, f.fixture, "remote", func(c *repo.Config) { c.ExcludesPattern = new(".terraform/**") })
			}
			for _, path := range []string{"versions", "1.0.0/download/linux/amd64"} {
				expect(t, f.get("reader", virtualProviderBase+path), 404)
			}
			if len(f.up.Requests()) != 0 {
				t.Fatal("refused member reached upstream", f.up.Requests())
			}
		})
	}
}

func TestAccVirtualProviderMissingAnnouncedPlatformIsNotACompleteBundle(t *testing.T) {
	f := newProviderFixture(t)
	virtualMembers(t, f.fixture, "remote")
	f.json(
		"/registry/providers/hashicorp/null/versions",
		map[string]any{"versions": []remoteProviderVersion{{Version: "1.0.0", Protocols: []string{"5.0"}, Platforms: []remotePlatform{{OS: "linux", Arch: "arm64"}}}}},
	)
	expect(t, f.get("reader", virtualProviderBase+"versions"), 200)
	expect(t, f.get("reader", virtualProviderBase+"1.0.0/download/linux/arm64"), 404)
	host, _ := url.Parse(f.up.URL)
	expect(t, f.get("reader", prefix+"virtual/providers/"+host.Host+"/hashicorp/null/1.0.0.json"), 404)
	if f.up.Count("GET", "/storage/provider.zip") != 0 {
		t.Fatal("unavailable descriptor fetched an unrelated package")
	}
}

func TestAccVirtualProviderUnknownPlatformDiscoveryPassesMissesButKeepsFaults(t *testing.T) {
	for _, mode := range []string{"later-platform", "storage-fault"} {
		t.Run(mode, func(t *testing.T) {
			f := newProviderFixture(t)
			virtualMembers(t, f.fixture, "remote")
			f.json("/registry/providers/hashicorp/null/versions", map[string]any{"versions": []remoteProviderVersion{{Version: "1.0.0", Protocols: []string{"5.0"}}}})
			f.up.Delete("/registry/providers/hashicorp/null/1.0.0/download/linux/amd64")
			if mode == "later-platform" {
				doc := map[string]any{}
				for k, v := range f.metadata {
					doc[k] = v
				}
				doc["arch"] = "arm64"
				doc["filename"] = "terraform-provider-null_1.0.0_linux_arm64.zip"
				f.json("/registry/providers/hashicorp/null/1.0.0/download/linux/arm64", doc)
			} else {
				backend := f.svc.HTTP
				f.svc.HTTP = providerRotationBackend(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
					if strings.Contains(q.Resource.Path, "/download/") {
						return source.Result{}, errors.Join(format.ErrStorageFault, errors.New("platform metadata storage fault"))
					}
					return backend.Read(ctx, q, p)
				})
			}
			response := f.get("reader", virtualProviderBase+"versions")
			if mode == "storage-fault" {
				expect(t, response, 500)
				return
			}
			expect(t, response, 200)
			if f.up.Count("GET", "/registry/providers/hashicorp/null/1.0.0/download/linux/amd64") != 1 ||
				f.up.Count("GET", "/registry/providers/hashicorp/null/1.0.0/download/linux/arm64") != 1 {
				t.Fatal("did not pass the first platform miss")
			}
		})
	}
}

func TestAccVirtualProviderManifestPolicyIsFencedAfterRead(t *testing.T) {
	f, _ := virtualProviderFixture(t)
	virtualMembers(t, f.fixture, "provider")
	manifest, err := f.db.Items().Get(t.Context(), "provider", false, "hosted/hashicorp/null/2.0.0/terraform-provider-null_2.0.0_manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	f.items.Blobs = &providerRevocationBlobs{Store: f.items.Blobs, digest: manifest.Actual.SHA256, revoke: func() {
		patchRepo(t, f.fixture, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("**/*_manifest.json") })
	}}
	response := f.get("admin", virtualProviderBase+"2.0.0/download/linux/amd64")
	expect(t, response, 404)
	if strings.Contains(response.Body.String(), "download_url") || strings.Contains(response.Body.String(), "?sig=") {
		t.Fatal("metadata capability crossed revoked manifest policy", response.Body.String())
	}
}
