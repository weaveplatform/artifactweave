package descriptor

import (
	"context"
	"encoding/xml"
	"errors"
	"slices"
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
		// name[field=value] selects the named children whose field has value.
		name, predicate, _ := strings.Cut(strings.TrimSuffix(part, "]"), "[")
		field, value, _ := strings.Cut(predicate, "=")
		var next []exportNode
		for _, node := range nodes {
			for _, child := range node.Children {
				if child.Name.Local == name && (predicate == "" || slices.ContainsFunc(child.Children, func(c exportNode) bool {
					return c.Name.Local == field && c.Text == value
				})) {
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
    services: jfrt,jfxr
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
		"proxies/proxy/key":                                 "proxy <>&",
		"proxies/proxy/services":                            "jfrt,jfxr",
		"proxies/proxy/redirectedToHosts":                   "one.invalid,two.invalid",
		"propertySets/propertySet/name":                     "set <>&",
		"propertySets/propertySet/visible":                  "false",
		"propertySets/propertySet/properties/property/name": "property <>&",
		"propertySets/propertySet/properties/property/predefinedValues/predefinedValue/value":        "value <>&",
		"propertySets/propertySet/properties/property/predefinedValues/predefinedValue/defaultValue": "true",
		"repoLayouts/repoLayout/name":                             "layout <>&",
		"repoLayouts/repoLayout/descriptorPathPattern":            "",
		"repoLayouts/repoLayout/distinctiveDescriptorPathPattern": "false",
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
	// 7.161.26 serves its repository sections empty, the repositories living in
	// its repository configuration store: the patch still created all four,
	// and the export names none of them.
	for _, key := range []string{"export-local", "export-remote", "export-virtual", "export-federated"} {
		if _, err := d.Repositories.Get(t.Context(), key); err != nil {
			t.Fatal("descriptor repository not created", key, err)
		}
		if strings.Contains(body, key) {
			t.Error("repository reached the XML", key)
		}
	}
	assertEmptyRepositorySections(t, node)
}

// assertEmptyRepositorySections checks that the export carries each of
// 7.161.26's repository sections, in XSD order and with no entry.
func assertEmptyRepositorySections(t *testing.T, node exportNode) {
	t.Helper()
	var got []string
	for _, section := range node.Children {
		if strings.HasSuffix(section.Name.Local, "Repositories") {
			got = append(got, section.Name.Local)
			if len(section.Children) != 0 {
				t.Errorf("%s carries %d entries", section.Name.Local, len(section.Children))
			}
		}
	}
	want := []string{"localRepositories", "remoteRepositories", "virtualRepositories", "federatedRepositories", "releaseBundlesRepositories"}
	if !slices.Equal(got, want) {
		t.Errorf("repository sections = %v, want %v", got, want)
	}
}

func TestUnitExportEmptyAndOptionalState(t *testing.T) {
	db := inmem.New()
	d := depsOf(t, db)
	node, body := exportXML(t, d)
	if xmlText(t, node, "security/anonAccessEnabled") != "false" {
		t.Fatal("unset anonymous access was not off")
	}
	for _, absent := range []string{"<mailServer>", "<downloadRedirectConfig>", "<backup>", "<ldapSettings>"} {
		if strings.Contains(body, absent) {
			t.Error("invented configuration", absent)
		}
	}
	assertEmptyRepositorySections(t, node)
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
	exportTransactions
	begin bool
}

func (s exportCommitFailure) Tx(ctx context.Context, fn func(context.Context) error) error {
	if s.begin {
		return errExportFailure
	}
	if err := s.exportTransactions.Tx(ctx, fn); err != nil {
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
				d.Tx = exportCommitFailure{exportTransactions: db, begin: true}
			case "commit":
				d.Tx = exportCommitFailure{exportTransactions: db}
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

func TestUnitExportLegacyProxyServiceList(t *testing.T) {
	db := inmem.New()
	// Older writes accepted lists here. The XML projection still reads them
	// although new writes follow ProxyType's xs:string/provider string field.
	legacy := configdoc.ConfigDocument{
		Kind: configdoc.ConfigProxy, Key: "legacy-services", Enabled: true,
		Document: []byte(`{"host":"proxy.invalid","port":3128,"services":["jfrt","jfxr"]}`),
	}
	if err := db.Configs().Put(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	node, _ := exportXML(t, depsOf(t, db))
	if got := xmlText(t, node, "proxies/proxy/services"); got != "jfrt,jfxr" {
		t.Fatalf("legacy services projection = %q", got)
	}
	after, err := db.Configs().Get(t.Context(), legacy.Kind, legacy.Key)
	if err != nil || !slices.Equal(after.Document, legacy.Document) {
		t.Fatal("reading legacy proxy services rewrote stored data", err)
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

func TestUnitExportRefusesAmbientTransaction(t *testing.T) {
	for _, outcome := range []string{"commit", "rollback"} {
		t.Run(outcome, func(t *testing.T) { testExportAmbientTransaction(t, inmem.New(), outcome) })
	}
}

type exportRunnerWithoutState struct{ tx.Runner }

func TestUnitExportRequiresTransactionStateProbe(t *testing.T) {
	d := depsOf(t, inmem.New())
	d.Tx = exportRunnerWithoutState{Runner: d.Tx}
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	body, err := NewAPI(d).Export.XML(ctx)
	if !errors.Is(err, kernel.ErrStore) || body != nil {
		t.Fatalf("unverifiable transaction published XML: %q %v", body, err)
	}
}

func TestUnitExportOwnsSnapshotInsideAnotherStoreTransaction(t *testing.T) {
	d := depsOf(t, inmem.New())
	other := inmem.New()
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	err := other.Tx(ctx, func(ctx context.Context) error {
		body, err := NewAPI(d).Export.XML(ctx)
		if err != nil || len(body) == 0 {
			t.Fatalf("independent committed snapshot failed: %q %v", body, err)
		}
		return errExportFailure
	})
	if !errors.Is(err, errExportFailure) {
		t.Fatal("other store's rollback changed", err)
	}
}

func TestUnitExportMailUsesJAXBElementOrder(t *testing.T) {
	d := depsOf(t, inmem.New())
	patch, err := Parse([]byte(`mailServer:
  enabled: false
  host: smtp.invalid
  port: 2525
  username: mail-user
  password: private-mail-fixture
  from: sender@example.invalid
  subjectPrefix: prefix
  tls: false
  ssl: false
  artifactoryUrl: https://registry.invalid
`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(t.Context(), d, patch, now, false); err != nil {
		t.Fatal(err)
	}
	node, _ := exportXML(t, d)
	var got []string
	for _, section := range node.Children {
		if section.Name.Local == "mailServer" {
			for _, field := range section.Children {
				got = append(got, field.Name.Local)
			}
		}
	}
	want := []string{"enabled", "host", "port", "username", "from", "subjectPrefix", "tls", "ssl", "artifactoryUrl"}
	if !slices.Equal(got, want) {
		t.Fatalf("mail elements = %v, want JAXB sequence %v with credentials omitted", got, want)
	}
}
