package descriptor

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func noEnv(string) (string, bool) { return "", false }

func env(vars map[string]string) Lookup {
	return func(name string) (string, bool) { v, ok := vars[name]; return v, ok }
}

// jfrogExample is JFrog's documented descriptor example (research 06) with
// every block the mapping handles.
const jfrogExample = `
localRepositories:
  docker-local:
    type: docker
    repoLayout: simple-default
    xray:
      enabled: false
  gone-local: ~
remoteRepositories:
  maven-central:
    type: maven
    url: https://repo1.maven.org/maven2/
    retrievalCachePeriodSecs: 7200
    password: ${UPSTREAM_PASSWORD}
    nuget:
      feedContextPath: api/v2
    vcs:
      type: GIT
      git:
        provider: GITHUB
        downloadUrl: https://example.test/dl
    externalDependencies:
      enabled: true
      patterns: ["**/*"]
      remoteRepo: npm-remote
    helm:
      chartsBaseUrl: https://charts.example
    bower:
      bowerRegistryUrl: https://bower.example
    composer:
      composerRegistryUrl: https://packagist.example
    pypi:
      registryUrl: https://pypi.example
      repositorySuffix: simple
virtualRepositories:
  maven-all:
    type: maven
    repositories:
      - maven-local
      - maven-central
    virtualCacheConfig:
      virtualRetrievalCachePeriodSecs: 600
proxies:
  corp:
    host: proxy.example.com
    port: 3128
    platformDefault: true
  old-proxy: null
repoLayouts:
  custom-layout:
    artifactPathPattern: "[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"
propertySets:
  build:
    visible: true
`

