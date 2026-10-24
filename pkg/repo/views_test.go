package repo_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitPublic(t *testing.T) {
	inc := "**/*"
	full := repo.Config{
		Key: "r", Class: repo.Remote, PackageType: repo.Maven, Description: "d",
		RepoLayoutRef: "maven-2-default", IncludesPattern: &inc, URL: "https://up",
		Username: "u", Password: "secret", Notes: "internal",
	}
	got := repo.Public(full)
	if got.Key != "r" || got.Class != repo.Remote || got.PackageType != repo.Maven ||
		got.Description != "d" || got.RepoLayoutRef != "maven-2-default" ||
		got.IncludesPattern != &inc {
		t.Fatalf("public lost an identity field: %+v", got)
	}
	if got.URL != "" || got.Username != "" || got.Password != "" || got.Notes != "" {
		t.Fatalf("public kept a private field: %+v", got)
	}
}

func TestUnitMaskedAndRestoreSecrets(t *testing.T) {
	stored := repo.Config{
		Key: "r", Class: repo.Remote, Password: "secret",
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "X-Token", Value: "t0ken", Sensitive: true},
			{Name: "X-Plain", Value: "visible"},
		},
	}
	masked := repo.Masked(stored)
	if masked.Password != "" || masked.CustomHTTPHeaders[0].Value != "" ||
		masked.CustomHTTPHeaders[1].Value != "visible" {
		t.Fatalf("masked = %+v", masked)
	}
	if stored.CustomHTTPHeaders[0].Value != "t0ken" {
		t.Fatal("Masked changed the caller's headers")
	}
	// Writing the masked document back keeps the stored secret; a new value
	// replaces it.
	back := masked
	repo.RestoreSecrets(stored, &back)
	if back.CustomHTTPHeaders[0].Value != "t0ken" {
		t.Fatalf("restore = %+v", back.CustomHTTPHeaders)
	}
	changed := repo.Config{CustomHTTPHeaders: []repo.CustomHTTPHeader{
		{Name: "X-Token", Value: "new", Sensitive: true},
		{Name: "X-Other", Sensitive: true},
	}}
	repo.RestoreSecrets(stored, &changed)
	if changed.CustomHTTPHeaders[0].Value != "new" || changed.CustomHTTPHeaders[1].Value != "" {
		t.Fatalf("restore overwrote or invented a value: %+v", changed.CustomHTTPHeaders)
	}
}

func TestUnitRelevant(t *testing.T) {
	generic := *repo.Defaults(&repo.Config{Key: "g", Class: repo.Local})
	got := repo.Relevant(generic)
	if got.HandleReleases != nil || got.MaxUniqueTags != nil || got.DockerAPIVersion != "" ||
		got.EnableFileListsIndexing != nil || got.ForceNugetAuthentication != nil {
		t.Fatalf("generic kept package-specific fields: %+v", got)
	}
	if got.IncludesPattern == nil || got.BlackedOut == nil || got.PriorityResolution == nil {
		t.Fatalf("generic lost common fields: %+v", got)
	}
	if got.ChecksumPolicyType != "" || got.ForceMetadataNameVersion != nil {
		t.Fatalf("generic kept a Maven or Helm field: %+v", got)
	}
	maven := *repo.Defaults(&repo.Config{Key: "m", Class: repo.Local, PackageType: repo.Maven})
	got = repo.Relevant(maven)
	if got.HandleReleases == nil || got.SnapshotVersionBehavior == "" ||
		got.ChecksumPolicyType == "" {
		t.Fatalf("maven lost its own fields: %+v", got)
	}
	if got.MaxUniqueTags != nil || got.DockerTagRetention != nil {
		t.Fatalf("maven kept docker fields: %+v", got)
	}
	helm := *repo.Defaults(&repo.Config{Key: "h", Class: repo.Local, PackageType: repo.Helm})
	if got := repo.Relevant(helm); got.ForceMetadataNameVersion == nil ||
		got.ForceNonDuplicateChart == nil {
		t.Fatalf("helm lost its own fields: %+v", got)
	}
	docker := *repo.Defaults(&repo.Config{Key: "d", Class: repo.Local, PackageType: repo.Docker})
	if got := repo.Relevant(docker); got.DockerAPIVersion == "" || got.HandleSnapshots != nil {
		t.Fatalf("docker = %+v", got)
	}
	for _, pt := range []repo.PackageType{
		repo.RPM, repo.Debian, repo.NuGet, repo.Conan, repo.P2, repo.Helm, repo.PyPI,
		repo.Go, repo.Terraform, repo.Bower, repo.Composer,
	} {
		c := repo.Config{Key: "x", Class: repo.Remote, PackageType: pt}
		_ = repo.Relevant(*repo.Defaults(&c))
	}
}

