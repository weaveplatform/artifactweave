package repos

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The binding's tests check that each method answers what the call it
// delegates to answers, errors included, and that the methods that write take
// part in the transaction their context carries. The rules themselves are
// tested on the functions, in repos_test.go and remove_test.go.

var (
	testKeys = &accessapitest.Keys{
		Keypairs: map[string]accessapi.Keypair{"kp": {Name: "kp"}},
	}
	testProjects = &accessapitest.Projects{
		Global:   []string{"QA"},
		Projects: map[string]accessapitest.Project{"proj": {Environments: []string{"stage"}}},
	}
)

func bound(db Store) artifactsapi.Repositories {
	return NewRepositories(db, testKeys, testProjects)
}

// sameError fails unless got is want unchanged: the binding wraps nothing, so
// the texts match and errors.Is still finds sentinel.
func sameError(t *testing.T, got, want, sentinel error) {
	t.Helper()
	if got == nil || want == nil || got.Error() != want.Error() || !errors.Is(got, sentinel) {
		t.Fatalf("error %v, want %v matching %v", got, want, sentinel)
	}
}

// sameAnswer fails unless the binding's result and error equal the
// function's.
func sameAnswer(t *testing.T, name string, got, want any, gotErr, wantErr error) {
	t.Helper()
	if !reflect.DeepEqual(got, want) || (gotErr == nil) != (wantErr == nil) ||
		(gotErr != nil && gotErr.Error() != wantErr.Error()) {
		t.Errorf("%s: %+v, %v; want %+v, %v", name, got, gotErr, want, wantErr)
	}
}

func TestUnitRepositoriesGet(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	r := bound(db)
	got, err := r.Get(ctx, "virt")
	want, _ := db.Repos().Get(ctx, "virt")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Get = %+v, %v; want %+v", got, err, want)
	}
	_, err = r.Get(ctx, "absent")
	_, wantErr := db.Repos().Get(ctx, "absent")
	sameError(t, err, wantErr, kernel.ErrNotFound)
}

