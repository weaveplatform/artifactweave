package terraform_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errFault = errors.New("store fault")

// faults says which store methods fail: each armed method lets its count
// of calls through, then fails every call after.
type faults struct {
	pass map[string]int
}

func (f *faults) fail(method string) bool {
	n, ok := f.pass[method]
	if !ok {
		return false
	}
	if n > 0 {
		f.pass[method] = n - 1
		return false
	}
	return true
}

// faulty fails the item and package store methods its faults arm and
// delegates the rest, transactions included.
type faulty struct {
	store.Store
	f *faults
}

func (f *faulty) Packages() store.PackageStore { return faultyPackages{f.Store.Packages(), f.f} }

func (f *faulty) Items() store.ItemStore { return faultyItems{f.Store.Items(), f.f} }

type faultyPackages struct {
	store.PackageStore
	f *faults
}

//nolint:wrapcheck // test double
func (p faultyPackages) Ensure(ctx context.Context, v types.Package) (types.Package, error) {
	if p.f.fail("Ensure") {
		return types.Package{}, errFault
	}
	return p.PackageStore.Ensure(ctx, v)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Find(ctx context.Context, key, name string) (types.Package, error) {
	if p.f.fail("Find") {
		return types.Package{}, errFault
	}
	return p.PackageStore.Find(ctx, key, name)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Version(
	ctx context.Context, id int64, version string,
) (types.PackageVersion, error) {
	if p.f.fail("Version") {
		return types.PackageVersion{}, errFault
	}
	return p.PackageStore.Version(ctx, id, version)
}

type faultyItems struct {
	store.ItemStore
	f *faults
}

//nolint:wrapcheck // test double
func (i faultyItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (types.Item, error) {
	if i.f.fail("Get") {
		return types.Item{}, errFault
	}
	return i.ItemStore.Get(ctx, key, cached, p)
}

//nolint:wrapcheck // test double
func (i faultyItems) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	if i.f.fail("Search") {
		return nil, errFault
	}
	return i.ItemStore.Search(ctx, q, o)
}

// unreadable is a blob store whose content cannot be opened.
type unreadable struct{ blob.Store }

//nolint:wrapcheck // test double
func (unreadable) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	return nil, blob.Info{}, errFault
}

// faultWorld is a MODULE and a PROVIDER repository, holding a module and a
// Terraform provider package's release, for hooks called directly with a
// store or blobs that fail.
type faultWorld struct {
	*world
	moduleRepo, providerRepo repo.Config
	module, archive          types.Item
	sums                     types.Item
}

func newFaultWorld(t *testing.T) *faultWorld {
	t.Helper()
	w := &faultWorld{world: newWorld(t)}
	w.moduleRepo = w.repo("tf-modules", repo.TerraformModule)
	w.providerRepo = w.repo("tf-providers", repo.TerraformProvider)
	w.must(w.deploy(w.moduleRepo, "acme/vpc/aws/1.0.0.zip", zipOf(t, [2]string{"main.tf", "#"})))
	r := newRelease(t, "hashicorp/null/3.3.2", "null", "3.3.2", "linux_amd64")
	for _, s := range []string{"linux_amd64.zip", "SHA256SUMS"} {
		w.must(w.deploy(w.providerRepo, r.path(s), r.files[r.name(s)]))
	}
	get := func(c repo.Config, p string) types.Item {
		it, err := w.st.Items().Get(w.ctx, c.Key, false, p)
		w.must(err)
		return it
	}
	w.module = get(w.moduleRepo, "acme/vpc/aws/1.0.0.zip")
	w.archive = get(w.providerRepo, r.path("linux_amd64.zip"))
	w.sums = get(w.providerRepo, r.path("SHA256SUMS"))
	return w
}

// wipe drops the PROVIDER repository's index, so a hook must hash the
// archive again.
func (w *faultWorld) wipe() {
	p, err := w.st.Packages().Find(w.ctx, w.providerRepo.Key, "hashicorp/null")
	w.must(err)
	w.must(w.st.Packages().Delete(w.ctx, p.ID))
}

// Every store or blob failure a hook meets is its error, so the write it
// runs in rolls back rather than leaving the index behind the tree.
func TestUnitFaults(t *testing.T) {
	h := terraform.Handler{}
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		fail string
		pass int
		// blobs fail when set.
		blobs bool
		run   func(w *faultWorld, env format.Env) error
	}{
		{"a module's spellings", "Get", 0, false, func(w *faultWorld, env format.Env) error {
			return h.Deployed(ctx, env, w.moduleRepo, w.module)
		}},
		{"a module's package", "Ensure", 0, false, func(w *faultWorld, env format.Env) error {
			return h.Deployed(ctx, env, w.moduleRepo, w.module)
		}},
		{"a deleted module", "Get", 0, false, func(w *faultWorld, env format.Env) error {
			return h.Deleted(ctx, env, w.moduleRepo, []types.Item{w.module})
		}},
		{"a release's files", "Search", 0, false, func(w *faultWorld, env format.Env) error {
			return h.Deployed(ctx, env, w.providerRepo, w.sums)
		}},
		{"a deleted release's files", "Search", 0, false, func(w *faultWorld, env format.Env) error {
			return h.Deleted(ctx, env, w.providerRepo, []types.Item{w.archive})
		}},
		{"a release's package", "Find", 0, false, func(w *faultWorld, env format.Env) error {
			return h.Deployed(ctx, env, w.providerRepo, w.sums)
		}},
		{"a release's version", "Version", 0, false, func(w *faultWorld, env format.Env) error {
			return h.Deployed(ctx, env, w.providerRepo, w.sums)
		}},
		{"an archive's content", "", 0, true, func(w *faultWorld, env format.Env) error {
			w.wipe()
			return h.Deployed(ctx, env, w.providerRepo, w.sums)
		}},
		{"an inspected archive", "", 0, true, func(w *faultWorld, env format.Env) error {
			_, err := h.Inspect(ctx, env, w.providerRepo, format.Write{
				Op: format.OpDeploy, Path: w.archive.FullPath(), Checksums: w.archive.Actual,
			})
			return err
		}},
		{"a module reindex", "Search", 0, false, func(w *faultWorld, env format.Env) error {
			return h.Reindex(ctx, env, w.moduleRepo)
		}},
		{"a release reindex, hashing", "Search", 0, false, func(w *faultWorld, env format.Env) error {
			return h.Reindex(ctx, env, w.providerRepo)
		}},
		{"a release reindex, indexing", "Search", 1, false, func(w *faultWorld, env format.Env) error {
			return h.Reindex(ctx, env, w.providerRepo)
		}},
		{"a release reindex's archives", "", 0, true, func(w *faultWorld, env format.Env) error {
			return h.Reindex(ctx, env, w.providerRepo)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newFaultWorld(t)
			f := &faults{pass: map[string]int{}}
			if tc.fail != "" {
				f.pass[tc.fail] = tc.pass
			}
			env := format.Env{Store: &faulty{Store: w.st, f: f}, Blobs: w.blobs}
			if tc.blobs {
				env.Blobs = unreadable{w.blobs}
			}
			if err := tc.run(w, env); !errors.Is(err, errFault) {
				t.Fatalf("%v", err)
			}
		})
	}
}

