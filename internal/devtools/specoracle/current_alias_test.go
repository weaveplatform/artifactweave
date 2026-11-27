package specoracle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Current operation identities and status codes are wire facts. All fixture
// descriptions are authored here; no vendor definition or prose is vendored.
const systemDefinition = `{"openapi":"3.1.0","info":{"title":"JFrog Platform - Artifactory System API","version":"1.0.0"},"paths":{"/artifactory/api/system/ping":{"get":{"operationId":"artifactoryPing","responses":{"200":{"description":"fixture"},"500":{"description":"fixture"}}}},"/artifactory/api/v1/system/readiness":{"get":{"operationId":"getReadinessProbe","responses":{"200":{"description":"fixture"},"500":{"description":"fixture"}}}},"/artifactory/api/v1/system/liveness":{"get":{"operationId":"getLivenessProbe","responses":{"200":{"description":"fixture"},"500":{"description":"fixture"}}}},"/artifactory/api/system/version":{"get":{"operationId":"getArtifactoryVersion","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"}}}},"/artifactory/api/system":{"get":{"operationId":"getSystemInfo","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/system/verifyconnection":{"post":{"operationId":"verifyConnection","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/system/configuration/baseUrl":{"put":{"operationId":"updateCustomUrlBase","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/system/configuration":{"get":{"operationId":"getSystemConfiguration","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}},"patch":{"operationId":"patchSystemConfiguration","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}},"post":{"operationId":"saveSystemConfiguration","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/system/license":{"get":{"operationId":"getLicenseInfo","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}},"post":{"operationId":"installLicense","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/system/licenses":{"get":{"operationId":"getHALicenseInfo","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}},"post":{"operationId":"installHALicenses","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}},"delete":{"operationId":"deleteHALicense","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/system/configuration/webServer":{"get":{"operationId":"getReverseProxyConfiguration","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}},"post":{"operationId":"updateReverseProxyConfiguration","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/system/configuration/reverseProxy/nginx":{"get":{"operationId":"getReverseProxySnippet","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/system/migration/sha2/start":{"post":{"operationId":"startSha256Migration","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/system/migration/sha2/stop":{"post":{"operationId":"stopSha256Migration","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/metadata_server/reindex":{"post":{"operationId":"reindexMetadataServer","responses":{"200":{"description":"fixture","content":{"text/plain":{"schema":{"type":"string"}}}},"202":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/system/locks":{"get":{"operationId":"getClusterLocks","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"503":{"description":"fixture"}}}},"/artifactory/api/system/locks/local":{"get":{"operationId":"getNodeLocks","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"503":{"description":"fixture"}}}},"/artifactory/api/system/support/bundle":{"post":{"operationId":"createSupportBundle","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/system/support/bundle/{bundleId}":{"get":{"operationId":"getSupportBundleMetadata","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"404":{"description":"fixture"}}},"delete":{"operationId":"deleteSupportBundle","responses":{"204":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"404":{"description":"fixture"}}}},"/artifactory/api/system/support/bundle/{bundleId}/archive":{"get":{"operationId":"getSupportBundle","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"404":{"description":"fixture"}}}},"/artifactory/api/system/support/bundles":{"get":{"operationId":"listSupportBundles","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/artifactory/api/curation/interfaceSettings":{"put":{"operationId":"updateCurationInterfaceSettings","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}},"get":{"operationId":"getCurationInterfaceSettings","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}}}}`

const awsDefinition = `{"openapi":"3.1.0","info":{"title":"JFrog Platform - AWS IAM API","version":"1.0.0"},"paths":{"/access/api/v1/aws/iam_role":{"put":{"operationId":"putAwsIamRole","responses":{"204":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"404":{"description":"fixture"},"409":{"description":"fixture"}}}},"/access/api/v1/aws/iam_role/{username}":{"get":{"operationId":"getAwsIamRole","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"404":{"description":"fixture"}}},"delete":{"operationId":"deleteAwsIamRole","responses":{"204":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}}}}`

const federationDefinition = `{"openapi":"3.1.0","info":{"title":"JFrog Platform - Access Federation API","version":"1.0.0"},"paths":{"/access/api/v1/system/federation":{"get":{"operationId":"getAllAccessFederations","responses":{"200":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}},"post":{"operationId":"registerAccessFederation","responses":{"201":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"409":{"description":"fixture"}}}},"/access/api/v1/system/federation/{serverName}":{"get":{"operationId":"getAccessFederation","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"404":{"description":"fixture"}}},"patch":{"operationId":"updateAccessFederation","responses":{"200":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"404":{"description":"fixture"}}},"delete":{"operationId":"deleteAccessFederation","responses":{"204":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"404":{"description":"fixture"}}}},"/access/api/v1/system/federation/validate_server":{"post":{"operationId":"validateFederationTarget","responses":{"204":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"}}}},"/access/api/v1/system/federation/{serverName}/full_broadcast":{"put":{"operationId":"fullBroadcastAccessFederation","responses":{"202":{"description":"fixture"},"204":{"description":"fixture"},"304":{"description":"fixture"},"400":{"description":"fixture"},"401":{"description":"fixture"},"403":{"description":"fixture"},"404":{"description":"fixture"}}}}}}`