func TestUnitRepositoriesList(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	for _, c := range []repo.Config{
		{Key: "npm-remote", Class: repo.Remote, PackageType: repo.NPM, URL: "https://registry.npmjs.org"},
		{Key: "a-npm", Class: repo.Local, PackageType: repo.NPM, ProjectKey: "proj"},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	r := bound(db)
	for name, tc := range map[string]struct {
		f    artifactsapi.RepoFilter
		keys []string
	}{
		"everything, ordered by key": {
			artifactsapi.RepoFilter{}, []string{"a-npm", "libs", "npm-remote", "virt"},
		},
		"a class":        {artifactsapi.RepoFilter{Class: repo.Remote}, []string{"npm-remote"}},
		"a package type": {artifactsapi.RepoFilter{PackageType: repo.NPM}, []string{"a-npm", "npm-remote"}},
		"a project":      {artifactsapi.RepoFilter{ProjectKey: "proj"}, []string{"a-npm"}},
		"all three": {
			artifactsapi.RepoFilter{Class: repo.Local, PackageType: repo.NPM, ProjectKey: "proj"},
			[]string{"a-npm"},
		},
	} {
		got, err := r.List(ctx, tc.f)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		keys := make([]string, 0, len(got))
		for _, c := range got {
			keys = append(keys, c.Key)
		}
		if !slices.Equal(keys, tc.keys) {
			t.Errorf("%s: %v, want %v", name, keys, tc.keys)
		}
	}
}

func TestUnitRepositoriesUsingKeypair(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	for _, c := range []repo.Config{
		{Key: "z-signed", Class: repo.Local, SecondaryKeyPairRef: "kp"},
		{Key: "a-signed", Class: repo.Local, PrimaryKeyPairRef: "kp"},
		{Key: "other", Class: repo.Local, PrimaryKeyPairRef: "other-kp"},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := bound(db).UsingKeypair(ctx, "kp")
	if err != nil || !slices.Equal(got, []string{"a-signed", "z-signed"}) {
		t.Fatalf("UsingKeypair = %v, %v", got, err)
	}
}

func TestUnitRepositoriesCreate(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	r := bound(db)
	c := repo.Config{Key: "new-local", Class: repo.Local, Description: "made here"}
	repo.Defaults(&c)
	if err := r.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, err := db.Repos().Get(ctx, "new-local"); err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("stored %+v, %v; want %+v", got, err, c)
	}
	err := r.Create(ctx, c)
	sameError(t, err, db.Repos().Create(ctx, c), kernel.ErrConflict)

	// A create in a transaction that rolls back is gone with it.
	rolledBack := repo.Config{Key: "rolled-back", Class: repo.Local}
	repo.Defaults(&rolledBack)
	if err := db.Tx(ctx, func(ctx context.Context) error {
		if err := r.Create(ctx, rolledBack); err != nil {
			return err
		}
		return errBoom
	}); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if _, err := db.Repos().Get(ctx, "rolled-back"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("the create outlived its transaction: %v", err)
	}
}

func TestUnitRepositoriesUpdate(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	r := bound(db)
	c, err := db.Repos().Get(ctx, "libs")
	if err != nil {
		t.Fatal(err)
	}
	c.Description = "updated"
	if err := r.Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Repos().Get(ctx, "libs"); got.Description != "updated" {
		t.Fatalf("stored description %q", got.Description)
	}
	absent := repo.Config{Key: "absent", Class: repo.Local}
	sameError(t, r.Update(ctx, absent), db.Repos().Update(ctx, absent), kernel.ErrNotFound)
}

func TestUnitRepositoriesRemove(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	r := bound(db)

	// Inside a transaction that rolls back, the delete rolls back too.
	if err := db.Tx(ctx, func(ctx context.Context) error {
		if _, _, err := r.Remove(ctx, "libs", "admin"); err != nil {
			return err
		}
		return errBoom
	}); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if _, err := db.Repos().Get(ctx, "libs"); err != nil {
		t.Fatalf("the delete outlived its transaction: %v", err)
	}

	files, found, err := r.Remove(ctx, "libs", "admin")
	if err != nil || !found || files != 1 {
		t.Fatalf("Remove = %d, %v, %v", files, found, err)
	}
	if _, found, err := r.Remove(ctx, "libs", "admin"); found || err != nil {
		t.Fatalf("second Remove = %v, %v", found, err)
	}

	// The content changing under the delete is refused with the api's
	// sentinel itself, as TestUnitRemoveWhileTheContentChanges races it.
	db = world(t)
	changing := racing{db, func(ctx context.Context) {
		if _, err := db.Items().Delete(ctx, "libs", false, "f.txt"); err != nil {
			t.Fatal(err)
		}
	}}
	_, found, err = bound(changing).Remove(ctx, "libs", "admin")
	if !errors.Is(err, artifactsapi.ErrChanged) || !found {
		t.Fatalf("Remove = %v, %v; want ErrChanged", found, err)
	}
}

// The payload is the one the configuration descriptor writes for a patch that
// owes a reindex (internal/configuration/core/descriptor's Apply).
func TestUnitRepositoriesRequestReindex(t *testing.T) {
	ctx := context.Background()
	db := world(t)
	r := bound(db)
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for by, literal := range map[string]string{"admin": `{"by":"admin"}`, "": `{}`} {
		key := "libs-" + by
		if err := r.RequestReindex(ctx, key, by, at); err != nil {
			t.Fatal(err)
		}
		item, err := db.WorkQueue().Get(ctx, format.KindReindex, key)
		if err != nil {
			t.Fatal(err)
		}
		descriptor, _ := json.Marshal(format.WorkRequest{By: by})
		if string(item.Payload) != string(descriptor) || string(item.Payload) != literal {
			t.Errorf("by %q: payload %s, want %s", by, item.Payload, descriptor)
		}
		if !item.NotBefore.Equal(at) {
			t.Errorf("by %q: due %v, want %v", by, item.NotBefore, at)
		}
	}

	// A request in a transaction that rolls back is gone with it.
	if err := db.Tx(ctx, func(ctx context.Context) error {
		if err := r.RequestReindex(ctx, "rolled-back", "admin", at); err != nil {
			return err
		}
		return errBoom
	}); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if _, err := db.WorkQueue().
		Get(ctx, format.KindReindex, "rolled-back"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("the request outlived its transaction: %v", err)
	}

	sameError(t, r.RequestReindex(ctx, "", "admin", at),
		db.WorkQueue().Enqueue(ctx, format.KindReindex, "", []byte(`{"by":"admin"}`), at),
		kernel.ErrInvalid)
}

func TestUnitRepositoriesProtected(t *testing.T) {
	r := bound(world(t))
	for _, key := range []string{
		"auto-trashcan", "JFrog-Support-Bundle", "artifactory-build-info", " ", "libs-local",
	} {
		if got, want := r.Protected(key), Protected(key); got != want {
			t.Errorf("Protected(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestUnitRepositoriesPrepareCreate(t *testing.T) {
	r := bound(world(t))
	existing := []repo.Config{{Key: "Libs", Class: repo.Local}}
	for name, c := range map[string]repo.Config{
		"valid":                   {Key: "fresh", Class: repo.Local},
		"taken in another case":   {Key: "libs", Class: repo.Local},
		"invalid package type":    {Key: "odd", Class: repo.Local, PackageType: "nope"},
		"defaults fill the class": {Key: "classless"},
	} {
		got, err := r.PrepareCreate(c, existing)
		want, wantErr := PrepareCreate(c, existing)
		sameAnswer(t, name, got, want, err, wantErr)
	}
	_, err := r.PrepareCreate(repo.Config{Key: "libs", Class: repo.Local}, existing)
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest ||
		ae.Message != artifactsapi.MsgKeyExists {
		t.Fatalf("taken key: %v", err)
	}
}

func TestUnitRepositoriesCheckMembers(t *testing.T) {
	r := bound(world(t))
	known := []repo.Config{{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM}}
	for name, c := range map[string]repo.Config{
		"known member": {
			Key: "v", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"npm-local"},
		},
		"unknown member": {
			Key: "v", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"ghost"},
		},
		"not virtual": {Key: "l", Class: repo.Local, Repositories: []string{"ghost"}},
	} {
		sameAnswer(t, name, nil, nil, r.CheckMembers(c, known), CheckMembers(c, known))
	}
}

func TestUnitRepositoriesCheckKeypairs(t *testing.T) {
	ctx := context.Background()
	r := bound(world(t))
	if err := r.CheckKeypairs(ctx, repo.Config{Key: "r", PrimaryKeyPairRef: "kp"}); err != nil {
		t.Fatalf("the bound key pair: %v", err)
	}
	err := r.CheckKeypairs(ctx, repo.Config{Key: "r", SecondaryKeyPairRef: "absent"})
	if err == nil || !strings.Contains(err.Error(), "Key pair 'absent' does not exist") {
		t.Fatalf("missing pair: %v", err)
	}
	failing := &accessapitest.Keys{Err: errBoom}
	c := repo.Config{Key: "r", PrimaryKeyPairRef: "kp"}
	sameError(t, NewRepositories(world(t), failing, testProjects).CheckKeypairs(ctx, c),
		CheckKeypairs(ctx, failing, c), errBoom)
}

func TestUnitRepositoriesCheckEnvironments(t *testing.T) {
	ctx := context.Background()
	r := bound(world(t))
	c := repo.Config{
		Key:          "r",
		ProjectKey:   "proj",
		Environments: []string{"qa", "PROJ-STAGE", "dev"},
	}
	if err := r.CheckEnvironments(ctx, &c); err != nil ||
		strings.Join(c.Environments, ",") != "QA,proj-stage,DEV" {
		t.Fatalf("environments %v, %v", c.Environments, err)
	}
	unknown := repo.Config{Key: "r", ProjectKey: "proj", Environments: []string{"nope"}}
	if err := r.CheckEnvironments(ctx, &unknown); err == nil ||
		err.Error() != "Environment 'nope' does not exist" {
		t.Fatalf("unknown environment: %v", err)
	}
	failing := &accessapitest.Projects{Err: errBoom}
	got, want := repo.Config{
		Key:        "r",
		ProjectKey: "proj",
	}, repo.Config{
		Key:        "r",
		ProjectKey: "proj",
	}
	sameError(t, NewRepositories(world(t), testKeys, failing).CheckEnvironments(ctx, &got),
		CheckEnvironments(ctx, failing, &want), errBoom)
}

func TestUnitRepositoriesMerge(t *testing.T) {
	r := bound(world(t))
	stored := repo.Config{Key: "libs", Class: repo.Local}
	repo.Defaults(&stored)
	for _, patch := range []string{
		`{"description": "merged"}`,
		`{"description": null}`,
		`{"key": "other"}`,
		`{"rclass": "LOCAL", "notes": "kept"}`,
	} {
		gotPatch, wantPatch := map[string]json.RawMessage{}, map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(patch), &gotPatch); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(patch), &wantPatch); err != nil {
			t.Fatal(err)
		}
		got, err := r.Merge(stored, gotPatch)
		want, wantErr := Merge(stored, wantPatch)
		sameAnswer(t, patch, got, want, err, wantErr)
	}
}

