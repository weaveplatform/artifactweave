package descriptor

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

type exportNode struct {
	Name     xml.Name
	Text     string       `xml:",chardata"`
	Children []exportNode `xml:",any"`
}

func (n *exportNode) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	type node exportNode
	var value node
	if err := decoder.DecodeElement(&value, &start); err != nil {
		return err
	}
	*n = exportNode(value)
	n.Name = start.Name
	return nil
}

func xmlText(t *testing.T, n exportNode, path string) string {
	t.Helper()
	nodes := []exportNode{n}
	for _, part := range strings.Split(path, "/") {
		var next []exportNode
		for _, node := range nodes {
			for _, child := range node.Children {
				if child.Name.Local == part {
					next = append(next, child)
				}
			}
		}
		nodes = next
	}
	if len(nodes) != 1 {
		t.Fatalf("XML path %s has %d matches", path, len(nodes))
	}
	return nodes[0].Text
}

func exportXML(t *testing.T, d Deps) (exportNode, string) {
	t.Helper()
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	body, err := NewAPI(d).Export.XML(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var node exportNode
	if err := xml.Unmarshal(body, &node); err != nil {
		t.Fatal(err)
	}
	return node, string(body)
}

func TestUnitExportMixedCurrentStateAndProviderPaths(t *testing.T) {
	db := inmem.New()
	d := depsOf(t, db)
	d.Projects = accesstest.Over(t, db).API().Projects
	const mixed = `
localRepositories:
  export-local:
    type: generic
    description: 'data <>&"'
remoteRepositories:
  export-remote:
    type: generic
    url: https://upstream.invalid/repo
    username: remote-user
    password: ${REMOTE_PASSWORD}
    customHttpHeaders:
      - name: X-Secret
        value: sensitive-header-fixture
        sensitive: true
      - name: X-Data
        value: 'data <>&'
        sensitive: false
virtualRepositories:
  export-virtual:
    type: generic
    repositories: [export-local, export-remote]
federatedRepositories:
  export-federated:
    type: generic
    members:
      - url: https://peer.invalid/repo
        enabled: false
proxies:
  'proxy <>&':
    host: proxy.invalid
    port: 3128
    password: ${PROXY_PASSWORD}
    services: [jfrt, jfxr]
    redirectedToHosts: one.invalid,two.invalid
propertySets:
  'set <>&':
    visible: false
    properties:
      'property <>&':
        closedPredefinedValues: true
        multipleChoice: false
        predefinedValues:
          - value: 'value <>&'
            defaultValue: true
repoLayouts:
  'layout <>&':
    name: 'layout <>&'
    artifactPathPattern: '[module]/[baseRev]/[module]-[baseRev].[ext]'
    descriptorPathPattern: ''
    distinctiveDescriptorPathPattern: false
mailServer:
  enabled: false
  host: smtp.invalid
  port: 2525
  password: ${MAIL_PASSWORD}
  from: 'sender <sender@example.invalid>'
security:
  anonAccessEnabled: true
downloadRedirectConfig:
  fileMinimumSize: 0
`
	plan, err := NewAPI(d).Descriptor.Parse([]byte(mixed), func(name string) (string, bool) { return name + "-resolved-fixture", true })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewAPI(d).Descriptor.Apply(t.Context(), plan, now, false); err != nil {
		t.Fatal(err)
	}
	result, err := NewAPI(d).Descriptor.Apply(t.Context(), plan, now, false)
	if err != nil || len(result.Created)+len(result.Updated)+len(result.Deleted) != 0 {
		t.Fatal("repeated apply changed state", result, err)
	}
	node, body := exportXML(t, d)
	if node.Name.Local != "config" || node.Name.Space != "http://artifactory.jfrog.org/xsd/3.5.11" {
		t.Fatal("wrong descriptor root", node.Name)
	}
	for path, want := range map[string]string{
		"localRepositories/localRepository/key":                                                      "export-local",
		"localRepositories/localRepository/type":                                                     "generic",
		"localRepositories/localRepository/description":                                              "data <>&\"",
		"remoteRepositories/remoteRepository/key":                                                    "export-remote",
		"remoteRepositories/remoteRepository/username":                                               "remote-user",
		"remoteRepositories/remoteRepository/url":                                                    "https://upstream.invalid/repo",
		"federatedRepositories/federatedRepository/federatedMembers/federatedMember/enabled":         "false",
		"proxies/proxy/key":                                                                          "proxy <>&",
		"proxies/proxy/services":                                                                     "jfrt,jfxr",
		"proxies/proxy/redirectedToHosts":                                                            "one.invalid,two.invalid",
		"propertySets/propertySet/name":                                                              "set <>&",
		"propertySets/propertySet/visible":                                                           "false",
		"propertySets/propertySet/properties/property/name":                                          "property <>&",
		"propertySets/propertySet/properties/property/predefinedValues/predefinedValue/value":        "value <>&",
		"propertySets/propertySet/properties/property/predefinedValues/predefinedValue/defaultValue": "true",
		"repoLayouts/repoLayout/name":                                                                "layout <>&",
		"repoLayouts/repoLayout/descriptorPathPattern":                                               "",
		"repoLayouts/repoLayout/distinctiveDescriptorPathPattern":                                    "false",
		"mailServer/from":                        "sender <sender@example.invalid>",
		"mailServer/enabled":                     "false",
		"security/anonAccessEnabled":             "true",
		"downloadRedirectConfig/fileMinimumSize": "0",
	} {
		if got := xmlText(t, node, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	for _, secret := range []string{"resolved-fixture", "sensitive-header-fixture", "${", "<password>"} {
		if strings.Contains(body, secret) {
			t.Error("protected data reached XML", secret)
		}
	}
	// The public API is the authority too: an update is visible without applying
	// another descriptor, and deleting its repository removes its XML entry.
	c, err := d.Repositories.Get(t.Context(), "export-local")
	if err != nil {
		t.Fatal(err)
	}
	c.Description = "changed through owning API"
	if err := d.Repositories.Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	node, _ = exportXML(t, d)
	if got := xmlText(t, node, "localRepositories/localRepository/description"); got != c.Description {
		t.Fatal("stale export", got)
	}
}

func TestUnitExportEmptyAndOptionalState(t *testing.T) {
	db := inmem.New()
	d := depsOf(t, db)
	node, body := exportXML(t, d)
	if xmlText(t, node, "security/anonAccessEnabled") != "false" {
		t.Fatal("unset anonymous access was not off")
	}
	for _, absent := range []string{"<mailServer>", "<downloadRedirectConfig>", "<backup>", "<localRepository>"} {
		if strings.Contains(body, absent) {
			t.Error("invented configuration", absent)
		}
	}
	// Explicit empty and false retain their presence; missing fields stay absent.
	if err := db.Configs().
		Put(t.Context(), configdoc.ConfigDocument{Kind: configdoc.ConfigMailServer, Key: "mailserver", Document: []byte(`{"enabled":false,"host":"","username":""}`)}); err != nil {
		t.Fatal(err)
	}
	node, body = exportXML(t, d)
	if xmlText(t, node, "mailServer/host") != "" || xmlText(t, node, "mailServer/username") != "" || xmlText(t, node, "mailServer/port") != "25" {
		t.Fatal("optional distinctions were lost", body)
	}
}

type exportFaultConfigs struct {
	configdoc.ConfigStore
	kind configdoc.ConfigKind
	get  bool
}

func (s exportFaultConfigs) List(ctx context.Context, kind configdoc.ConfigKind) ([]configdoc.ConfigDocument, error) {
	if !s.get && kind == s.kind {
		return nil, errExportFailure
	}
	return s.ConfigStore.List(ctx, kind)
}

func (s exportFaultConfigs) Get(ctx context.Context, kind configdoc.ConfigKind, key string) (configdoc.ConfigDocument, error) {
	if s.get && kind == s.kind {
		return configdoc.ConfigDocument{}, errExportFailure
	}
	return s.ConfigStore.Get(ctx, kind, key)
}

var errExportFailure = errors.New("private export store failure")

type exportFaultRepos struct{ artifactsapi.Repositories }

func (exportFaultRepos) List(context.Context, artifactsapi.RepoFilter) ([]repo.Config, error) {
	return nil, errExportFailure
}

type exportCommitFailure struct {
	tx.Runner
	begin bool
}

func (s exportCommitFailure) Tx(ctx context.Context, fn func(context.Context) error) error {
	if s.begin {
		return errExportFailure
	}
	if err := s.Runner.Tx(ctx, fn); err != nil {
		return err
	}
	return errExportFailure
}

func TestUnitExportFailuresNeverReturnPartialXML(t *testing.T) {
	for _, name := range []string{"begin", "commit", "guard", "repositories", "proxy", "properties", "layouts", "mail", "redirect", "anonymous", "corrupt", "null mail", "invalid mail", "overflow mail", "current", "authorize"} {
		t.Run(name, func(t *testing.T) {
			db := inmem.New()
			d := depsOf(t, db)
			switch name {
			case "begin":
				d.Tx = exportCommitFailure{Runner: db, begin: true}
			case "commit":
				d.Tx = exportCommitFailure{Runner: db}
			case "guard":
				d.Projects = &accessapitest.Projects{Err: errExportFailure}
			case "repositories":
				d.Repositories = exportFaultRepos{d.Repositories}
			case "proxy":
				d.Configs = exportFaultConfigs{ConfigStore: d.Configs, kind: configdoc.ConfigProxy}
			case "properties":
				d.Configs = exportFaultConfigs{ConfigStore: d.Configs, kind: configdoc.ConfigPropertySet}
			case "layouts":
				d.Configs = exportFaultConfigs{ConfigStore: d.Configs, kind: configdoc.ConfigLayout}
			case "mail":
				d.Configs = exportFaultConfigs{ConfigStore: d.Configs, kind: configdoc.ConfigMailServer, get: true}
			case "redirect":
				d.Configs = exportFaultConfigs{ConfigStore: d.Configs, kind: downloadRedirectKind, get: true}
			case "anonymous":
				d.AnonymousAccess = &accessapitest.AnonymousAccess{Err: errExportFailure}
			case "corrupt":
				if err := db.Configs().Put(t.Context(), configdoc.ConfigDocument{Kind: configdoc.ConfigLayout, Key: "corrupt", Document: []byte(`{`)}); err != nil {
					t.Fatal(err)
				}
			case "null mail":
				if err := db.Configs().Put(t.Context(), configdoc.ConfigDocument{Kind: configdoc.ConfigMailServer, Key: "mailserver", Document: []byte(`null`)}); err != nil {
					t.Fatal(err)
				}
			case "invalid mail", "overflow mail":
				body := []byte(`{"port":0}`)
				if name == "overflow mail" {
					body = []byte(`{"port":1e1000}`)
				}
				if err := db.Configs().Put(t.Context(), configdoc.ConfigDocument{Kind: configdoc.ConfigMailServer, Key: "mailserver", Document: body}); err != nil {
					t.Fatal(err)
				}
			case "current":
				d.Authorizer = &accessapitest.Authorizer{}
				d.Authenticator = &accessapitest.Authenticator{CurrentFunc: func(context.Context, kernel.Principal) (kernel.Principal, error) {
					return kernel.Principal{}, errExportFailure
				}}
			case "authorize":
				d.Authenticator = &accessapitest.Authenticator{}
				d.Authorizer = &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
					return nil, errExportFailure
				}}
			}
			ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
			body, err := NewAPI(d).Export.XML(ctx)
			if err == nil || body != nil {
				t.Fatalf("failure published XML: %q %v", body, err)
			}
		})
	}
}

func TestUnitExportRechecksCurrentAdministratorAndTokenCeiling(t *testing.T) {
	for _, name := range []string{"absent", "anonymous", "user", "revoked", "demoted", "token ceiling"} {
		t.Run(name, func(t *testing.T) {
			db := inmem.New()
			d := depsOf(t, db)
			ctx := t.Context()
			want := kernel.ErrForbidden
			switch name {
			case "absent":
				want = kernel.ErrUnauthenticated
			case "anonymous":
				ctx = kernel.WithPrincipal(ctx, kernel.Principal{Name: "anonymous", Anonymous: true})
				want = kernel.ErrUnauthenticated
			case "user":
				ctx = kernel.WithPrincipal(ctx, kernel.Principal{Name: "user"})
			default:
				ctx = kernel.WithPrincipal(ctx, kernel.Principal{Name: "admin", Admin: true})
				d.Projects = &accessapitest.Projects{}
				d.Authenticator = &accessapitest.Authenticator{}
				d.Authorizer = &accessapitest.Authorizer{}
				if name == "revoked" {
					want = kernel.ErrBadCredentials
					d.Authenticator = &accessapitest.Authenticator{
						CurrentFunc: func(context.Context, kernel.Principal) (kernel.Principal, error) { return kernel.Principal{}, want },
					}
				} else {
					d.Authorizer = &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
						return &accessapitest.Decider{}, nil
					}}
				}
			}
			body, err := NewAPI(d).Export.XML(ctx)
			if !errors.Is(err, want) || body != nil {
				t.Fatalf("%s = %q %v; want %v", name, body, err, want)
			}
		})
	}
}

