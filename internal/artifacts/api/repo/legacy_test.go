package repo_test

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// legacyFlags are the JSON names of the legacy enable<Type>Support flags.
var legacyFlags = []string{
	"enableBowerSupport", "enableChefSupport", "enableCocoaPodsSupport", "enableComposerSupport",
	"enableConanSupport", "enableDebianSupport", "enableDistRepoSupport", "enableDockerSupport",
	"enableGemsSupport", "enableGitLfsSupport", "enableNpmSupport", "enableNuGetSupport",
	"enablePuppetSupport", "enablePypiSupport", "enableVagrantSupport",
}

func fieldsOf(t *testing.T, c repo.Config) map[string]any {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// The v1 read of every class carries every legacy flag, true for the
// repository's own type and false for the rest, whatever the client sent
// (RTFACT-29415's Docker local, RTFACT-30672's npm local and RTFACT-31100's
// Maven local; 7.161.26's setPackageTypeConfig sets enableDockerSupport for
// OCI and Helm OCI too). The v2 read carries none (#1479).
func TestUnitFullCarriesEveryLegacyFlag(t *testing.T) {
	for _, tc := range []struct {
		pt  repo.PackageType
		own string
	}{
		{repo.NPM, "enableNpmSupport"},
		{repo.Docker, "enableDockerSupport"},
		{repo.OCI, "enableDockerSupport"},
		{repo.HelmOCI, "enableDockerSupport"},
		{repo.Conan, "enableConanSupport"},
		{repo.NuGet, "enableNuGetSupport"},
		{repo.GitLFS, "enableGitLfsSupport"},
		{repo.Maven, ""},
		{repo.Generic, ""},
		{repo.Conda, ""},
		{repo.P2, ""},
	} {
		for _, class := range []repo.Class{repo.Local, repo.Remote, repo.Virtual, repo.Federated} {
			// A flag the client sent for another type changes nothing.
			c := repo.Config{
				Key: "k", Class: class, PackageType: tc.pt,
				EnableChefSupport: ptr(true), EnableNpmSupport: ptr(false),
			}
			v1 := fieldsOf(t, repo.Full(c))
			want := map[string]any{}
			for _, f := range legacyFlags {
				want[f] = f == tc.own
			}
			got := map[string]any{}
			for _, f := range legacyFlags {
				if v, ok := v1[f]; ok {
					got[f] = v
				}
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("%s %s v1 flags (-want +got):\n%s", class, tc.pt, diff)
			}
			v2 := fieldsOf(t, repo.Relevant(repo.Full(c)))
			for _, f := range legacyFlags {
				if v, ok := v2[f]; ok {
					t.Errorf("%s %s v2 %s %v, want none", class, tc.pt, f, v)
				}
			}
		}
	}
}

// forceNugetAuthentication, forceP2Authentication and
// forceConanAuthentication are on every class's v1 read, false by default
// (RTFACT-30672, RTFACT-31100; 7.161.26's RepositoryConfigurationBase holds
// them for every class, the virtual one included). Each is read from the
// configuration of its own type only, as setPackageTypeConfig does, so
// another type's read gives false whatever it stored. A virtual repository
// is created with all three false, so a Conan or P2 virtual repository's v2
// read carries its flag, as @IncludeTypeSpecific marks each for its type
// and no class (#1479).
func TestUnitForceFlagsOnEveryClass(t *testing.T) {
	flags := []string{
		"forceNugetAuthentication",
		"forceP2Authentication",
		"forceConanAuthentication",
	}
	for _, class := range []repo.Class{repo.Local, repo.Remote, repo.Virtual, repo.Federated} {
		created := *repo.Defaults(&repo.Config{Key: "k", Class: class, PackageType: repo.Conan})
		for _, f := range flags {
			if v, ok := fieldsOf(t, created)[f]; !ok || v != false {
				t.Errorf("%s created %s %v (stored %v), want false", class, f, v, ok)
			}
		}
		for _, pt := range []repo.PackageType{repo.NuGet, repo.P2, repo.Conan, repo.Generic} {
			c := repo.Config{
				Key: "k", Class: class, PackageType: pt,
				ForceNugetAuthentication: ptr(true), ForceP2Authentication: ptr(true),
				ForceConanAuthentication: ptr(true),
			}
			v1 := fieldsOf(t, repo.Full(c))
			unset := fieldsOf(t, repo.Full(repo.Config{Key: "k", Class: class, PackageType: pt}))
			for i, f := range flags {
				own := []repo.PackageType{repo.NuGet, repo.P2, repo.Conan}[i] == pt
				if v1[f] != own || unset[f] != false {
					t.Errorf("%s %s v1 %s %v when set, %v when unset; want %v and false",
						class, pt, f, v1[f], unset[f], own)
				}
			}
		}
	}
	for pt, flag := range map[repo.PackageType]string{
		repo.Conan: "forceConanAuthentication", repo.P2: "forceP2Authentication",
	} {
		c := *repo.Defaults(&repo.Config{Key: "v", Class: repo.Virtual, PackageType: pt})
		v2 := fieldsOf(t, repo.Relevant(repo.Full(c)))
		for _, f := range flags {
			if v, ok := v2[f]; (f == flag) != ok || ok && v != false {
				t.Errorf("%s virtual v2 %s %v (present %v)", pt, f, v, ok)
			}
		}
	}
}

// A create without packageType takes its type from the first legacy flag
// sent true, in 7.161.26's getPackageType order (NuGet, Gems, npm, Bower,
// CocoaPods, Debian, Distribution, PyPI, Puppet, Docker, Vagrant, GitLFS,
// Composer, Conan, Chef), else generic. enableConanSupport gives Conan, the
// intended type, where 7.161.26 gives Conda. A packageType that is sent
// wins, and the stored configuration keeps none of the flags (#1479).
func TestUnitCreateTakesItsTypeFromTheLegacyFlags(t *testing.T) {
	order := []struct {
		flag string
		pt   repo.PackageType
	}{
		{"enableNuGetSupport", repo.NuGet},
		{"enableGemsSupport", repo.Gems},
		{"enableNpmSupport", repo.NPM},
		{"enableBowerSupport", repo.Bower},
		{"enableCocoaPodsSupport", repo.CocoaPods},
		{"enableDebianSupport", repo.Debian},
		{"enableDistRepoSupport", repo.Distribution},
		{"enablePypiSupport", repo.PyPI},
		{"enablePuppetSupport", repo.Puppet},
		{"enableDockerSupport", repo.Docker},
		{"enableVagrantSupport", repo.Vagrant},
		{"enableGitLfsSupport", repo.GitLFS},
		{"enableComposerSupport", repo.Composer},
		{"enableConanSupport", repo.Conan},
		{"enableChefSupport", repo.Chef},
	}
	create := func(body string) repo.Config {
		t.Helper()
		var c repo.Config
		if err := json.Unmarshal([]byte(body), &c); err != nil {
			t.Fatal(err)
		}
		return *repo.Defaults(&c)
	}
	for i, o := range order {
		c := create(`{"key":"k","rclass":"local","` + o.flag + `":true}`)
		if c.PackageType != o.pt {
			t.Errorf("%s alone: %q, want %q", o.flag, c.PackageType, o.pt)
		}
		stored := fieldsOf(t, c)
		for _, f := range legacyFlags {
			if _, ok := stored[f]; ok {
				t.Errorf("%s alone: stored %s", o.flag, f)
			}
		}
		// The earlier flag wins over every later one, whichever way the
		// body lists them.
		for _, later := range order[i+1:] {
			body := `{"key":"k","rclass":"local","` + later.flag + `":true,"` + o.flag + `":true}`
			if got := create(body).PackageType; got != o.pt {
				t.Errorf("%s and %s: %q, want %q", later.flag, o.flag, got, o.pt)
			}
		}
	}
	for body, want := range map[string]repo.PackageType{
		`{"key":"k","rclass":"local"}`:                                               repo.Generic,
		`{"key":"k","rclass":"local","enableNpmSupport":false}`:                      repo.Generic,
		`{"key":"k","rclass":"local","packageType":"maven","enableNpmSupport":true}`: repo.Maven,
		`{"key":"k","rclass":"remote","enableConanSupport":true}`:                    repo.Conan,
	} {
		if got := create(body).PackageType; got != want {
			t.Errorf("%s: %q, want %q", body, got, want)
		}
	}
	// No class accepts the Distribution type 7.x gives enableDistRepoSupport.
	dist := create(`{"key":"k","rclass":"local","enableDistRepoSupport":true}`)
	if err := repo.Validate(&dist); err == nil {
		t.Errorf("a distribution repository was accepted")
	}
}

// The v1 read, the v2 read and the configurations list carry signedUrlTtl,
// 90 when it was never set: 7.161.26's RepositoryConfigurationBase holds it
// with that default and marks it @IncludeTypeSpecific for every type and
// class, its v1 dumps show it (RTFACT-30672, RTFACT-31100), and so does the
// configurations list's sample ([D] getallrepositoryconfigurations). A
// reader who is not an administrator sees none (#1479).
func TestUnitSignedURLTTLIsOnEveryRead(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Remote, repo.Virtual, repo.Federated} {
		c := *repo.Defaults(&repo.Config{Key: "k", Class: class, PackageType: repo.NPM})
		for name, got := range map[string]repo.Config{
			"v1": repo.Full(c), "v2": repo.Relevant(repo.Full(c)),
		} {
			if got.SignedURLTTL == nil || *got.SignedURLTTL != 90 {
				t.Errorf("%s %s signedUrlTtl %v, want 90", class, name, got.SignedURLTTL)
			}
		}
		c.SignedURLTTL = ptr(30)
		if got := repo.Full(c).SignedURLTTL; *got != 30 {
			t.Errorf("%s set signedUrlTtl reads %d", class, *got)
		}
		if got := repo.Public(c).SignedURLTTL; got != nil {
			t.Errorf("%s non-admin signedUrlTtl %d", class, *got)
		}
	}
}