func TestUnitRepositoriesOwesReindex(t *testing.T) {
	r := bound(world(t))
	untyped := repo.Config{Key: "tf", Class: repo.Local, PackageType: repo.Terraform}
	typed := untyped
	typed.TerraformType = repo.TerraformModule
	npm := func(class repo.Class, primary, secondary string) repo.Config {
		return repo.Config{
			Key: "n", Class: class, PackageType: repo.NPM,
			PrimaryKeyPairRef: primary, SecondaryKeyPairRef: secondary,
		}
	}
	for name, tc := range map[string]struct {
		stored, updated repo.Config
		want            bool
	}{
		"type given": {untyped, typed, true},
		"type kept":  {typed, typed, false},
		"still none": {untyped, untyped, false},
		// An npm repository signs every version again when its primary
		// pair changes (decision 0034, "Signatures").
		"npm primary set":     {npm(repo.Local, "", ""), npm(repo.Local, "k1", ""), true},
		"npm primary changed": {npm(repo.Local, "k1", ""), npm(repo.Local, "k2", "k1"), true},
		"npm primary cleared": {npm(repo.Federated, "k1", ""), npm(repo.Federated, "", ""), true},
		"npm primary kept":    {npm(repo.Local, "k1", ""), npm(repo.Local, "k1", "k2"), false},
		"npm remote":          {npm(repo.Remote, "", ""), npm(repo.Remote, "k1", ""), false},
		"not terraform": {
			repo.Config{Key: "g", PackageType: repo.Generic},
			repo.Config{Key: "g", PackageType: repo.Generic, TerraformType: repo.TerraformModule},
			false,
		},
	} {
		if got := r.OwesReindex(tc.stored, tc.updated); got != tc.want ||
			got != OwesReindex(tc.stored, tc.updated) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}
