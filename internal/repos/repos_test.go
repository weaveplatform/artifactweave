package repos

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The repositories API's tests in internal/server cover the ordinary paths;
// these cover the failures a store can only produce on purpose.

func TestMergeRefusesMalformedUpdates(t *testing.T) {
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
func TestMergeNeverChangesTheKey(t *testing.T) {
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
func TestMergeKeepsCaseRespeltClassAndType(t *testing.T) {
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
		Key: types.BuildInfoRepository, Class: repo.Local, PackageType: repo.BuildInfo,
	}
	if got, err := update(t, buildInfo, `{"packageType": "BuildInfo"}`); err != nil ||
		got.PackageType != repo.BuildInfo {
		t.Errorf("build-info: %+v, %v", got, err)
	}
}

// The seeded build-info repository takes updates and keeps its package
// type; no other repository may have it (#472).
func TestMergeUpdatesTheBuildInfoRepository(t *testing.T) {
	buildInfo := repo.Config{
		Key: types.BuildInfoRepository, Class: repo.Local, PackageType: repo.BuildInfo,
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
func TestMergeKeepsCreationOnlyFields(t *testing.T) {
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

// The support bundle repository is jfrog-support-bundle, as JFrog's reserved
// names spell it; the plural is an ordinary key (#472).
func TestProtectedKeys(t *testing.T) {
	for key, want := range map[string]bool{
		"auto-trashcan": true, "Auto-Trashcan": true, "jfrog-support-bundle": true,
		"JFrog-Support-Bundle": true, types.BuildInfoRepository: true, " ": true,
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

func (f faulty) Items() store.ItemStore       { return faultyItems{f.Store.Items(), f.fail} }
func (f faulty) Blobs() store.BlobIndex       { return faultyBlobs{f.Store.Blobs(), f.fail} }
func (f faulty) Packages() store.PackageStore { return faultyPackages{f.Store.Packages(), f.fail} }
func (f faulty) Repos() store.RepoStore       { return faultyRepos{f.Store.Repos(), f.fail} }
func (f faulty) Trash() store.TrashStore      { return faultyTrash{f.Store.Trash(), f.fail} }
func (f faulty) Settings() store.SettingsStore {
	return faultySettings{f.Store.Settings(), f.fail}
}

type faultyTrash struct {
	store.TrashStore
	fail string
}

// Add fails on the second entry, so the first is in the trash can when
// the delete rolls back.
func (f faultyTrash) Add(ctx context.Context, e types.TrashEntry) (types.TrashEntry, error) {
	if f.fail == "trash" && e.Record.Item.Name == "g.txt" {
		return e, errBoom
	}
	return f.TrashStore.Add(ctx, e) //nolint:wrapcheck // test double
}

type faultySettings struct {
	store.SettingsStore
	fail string
}

func (f faultySettings) Get(ctx context.Context, key string, into any) error {
	if f.fail == "settings" {
		return errBoom
	}
	return f.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // test double
}

type faultyItems struct {
	store.ItemStore
	fail string
}

func (f faultyItems) Delete(
	ctx context.Context,
	repoKey string,
	cached bool,
	path string,
) (store.DeleteResult, error) {
	if f.fail == "items" {
		return store.DeleteResult{}, errBoom
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
	store.BlobIndex
	fail string
}

func (f faultyBlobs) Unref(ctx context.Context, sha string, n int64) (types.Blob, error) {
	if f.fail == "unref" {
		return types.Blob{}, errBoom
	}
	return f.BlobIndex.Unref(ctx, sha, n) //nolint:wrapcheck // test double
}

type faultyPackages struct {
	store.PackageStore
	fail string
}

func (f faultyPackages) List(
	ctx context.Context,
	repoKey string,
	p store.Page,
) ([]types.Package, string, error) {
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
	store.RepoStore
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
		Ref(ctx, types.Blob{Checksums: types.Checksums{SHA256: sha}, Size: 3}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Items().Put(ctx, types.Item{
		RepoKey: "libs", Name: "f.txt", Size: 3, Actual: types.Checksums{SHA256: sha},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Packages().
		Ensure(ctx, types.Package{RepoKey: "libs", Type: repo.Generic, Name: "p"}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRemove(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	root, err := db.Items().EnsureRoot(ctx, types.Item{RepoKey: "libs"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Items().
		PatchProperties(ctx, root.ID, types.Properties{"k": {"v"}}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	files, found, err := Remove(ctx, db, "libs", "admin")
	if err != nil || !found || files != 1 {
		t.Fatalf("Remove = %d, %v, %v", files, found, err)
	}
	// A repository created again under the key starts with a bare root.
	if _, err := db.Items().Root(ctx, "libs", false); !errors.Is(err, store.ErrNotFound) {
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

func TestCheckMembers(t *testing.T) {
	known := []repo.Config{
		{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
		{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven},
		{Key: "gradle-remote", Class: repo.Remote, PackageType: repo.Gradle},
		{Key: "npm-virtual", Class: repo.Virtual, PackageType: repo.NPM},
	}
	virtual := func(t repo.PackageType, members ...string) repo.Config {
		return repo.Config{Key: "v", Class: repo.Virtual, PackageType: t, Repositories: members}
	}
	for name, tt := range map[string]struct {
		c    repo.Config
		want string
	}{
		"not virtual":        {repo.Config{Key: "l", Class: repo.Local, Repositories: []string{"x"}}, ""},
		"no members":         {virtual(repo.NPM), ""},
		"same type":          {virtual(repo.NPM, "npm-local", "npm-virtual"), ""},
		"generic takes any":  {virtual(repo.Generic, "npm-local", "maven-local", "npm-virtual"), ""},
		"maven family mixes": {virtual(repo.Maven, "maven-local", "gradle-remote"), ""},
		"unknown member": {
			virtual(repo.NPM, "ghost"),
			"Virtual repository member 'ghost' does not exist",
		},
		"local of another type": {
			virtual(repo.NPM, "npm-local", "maven-local"),
			"Virtual repository member 'maven-local' has package type 'maven', which does " +
				"not match the virtual repository's package type 'npm'",
		},
		"virtual of another type": {
			virtual(repo.Go, "npm-virtual"),
			"Virtual repository member 'npm-virtual' has package type 'npm'",
		},
		"outside the family": {
			virtual(repo.Gradle, "npm-local"),
			"package type 'gradle'",
		},
	} {
		err := CheckMembers(tt.c, known)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%s: %v, want %q", name, err, tt.want)
		}
	}
}

// Deleting a virtual repository's last member must not leave it unable to
// take an update, which the old at-least-one-member rule did.
func TestVirtualEmptiedByRemoveStillUpdates(t *testing.T) {
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
	known, _ := db.Repos().List(ctx, store.RepoFilter{})
	if err := CheckMembers(updated, known); err != nil {
		t.Fatal(err)
	}
	// Still no deployment repository to name.
	if _, err := Merge(
		v,
		map[string]json.RawMessage{"defaultDeploymentRepo": []byte(`"libs"`)},
	); err == nil {
		t.Fatal("a virtual without members took a deployment repository")
	}
}

// A failure anywhere in a delete leaves the repository whole: its
// configuration, its content and its blob references, and the trash can
// as it was.
func TestRemoveFailures(t *testing.T) {
	ctx := context.Background()
	for _, fail := range []string{
		"settings", "trash", "items", "unref", "root", "list packages", "delete package",
		"get virtual", "update virtual",
	} {
		db := world(t)
		sha := strings.Repeat("ab", 32)
		// A second file with the same content, which "trash" fails on.
		if _, err := db.Blobs().
			Ref(ctx, types.Blob{Checksums: types.Checksums{SHA256: sha}, Size: 3}, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Items().Put(ctx, types.Item{
			RepoKey: "libs", Name: "g.txt", Size: 3, Actual: types.Checksums{SHA256: sha},
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

// failingKeys is a key store whose reads fail.
type failingKeys struct{ store.KeyStore }

func (failingKeys) Keypair(context.Context, string) (types.Keypair, error) {
	return types.Keypair{}, errors.New("boom")
}

func TestCheckKeypairs(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	if err := db.Keys().CreateKeypair(ctx, types.Keypair{Name: "kp"}); err != nil {
		t.Fatal(err)
	}
	ok := repo.Config{Key: "r", PrimaryKeyPairRef: "kp", SecondaryKeyPairRef: "kp"}
	if err := CheckKeypairs(ctx, db.Keys(), ok); err != nil {
		t.Fatalf("existing pairs: %v", err)
	}
	if err := CheckKeypairs(ctx, db.Keys(), repo.Config{Key: "r"}); err != nil {
		t.Fatalf("no references: %v", err)
	}
	err := CheckKeypairs(ctx, db.Keys(), repo.Config{Key: "r", SecondaryKeyPairRef: "absent"})
	if err == nil || !strings.Contains(err.Error(), "Key pair 'absent' does not exist") {
		t.Fatalf("missing pair: %v", err)
	}
	if err := CheckKeypairs(ctx, failingKeys{db.Keys()}, ok); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("store failure: %v", err)
	}
}

// A Terraform PROVIDER repository names GPG key pairs only, in either
// field; a MODULE repository and other types take an RSA pair (decision
// 0040).
func TestCheckKeypairsOfAProviderRepository(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	for _, k := range []types.Keypair{
		{Name: "gpg", Type: types.KeypairGPG}, {Name: "rsa", Type: types.KeypairRSA},
	} {
		if err := db.Keys().CreateKeypair(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	tf := func(tt repo.TerraformType, primary, secondary string) repo.Config {
		return repo.Config{
			Key: "tf", Class: repo.Local, PackageType: repo.Terraform, TerraformType: tt,
			PrimaryKeyPairRef: primary, SecondaryKeyPairRef: secondary,
		}
	}
	for _, c := range []repo.Config{
		tf(repo.TerraformProvider, "gpg", "gpg"),
		tf(repo.TerraformModule, "rsa", "rsa"),
		{Key: "deb", Class: repo.Local, PackageType: repo.Debian, PrimaryKeyPairRef: "rsa"},
	} {
		if err := CheckKeypairs(ctx, db.Keys(), c); err != nil {
			t.Errorf("%s %s: %v", c.Key, c.TerraformType, err)
		}
	}
	for field, c := range map[string]repo.Config{
		"primaryKeyPairRef":   tf(repo.TerraformProvider, "rsa", "gpg"),
		"secondaryKeyPairRef": tf(repo.TerraformProvider, "gpg", "rsa"),
	} {
		err := CheckKeypairs(ctx, db.Keys(), c)
		var ae *apierror.Error
		if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest ||
			!strings.Contains(ae.Message, field+": names the RSA key pair 'rsa'") {
			t.Errorf("%s: %v", field, err)
		}
	}
}

// terraformType cannot change or be removed once set, and an update that
// sets it where there was none owes the repository a reindex.
func TestMergeKeepsTerraformType(t *testing.T) {
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

type failingProjects struct {
	store.ProjectStore
	scope string
}

func (f failingProjects) Environments(
	ctx context.Context,
	key string,
) ([]types.Environment, error) {
	if key == f.scope {
		return nil, errors.New("boom")
	}
	return f.ProjectStore.Environments(ctx, key) //nolint:wrapcheck // test double
}

func TestCheckEnvironments(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	if err := db.Projects().Create(ctx, types.Project{Key: "proj"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []types.Environment{
		{Name: "QA"},
		{ProjectKey: "proj", Name: "stage"},       // stored without the prefix
		{ProjectKey: "proj", Name: "proj-canary"}, // stored with it
	} {
		if err := db.Projects().CreateEnvironment(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	check := func(c repo.Config) (string, error) {
		t.Helper()
		err := CheckEnvironments(ctx, db.Projects(), &c)
		return strings.Join(c.Environments, ","), err
	}
	for name, tc := range map[string]struct {
		c    repo.Config
		want string
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
		if got, err := check(tc.c); err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, tc.want)
		}
	}
	for name, c := range map[string]repo.Config{
		"unknown": {Key: "r", ProjectKey: "proj", Environments: []string{"DEV", "nope"}},
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
		if err := CheckEnvironments(ctx, failingProjects{db.Projects(), scope}, &c); err == nil ||
			!strings.Contains(err.Error(), "boom") {
			t.Errorf("store failure for %q: %v", scope, err)
		}
	}
}