type aliasCase struct {
	name, suffix, page, schema, title, parent, path, id, definition string
	known                                                           []string
	count                                                           int
}

var aliasCases = []aliasCase{
	{
		"system",
		"artifactory-system-api",
		"getclusterlocks",
		"artifactory-system-api.yaml",
		"JFrog Platform - Artifactory System API",
		"system-1",
		"/artifactory/api/system/locks",
		"getClusterLocks",
		systemDefinition,
		[]string{"getcurationinterfacesettings", "getclusterlocks", "getnodelocks", "updatecurationinterfacesettings"},
		30,
	},
	{
		"aws",
		"aws-iam-api",
		"getawsiamrole",
		"aws-iam-openapi.yml",
		"JFrog Platform - AWS IAM API",
		"aws-iam-role",
		"/access/api/v1/aws/iam_role/{username}",
		"getAwsIamRole",
		awsDefinition,
		[]string{"deleteawsiamrole", "getawsiamrole", "putawsiamrole"},
		3,
	},
	{
		"federation",
		"access-federation-api",
		"getallaccessfederations",
		"federation-openapi.yml",
		"JFrog Platform - Access Federation API",
		"federation",
		"/access/api/v1/system/federation",
		"getAllAccessFederations",
		federationDefinition,
		[]string{
			"deleteaccessfederation",
			"getallaccessfederations",
			"getaccessfederation",
			"updateaccessfederation",
			"registeraccessfederation",
			"validatefederationtarget",
			"fullbroadcastaccessfederation",
		},
		7,
	},
}

func aliasSite(t *testing.T, tc aliasCase, htmlSchema, endorsingID, method string) *docsSite {
	t.Helper()
	var full map[string]any
	if err := json.Unmarshal([]byte(tc.definition), &full); err != nil {
		t.Fatal(err)
	}
	paths := full["paths"].(map[string]any)
	pages := map[string]string{"unrelated": theirRepoPage}
	for _, slug := range tc.known {
		for p, raw := range paths {
			for method, rawOp := range raw.(map[string]any) {
				op := rawOp.(map[string]any)
				if strings.EqualFold(slug, op["operationId"].(string)) {
					partial := map[string]any{"openapi": "3.1.0", "info": full["info"], "paths": map[string]any{p: map[string]any{method: op}}}
					data, err := json.Marshal(partial)
					if err != nil {
						t.Fatal(err)
					}
					pages[slug] = "```json\n" + string(data) + "\n```"
				}
			}
		}
	}
	site := newDocsSite(t, pages)
	var parent strings.Builder
	parent.WriteString("## API Reference: JFrog Platform REST APIs\n- Unrelated\n  - [Unrelated]({URL}/administration/reference/unrelated.md)\n")
	if tc.name == "system" {
		parent.WriteString("\n## API Reference: JFrog Artifactory Security API\n- Curation and System\n")
	}
	for _, slug := range tc.known {
		parent.WriteString("  - [" + slug + "]({URL}/administration/reference/" + slug + ".md)\n")
	}
	site.parent("/administration/reference/llms.txt", http.StatusOK, parent.String())
	site.index("/administration/reference/jfrog-platform-"+tc.suffix+"/llms.txt", http.StatusNotFound, "")
	site.index("/administration/reference/jfrog-platform-rest-apis/llms.txt", http.StatusOK, parent.String())
	schema := map[string]any{"openapi": "3.1.0", "info": full["info"], "paths": map[string]any{tc.path: map[string]any{"get": map[string]any{"operationId": endorsingID}}}}
	props := map[string]any{
		"oasPublicUrl": "@administration/v1.0#" + tc.name,
		"document": map[string]any{
			"parent": map[string]string{"uri": "/branches/1.0/reference/" + tc.parent},
			"api":    map[string]any{"uri": "/branches/1.0/apis/" + htmlSchema, "method": method, "path": tc.path, "schema": schema},
		},
		// System's current parent includes a different definition's endpoint.
		// Full-definition ownership must not pull that unrelated sidebar child in.
		"sidebar": []any{map[string]any{"pages": []any{map[string]any{"slug": tc.parent, "pages": []any{
			map[string]string{"slug": tc.page, "api_method": "get", "type": "endpoint"},
			map[string]string{"slug": "getserviceid-1", "api_method": "get", "type": "endpoint"},
		}}}}},
	}
	data, err := json.Marshal(props)
	if err != nil {
		t.Fatal(err)
	}
	site.index("/administration/reference/"+tc.page, http.StatusOK, `<script id="ssr-props">`+string(data)+`</script>`)
	site.index("/api/v1/api-registry/"+tc.name, http.StatusOK, tc.definition)
	return site
}

