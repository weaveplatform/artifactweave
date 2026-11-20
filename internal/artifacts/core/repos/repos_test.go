package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// The repositories API's route suites in test/server/artifacts cover the
// ordinary paths; these cover the failures a store can only produce on
// purpose.

func TestUnitMergeRefusesMalformedUpdates(t *testing.T) {
	stored := *repo.Defaults(&repo.Config{Key: "k", Class: repo.Local})
	for name, patch := range map[string]string{
		"key not a string":     `{"key": 5}`,
		"field of wrong shape": `{"includesPattern": 5}`,
	} {
		var p map[string]json.RawMessage
		if err := json.Unmarshal([]byte(patch), &p); err != nil {
			t.Fatal(err)
		}
		if _, err := Merge(stored, p); err == nil {
			t.Errorf("%s: merged", name)
		}
	}
}

func update(t *testing.T, stored repo.Config, patch string) (repo.Config, error) {
	t.Helper()
	var p map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patch), &p); err != nil {
		t.Fatal(err)
	}
	return Merge(*repo.Defaults(&stored), p)
}

// An update keeps a key the server created under a reserved name, and
// refuses a key that differs from the stored one in any way, case included,
// as the key mismatch it is, whatever the package type (#472).
func TestUnitMergeNeverChangesTheKey(t *testing.T) {
	const mismatch = "The repository key cannot be changed"
	reserved := repo.Config{Key: "release-bundles", Class: repo.Local}
	if _, err := update(t, reserved, `{"description": "x"}`); err != nil {
		t.Errorf("stored reserved key: %v", err)
	}
	if _, err := update(t, reserved, `{"key": "release-bundles", "description": "x"}`); err != nil {
		t.Errorf("stored reserved key in the body: %v", err)
	}
	for _, stored := range []repo.Config{
		reserved,
		{Key: "libs-local", Class: repo.Local, PackageType: repo.Maven},
		{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
		{Key: "up", Class: repo.Remote, URL: "https://up.example"},
	} {
		for _, key := range []string{strings.ToUpper(stored.Key), stored.Key + "x"} {
			_, err := update(t, stored, `{"key": "`+key+`"}`)
			if err == nil || !strings.Contains(err.Error(), mismatch) {
				t.Errorf("%s as %s: %v", stored.Key, key, err)
			}
		}
	}
}

// A class or package type respelt in case is the stored one, which keeps
// its spelling; any other value is still a change (#492).
func TestUnitMergeKeepsCaseRespeltClassAndType(t *testing.T) {
	const mismatch = "cannot be changed"
	stored := repo.Config{Key: "libs-local", Class: repo.Local, PackageType: repo.Maven}
	for _, patch := range []string{
		`{"packageType": "Maven", "description": "x"}`,
		`{"packageType": "MAVEN", "rclass": "Local", "description": "x"}`,
		`{"rclass": "LOCAL", "description": "x"}`,
		`{"key": "libs-local", "packageType": "maven", "rclass": "local", "description": "x"}`,
	} {
		got, err := update(t, stored, patch)
		if err != nil || got.PackageType != repo.Maven || got.Class != repo.Local ||
			got.Description != "x" {
			t.Errorf("%s: %+v, %v", patch, got, err)
		}
	}
	for _, patch := range []string{
		`{"packageType": "Gradle"}`, `{"rclass": "Remote"}`, `{"packageType": "mavenx"}`,
	} {
		_, err := update(t, stored, patch)
		if err == nil || !strings.Contains(err.Error(), mismatch) {
			t.Errorf("%s: %v", patch, err)
		}
	}
	// The build-info repository keeps its type respelt too.
	buildInfo := repo.Config{
		Key: accessapi.BuildInfoRepository, Class: repo.Local, PackageType: repo.BuildInfo,
	}
	if got, err := update(t, buildInfo, `{"packageType": "BuildInfo"}`); err != nil ||
		got.PackageType != repo.BuildInfo {
		t.Errorf("build-info: %+v, %v", got, err)
	}
}

// The seeded build-info repository takes updates and keeps its package
// type; no other repository may have it (#472).
func TestUnitMergeUpdatesTheBuildInfoRepository(t *testing.T) {
	buildInfo := repo.Config{
		Key: accessapi.BuildInfoRepository, Class: repo.Local, PackageType: repo.BuildInfo,
	}
	got, err := update(t, buildInfo, `{"description": "builds", "packageType": "buildinfo"}`)
	if err != nil || got.Description != "builds" || got.PackageType != repo.BuildInfo {
		t.Fatalf("update: %+v, %v", got, err)
	}
	if _, err := update(t, buildInfo, `{"packageType": "generic"}`); err == nil {
		t.Error("the build-info repository changed its package type")
	}
	// A stored repository of another key with the type, which no write path
	// can make, is still refused.
	other := repo.Config{Key: "builds-copy", Class: repo.Local, PackageType: repo.BuildInfo}
	if _, err := update(t, other, `{"description": "x"}`); err == nil ||
		!strings.Contains(err.Error(), `"buildinfo" is not allowed`) {
		t.Errorf("another key: %v", err)
	}
	if _, err := PrepareCreate(buildInfo, nil); err == nil {
		t.Error("a create of the build-info repository was accepted")
	}
}

// Every entry point that updates a repository refuses a change to a field
// its package type takes only at creation, with 400 and the field's name,
// whether the body sets another value or resets it with null; repeating the
// stored value or leaving it out passes (decision 0039).
func TestUnitMergeKeepsCreationOnlyFields(t *testing.T) {
	yes := true
	stored := repo.Config{
		Key: "charts", Class: repo.Local, PackageType: repo.Helm, ForceNonDuplicateChart: &yes,
	}
	for _, patch := range []string{
		`{"description": "x"}`,
		`{"forceNonDuplicateChart": true, "forceMetadataNameVersion": false}`,
	} {
		if got, err := update(t, stored, patch); err != nil || !*got.ForceNonDuplicateChart {
			t.Errorf("%s: %+v, %v", patch, got, err)
		}
	}
	for patch, field := range map[string]string{
		`{"forceNonDuplicateChart": false}`:  "forceNonDuplicateChart",
		`{"forceNonDuplicateChart": null}`:   "forceNonDuplicateChart",
		`{"forceMetadataNameVersion": true}`: "forceMetadataNameVersion",
	} {
		_, err := update(t, stored, patch)
		var e *apierror.Error
		if !errors.As(err, &e) || e.Status != http.StatusBadRequest || e.Message !=
			"The field '"+field+"' of a Helm repository can be set only when the repository is created" {
			t.Errorf("%s: %v", patch, err)
		}
	}
}

// A remote's URL that carries a user name and a password gives them up to
// username and password on create and update, and loses its query and
// fragment with them; a URL with no password, or an empty one, or one that
// does not parse, stays as it was sent (#1510).
func TestUnitRemoteURLCredentialsMoveOnSave(t *testing.T) {
	type saved struct{ url, username, password string }
	for raw, want := range map[string]saved{
		"https://u:p@up.example/private":      {"https://up.example/private", "u", "p"},
		"https://u:p@up.example:8443/a?b=c#d": {"https://up.example:8443/a", "u", "p"},
		"https://u:p@up.example":              {"https://up.example", "u", "p"},
		"https://u:p%40ss@up.example/a/":      {"https://up.example/a/", "u", "p@ss"},
		"https://u:p:q@up.example/a":          {"https://up.example/a", "u", "p:q"},
		"https://:p@up.example/a":             {"https://up.example/a", "", "p"},
		"https://u@up.example/a":              {"https://u@up.example/a", "", ""},
		"https://u:@up.example/a":             {"https://u:@up.example/a", "", ""},
		"https://up.example/a?b=c":            {"https://up.example/a?b=c", "", ""},
		"https://u:p@up.example/%zz":          {"https://u:p@up.example/%zz", "", ""},
	} {
		created, err := PrepareCreate(repo.Config{Key: "r", Class: repo.Remote, URL: raw}, nil)
		if got := (saved{created.URL, created.Username, created.Password}); err != nil ||
			got != want {
			t.Errorf("create %s: %+v %v, want %+v", raw, got, err, want)
		}
		stored := repo.Config{Key: "r", Class: repo.Remote, URL: "https://old.example"}
		updated, err := update(t, stored, `{"url": "`+raw+`"}`)
		if got := (saved{updated.URL, updated.Username, updated.Password}); err != nil ||
			got != want {
			t.Errorf("update %s: %+v %v, want %+v", raw, got, err, want)
		}
	}
	// The URL's pair replaces one sent beside it or stored before.
	sent, err := PrepareCreate(repo.Config{
		Key: "r", Class: repo.Remote, URL: "https://u:p@up.example/a",
		Username: "other", Password: "secret",
	}, nil)
	if err != nil || sent.Username != "u" || sent.Password != "p" {
		t.Errorf("create beside a pair: %s %s %s %v", sent.URL, sent.Username, sent.Password, err)
	}
	stored := repo.Config{
		Key: "r", Class: repo.Remote, URL: "https://up.example/a",
		Username: "other", Password: "secret",
	}
	kept, err := update(t, stored, `{"url": "https://u:p@up.example/a"}`)
	if err != nil || kept.URL != "https://up.example/a" || kept.Username != "u" ||
		kept.Password != "p" {
		t.Errorf(
			"update over a stored pair: %s %s %s %v",
			kept.URL,
			kept.Username,
			kept.Password,
			err,
		)
	}
	// A refused write keeps the URL it refused: validation sees it first.
	vcs := repo.Config{
		Key: "r", Class: repo.Remote, PackageType: repo.VCS, VCSGitProvider: repo.ArtifactoryVCS,
		URL: "https://u:p@art.example/artifactory/api/vcs/peer",
	}
	if _, err := PrepareCreate(vcs, nil); err == nil ||
		!strings.Contains(err.Error(), "must end in /api/vcs/<peer-repository>") {
		t.Errorf("Artifactory VCS URL with credentials: %v", err)
	}
}

// A Terraform remote on a registry 7.x bypasses HEAD for has
// bypassHeadRequests on after every create and update, whatever was sent.
// An npm remote gets it on create only, from its URL once the URL has given
// up its credentials, as 7.x's interceptors run after processUrlCredentials;
// an update keeps what is stored or sent (#1583).
func TestUnitRemoteBypassHeadOnSave(t *testing.T) {
	on := func(c repo.Config) bool { return c.BypassHeadRequests != nil && *c.BypassHeadRequests }
	tf, err := PrepareCreate(repo.Config{
		Key: "tf", Class: repo.Remote, PackageType: repo.Terraform, URL: "https://up.example",
		BypassHeadRequests: new(false),
	}, nil)
	if err != nil || !on(tf) {
		t.Fatalf("terraform create: %v %v", on(tf), err)
	}
	if tf, err = update(t, tf, `{"bypassHeadRequests": false}`); err != nil || !on(tf) {
		t.Errorf("terraform update: %v %v", on(tf), err)
	}
	other, err := update(
		t,
		tf,
		`{"terraformRegistryUrl": "https://tf.example", "bypassHeadRequests": false}`,
	)
	if err != nil || on(other) {
		t.Errorf("terraform on another registry: %v %v", on(other), err)
	}
	npm, err := PrepareCreate(repo.Config{
		Key:         "npm",
		Class:       repo.Remote,
		PackageType: repo.NPM,
		URL:         "https://u:p@npm.pkg.github.com/",
	}, nil)
	if err != nil || !on(npm) || npm.URL != "https://npm.pkg.github.com/" {
		t.Fatalf("npm create: %s %v %v", npm.URL, on(npm), err)
	}
	if npm, err = update(t, npm, `{"bypassHeadRequests": false}`); err != nil || on(npm) {
		t.Errorf("npm update: %v %v", on(npm), err)
	}
	public := repo.Config{
		Key:         "npm",
		Class:       repo.Remote,
		PackageType: repo.NPM,
		URL:         "https://registry.npmjs.org",
	}
	if moved, err := update(
		t,
		public,
		`{"url": "https://npm.pkg.github.com"}`,
	); err != nil ||
		on(moved) {
		t.Errorf("npm moved to GitHub on update: %v %v", on(moved), err)
	}
}

// A remote created or updated without a URL answers 400 with 7.x's message
// alone (#1584).
func TestUnitRemoteWithoutURLIsRefused(t *testing.T) {
	stored := repo.Config{Key: "r", Class: repo.Remote, URL: "https://up.example"}
	_, created := PrepareCreate(
		repo.Config{Key: "r", Class: repo.Remote, PackageType: repo.NIMModel},
		nil,
	)
	_, emptied := update(t, stored, `{"url": ""}`)
	_, reset := update(t, stored, `{"url": null}`)
	for name, err := range map[string]error{"create": created, "empty": emptied, "null": reset} {
		var e *apierror.Error
		if !errors.As(err, &e) || e.Status != http.StatusBadRequest ||
			e.Message != "No URL defined for remote repository" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The support bundle repository is jfrog-support-bundle, as JFrog's reserved
// names spell it; the plural is an ordinary key (#472).
func TestUnitProtectedKeys(t *testing.T) {
	for key, want := range map[string]bool{
		"auto-trashcan": true, "Auto-Trashcan": true, "jfrog-support-bundle": true,
		"JFrog-Support-Bundle": true, accessapi.BuildInfoRepository: true, " ": true,
		"jfrog-support-bundles": false, "libs-local": false,
	} {
		if got := Protected(key); got != want {
			t.Errorf("Protected(%q) = %v", key, got)
		}
	}
}

var errBoom = errors.New("boom")

// faulty fails the operation named in fail.
type faulty struct {
	*inmem.Store
	fail string
}

func (f faulty) Items() port.ItemStore       { return faultyItems{f.Store.Items(), f.fail} }
func (f faulty) Blobs() port.BlobIndex       { return faultyBlobs{f.Store.Blobs(), f.fail} }
func (f faulty) Packages() port.PackageStore { return faultyPackages{f.Store.Packages(), f.fail} }
func (f faulty) Repos() port.RepoStore       { return faultyRepos{f.Store.Repos(), f.fail} }
func (f faulty) Trash() port.TrashStore      { return faultyTrash{f.Store.Trash(), f.fail} }
func (f faulty) Settings() settings.SettingsStore {
	return faultySettings{f.Store.Settings(), f.fail}
}

type faultyTrash struct {
	port.TrashStore
	fail string
}

// Add fails on the second entry, so the first is in the trash can when
// the delete rolls back.
func (f faultyTrash) Add(ctx context.Context, e model.TrashEntry) (model.TrashEntry, error) {
	if f.fail == "trash" && e.Record.Item.Name == "g.txt" {
		return e, errBoom
	}
	return f.TrashStore.Add(ctx, e) //nolint:wrapcheck // test double
}

type faultySettings struct {
	settings.SettingsStore
	fail string
}

func (f faultySettings) Get(ctx context.Context, key string, into any) error {
	if f.fail == "settings" {
		return errBoom
	}
	return f.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // test double
}

type faultyItems struct {
	port.ItemStore
	fail string
}

func (f faultyItems) Delete(
	ctx context.Context,
	repoKey string,
	cached bool,
	path string,
) (port.DeleteResult, error) {
	if f.fail == "items" {
		return port.DeleteResult{}, errBoom
	}
	return f.ItemStore.Delete(ctx, repoKey, cached, path) //nolint:wrapcheck // test double
}

func (f faultyItems) DeleteRoot(ctx context.Context, repoKey string, cached bool) error {
	if f.fail == "root" {
		return errBoom
	}
	return f.ItemStore.DeleteRoot(ctx, repoKey, cached) //nolint:wrapcheck // test double
}

type faultyBlobs struct {
	port.BlobIndex
	fail string
}

func (f faultyBlobs) Unref(ctx context.Context, sha string, n int64) (blob.Blob, error) {
	if f.fail == "unref" {
		return blob.Blob{}, errBoom
	}
	return f.BlobIndex.Unref(ctx, sha, n) //nolint:wrapcheck // test double
}

type faultyPackages struct {
	port.PackageStore
	fail string
}

func (f faultyPackages) List(
	ctx context.Context,
	repoKey string,
	p kernel.Page,
) ([]model.Package, string, error) {
	if f.fail == "list packages" {
		return nil, "", errBoom
	}
	return f.PackageStore.List(ctx, repoKey, p) //nolint:wrapcheck // test double
}

func (f faultyPackages) Delete(ctx context.Context, id int64) error {
	if f.fail == "delete package" {
		return errBoom
	}
	return f.PackageStore.Delete(ctx, id) //nolint:wrapcheck // test double
}

type faultyRepos struct {
	port.RepoStore
	fail string
}

func (f faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if f.fail == "get virtual" && key == "virt" {
		return repo.Config{}, errBoom
	}
	return f.RepoStore.Get(ctx, key) //nolint:wrapcheck // test double
}

func (f faultyRepos) Update(ctx context.Context, c repo.Config) error {
	if f.fail == "update virtual" {
		return errBoom
	}
	return f.RepoStore.Update(ctx, c) //nolint:wrapcheck // test double
}

// world is a local repository with an item, its blob reference and a
// package, listed by a virtual repository.
func world(t *testing.T) *inmem.Store {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: "libs", Class: repo.Local},
		{Key: "virt", Class: repo.Virtual, Repositories: []string{"libs"}, DefaultDeploymentRepo: "libs"},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	sha := strings.Repeat("ab", 32)
	if _, err := db.Blobs().
		Ref(ctx, blob.Blob{Checksums: kernel.Checksums{SHA256: sha}, Size: 3}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Items().Put(ctx, model.Item{
		RepoKey: "libs", Name: "f.txt", Size: 3, Actual: kernel.Checksums{SHA256: sha},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Packages().
		Ensure(ctx, model.Package{RepoKey: "libs", Type: repo.Generic, Name: "p"}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestUnitRemove(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	root, err := db.Items().EnsureRoot(ctx, model.Item{RepoKey: "libs"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Items().
		PatchProperties(ctx, root.ID, model.Properties{"k": {"v"}}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	files, found, err := Remove(ctx, db, "libs", "admin")
	if err != nil || !found || files != 1 {
		t.Fatalf("Remove = %d, %v, %v", files, found, err)
	}
	// A repository created again under the key starts with a bare root.
	if _, err := db.Items().Root(ctx, "libs", false); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("the root outlived its repository: %v", err)
	}
	if v, _ := db.Repos().
		Get(ctx, "virt"); len(v.Repositories) != 0 ||
		v.DefaultDeploymentRepo != "" {
		t.Fatalf("virtual still lists the deleted repository: %+v", v)
	}
	if _, found, err := Remove(ctx, db, "libs", "admin"); found || err != nil {
		t.Fatalf("second Remove = %v, %v", found, err)
	}
}

// checkMembersKnown is the stored repositories the CheckMembers tables
// check against.
var checkMembersKnown = []repo.Config{
	{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
	{Key: "npm-other", Class: repo.Local, PackageType: repo.NPM},
	{Key: "npm-fed", Class: repo.Federated, PackageType: repo.NPM},
	{Key: "npm-remote", Class: repo.Remote, PackageType: repo.NPM},
	{Key: "npm-virtual", Class: repo.Virtual, PackageType: repo.NPM},
	{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven},
	{Key: "gradle-remote", Class: repo.Remote, PackageType: repo.Gradle},
	{Key: "oci-local", Class: repo.Local, PackageType: repo.OCI},
	{Key: "helmoci-local", Class: repo.Local, PackageType: repo.HelmOCI},
}

const (
	missingRef = "Cannot create or update repo 'v': repo reference '%s' does not exist " +
		"in the config descriptor."
	notIncluded = "Cannot set repository reference '%s' as the default deployment repository " +
		"for virtual repository 'v' since it is not included in the repository references"
	notLocal = "Cannot set repository reference '%s' as the default deployment repository " +
		"for virtual repository 'v' since it is not a local repository"
	removed = "Cannot remove repository reference '%s' since it is defined as the default " +
		"deployment repository for the virtual repository 'v'"
	mismatched = "Virtual repository 'v' has package type '%s' but includes non-matching " +
		"repository '%s' with package type '%s'"
)

// virtualConfig is a virtual repository "v" of package type t with
// members and, when ddr is not empty, a deployment repository.
func virtualConfig(t repo.PackageType, ddr string, members ...string) repo.Config {
	return repo.Config{
		Key: "v", Class: repo.Virtual, PackageType: t, Repositories: members,
		DefaultDeploymentRepo: ddr,
	}
}

// wantMessage fails the test unless err is nil when want is empty, or a 400
// apierror whose message is want.
func wantMessage(t *testing.T, name string, err error, want string) {
	t.Helper()
	var ae *apierror.Error
	switch {
	case want == "" && err != nil:
		t.Errorf("%s: %v", name, err)
	case want != "" && (!errors.As(err, &ae) || ae.Status != http.StatusBadRequest ||
		ae.Message != want):
		t.Errorf("%s: %v, want 400 %q", name, err, want)
	}
}

func TestUnitCheckMembersCreate(t *testing.T) {
	for name, tt := range map[string]struct {
		c        repo.Config
		expected string
	}{
		"not virtual": {repo.Config{Key: "l", Class: repo.Local, Repositories: []string{"x"}}, ""},
		"no members":  {virtualConfig(repo.NPM, ""), ""},
		"same type":   {virtualConfig(repo.NPM, "", "npm-local", "npm-virtual", "npm-remote"), ""},
		"itself":      {virtualConfig(repo.NPM, "", "v", "npm-local"), ""},
		"generic takes any": {
			virtualConfig(repo.Generic, "", "npm-local", "maven-local", "npm-virtual"), "",
		},
		"maven group mixes": {virtualConfig(repo.Maven, "", "maven-local", "gradle-remote"), ""},
		"oci over helm oci": {virtualConfig(repo.OCI, "", "oci-local", "helmoci-local"), ""},
		"helm oci over oci": {virtualConfig(repo.HelmOCI, "", "helmoci-local", "oci-local"), ""},
		"local ddr":         {virtualConfig(repo.NPM, "npm-local", "npm-remote", "npm-local"), ""},
		"federated ddr":     {virtualConfig(repo.NPM, "npm-fed", "npm-fed"), ""},
		"missing member": {
			virtualConfig(repo.NPM, "", "npm-local", "ghost"), fmt.Sprintf(missingRef, "ghost"),
		},
		"missing ddr": {
			virtualConfig(repo.NPM, "ghost", "npm-local"), fmt.Sprintf(missingRef, "ghost"),
		},
		"ddr outside the members": {
			virtualConfig(repo.NPM, "npm-local", "npm-other"),
			fmt.Sprintf(notIncluded, "npm-local"),
		},
		"ddr without members": {
			virtualConfig(repo.NPM, "npm-local"), fmt.Sprintf(notIncluded, "npm-local"),
		},
		"remote ddr": {
			virtualConfig(repo.NPM, "npm-remote", "npm-remote"),
			fmt.Sprintf(notLocal, "npm-remote"),
		},
		"virtual ddr": {
			virtualConfig(repo.NPM, "npm-virtual", "npm-virtual"),
			fmt.Sprintf(notLocal, "npm-virtual"),
		},
		"itself as ddr": {virtualConfig(repo.Generic, "v", "v"), fmt.Sprintf(notLocal, "v")},
		"local of another type": {
			virtualConfig(repo.NPM, "", "npm-local", "maven-local"),
			fmt.Sprintf(mismatched, "Npm", "maven-local", "Maven"),
		},
		"local ddr, another member of another type": {
			virtualConfig(repo.NPM, "npm-local", "maven-local", "npm-local"),
			fmt.Sprintf(mismatched, "Npm", "maven-local", "Maven"),
		},
		"virtual of another type": {
			virtualConfig(repo.Go, "", "npm-virtual"),
			fmt.Sprintf(mismatched, "Go", "npm-virtual", "Npm"),
		},
		"outside the group": {
			virtualConfig(repo.Gradle, "", "npm-local"),
			fmt.Sprintf(mismatched, "Gradle", "npm-local", "Npm"),
		},
		"rpm named YUM": {
			virtualConfig(repo.RPM, "", "npm-local"),
			fmt.Sprintf(mismatched, "YUM", "npm-local", "Npm"),
		},
		"helm over helm oci": {
			virtualConfig(repo.Helm, "", "helmoci-local"),
			fmt.Sprintf(mismatched, "Helm", "helmoci-local", "HelmOCI"),
		},
	} {
		c := tt.c
		err := CheckMembers(&c, nil, checkMembersKnown)
		wantMessage(t, name, err, tt.expected)
		if err == nil && !slices.Equal(c.Repositories, tt.c.Repositories) {
			t.Errorf("%s: a create changed the members to %v", name, c.Repositories)
		}
	}
}

func TestUnitCheckMembersUpdate(t *testing.T) {
	for name, tt := range map[string]struct {
		stored, c repo.Config
		expected  string
		// members and ddr are what the update keeps, when it is accepted.
		members []string
		ddr     string
	}{
		"description only, stored ddr outside the members": {
			stored:  virtualConfig(repo.NPM, "npm-local", "npm-other"),
			c:       virtualConfig(repo.NPM, "npm-local", "npm-other"),
			members: []string{"npm-other"}, ddr: "npm-local",
		},
		"description only, stored member of another type": {
			stored:  virtualConfig(repo.NPM, "", "maven-local"),
			c:       virtualConfig(repo.NPM, "", "maven-local"),
			members: []string{"maven-local"},
		},
		"description only, stored remote ddr": {
			stored:  virtualConfig(repo.NPM, "npm-remote", "npm-remote"),
			c:       virtualConfig(repo.NPM, "npm-remote", "npm-remote"),
			members: []string{"npm-remote"}, ddr: "npm-remote",
		},
		"stale member and ddr dropped": {
			stored:  virtualConfig(repo.NPM, "gone", "npm-local", "gone"),
			c:       virtualConfig(repo.NPM, "gone", "npm-local", "gone"),
			members: []string{"npm-local"},
		},
		"stale member dropped while another is added": {
			stored:  virtualConfig(repo.NPM, "", "gone"),
			c:       virtualConfig(repo.NPM, "", "gone", "npm-other"),
			members: []string{"npm-other"},
		},
		"new missing member": {
			stored:   virtualConfig(repo.NPM, "", "npm-local"),
			c:        virtualConfig(repo.NPM, "", "npm-local", "ghost"),
			expected: fmt.Sprintf(missingRef, "ghost"),
		},
		"new missing ddr": {
			stored:   virtualConfig(repo.NPM, "gone", "npm-local"),
			c:        virtualConfig(repo.NPM, "ghost", "npm-local"),
			expected: fmt.Sprintf(missingRef, "ghost"),
		},
		"ddr changed outside the members": {
			stored:   virtualConfig(repo.NPM, "npm-local", "npm-local", "npm-other"),
			c:        virtualConfig(repo.NPM, "npm-fed", "npm-local", "npm-other"),
			expected: fmt.Sprintf(notIncluded, "npm-fed"),
		},
		"ddr changed to a remote member": {
			stored:   virtualConfig(repo.NPM, "npm-local", "npm-local", "npm-remote"),
			c:        virtualConfig(repo.NPM, "npm-remote", "npm-local", "npm-remote"),
			expected: fmt.Sprintf(notLocal, "npm-remote"),
		},
		"ddr changed to another member": {
			stored:  virtualConfig(repo.NPM, "npm-local", "npm-local", "npm-other"),
			c:       virtualConfig(repo.NPM, "npm-other", "npm-local", "npm-other"),
			members: []string{"npm-local", "npm-other"}, ddr: "npm-other",
		},
		"ddr changed, stored member of another type": {
			stored:  virtualConfig(repo.NPM, "", "maven-local", "npm-local"),
			c:       virtualConfig(repo.NPM, "npm-local", "maven-local", "npm-local"),
			members: []string{"maven-local", "npm-local"}, ddr: "npm-local",
		},
		"ddr changed to a member of another type": {
			stored:   virtualConfig(repo.NPM, "", "maven-local"),
			c:        virtualConfig(repo.NPM, "maven-local", "maven-local"),
			expected: fmt.Sprintf(mismatched, "Npm", "maven-local", "Maven"),
		},
		"ddr cleared": {
			stored:  virtualConfig(repo.NPM, "npm-local", "npm-local"),
			c:       virtualConfig(repo.NPM, "", "npm-other"),
			members: []string{"npm-other"},
		},
		"the ddr's member removed": {
			stored:   virtualConfig(repo.NPM, "npm-local", "npm-local", "npm-other"),
			c:        virtualConfig(repo.NPM, "npm-local", "npm-other"),
			expected: fmt.Sprintf(removed, "npm-local"),
		},
		"members changed, one of another type": {
			stored:   virtualConfig(repo.NPM, "", "npm-local"),
			c:        virtualConfig(repo.NPM, "", "npm-local", "maven-local"),
			expected: fmt.Sprintf(mismatched, "Npm", "maven-local", "Maven"),
		},
		"members reordered": {
			stored:  virtualConfig(repo.NPM, "", "npm-local", "npm-other"),
			c:       virtualConfig(repo.NPM, "", "npm-other", "npm-local"),
			members: []string{"npm-other", "npm-local"},
		},
		"cycle": {
			stored:  virtualConfig(repo.NPM, ""),
			c:       virtualConfig(repo.NPM, "", "npm-virtual", "v"),
			members: []string{"npm-virtual", "v"},
		},
	} {
		stored, c := tt.stored, tt.c
		err := CheckMembers(&c, &stored, checkMembersKnown)
		wantMessage(t, name, err, tt.expected)
		if err == nil && (!slices.Equal(c.Repositories, tt.members) ||
			c.DefaultDeploymentRepo != tt.ddr) {
			t.Errorf("%s: kept %v %q, want %v %q", name, c.Repositories,
				c.DefaultDeploymentRepo, tt.members, tt.ddr)
		}
	}
}

func TestUnitP2VirtualMembers(t *testing.T) {
	known := []repo.Config{
		{Key: "generic-local", Class: repo.Local, PackageType: repo.Generic},
		{Key: "generic-remote", Class: repo.Remote, PackageType: repo.Generic},
		{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven},
		{Key: "maven-remote", Class: repo.Remote, PackageType: repo.Maven},
		{Key: "gradle-local", Class: repo.Local, PackageType: repo.Gradle},
		{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
		{Key: "p2-remote", Class: repo.Remote, PackageType: repo.P2},
	}
	for _, tc := range []struct{ member, expected string }{
		{"generic-local", ""},
		{"maven-local", ""},
		{"gradle-local", ""},
		{"p2-remote", ""},
		{
			"generic-remote",
			"P2 virtual repository member 'generic-remote' with package type 'generic' must be local",
		},
		{
			"maven-remote",
			"P2 virtual repository member 'maven-remote' with package type 'maven' must be local",
		},
		{
			"npm-local",
			"Virtual repository 'p2-virtual' has package type 'P2' but includes non-matching " +
				"repository 'npm-local' with package type 'Npm'",
		},
	} {
		v := repo.Config{
			Key: "p2-virtual", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{tc.member},
		}
		wantMessage(t, "P2 member "+tc.member, CheckMembers(&v, nil, known), tc.expected)
	}
}

// Deleting a virtual repository's last member must not leave it unable to
// take an update, which the old at-least-one-member rule did.
func TestUnitVirtualEmptiedByRemoveStillUpdates(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	if _, _, err := Remove(ctx, db, "libs", "admin"); err != nil {
		t.Fatal(err)
	}
	v, err := db.Repos().Get(ctx, "virt")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := Merge(v, map[string]json.RawMessage{"description": []byte(`"empty now"`)})
	if err != nil || updated.Description != "empty now" || len(updated.Repositories) != 0 {
		t.Fatalf("update of an emptied virtual: %+v, %v", updated, err)
	}
	known, _ := db.Repos().List(ctx, port.RepoFilter{})
	if err := CheckMembers(&updated, &v, known); err != nil {
		t.Fatal(err)
	}
	// Still no deployment repository to name.
	updated, err = Merge(v, map[string]json.RawMessage{"defaultDeploymentRepo": []byte(`"libs"`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckMembers(&updated, &v, known); err == nil {
		t.Fatal("a virtual without members took a deployment repository")
	}
}

// A failure anywhere in a delete leaves the repository whole: its
// configuration, its content and its blob references, and the trash can
// as it was.
func TestUnitRemoveFailures(t *testing.T) {
	ctx := context.Background()
	for _, fail := range []string{
		"settings", "trash", "items", "unref", "root", "list packages", "delete package",
		"get virtual", "update virtual",
	} {
		db := world(t)
		sha := strings.Repeat("ab", 32)
		// A second file with the same content, which "trash" fails on.
		if _, err := db.Blobs().
			Ref(ctx, blob.Blob{Checksums: kernel.Checksums{SHA256: sha}, Size: 3}, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Items().Put(ctx, model.Item{
			RepoKey: "libs", Name: "g.txt", Size: 3, Actual: kernel.Checksums{SHA256: sha},
		}); err != nil {
			t.Fatal(err)
		}
		if fail == "unref" {
			// While the trash can is on it takes the references over, and
			// nothing is released.
			trashcan(t, db, false)
		}
		if _, _, err := Remove(ctx, faulty{db, fail}, "libs", "admin"); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", fail, err)
		}
		if _, err := db.Repos().Get(ctx, "libs"); err != nil {
			t.Errorf("%s: the failed delete was not rolled back: %v", fail, err)
		}
		if _, err := db.Items().Get(ctx, "libs", false, "g.txt"); err != nil {
			t.Errorf("%s: the content went: %v", fail, err)
		}
		if n := entries(t, db, "libs"); n != 0 {
			t.Errorf("%s: %d entries in the trash can", fail, n)
		}
		if n := refs(t, db, sha); n != 2 {
			t.Errorf("%s: %d references, want 2", fail, n)
		}
	}
}

func TestUnitCheckKeypairs(t *testing.T) {
	ctx := context.Background()
	keys := &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{"kp": {Name: "kp"}}}
	ok := repo.Config{Key: "r", PrimaryKeyPairRef: "kp", SecondaryKeyPairRef: "kp"}
	if err := CheckKeypairs(ctx, keys, ok); err != nil {
		t.Fatalf("existing pairs: %v", err)
	}
	if err := CheckKeypairs(ctx, keys, repo.Config{Key: "r"}); err != nil {
		t.Fatalf("no references: %v", err)
	}
	err := CheckKeypairs(ctx, keys, repo.Config{Key: "r", SecondaryKeyPairRef: "absent"})
	if err == nil || !strings.Contains(err.Error(), "Key pair 'absent' does not exist") {
		t.Fatalf("missing pair: %v", err)
	}
	if err := CheckKeypairs(ctx, &accessapitest.Keys{Err: errors.New("boom")}, ok); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("store failure: %v", err)
	}
}

// A Terraform PROVIDER repository names GPG key pairs only, in either
// field; a MODULE repository and other types take an RSA pair (decision
// 0040).
func TestUnitCheckKeypairsOfAProviderRepository(t *testing.T) {
	ctx := context.Background()
	keys := &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{
		"gpg": {Name: "gpg", Type: accessapi.KeypairGPG},
		"rsa": {Name: "rsa", Type: accessapi.KeypairRSA},
	}}
	tf := func(tt repo.TerraformType, primary, secondary string) repo.Config {
		return repo.Config{
			Key: "tf", Class: repo.Local, PackageType: repo.Terraform, TerraformType: tt,
			PrimaryKeyPairRef: primary, SecondaryKeyPairRef: secondary,
		}
	}
	for _, c := range []repo.Config{
		tf(repo.TerraformProvider, "gpg", "gpg"),
		tf(repo.TerraformModule, "rsa", "rsa"),
		{Key: "deb", Class: repo.Local, PackageType: repo.Debian, PrimaryKeyPairRef: "gpg"},
	} {
		if err := CheckKeypairs(ctx, keys, c); err != nil {
			t.Errorf("%s %s: %v", c.Key, c.TerraformType, err)
		}
	}
	for field, c := range map[string]repo.Config{
		"primaryKeyPairRef":   tf(repo.TerraformProvider, "rsa", "gpg"),
		"secondaryKeyPairRef": tf(repo.TerraformProvider, "gpg", "rsa"),
	} {
		err := CheckKeypairs(ctx, keys, c)
		var ae *apierror.Error
		if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest ||
			!strings.Contains(ae.Message, field+": names the RSA key pair 'rsa'") {
			t.Errorf("%s: %v", field, err)
		}
	}
	if err := CheckKeypairs(ctx, keys, repo.Config{
		Key: "deb", Class: repo.Local, PackageType: repo.Debian, PrimaryKeyPairRef: "rsa",
	}); err == nil || !strings.Contains(err.Error(), "Debian metadata signing requires GPG") {
		t.Errorf("Debian accepted an RSA pair that cannot sign apt Release: %v", err)
	}
}

// terraformType cannot change or be removed once set, and an update that
// sets it where there was none owes the repository a reindex.
func TestUnitMergeKeepsTerraformType(t *testing.T) {
	module := repo.Config{
		Key: "tf", Class: repo.Local, PackageType: repo.Terraform,
		TerraformType: repo.TerraformModule,
	}
	for _, patch := range []string{
		`{"terraformType": "module", "description": "x"}`, `{"description": "x"}`,
	} {
		got, err := update(t, module, patch)
		if err != nil || got.TerraformType != repo.TerraformModule || OwesReindex(module, got) {
			t.Errorf("%s: %+v %v", patch, got.TerraformType, err)
		}
	}
	for _, patch := range []string{`{"terraformType": "provider"}`, `{"terraformType": null}`} {
		if _, err := update(t, module, patch); err == nil ||
			!strings.Contains(
				err.Error(),
				"The field 'terraformType' of a Terraform repository cannot change once set",
			) {
			t.Errorf("%s: %v", patch, err)
		}
	}
	untyped := module
	untyped.TerraformType = ""
	got, err := update(t, untyped, `{"terraformType": "Provider"}`)
	if err != nil || got.TerraformType != repo.TerraformProvider || !OwesReindex(untyped, got) {
		t.Fatalf("setting it: %+v %v", got.TerraformType, err)
	}
	if again, err := update(t, untyped, `{"description": "x"}`); err != nil ||
		OwesReindex(untyped, again) {
		t.Fatalf("leaving it unset: %+v %v", again.TerraformType, err)
	}
}

// failingProjects fails the environment reads of one scope: "" for the
// global environments, or a project's key.
type failingProjects struct {
	accessapi.Projects
	scope string
}

func (f failingProjects) Environments(ctx context.Context, key string) ([]string, error) {
	if key == f.scope {
		return nil, errors.New("boom")
	}
	return f.Projects.Environments(ctx, key) //nolint:wrapcheck // test double
}

func TestUnitCheckEnvironments(t *testing.T) {
	ctx := context.Background()
	projects := &accessapitest.Projects{
		Global: []string{"QA"},
		Projects: map[string]accessapitest.Project{"ghost": {}, "proj": {Environments: []string{
			"stage",       // stored without the prefix
			"proj-canary", // stored with it
		}}},
	}
	check := func(c repo.Config) (string, error) {
		t.Helper()
		err := CheckEnvironments(ctx, projects, &c)
		return strings.Join(c.Environments, ","), err
	}
	for name, tc := range map[string]struct {
		c        repo.Config
		expected string
	}{
		"outside a project, ignored": {
			repo.Config{Key: "r", Environments: []string{"nope", "DEV"}}, "",
		},
		"a project without a list gets DEV": {repo.Config{Key: "r", ProjectKey: "proj"}, "DEV"},
		"built-in and global, any case, once each": {
			repo.Config{Key: "r", ProjectKey: "proj", Environments: []string{"dev", "qa", "DEV"}},
			"DEV,QA",
		},
		"the project's own, by <projectKey>-<name>": {
			repo.Config{
				Key: "r", ProjectKey: "proj",
				Environments: []string{"PROJ-STAGE", "proj-Canary", "prod"},
			},
			"proj-stage,proj-canary,PROD",
		},
		"a project without environments still has the built-ins": {
			repo.Config{Key: "r", ProjectKey: "ghost", Environments: []string{"PROD"}},
			"PROD",
		},
	} {
		if got, err := check(tc.c); err != nil || got != tc.expected {
			t.Errorf("%s: %q, %v; want %q", name, got, err, tc.expected)
		}
	}
	for name, c := range map[string]repo.Config{
		"missing project": {Key: "r", ProjectKey: "missing", Environments: []string{"PROD"}},
		"unknown":         {Key: "r", ProjectKey: "proj", Environments: []string{"DEV", "nope"}},
		"a project environment without its prefix": {
			Key: "r", ProjectKey: "proj", Environments: []string{"stage"},
		},
		"another project's": {Key: "r", ProjectKey: "ghost", Environments: []string{"proj-stage"}},
	} {
		if _, err := check(c); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("%s: %v", name, err)
		}
	}
	for scope, c := range map[string]repo.Config{
		"":     {Key: "r", ProjectKey: "proj"},
		"proj": {Key: "r", ProjectKey: "proj"},
	} {
		if err := CheckEnvironments(ctx, failingProjects{projects, scope}, &c); err == nil ||
			!strings.Contains(err.Error(), "boom") {
			t.Errorf("store failure for %q: %v", scope, err)
		}
	}
}