// The v1 and v2 reads of a generic local, an RPM remote and a Maven virtual
// repository created with every default, field by field (#1479). The v1
// read carries the legacy flags, the three force flags, both Debian fields
// and signedUrlTtl on every class; the v2 read none of them but
// signedUrlTtl. The other fields' differences from 7.161.26's
// @IncludeTypeSpecific marks are listed in research 03, "The v2 read and
// 7.161.26's annotations".
func TestUnitReadFieldSets(t *testing.T) {
	common := []string{
		"blackedOut", "cdnRedirect", "description", "downloadRedirect", "environments",
		"excludesPattern", "includesPattern", "key", "notes", "packageType", "priorityResolution",
		"rclass", "repoLayoutRef", "signedUrlTtl", "xrayIndex",
	}
	v1Only := slices.Concat(legacyFlags, []string{
		"ddebSupported", "debianTrivialLayout", "forceConanAuthentication",
		"forceNugetAuthentication", "forceP2Authentication",
	})
	local := []string{"archiveBrowsingEnabled", "propertySets"}
	remote := []string{
		"allowAnyHostAuth", "assumedOfflinePeriodSecs", "blockMismatchingMimeTypes",
		"bypassHeadRequests", "disableProxy", "disableUrlNormalization", "enableCookieManagement",
		"enableTokenAuthentication", "externalDependenciesEnabled", "hardFail",
		"listRemoteFolderItems", "metadataRetrievalTimeoutSecs", "missedRetrievalCachePeriodSecs",
		"offline", "propertySets", "remoteRepoChecksumPolicyType", "retrievalCachePeriodSecs",
		"shareConfiguration", "socketTimeoutMillis", "storeArtifactsLocally",
		"synchronizeProperties", "unusedArtifactsCleanupPeriodHours", "url",
	}
	virtual := []string{
		"artifactoryRequestsCanRetrieveRemoteArtifacts", "externalDependenciesEnabled",
		"forceMavenAuthentication", "hideUnauthorizedResources",
		"pomRepositoryReferencesCleanupPolicy", "repositories", "useNamespaces",
		"virtualRetrievalCachePeriodSecs",
	}
	for _, tc := range []struct {
		c      repo.Config
		v1, v2 []string
	}{
		{
			repo.Config{Key: "generic-local", Class: repo.Local},
			slices.Concat(common, v1Only, local, []string{
				"blockPushingSchema1", "checksumPolicyType", "dockerApiVersion",
				"dockerTagRetention", "enableFileListsIndexing", "encryptStates",
				"forceMetadataNameVersion", "forceNonDuplicateChart", "handleReleases",
				"handleSnapshots", "maxUniqueSnapshots", "maxUniqueTags",
				"snapshotVersionBehavior", "suppressPomConsistencyChecks",
			}),
			slices.Concat(common, local),
		},
		{
			repo.Config{
				Key: "rpm-remote", Class: repo.Remote, PackageType: repo.RPM,
				URL: "https://up.example",
			},
			slices.Concat(common, v1Only, remote, []string{
				"blockPushingSchema1", "fetchJarsEagerly", "fetchSourcesEagerly",
				"handleReleases", "handleSnapshots", "maxUniqueSnapshots", "rejectInvalidJars",
				"suppressPomConsistencyChecks",
			}),
			slices.Concat(common, remote),
		},
		{
			repo.Config{Key: "maven-virtual", Class: repo.Virtual, PackageType: repo.Maven},
			slices.Concat(common, v1Only, virtual, []string{
				"dockerApiVersion", "resolveDockerTagsByTimestamp",
			}),
			slices.Concat(common, virtual),
		},
	} {
		c := *repo.Defaults(&tc.c)
		for name, want := range map[string][]string{"v1": tc.v1, "v2": tc.v2} {
			read := repo.Full(c)
			if name == "v2" {
				read = repo.Relevant(read)
			}
			got := slices.Sorted(maps.Keys(fieldsOf(t, read)))
			slices.Sort(want)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("%s %s fields (-want +got):\n%s", c.Key, name, diff)
			}
		}
	}
}
