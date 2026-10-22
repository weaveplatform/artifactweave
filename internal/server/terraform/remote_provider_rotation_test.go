package terraform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestRemoteProviderMirrorDescriptorUsesOneRevision(t *testing.T) {
	for _, mode := range []string{"zero-ttl", "upstream-no-store", "repository-no-store"} {
		t.Run(mode, func(t *testing.T) {
			f := newProviderFixture(t)
			f.configure(func(c *repo.Config) {
				c.TerraformRegistryURL = f.up.URL + "/smart"
				if mode == "zero-ttl" {
					c.RetrievalCachePeriodSecs = new(int)
				}
				if mode == "repository-no-store" {
					c.StoreArtifactsLocally = new(false)
				}
			})
			permission, _ := f.db.Permissions().Get(t.Context(), "remote-read")
			resource := permission.Resources[types.ResourceArtifact]
			resource.Targets["remote"] = types.Target{
				Include: []string{"provider-mirrors/registry.terraform.io/hashicorp/null/**"},
			}
			permission.Resources[types.ResourceArtifact] = resource
			if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
				t.Fatal(err)
			}
			upstream := "/smart/providers/registry.terraform.io/hashicorp/null/"
			f.json(
				upstream+"index.json",
				map[string]any{"versions": map[string]any{"1.0.0": map[string]any{}}},
			)
			original, replacement := f.archive, archiveZip(t, "replacement-provider")
			sumA, sumB := sha256.Sum256(original), sha256.Sum256(replacement)
			digestA, digestB := hex.EncodeToString(sumA[:]), hex.EncodeToString(sumB[:])
			documents := make([][]byte, 2)
			for index, item := range []struct {
				name, digest string
				body         []byte
			}{{"original", digestA, original}, {"replacement", digestB, replacement}} {
				documents[index], _ = json.Marshal(
					map[string]any{
						"archives": map[string]remoteMirrorArchive{
							"linux_amd64": {
								URL:    "/storage/" + item.name + ".zip",
								Hashes: []string{"zh:" + item.digest},
							},
						},
					},
				)
				f.up.Set(
					"/storage/"+item.name+".zip",
					fakeupstream.Resource{
						Body:   item.body,
						Header: http.Header{"Content-Type": {"application/zip"}},
					},
				)
			}
			header := http.Header{"Content-Type": {"application/json"}}
			if mode == "upstream-no-store" {
				header.Set("Cache-Control", "no-store")
			}
			f.up.Set(
				upstream+"1.0.0.json",
				fakeupstream.Resource{Body: documents[0], Header: header},
			)
			backend := f.svc.HTTP
			rotated := false
			f.svc.HTTP = providerRotationBackend(
				func(ctx context.Context, q source.Request, permit source.Permit) (source.Result, error) {
					result, err := backend.Read(ctx, q, permit)
					// Rotate after one admitted document has returned from the real backend.
					// Unknown no-store responses can require a private refetch internally;
					// both reads still belong to this same admitted representation.
					if err == nil && !rotated && q.Resource.Kind == source.Metadata &&
						strings.HasSuffix(q.Resource.Path, "/1.0.0.json") {
						rotated = true
						f.up.Set(
							upstream+"1.0.0.json",
							fakeupstream.Resource{Body: documents[1], Header: header},
						)
					}
					return result, err
				},
			)
			target := prefix + "remote/providers/registry.terraform.io/hashicorp/null/1.0.0.json"
			got := f.get("reader", target)
			expect(t, got, 200)
			entry := mirrorRotationEntry(t, got)
			parsed, err := url.Parse(entry.URL)
			if err != nil {
				t.Fatal(err)
			}
			h := handler{o: Options{Keys: f.keys, Now: func() time.Time { return f.now }}}
			claim, valid := h.verify(parsed.Query().Get("sig"))
			metadataDigest := sha256.Sum256(documents[0])
			if !valid || claim.SHA256 != digestA || claim.Provider == nil ||
				claim.Provider.Metadata != hex.EncodeToString(metadataDigest[:]) ||
				len(entry.Hashes) != 1 ||
				entry.Hashes[0] != "zh:"+digestA {
				t.Fatalf(
					"mixed mirror descriptor: signed archive=%s metadata=%v hashes=%v",
					claim.SHA256,
					claim.Provider,
					entry.Hashes,
				)
			}
			metadataReads := f.up.Count(http.MethodGet, upstream+"1.0.0.json")
			wantReads := 1
			if mode == "upstream-no-store" {
				wantReads = 2
			}
			if metadataReads != wantReads {
				t.Fatalf("version document fetched %d times, want %d", metadataReads, wantReads)
			}
			// A signed link cannot silently follow a later metadata revision.
			expect(t, f.get("", entry.URL), 403)
			corrected := f.get("reader", target)
			expect(t, corrected, 200)
			entry = mirrorRotationEntry(t, corrected)
			if len(entry.Hashes) != 1 || entry.Hashes[0] != "zh:"+digestB {
				t.Fatal("replacement hash missing", entry.Hashes)
			}
			downloaded := f.get("", entry.URL)
			if downloaded.Code != 200 || !bytes.Equal(downloaded.Body.Bytes(), replacement) {
				t.Fatalf("corrected archive: %d %s", downloaded.Code, downloaded.Body)
			}
		})
	}
}

func mirrorRotationEntry(t *testing.T, response *httptest.ResponseRecorder) remoteMirrorArchive {
	t.Helper()
	var doc struct {
		Archives map[string]remoteMirrorArchive `json:"archives"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	entry, ok := doc.Archives["linux_amd64"]
	if !ok || !strings.Contains(entry.URL, "?sig=") {
		t.Fatal("missing archive", response.Body)
	}
	return entry
}

type providerRotationBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f providerRotationBackend) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}