func fetchAlias(t *testing.T, site *docsSite, tc aliasCase, dir string) Manifest {
	t.Helper()
	base, err := url.Parse(site.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	index := site.url("/administration/reference/jfrog-platform-" + tc.suffix + "/llms.txt")
	var known []string
	for _, slug := range tc.known {
		known = append(known, site.url("/administration/reference/"+slug+".md"))
	}
	m, err := Fetch(context.Background(), FetchOptions{
		Indexes:  []string{site.url("/administration/reference/jfrog-platform-rest-apis/llms.txt"), index},
		CacheDir: dir, Known: map[string][]string{FamilySlug(index): known}, Delay: time.Millisecond,
		Client: &http.Client{Transport: currentRegistryTransport{base: base}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAccFetchCurrentAliasesOwnOnlyTheirFullDefinitions(t *testing.T) {
	for _, tc := range aliasCases {
		t.Run(tc.name, func(t *testing.T) {
			site := aliasSite(t, tc, tc.schema, tc.id, "get")
			dir := t.TempDir()
			family := "administration-jfrog-platform-" + tc.suffix
			// This is a physical leftover from the former broad-family fetch.
			writeCache(t, dir, family, "stale-unrelated.json", theirRepoBlock)
			m := fetchAlias(t, site, tc, dir)
			if len(m.Missing()) != 0 {
				t.Fatalf("current alias missing: %+v", m.Missing())
			}
			if err := CheckFamilies(dir); err != nil {
				t.Fatal(err)
			}
			o, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for key, op := range o.Ops {
				if strings.HasPrefix(key, "GET /artifactory/api/repositories/") || strings.HasPrefix(key, "PUT /artifactory/api/repositories/") {
					if op.Family == family {
						t.Errorf("unrelated operation attributed to %s: %s", family, key)
					}
				} else {
					count++
					if op.Family != family {
						t.Errorf("published family operation has wrong owner: %s -> %s", key, op.Family)
					}
				}
			}
			if count != tc.count {
				t.Errorf("complete current definition has %d operations; want %d", count, tc.count)
			}
			for _, f := range m.Families {
				if f.Family == family && (!strings.Contains(f.Error, "status 404") || f.Source != "https://dash.readme.com/api/v1/api-registry/"+tc.name) {
					t.Errorf("missing current-source provenance: %+v", f)
				}
			}
			inv, err := BuildInventory(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range inv.Operations {
				if strings.Contains(op.Key, "/repositories/") && op.Family == family {
					t.Errorf("inventory assigned unrelated operation to %s", family)
				}
			}
		})
	}
}

func TestAccFetchCurrentAliasesRejectWrongEndorsement(t *testing.T) {
	for _, tc := range aliasCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, bad := range []struct{ name, schema, id, method string }{
				{"wrong source file", "unrelated.yml", tc.id, "get"},
				{"wrong endorsing operation", tc.schema, "notThePublishedOperation", "get"},
				{"wrong endorsing method", tc.schema, tc.id, "post"},
			} {
				t.Run(bad.name, func(t *testing.T) {
					site := aliasSite(t, tc, bad.schema, bad.id, bad.method)
					dir := t.TempDir()
					m := fetchAlias(t, site, tc, dir)
					if len(m.Missing()) != 1 {
						t.Fatalf("unverified alias must stay missing, got %+v", m.Missing())
					}
					if err := CheckFamilies(dir); err == nil {
						t.Fatal("guard accepted an unverified alias")
					}
				})
			}
		})
	}
}

func TestAccFetchCurrentAliasNormalizesEndorsingPathParameters(t *testing.T) {
	tc := aliasCases[1]
	tc.path = "/access/api/v1/aws/iam_role/{differentName}"
	site := aliasSite(t, tc, tc.schema, tc.id, "get")
	dir := t.TempDir()
	m := fetchAlias(t, site, tc, dir)
	if len(m.Missing()) != 0 {
		t.Fatalf("path parameter spelling changed the wire identity: %+v", m.Missing())
	}
	o, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := "GET /access/api/v1/aws/iam_role/{}"
	if op, ok := o.Ops[key]; !ok || op.Family != "administration-jfrog-platform-aws-iam-api" {
		t.Fatalf("normalized endorsing operation missing: %+v", o.Ops)
	}
}