func TestUnitExportRepositoryXMLNamesAndCollections(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want map[string]string
	}{
		{
			name: "local",
			body: `{"key":"local","rclass":"local","packageType":"generic","checksumPolicyType":"client-checksums","downloadRedirect":false,"xrayIndex":false,"xrayDataTtl":0,"propertySets":["release"],"cargoAnonymousAccess":false}`,
			want: map[string]string{
				"localRepoChecksumPolicyType":      "client-checksums",
				"downloadRedirect/enabled":         "false",
				"xray/enabled":                     "false",
				"xray/dataTtl":                     "0",
				"propertySets/propertySetRef":      "release",
				"cargoConfig/cargoAnonymousAccess": "false",
			},
		},
		{
			name: "remote",
			body: `{"key":"remote","rclass":"remote","packageType":"generic","proxy":"corp","vcsType":"GIT","vcsGitProvider":"GITHUB","vcsGitDownloadUrl":"https://git.invalid","feedContextPath":"v2","pyPIRepositorySuffix":"simple","externalDependenciesEnabled":false,"externalDependenciesPatterns":["**"],"contentSynchronisation":{"enabled":true,"statistics":{"enabled":false},"properties":{"enabled":true},"source":{"originAbsenceDetection":true}},"customHttpHeaders":[{"name":"X-Public","value":"value <>&","sensitive":false},{"name":"X-Private","value":"private-fixture","sensitive":true}]}`,
			want: map[string]string{
				"proxyRef":                                             "corp",
				"vcs/type":                                             "git",
				"vcs/git/provider":                                     "github",
				"vcs/git/downloadUrl":                                  "https://git.invalid",
				"nuget/feedContextPath":                                "v2",
				"pypi/repositorySuffix":                                "simple",
				"externalDependencies/enabled":                         "false",
				"externalDependencies/patterns/pattern":                "**",
				"contentSynchronisation/statistics/enabled":            "false",
				"contentSynchronisation/properties/enabled":            "true",
				"contentSynchronisation/source/originAbsenceDetection": "true",
			},
		},
		{
			name: "virtual",
			body: `{"key":"virtual","rclass":"virtual","packageType":"generic","repositories":["local"],"p2Urls":["https://p2.invalid"],"virtualRetrievalCachePeriodSecs":0,"optionalIndexCompressionFormats":["bz2"],"projectKey":"private-project","environments":["DEV"]}`,
			want: map[string]string{
				"repositories/repositoryRef": "local",
				"p2/urls/url":                "https://p2.invalid",
				"virtualCacheConfig/virtualRetrievalCachePeriodSecs": "0",
				"debianOptionalIndexCompressionFormats/debianFormat": "bz2",
			},
		},
		{
			name: "federated",
			body: `{"key":"federated","rclass":"federated","packageType":"generic","members":[{"url":"https://peer.invalid/repo","enabled":false}],"disableProxy":false}`,
			want: map[string]string{
				"federatedMembers/federatedMember/url":     "https://peer.invalid/repo",
				"federatedMembers/federatedMember/enabled": "false",
				"disableProxy": "false",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg repo.Config
			if err := json.Unmarshal([]byte(tc.body), &cfg); err != nil {
				t.Fatal(err)
			}
			fields, err := exportRepository(cfg)
			if err != nil {
				t.Fatal(err)
			}
			body, err := xml.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			var node exportNode
			if err := xml.Unmarshal(body, &node); err != nil {
				t.Fatal(err)
			}
			for path, want := range tc.want {
				if got := xmlText(t, node, path); got != want {
					t.Errorf("%s = %q, want %q", path, got, want)
				}
			}
			for _, private := range []string{"private-fixture", "private-project", "<environments>"} {
				if strings.Contains(string(body), private) {
					t.Error("non-descriptor data reached XML", private)
				}
			}
		})
	}
}

