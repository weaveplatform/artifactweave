package access_test

import (
	"crypto/md5" //nolint:gosec // Terraform state wire checksum
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	docker "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	tfbe "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

type nativePublicationBody struct {
	reader   *strings.Reader
	onEnd    func()
	finished bool
}

func (b *nativePublicationBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	// A JSON decoder can finish its value without requesting another read for
	// EOF. Change authority when the final client bytes are handed over, before
	// either streaming or JSON publication can use the completed request.
	if b.reader.Len() == 0 && !b.finished {
		b.finished = true
		b.onEnd()
	}
	return n, err
}

func TestAccProjectRolesRecheckedAtNativePublicationAfterStreaming(t *testing.T) {
	for _, kind := range []string{"docker", "terraformbackend", "terraformbackend-capability", "build", "properties", "aql-properties", "aql-delete"} {
		for _, revoked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/revoked=%t", kind, revoked), func(t *testing.T) {
				var db *inmem.Store
				h := tokenFixture(t, allowBasicTokens, func(c *Config) {
					db = c.Store.(*inmem.Store)
					c.Keys = support.TerraformBackendKeys(t)
					c.FormatSettings.TerraformBackend.Keys = c.Keys
				})
				ctx := t.Context()
				if err := db.Users().Create(ctx, accesstest.User{Name: "publisher", PasswordHash: cheapHash}); err != nil {
					t.Fatal(err)
				}
				projectRequest(t, h, http.MethodPost, "", "admin", `{"project_key":"alpha","display_name":"Alpha"}`, http.StatusCreated)
				role := accesstest.Role{
					ProjectKey: "alpha",
					Name:       "publisher",
					Type:       accesstest.RoleCustom,
					Actions: []string{
						"READ_REPOSITORY",
						"DEPLOY_CACHE_REPOSITORY",
						"ANNOTATE_REPOSITORY",
						"DELETE_OVERWRITE_REPOSITORY",
						"READ_BUILD",
						"DEPLOY_BUILD",
					},
					Environments: []string{"DEV"},
				}
				if err := db.Projects().PutRole(ctx, role); err != nil {
					t.Fatal(err)
				}
				projectRequest(t, h, http.MethodPut, "/alpha/users/publisher", "admin", `{"roles":["publisher"]}`, http.StatusOK)
				if kind != "build" {
					packageType := strings.TrimSuffix(kind, "-capability")
					if kind == "properties" || strings.HasPrefix(kind, "aql-") {
						packageType = "generic"
					}
					wantStatus(t, do(t, h, http.MethodPut, "/artifactory/api/repositories/publish", jsonAs(basic("admin", "pw")),
						`{"rclass":"local","packageType":"`+packageType+`","projectKey":"alpha"}`), http.StatusOK)
				}
				method, target, payload, storedPath, want := http.MethodPost, "", "", "", http.StatusCreated
				switch kind {
				case "docker":
					payload = "native blob payload"
					sum := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
					target = "/v2/publish/image/blobs/uploads/?digest=sha256:" + sum
					storedPath = docker.UploadPath("image", sum)
				case "terraformbackend", "terraformbackend-capability":
					const base = "/artifactory/api/terraform/remote/v2"
					wantStatus(t, do(t, h, http.MethodPost, base+"/organizations/publish/workspaces", jsonAs(basic("admin", "pw")),
						`{"data":{"type":"workspaces","attributes":{"name":"app"}}}`), http.StatusCreated)
					state := `{"version":4,"serial":1,"lineage":"project-publication","outputs":{},"resources":[]}`
					payload = fmt.Sprintf(`{"data":{"type":"state-versions","attributes":{"serial":1,"md5":"%x","state":"%s"}}}`,
						md5.Sum([]byte(state)), base64.StdEncoding.EncodeToString([]byte(state))) //nolint:gosec // native wire checksum
					target, storedPath = base+"/workspaces/publish__app/state-versions", tfbe.LatestPath("app")
					if kind == "terraformbackend-capability" {
						issued := mintAdmin(t, h, `{"username":"publisher","scope":"applied-permissions/roles:alpha:publisher","expires_in":600}`)
						credential, _ := issued["access_token"].(string)
						pending := do(t, h, http.MethodPost, target, jsonAs(bearerAuth(credential)),
							fmt.Sprintf(`{"data":{"type":"state-versions","attributes":{"serial":1,"md5":"%x"}}}`, md5.Sum([]byte(state)))) //nolint:gosec // native wire checksum
						wantStatus(t, pending, http.StatusCreated)
						resource := decodeObject(t, pending.Body.String())["data"].(map[string]any)
						target, _ = resource["attributes"].(map[string]any)["hosted-state-upload-url"].(string)
						if target == "" {
							t.Fatal("pending state did not return an upload capability")
						}
						method, payload, want = http.MethodPut, state, http.StatusOK
					}
				case "build":
					method, target, want = http.MethodPut, "/artifactory/api/build?project=alpha", http.StatusNoContent
					payload = `{"name":"demo","number":"1","started":"2026-11-20T00:00:00.000+0000","modules":[]}`
				case "properties", "aql-properties", "aql-delete":
					storedPath = "file.bin"
					wantStatus(t, do(t, h, http.MethodPut, "/artifactory/publish/file.bin", basic("admin", "pw"), "original"), http.StatusCreated)
					wantStatus(t, do(t, h, http.MethodPut, "/artifactory/api/storage/publish/file.bin?properties=k=before", basic("admin", "pw"), ""), http.StatusNoContent)
					if kind == "properties" {
						method, target, payload, want = http.MethodPatch, "/artifactory/api/metadata/publish/file.bin", `{"props":{"k":["after"]}}`, http.StatusNoContent
					} else {
						target, want = "/artifactory/api/search/aql", http.StatusOK
						payload = `properties.update({"item.repo":"publish","item.name":"file.bin"}).keys("k").newValue("after").dryRun("false")`
						if kind == "aql-delete" {
							payload = `items.delete({"repo":"publish","name":"file.bin"}).dryRun("false")`
						}
					}
				}
				ended := false
				stream := &nativePublicationBody{reader: strings.NewReader(payload), onEnd: func() {
					if db.InTx(ctx) {
						t.Fatal("native client body holds a metadata transaction")
					}
					ended = true
					if revoked {
						role.Actions = nil
						if err := db.Projects().PutRole(ctx, role); err != nil {
							t.Fatal(err)
						}
					}
				}}
				r := httptest.NewRequestWithContext(ctx, method, target, stream)
				if kind != "terraformbackend-capability" {
					r.SetBasicAuth("publisher", "pw")
				}
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if revoked {
					want = http.StatusForbidden
				}
				wantStatus(t, w, want)
				if !ended {
					t.Fatal("request did not reach the streaming barrier")
				}
				var err error
				if kind == "properties" || kind == "aql-properties" {
					it, err := db.Items().Get(ctx, "publish", false, storedPath)
					if err != nil {
						t.Fatal("property mutation lost the item", err)
					}
					properties, err := db.Items().Properties(ctx, it.ID)
					value := "after"
					if revoked {
						value = "before"
					}
					if err != nil || len(properties["k"]) != 1 || properties["k"][0] != value {
						t.Fatalf("property effect escaped current role revoked=%t: %v", revoked, err)
					}
					return
				}
				if kind == "build" {
					_, err = db.Builds().Get(ctx, "alpha", "demo", "1", time.Time{})
				} else {
					_, err = db.Items().Get(ctx, "publish", false, storedPath)
				}
				missing := revoked
				if kind == "aql-delete" {
					missing = !revoked
				}
				if missing && !errors.Is(err, kernel.ErrNotFound) || !missing && err != nil {
					t.Fatalf("native publication effect revoked=%t: %v", revoked, err)
				}
			})
		}
	}
}