// The fields the clients send that only one ecosystem has stay in that
// ecosystem's v2 read; the curation flags, which several have, stay in
// every read (#396).
func TestUnitRelevantKeepsEachEcosystemsClientFields(t *testing.T) {
	all := repo.Config{
		Key: "r", Class: repo.Remote, URL: "https://x",
		PropagateQueryParams: ptr(true), RetrieveSha256FromServer: ptr(true),
		CargoAnonymousAccess: ptr(true), CargoInternalIndex: ptr(true),
		GitRegistryURL: "https://git", YumGroupFileNames: "groups.xml",
		DdebSupported: ptr(true), EnableConanSupport: ptr(true),
		P2URLs: []string{"https://p2"}, PodsSpecsRepoURL: "https://specs",
		HexPublicKey: "key", Curated: ptr(true), PassThrough: ptr(false),
	}
	kept := func(c repo.Config) map[string]bool {
		return map[string]bool{
			"generic": c.PropagateQueryParams != nil && c.RetrieveSha256FromServer != nil,
			"cargo": c.CargoAnonymousAccess != nil && c.CargoInternalIndex != nil &&
				c.GitRegistryURL != "",
			"rpm":       c.YumGroupFileNames != "",
			"debian":    c.DdebSupported != nil,
			"conan":     c.EnableConanSupport != nil,
			"p2":        c.P2URLs != nil,
			"cocoapods": c.PodsSpecsRepoURL != "",
			"hex":       c.HexPublicKey != "",
			"curation":  c.Curated != nil && c.PassThrough != nil,
		}
	}
	for _, pt := range []repo.PackageType{
		repo.Generic, repo.Cargo, repo.RPM, repo.Debian, repo.Conan, repo.P2, repo.CocoaPods,
		repo.Hex, repo.Docker,
	} {
		c := all
		c.PackageType = pt
		for group, ok := range kept(repo.Relevant(c)) {
			if want := group == string(pt) || group == "curation"; ok != want {
				t.Errorf("%s: %s fields kept %v, want %v", pt, group, ok, want)
			}
		}
	}
}

// The configurations list's entry for a generic local repository has the
// fields of JFrog's sample (getallrepositoryconfigurations.md), but for the
// read-only TTLs and a project key, which the sample's repository has and
// this one does not (#457).
func TestUnitConfigurationsEntryMatchesTheDocumentedSample(t *testing.T) {
	sample := []string{
		"key", "packageType", "description", "notes", "includesPattern", "excludesPattern",
		"repoLayoutRef", "signedUrlTtl", "priorityResolution", "projectKey", "environments",
		"blackedOut", "propertySets", "archiveBrowsingEnabled", "xrayDataTtl", "downloadRedirect",
		"cdnRedirect", "xrayIndex", "rclass",
	}
	c := repo.Defaults(&repo.Config{Key: "example-repo-local", Class: repo.Local})
	out, err := json.Marshal(repo.Relevant(repo.Masked(*c)))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for field := range got {
		if !slices.Contains(sample, field) {
			t.Errorf("%s is not in the sample", field)
		}
	}
	for _, field := range sample {
		if _, ok := got[field]; !ok &&
			!slices.Contains([]string{"signedUrlTtl", "xrayDataTtl", "projectKey"}, field) {
			t.Errorf("%s is missing", field)
		}
	}
}

// NuGet keeps maxUniqueSnapshots in the v2 read, as the Maven family does;
// other types drop it (#457).
func TestUnitRelevantKeepsMaxUniqueSnapshotsForNuGet(t *testing.T) {
	for pt, kept := range map[repo.PackageType]bool{
		repo.NuGet: true, repo.Maven: true, repo.SBT: true, repo.NPM: false, repo.Generic: false,
	} {
		c := repo.Defaults(&repo.Config{Key: "k", Class: repo.Local, PackageType: pt})
		if got := repo.Relevant(*c).MaxUniqueSnapshots != nil; got != kept {
			t.Errorf("%s: kept %v, want %v", pt, got, kept)
		}
	}
	nuget := repo.Relevant(*repo.Defaults(&repo.Config{
		Key: "k", Class: repo.Local, PackageType: repo.NuGet,
	}))
	if nuget.HandleReleases != nil || nuget.ChecksumPolicyType != "" {
		t.Errorf("nuget kept other Maven fields: %+v", nuget)
	}
}