func TestUnitExportPropertySetProviderMapValues(t *testing.T) {
	d := depsOf(t, inmem.New())
	plan, err := Parse([]byte(`propertySets:
  provider-set:
    properties:
      release:
        predefinedValues:
          'a <>&':
            defaultValue: true
          z-last:
            defaultValue: false
`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(t.Context(), d, plan, now, false); err != nil {
		t.Fatal(err)
	}
	_, body := exportXML(t, d)
	var document struct {
		Values []struct {
			Value   string `xml:"value"`
			Default *bool  `xml:"defaultValue"`
		} `xml:"propertySets>propertySet>properties>property>predefinedValues>predefinedValue"`
	}
	if err := xml.Unmarshal([]byte(body), &document); err != nil {
		t.Fatal(err)
	}
	values := document.Values
	if len(values) != 2 || values[0].Value != "a <>&" || values[1].Value != "z-last" || values[0].Default == nil || !*values[0].Default || values[1].Default == nil ||
		*values[1].Default {
		t.Fatal("provider names/defaults were lost or reordered", body)
	}
}

func TestUnitExportRepositoryPreservesAbsentDebianFlags(t *testing.T) {
	fields, err := exportRepository(repo.Config{Key: "debian", Class: repo.Local, PackageType: repo.Debian})
	if err != nil {
		t.Fatal(err)
	}
	body, err := xml.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "<ddebSupported>") || strings.Contains(string(body), "<debianTrivialLayout>") {
		t.Fatal("REST defaults replaced absent XML configuration", string(body))
	}
}

func TestUnitExportDoesNotPromoteStoredValueKeysToXMLNames(t *testing.T) {
	db := inmem.New()
	// An unexpected structured value can exist in an older configuration
	// document. Its keys must never become XML structure in the read model.
	if err := db.Configs().Put(t.Context(), configdoc.ConfigDocument{
		Kind: configdoc.ConfigPropertySet, Key: "old-set",
		Document: []byte(`{"properties":{"release":{"multipleChoice":{"injected":"data <>&"}}}}`),
	}); err != nil {
		t.Fatal(err)
	}
	_, body := exportXML(t, depsOf(t, db))
	if strings.Contains(body, "<injected>") {
		t.Fatal("stored value keys became XML names", body)
	}
}

func TestUnitExportRejectsPartialAuthorityBindings(t *testing.T) {
	for _, name := range []string{"current caller only", "authorizer only"} {
		t.Run(name, func(t *testing.T) {
			d := depsOf(t, inmem.New())
			if name == "current caller only" {
				d.Authenticator = &accessapitest.Authenticator{CurrentFunc: func(context.Context, kernel.Principal) (kernel.Principal, error) {
					return kernel.Principal{Name: "admin", Admin: false}, nil
				}}
			} else {
				d.Authorizer = &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
					return &accessapitest.Decider{Admin: true}, nil
				}}
			}
			ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
			body, err := NewAPI(d).Export.XML(ctx)
			if !errors.Is(err, kernel.ErrStore) || body != nil {
				t.Fatalf("partial authority binding published XML: %q %v", body, err)
			}
		})
	}
}

