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
security:
  anonAccessEnabled: true
backups: {}
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
	if strings.Join(p.Ignored, ",") != "backups,security" {
		t.Fatalf("ignored %v", p.Ignored)
	}
}

func TestParseNullBlockResets(t *testing.T) {
	p, err := Parse([]byte("remoteRepositories:\n  r:\n    xray: null\n    vcs: null\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"xrayIndex", "vcsType", "vcsGitProvider", "vcsGitDownloadUrl"} {
		if field(t, p.Repos[0], name) != nil {
			t.Errorf("%s not reset", name)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":               "",
		"only a comment":      "# nothing\n",
		"not a mapping":       "- a\n- b\n",
		"bad yaml":            "a: [",
		"section scalar":      "localRepositories: 3\n",
		"section null":        "localRepositories: null\n",
		"entry scalar":        "localRepositories:\n  x: 3\n",
		"unknown field":       "localRepositories:\n  x:\n    nonsense: 1\n",
		"unknown block field": "localRepositories:\n  x:\n    xray:\n      enabled: true\n      depth: 2\n",
		"block scalar":        "localRepositories:\n  x:\n    xray: yes-please\n",
		"nested block":        "remoteRepositories:\n  x:\n    vcs:\n      git:\n        colour: blue\n",
		"key mismatch":        "localRepositories:\n  x:\n    key: y\n",
		"rclass mismatch":     "localRepositories:\n  x:\n    rclass: remote\n",
		"unset env":           "remoteRepositories:\n  x:\n    password: ${NOT_SET}\n",
		"unset env in list":   "virtualRepositories:\n  v:\n    repositories: [\"${NOT_SET}\"]\n",
		"proxy unknown field": "proxies:\n  p:\n    colour: blue\n",
		"proxy scalar":        "proxies:\n  p: 3\n",
		"proxies scalar":      "proxies: 3\n",
		"unencodable field":   "localRepositories:\n  x:\n    contentSynchronisation: {[a]: true}\n",
	} {
		if _, err := Parse([]byte(doc), noEnv); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