// A local or federated npm repository applies the checksum policy and the
// release and snapshot handling, so its v2 read keeps them; an npm remote
// or virtual one, and a generic one, do not (#457, #481).
func TestUnitRelevantKeepsTheDeployPolicyForHostedNpm(t *testing.T) {
	for _, tc := range []struct {
		class repo.Class
		pt    repo.PackageType
		kept  bool
	}{
		{repo.Local, repo.NPM, true},
		{repo.Federated, repo.NPM, true},
		{repo.Remote, repo.NPM, false},
		{repo.Virtual, repo.NPM, false},
		{repo.Local, repo.Generic, false},
		{repo.Remote, repo.Maven, true},
	} {
		c := repo.Config{
			Key: "k", Class: tc.class, PackageType: tc.pt,
			ChecksumPolicyType: repo.ServerGeneratedChecksums,
			HandleReleases:     ptr(true), HandleSnapshots: ptr(false),
		}
		got := repo.Relevant(c)
		kept := got.ChecksumPolicyType != "" && got.HandleReleases != nil &&
			got.HandleSnapshots != nil
		gone := got.ChecksumPolicyType == "" && got.HandleReleases == nil &&
			got.HandleSnapshots == nil
		if (tc.kept && !kept) || (!tc.kept && !gone) {
			t.Errorf("%s %s: %+v", tc.class, tc.pt, got)
		}
	}
	npm := repo.Relevant(*repo.Defaults(&repo.Config{
		Key: "k", Class: repo.Local, PackageType: repo.NPM,
	}))
	if npm.SnapshotVersionBehavior != "" || npm.MaxUniqueSnapshots != nil ||
		npm.SuppressPomConsistencyChecks != nil {
		t.Errorf("npm kept Maven-only fields: %+v", npm)
	}
}

func TestUnitEnvironmentsRenderAsAnEmptyListOnlyInMasked(t *testing.T) {
	c := repo.Config{Key: "k", Class: repo.Local}
	stored, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "environments") {
		t.Errorf("stored document gained environments: %s", stored)
	}
	shown, err := json.Marshal(repo.Masked(c))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shown), `"environments":[]`) {
		t.Errorf("want environments [] in %s", shown)
	}
	c.Environments = []string{"DEV"}
	shown, _ = json.Marshal(repo.Masked(c))
	if !strings.Contains(string(shown), `"environments":["DEV"]`) {
		t.Errorf("want environments [DEV] in %s", shown)
	}
}

// The v2 read of a generic local repository with every default is JFrog's
// genericLocalV2 sample (getrepositoryconfigurationv2.md), plus environments
// and priorityResolution, which that sample predates and which stay until a
// 7.x capture shows otherwise (#216).
func TestUnitGenericLocalV2MatchesTheDocumentedSample(t *testing.T) {
	c := repo.Defaults(&repo.Config{Key: "generic-local", Class: repo.Local})
	out, err := json.Marshal(repo.Relevant(repo.Masked(*c)))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"key": "generic-local", "rclass": "local", "packageType": "generic",
		"description": "", "notes": "", "includesPattern": "**/*", "excludesPattern": "",
		"repoLayoutRef": "maven-2-default", "blackedOut": false, "propertySets": []any{},
		"archiveBrowsingEnabled": false, "downloadRedirect": false, "xrayIndex": false,
		"cdnRedirect": false, "environments": []any{}, "priorityResolution": false,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("v2 generic local (-want +got):\n%s", diff)
	}
}

func TestUnitReadsCarryEmptyDescriptionNotesAndPropertySets(t *testing.T) {
	c := repo.Config{Key: "k", Class: repo.Local, Notes: "internal"}
	fields := func(c repo.Config) map[string]any {
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
	admin := fields(repo.Masked(c))
	if admin["description"] != "" || admin["notes"] != "internal" ||
		!cmp.Equal(admin["propertySets"], []any{}) {
		t.Errorf("admin read %v", admin)
	}
	public := fields(repo.Public(c))
	if public["description"] != "" || public["notes"] != "" || public["propertySets"] != nil {
		t.Errorf("non-admin read %v", public)
	}
	// A stored document keeps no property sets as an absent key, like
	// environments; Masked adds both on the way out.
	stored := fields(c)
	if _, ok := stored["propertySets"]; ok || stored["description"] != "" {
		t.Errorf("stored document %v", stored)
	}
	virtual := fields(repo.Masked(repo.Config{Key: "v", Class: repo.Virtual}))
	if _, ok := virtual["propertySets"]; ok || virtual["notes"] != "" {
		t.Errorf("virtual read %v", virtual)
	}
	c.PropertySets = []string{"artifactory"}
	if got := fields(repo.Masked(c)); !cmp.Equal(got["propertySets"], []any{"artifactory"}) {
		t.Errorf("set property sets %v", got["propertySets"])
	}
}