func TestUnitExportJavaOnlyRemoteFields(t *testing.T) {
	d := depsOf(t, inmem.New())
	flag := false
	if err := d.Repositories.Create(t.Context(), repo.Config{
		Key: "java-remote", Class: repo.Remote, PackageType: repo.HuggingFaceML, URL: "https://upstream.invalid/repo",
		RetrieveSha256FromServer: &flag, MLRepoLayout: &flag,
	}); err != nil {
		t.Fatal(err)
	}
	node, _ := exportXML(t, d)
	for _, path := range []string{"retrieveSha256FromServer", "huggingFace/mlRepoLayout"} {
		if got := xmlText(t, node, "remoteRepositories/remoteRepository/"+path); got != "false" {
			t.Fatalf("%s = %q, want explicit false", path, got)
		}
	}
	patch, err := Parse([]byte("remoteRepositories:\n  java-remote:\n    retrieveSha256FromServer: true\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(t.Context(), d, patch, now, false); err != nil {
		t.Fatal(err)
	}
	node, _ = exportXML(t, d)
	if got := xmlText(t, node, "remoteRepositories/remoteRepository/retrieveSha256FromServer"); got != "true" {
		t.Fatal("PATCH-owned Java-only field did not round-trip", got)
	}
}

func TestUnitExportUsesXMLPackageTypeEnumValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind repo.PackageType
		want string
	}{
		{"generic", repo.Generic, "generic"},
		{"rpm", repo.RPM, "yum"},
		{"helm oci", repo.HelmOCI, "helmOci"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields, err := exportRepository(repo.Config{Key: "enum", Class: repo.Local, PackageType: tc.kind})
			if err != nil {
				t.Fatal(err)
			}
			body, err := xml.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			var node exportNode
			if err := xml.Unmarshal(body, &node); err != nil {
				t.Fatal(err)
			}
			if got := xmlText(t, node, "type"); got != tc.want {
				t.Fatalf("XML type = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUnitExportTrustedEmbeddingAndCompleteAuthority(t *testing.T) {
	for _, name := range []string{"trusted embedding", "complete HTTP authority"} {
		t.Run(name, func(t *testing.T) {
			d := depsOf(t, inmem.New())
			if name == "complete HTTP authority" {
				d.Authenticator = &accessapitest.Authenticator{CurrentFunc: func(context.Context, kernel.Principal) (kernel.Principal, error) {
					return kernel.Principal{Name: "current-admin", Admin: true}, nil
				}}
				d.Authorizer = &accessapitest.Authorizer{DecideFunc: func(_ context.Context, subject accessapi.Subject, _ bool) (accessapi.Decider, error) {
					if subject.Name != "current-admin" {
						t.Fatalf("authorized stale caller %q", subject.Name)
					}
					return &accessapitest.Decider{Admin: true}, nil
				}}
			}
			ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "initial-admin", Admin: true})
			body, err := NewAPI(d).Export.XML(ctx)
			if err != nil || len(body) == 0 {
				t.Fatalf("valid authority binding failed: %q %v", body, err)
			}
		})
	}
}

func TestUnitExportJAXBTransientAndClassSpecificValues(t *testing.T) {
	flag := true
	for _, class := range []repo.Class{repo.Local, repo.Remote} {
		t.Run(string(class), func(t *testing.T) {
			cfg := repo.Config{
				Key: "audit", Class: class, PackageType: repo.HuggingFaceML, MLRepoLayout: &flag,
				ProjectKey: "api-only-project", Environments: []string{"api-only-environment"},
				P2ChildOrigins: []string{"https://api-only.invalid"}, DockerProjectID: "api-only-docker-project",
				ForceP2Authentication: &flag, EnableNormalizedVersion: &flag, EncryptStates: &flag,
				ForceMetadataNameVersion: &flag, ForceNonDuplicateChart: &flag,
				SendContext: &flag, PassThrough: &flag, Curated: &flag, EnableConanSupport: &flag,
				PodsCdnURL: "https://api-only-cdn.invalid",
			}
			fields, err := exportRepository(cfg)
			if err != nil {
				t.Fatal(err)
			}
			body, err := xml.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"projectKey", "environments", "p2ChildOrigins", "dockerProjectId", "forceP2Authentication", "enableNormalizedVersion", "encryptStates", "forceMetadataNameVersion", "forceNonDuplicateChart", "sendContext", "passThrough", "curated", "enableConanSupport", "podsCdnUrl"} {
				if strings.Contains(string(body), "<"+name+">") {
					t.Error("API-only or XmlTransient field exported", name)
				}
			}
			if got := strings.Contains(string(body), "<mlRepoLayout>true</mlRepoLayout>"); got != (class == repo.Remote) {
				t.Fatal("mlRepoLayout did not follow its class-specific JAXB visibility", string(body))
			}
		})
	}
}

func TestUnitExportVCSUsesXMLEnumValues(t *testing.T) {
	for _, tc := range []struct {
		provider repo.VCSGitProvider
		want     string
	}{
		{repo.GitHub, "github"},
		{repo.GitHubEnterprise, "githubenterprise"},
		{repo.GitLab, "gitlab"},
		{repo.Bitbucket, "bitbucket"},
		{repo.OldStash, "oldstash"},
		{repo.Stash, "stash"},
		{repo.ArtifactoryVCS, "artifactory"},
		{repo.CustomVCS, "custom"},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			fields, err := exportRepository(repo.Config{Key: "vcs", Class: repo.Remote, PackageType: repo.VCS, VCSType: "GIT", VCSGitProvider: tc.provider})
			if err != nil {
				t.Fatal(err)
			}
			body, err := xml.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			var node exportNode
			if err := xml.Unmarshal(body, &node); err != nil {
				t.Fatal(err)
			}
			if xmlText(t, node, "vcs/type") != "git" || xmlText(t, node, "vcs/git/provider") != tc.want {
				t.Fatal("Jackson enum spelling reached XML", string(body))
			}
		})
	}
}