func field(t *testing.T, c RepoChange, name string) any {
	t.Helper()
	raw, ok := c.Fields[name]
	if !ok {
		t.Fatalf("%s has no field %s: %v", c.Key, name, c.Fields)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseJFrogExample(t *testing.T) {
	p, err := Parse([]byte(jfrogExample), env(map[string]string{"UPSTREAM_PASSWORD": "s3cret"}))
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, c := range p.Repos {
		keys = append(keys, c.Section+"."+c.Key)
	}
	if got := strings.Join(
		keys,
		" ",
	); got != "localRepositories.docker-local localRepositories.gone-local "+
		"remoteRepositories.maven-central virtualRepositories.maven-all" {
		t.Fatalf("repository order %s", got)
	}
	docker, gone, remote, virtual := p.Repos[0], p.Repos[1], p.Repos[2], p.Repos[3]
	if !gone.Delete || docker.Class != repo.Local || remote.Class != repo.Remote ||
		virtual.Class != repo.Virtual {
		t.Fatalf("classes or delete wrong: %+v", p.Repos)
	}
	for name, want := range map[string]any{
		"packageType": "docker", "repoLayoutRef": "simple-default", "xrayIndex": false,
		"key": "docker-local", "rclass": "local",
	} {
		if got := field(t, docker, name); got != want {
			t.Errorf("docker-local %s = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]any{
		"password": "s3cret", "feedContextPath": "api/v2", "vcsType": "GIT",
		"vcsGitProvider": "GITHUB", "vcsGitDownloadUrl": "https://example.test/dl",
		"externalDependenciesEnabled": true, "externalDependenciesRemoteRepo": "npm-remote",
		"chartsBaseUrl": "https://charts.example", "bowerRegistryUrl": "https://bower.example",
		"composerRegistryUrl": "https://packagist.example", "pyPIRegistryUrl": "https://pypi.example",
		"pyPIRepositorySuffix": "simple", "retrievalCachePeriodSecs": float64(7200),
	} {
		if got := field(t, remote, name); got != want {
			t.Errorf("maven-central %s = %v, want %v", name, got, want)
		}
	}
	if got := field(t, virtual, "virtualRetrievalCachePeriodSecs"); got != float64(600) {
		t.Errorf("virtual cache period %v", got)
	}
	kinds := []string{}
	for _, c := range p.Configs {
		kinds = append(kinds, string(c.Kind)+":"+c.Key)
	}
	if got := strings.Join(
		kinds,
		" ",
	); got != "proxy:corp proxy:old-proxy property_set:build layout:custom-layout" {
		t.Fatalf("configuration documents %s", got)
	}
	if !p.Configs[1].Delete || p.Configs[0].Kind != types.ConfigProxy {
		t.Fatalf("proxy changes %+v", p.Configs[:2])
	}
	// Every scalar, null and list element: 4 in docker-local and gone-local,
	// 16 in maven-central, 4 in maven-all, 4 in the proxies, 1 layout
	// pattern and 1 property set flag.
	if p.Changes != 30 {
		t.Fatalf("changes %d", p.Changes)
	}
}

// documentedKeys holds the samples on JFrog's "Repositories Configurations in
// Artifactory YAML" page, one value chosen where the page lists several. The
// page leaves includesPattern and excludesPattern empty, which YAML reads as
// null.
const documentedKeys = `
localRepositories:
  my-local-repo:
    type: rpm
    checksumPolicyType: server-generated-checksums
    description: "Hello world"
    dockerApiVersion: V2
    excludesPattern:
    includesPattern:
    maxUniqueSnapshots: 0
    maxUniqueTags: 0
    notes: "Hello world 2"
    propertySets:
      - set1
      - set2
    repoLayout: maven-2-default
    snapshotVersionBehavior: unique
    xray:
      enabled: false
    yumGroupFileNames: yumGroup1
    yumRootDepth: 0
    blackedOut: false
    calculateYumMetadata: false
    debianTrivialLayout: false
    enableFileListsIndexing: false
    forceNugetAuthentication: false
    handleReleases: true
    handleSnapshots: true
    suppressPomConsistencyChecks: true
    downloadRedirect:
      enabled: true
    cdnRedirect: false
    blockPushingSchema1: false
    primaryKeyPairDescriptor: "mygpgkey"
    secondaryKeyPairRef: "mysecgpgkey"
  tf-state:
    type: terraformbe
remoteRepositories:
  my-remote-repo:
    type: cocoapods
    url: https://repo1.maven.org/maven2/
    username: user
    password: password
    assumedOfflinePeriodSecs: 300
    remoteRepoChecksumPolicyType: generate-if-absent
    clientTlsCertificate: tls-certificate
    description: "Hello world"
    dockerApiVersion: V2
    excludesPattern: excludingremote1
    includesPattern: includingremote1
    maxUniqueSnapshots: 0
    maxUniqueTags: 0
    blockMismatchingMimeTypes: true
    mismatchingMimeTypesOverrideList: xml
    missedRetrievalCachePeriodSecs: 1800
    notes: "Hello world 2"
    propertySets:
      - set1
      - set2
    proxy: proxy1
    disableProxy: false
    queryParams: query-params&for-my-remote-repo
    repoLayout: nameremote1
    remoteRepoLayout: simple-default
    retrievalCachePeriodSecs: 600
    socketTimeoutMillis: 15000
    unusedArtifactsCleanupPeriodHours: 0
    allowAnyHostAuth: false
    blackedOut: false
    bypassHeadRequests: false
    enableCookieManagement: false
    enableTokenAuthentication: false
    fetchJarsEagerly: false
    fetchSourcesEagerly: false
    forceNugetAuthentication: false
    handleReleases: true
    handleSnapshots: true
    listRemoteFolderItems: true
    offline: false
    synchronizeProperties: false
    xray:
      enabled: false
    bower:
      bowerRegistryUrl: http://someurl.com
    cocoaPods:
      cocoaPodsSpecsRepoUrl: http://specs.example
      specRepoProvider:
        provider: github
        downloadUrl: http://someurl.com
    nuget:
      downloadContextPath: api/v2/package
      v3FeedUrl: "https://api.nuget.org/v3/index.json"
      feedContextPath: api/v2
    helm:
      chartsBaseUrl: https://github.com/newrelic/helm-charts/releases/download/
    composer:
      composerRegistryUrl: http://someurl.com
    vcs:
      git:
        provider: github
        downloadUrl: http://someurl.com
      type: git
    downloadRedirect:
      enabled: false
    blockPushingSchema1: false
    priorityResolution: false
    disableUrlNormalization: false
    customHttpHeaders:
      - name: X-Api-Key
        value: "example"
        sensitive: false
    externalDependencies:
      enabled: false
      patterns:
        - "**/github.com/**"
    contentSynchronisation:
      enabled: true
      statistics:
        enabled: true
      properties:
        enabled: true
      source:
        originAbsenceDetection: true
virtualRepositories:
  virtual1:
    type: p2
    repositories:
      - my-local-repo
      - my-remote-repo
    description: ""
    notes: ""
    dockerApiVersion: V2
    includesPattern:
    excludesPattern:
    externalDependencies:
      enabled: false
      patterns:
        - "**/github.com/**"
    keyPair: keypairvirtual1
    p2:
      urls:
        - http://someurl.com
        - http://someurl2.com
    pomRepositoryReferencesCleanupPolicy: discard_any_reference
    repoLayout: maven-2-default
    virtualCacheConfig:
      virtualRetrievalCachePeriodSecs: 600
    artifactoryRequestsCanRetrieveRemoteArtifacts: false
    forceNugetAuthentication: false
    forceMavenAuthentication: false
`

func TestParseDocumentedKeys(t *testing.T) {
	p, err := Parse([]byte(documentedKeys), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]RepoChange{}
	for _, c := range p.Repos {
		byKey[c.Key] = c
	}
	for key, want := range map[string]map[string]any{
		"my-local-repo": {
			"downloadRedirect": true, "primaryKeyPairRef": "mygpgkey",
			"yumGroupFileNames": "yumGroup1", "repoLayoutRef": "maven-2-default",
			"includesPattern": nil, "excludesPattern": nil, "xrayIndex": false,
		},
		"tf-state": {"packageType": "terraformbackend"},
		"my-remote-repo": {
			"downloadRedirect": false, "remoteRepoLayoutRef": "simple-default",
			"podsSpecsRepoUrl": "http://specs.example", "vcsGitProvider": "github",
			"vcsGitDownloadUrl": "http://someurl.com", "vcsType": "git",
		},
		"virtual1": {"virtualRetrievalCachePeriodSecs": float64(600)},
	} {
		for name, value := range want {
			if got := field(t, byKey[key], name); got != value {
				t.Errorf("%s %s = %v, want %v", key, name, got, value)
			}
		}
	}
	urls, _ := field(t, byKey["virtual1"], "p2Urls").([]any)
	if len(urls) != 2 || urls[1] != "http://someurl2.com" {
		t.Errorf("p2Urls %v", urls)
	}
	// The specs repository's provider has no field in the repositories API.
	for name := range byKey["my-remote-repo"].Fields {
		if strings.Contains(strings.ToLower(name), "specrepo") {
			t.Errorf("stored %s", name)
		}
	}
}

// TestParseDownloadRedirectSpellings takes the flag as the JSON API writes
// it as well as the descriptor's block.
func TestParseDownloadRedirectSpellings(t *testing.T) {
	for doc, want := range map[string]any{
		"localRepositories:\n  l:\n    downloadRedirect: true\n":                 true,
		"localRepositories:\n  l:\n    downloadRedirect: \"false\"\n":            "false",
		"localRepositories:\n  l:\n    downloadRedirect:\n      enabled: true\n": true,
	} {
		p, err := Parse([]byte(doc), noEnv)
		if err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		if got := field(t, p.Repos[0], "downloadRedirect"); got != want {
			t.Errorf("%q: %v", doc, got)
		}
	}
}

// TestParseNullOnlyOnStrings follows JFrog's rule that a repository field may
// be null only when it holds a string, and never type.
func TestParseNullOnlyOnStrings(t *testing.T) {
	for _, name := range []string{
		"description", "notes", "includesPattern", "username", "password", "proxy",
		"repoLayout", "remoteRepoLayout", "primaryKeyPairDescriptor", "yumGroupFileNames",
		"checksumPolicyType", "key", "rclass",
	} {
		doc := "remoteRepositories:\n  r:\n    " + name + ": null\n"
		if _, err := Parse([]byte(doc), noEnv); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for doc, where := range map[string]string{
		"blackedOut: null":                    "blackedOut",
		"type: ~":                             "type",
		"packageType: null":                   "packageType",
		"propertySets: null":                  "propertySets",
		"retrievalCachePeriodSecs: null":      "retrievalCachePeriodSecs",
		"downloadRedirect: null":              "downloadRedirect",
		"downloadRedirect:\n      enabled: ~": "downloadRedirect.enabled",
		"xray: null":                          "xray",
		"xray:\n      enabled: null":          "xray.enabled",
		"vcs: null":                           "vcs",
		"vcs:\n      git: null":               "vcs.git",
		"p2:\n      urls: null":               "p2.urls",
		"p2Urls: null":                        "p2Urls",
		"contentSynchronisation: null":        "contentSynchronisation",
	} {
		_, err := Parse([]byte("remoteRepositories:\n  r:\n    "+doc+"\n"), noEnv)
		want := "remoteRepositories.r." + where + " cannot be null"
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", doc, err, want)
		}
	}
	// A string inside a block may be null; the provider of the specs
	// repository, which is not stored, may be anything.
	for _, doc := range []string{
		"helm:\n      chartsBaseUrl: null",
		"vcs:\n      git:\n        downloadUrl: ~",
		"cocoaPods:\n      cocoaPodsSpecsRepoUrl: null\n      specRepoProvider:\n        provider: null",
	} {
		if _, err := Parse([]byte("remoteRepositories:\n  r:\n    "+doc+"\n"), noEnv); err != nil {
			t.Errorf("%s: %v", doc, err)
		}
	}
}

// TestParseRefusesUnappliedSections names every section that is not
// applied, before resolving any variable inside one.
func TestParseRefusesUnappliedSections(t *testing.T) {
	doc := `
localRepositories:
  l:
    type: generic
security:
  ldapSettings:
    corp:
      search:
        managerPassword: ${NOT_SET}
mailServer:
  host: smtp.example
backups: null
urlBase: https://example.test
`
	_, err := Parse([]byte(doc), noEnv)
	want := "sections not supported: backups, mailServer, security, urlBase; " +
		"the supported sections are localRepositories, remoteRepositories, virtualRepositories, federatedRepositories, " +
		"proxies, propertySets, repoLayouts"
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
		t.Fatalf("%v", err)
	}
}

// TestParseCountsValues counts as Artifactory's MapToDiffConverter does.
func TestParseCountsValues(t *testing.T) {
	for doc, want := range map[string]int{
		"localRepositories: {}\n":                                    0,
		"localRepositories:\n  gone: null\n":                         1,
		"localRepositories:\n  l:\n    type: generic\n":              1,
		"virtualRepositories:\n  v:\n    repositories: []\n":         0,
		"virtualRepositories:\n  v:\n    repositories: [a, b, c]\n":  3,
		"localRepositories:\n  l:\n    xray:\n      enabled: true\n": 1,
		"propertySets:\n  s:\n    properties:\n      p:\n        predefinedValues:\n" +
			"          - {value: a, defaultValue: true}\n          - {value: b}\n": 3,
		"localRepositories:\n  a:\n    type: generic\n    notes: ~\n  b: ~\n" +
			"proxies:\n  p:\n    host: h\n    port: 80\n": 5,
	} {
		p, err := Parse([]byte(doc), noEnv)
		if err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		if p.Changes != want {
			t.Errorf("%q: %d, want %d", doc, p.Changes, want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":                "",
		"only a comment":       "# nothing\n",
		"not a mapping":        "- a\n- b\n",
		"bad yaml":             "a: [",
		"section scalar":       "localRepositories: 3\n",
		"section null":         "localRepositories: null\n",
		"entry scalar":         "localRepositories:\n  x: 3\n",
		"unknown field":        "localRepositories:\n  x:\n    nonsense: 1\n",
		"unknown block field":  "localRepositories:\n  x:\n    xray:\n      enabled: true\n      depth: 2\n",
		"block scalar":         "localRepositories:\n  x:\n    xray: yes-please\n",
		"unknown in new block": "localRepositories:\n  x:\n    downloadRedirect:\n      enabled: true\n      ttl: 2\n",
		"unapplied section":    "localRepositories: {}\nbackups: {}\n",
		"nested block":         "remoteRepositories:\n  x:\n    vcs:\n      git:\n        colour: blue\n",
		"key mismatch":         "localRepositories:\n  x:\n    key: y\n",
		"rclass mismatch":      "localRepositories:\n  x:\n    rclass: remote\n",
		"unset env":            "remoteRepositories:\n  x:\n    password: ${NOT_SET}\n",
		"unset env in list":    "virtualRepositories:\n  v:\n    repositories: [\"${NOT_SET}\"]\n",
		"proxy unknown field":  "proxies:\n  p:\n    colour: blue\n",
		"proxy scalar":         "proxies:\n  p: 3\n",
		"proxies scalar":       "proxies: 3\n",
		"unencodable field":    "localRepositories:\n  x:\n    contentSynchronisation: {[a]: true}\n",
	} {
		if _, err := Parse([]byte(doc), noEnv); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