// A version whose stored metadata cannot be read fails the hook that would
// reuse its hashes.
func TestUnitUnreadableMetadata(t *testing.T) {
	w := newFaultWorld(t)
	p, err := w.st.Packages().Find(w.ctx, w.providerRepo.Key, "hashicorp/null")
	w.must(err)
	pv, err := w.st.Packages().Version(w.ctx, p.ID, "3.3.2")
	w.must(err)
	pv.Metadata = []byte("{")
	_, err = w.st.Packages().PutVersion(w.ctx, pv)
	w.must(err)
	env := format.Env{Store: w.st, Blobs: w.blobs}
	if err := (terraform.Handler{}).Deployed(w.ctx, env, w.providerRepo, w.sums); err == nil {
		t.Fatal("no error")
	}
	if _, err := terraform.ReadMeta([]byte("[]")); err == nil {
		t.Fatal("an array read as metadata")
	}
}

// Folders, files outside where the index reads, and files the
// repository's type does not index touch nothing, on deploy and on delete;
// a folder where a module's zip would be stored is passed over for the
// other spelling.
func TestUnitWhatTheHooksPassOver(t *testing.T) {
	w := newFaultWorld(t)
	f := &faults{pass: map[string]int{"Get": 0, "Search": 0, "Ensure": 0}}
	env := format.Env{Store: &faulty{Store: w.st, f: f}, Blobs: unreadable{w.blobs}}
	h := terraform.Handler{}
	folder := types.Item{
		RepoKey: w.providerRepo.Key,
		Path:    "hashicorp/null",
		Name:    "3.3.2",
		Folder:  true,
	}
	cached := w.archive
	cached.Cached = true
	readme := types.Item{
		RepoKey: w.providerRepo.Key,
		Path:    "hashicorp/null/3.3.2",
		Name:    "README.md",
	}
	// What the repository's type indexes, which the faults would fail.
	indexes := func(c repo.Config, it types.Item) bool {
		p := it.FullPath()
		return !it.Cached && (p == w.module.FullPath() && c.TerraformType == repo.TerraformModule ||
			p == w.archive.FullPath() && c.TerraformType == repo.TerraformProvider)
	}
	for _, c := range []repo.Config{w.moduleRepo, w.providerRepo, w.repo("tf-untyped", "")} {
		for _, it := range []types.Item{folder, cached, readme, w.module, w.archive} {
			if indexes(c, it) {
				continue
			}
			if err := h.Deployed(w.ctx, env, c, it); err != nil {
				t.Errorf("%s: deployed %s: %v", c.Key, it.FullPath(), err)
			}
		}
		if err := h.Deleted(w.ctx, env, c, []types.Item{folder, cached, readme}); err != nil {
			t.Errorf("%s: deleted: %v", c.Key, err)
		}
		if err := h.Deleting(w.ctx, env, c, w.module); err != nil {
			t.Errorf("%s: deleting: %v", c.Key, err)
		}
	}
	// A folder named as the module's zip.
	w.must(w.deploy(w.moduleRepo, "acme/vpc/aws/2.0.0.zip/README.md", []byte("not a zip")))
	w.must(w.deploy(w.moduleRepo, "acme/vpc/aws/v2.0.0.zip", zipOf(t, [2]string{"main.tf", "#"})))
	if v, ok := w.version(w.moduleRepo, "acme/vpc/aws", "2.0.0"); !ok ||
		len(v.files) != 1 || v.files[0] != "acme/vpc/aws/v2.0.0.zip" {
		t.Fatalf("2.0.0: %+v %v", v, ok)
	}
}
