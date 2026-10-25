package cran

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type cranReadCloser struct{ *bytes.Reader }

func (cranReadCloser) Close() error { return nil }

type cranFixtureBlobs struct{ body []byte }

func (b cranFixtureBlobs) Open(_ context.Context, _ string) (io.ReadSeekCloser, blob.Info, error) {
	return cranReadCloser{bytes.NewReader(b.body)}, blob.Info{Size: int64(len(b.body))}, nil
}

func (b cranFixtureBlobs) Stat(_ context.Context, _ string) (blob.Info, error) {
	return blob.Info{Size: int64(len(b.body))}, nil
}

func TestUnitCRANLifecycleRegeneratesOnlyAdvertisedPackages(t *testing.T) {
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "cran-local", Class: repo.Local, PackageType: repo.Cran})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	const packagePath = "src/contrib/assertthat_0.2.1.tar.gz"
	body, err := os.ReadFile("testdata/assertthat_0.2.1.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)), packagePath)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	it, err := st.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key, Name: packagePath, Size: int64(len(body)),
		Actual: types.Checksums{SHA256: fmt.Sprintf("%x", hash)},
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := "src/contrib/PACKAGES"
	if _, err := st.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key, Name: stale, Size: 7,
	}); err != nil {
		t.Fatal(err)
	}
	generated := map[string][]byte{}
	removed := map[string]bool{}
	queued := 0
	env := format.Env{
		Store:      st,
		Inspection: inspection{sha: it.Actual.SHA256, path: packagePath, pkg: pkg},
		Derive: func(_ context.Context, d format.Derived) (types.Item, error) {
			body, err := io.ReadAll(d.Body)
			if err != nil {
				return types.Item{}, err
			}
			generated[d.Path] = body
			return types.Item{}, nil
		},
		Underive: func(_ context.Context, _ repo.Config, _ bool, path string) error {
			removed[path] = true
			return nil
		},
		Enqueue: func(_ context.Context, kind, key string, _ []byte) error {
			if kind != KindIndex || key != c.Key {
				t.Errorf("unexpected index work %q %q", kind, key)
			}
			queued++
			return nil
		},
	}
	h := Handler{}
	if err := h.Deployed(t.Context(), env, c, it); err != nil {
		t.Fatal(err)
	}
	props, err := st.Items().Properties(t.Context(), it.ID)
	if err != nil || len(props[PropName]) != 1 || props[PropName][0] != "assertthat" {
		t.Fatalf("deployed package properties = %v, %+v", err, props)
	}
	if err := h.Work(t.Context(), env, types.WorkItem{Kind: KindIndex, Key: c.Key}); err != nil {
		t.Fatal(err)
	}
	env.Derive = func(_ context.Context, _ format.Derived) (types.Item, error) {
		return types.Item{}, errors.New("derived storage unavailable")
	}
	if err := h.Work(t.Context(), env, types.WorkItem{Kind: KindIndex, Key: c.Key}); err == nil {
		t.Fatal("failed derived index publication was ignored")
	}
	plain := generated[stale]
	got, err := DecodeIndex(plain, stale)
	if err != nil || len(got) != 1 || got[0].Name != "assertthat" ||
		got[0].Version != "0.2.1" {
		t.Fatalf("generated native PACKAGES = %+v, %v", got, err)
	}
	if len(generated["src/contrib/PACKAGES.gz"]) == 0 || removed[stale] {
		t.Fatalf("index output incomplete: generated=%v removed=%v", generated, removed)
	}
	if err := h.Reindex(t.Context(), env, c); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleting(t.Context(), env, c, it); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleted(t.Context(), env, c, []types.Item{it, it}); err != nil {
		t.Fatal(err)
	}
	if queued < 3 {
		t.Fatalf("expected lifecycle and reindex work, queued %d", queued)
	}
}

func TestUnitCRANIndexWorkRemovesStaleMetadataAfterLastPackage(t *testing.T) {
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "cran-local", Class: repo.Local, PackageType: repo.Cran})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PACKAGES", "PACKAGES.gz"} {
		if _, err := st.Items().Put(t.Context(), types.Item{
			RepoKey: c.Key, Name: "src/contrib/" + name, Size: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed := map[string]bool{}
	env := format.Env{
		Store: st,
		Underive: func(_ context.Context, _ repo.Config, _ bool, p string) error {
			removed[p] = true
			return nil
		},
	}
	if err := (Handler{}).Work(
		t.Context(),
		env,
		types.WorkItem{Kind: KindIndex, Key: c.Key},
	); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PACKAGES", "PACKAGES.gz"} {
		if !removed["src/contrib/"+name] {
			t.Fatalf("stale %s was not removed", name)
		}
	}
	if err := (Handler{}).Work(
		t.Context(),
		env,
		types.WorkItem{Kind: KindIndex, Key: "absent"},
	); err != nil {
		t.Fatalf("deleted repository work must be harmless: %v", err)
	}
	env.Underive = func(_ context.Context, _ repo.Config, _ bool, _ string) error {
		return errors.New("derived storage unavailable")
	}
	if err := (Handler{}).Work(
		t.Context(),
		env,
		types.WorkItem{Kind: KindIndex, Key: c.Key},
	); err == nil {
		t.Fatal("failed stale index removal was ignored")
	}
	remote := *repo.Defaults(&repo.Config{Key: "cran-remote", Class: repo.Remote, PackageType: repo.Cran})
	if err := st.Repos().Create(t.Context(), remote); err != nil {
		t.Fatal(err)
	}
	if err := (Handler{}).Work(
		t.Context(),
		env,
		types.WorkItem{Kind: KindIndex, Key: remote.Key},
	); err != nil {
		t.Fatalf("remote index must not derive local metadata: %v", err)
	}
}

func TestUnitCRANInspectsCommittedBytesBeforeAdmission(t *testing.T) {
	const p = "src/contrib/assertthat_0.2.1.tar.gz"
	body, err := os.ReadFile("testdata/assertthat_0.2.1.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	w := format.Write{
		Op: format.OpDeploy, Path: p,
		Checksums: types.Checksums{SHA256: fmt.Sprintf("%x", hash)},
	}
	h := Handler{}
	c := repo.Config{Key: "cran-local", Class: repo.Local, PackageType: repo.Cran}
	env := format.Env{Blobs: cranFixtureBlobs{body: body}}
	got, err := h.Inspect(t.Context(), env, c, w)
	if err != nil {
		t.Fatal(err)
	}
	inspected, ok := got.(inspection)
	if !ok || inspected.pkg.Name != "assertthat" || inspected.pkg.Version != "0.2.1" {
		t.Fatalf("inspection = %+v", got)
	}
	env.Inspection = got
	if _, err := h.Admit(t.Context(), env, c, w); err != nil {
		t.Fatalf("valid committed archive refused: %v", err)
	}
	other := w
	other.Checksums.SHA256 = "different"
	if _, err := h.Admit(t.Context(), env, c, other); err != nil {
		t.Fatalf("stale inspection should revalidate bytes: %v", err)
	}
	env.Blobs = cranFixtureBlobs{body: []byte("invalid archive")}
	if _, err := h.Inspect(t.Context(), env, c, w); err == nil {
		t.Fatal("invalid committed archive passed inspection")
	}
	if _, err := h.Admit(t.Context(), env, c, other); err == nil {
		t.Fatal("stale inspection admitted invalid bytes")
	}
}

func TestUnitCRANRejectsMissingCommittedBytesAndFailedWorkQueue(t *testing.T) {
	t.Parallel()
	h := Handler{}
	c := repo.Config{Key: "cran-local", Class: repo.Local, PackageType: repo.Cran}
	w := format.Write{Op: format.OpDeploy, Path: "src/contrib/demo_1.0.tar.gz"}
	if _, err := h.Inspect(t.Context(), format.Env{}, c, w); err == nil {
		t.Fatal("package with no committed blob passed inspection")
	}
	it := types.Item{RepoKey: c.Key, Name: w.Path}
	if err := h.Deployed(t.Context(), format.Env{}, c, it); err == nil {
		t.Fatal("package with no committed blob passed deployment")
	}
	want := errors.New("work queue unavailable")
	env := format.Env{Enqueue: func(_ context.Context, _, _ string, _ []byte) error {
		return want
	}}
	if err := h.Deleting(t.Context(), env, c, it); !errors.Is(err, want) {
		t.Fatalf("failed index work queue = %v", err)
	}
}

func TestUnitCRANLifecycleRefusesPersistedPackageWithoutContent(t *testing.T) {
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "cran-damaged", Class: repo.Local, PackageType: repo.Cran})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	it, err := st.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key, Name: "src/contrib/assertthat_0.2.1.tar.gz", Size: 12742,
	})
	if err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: st}
	h := Handler{}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"reindex", func() error { return h.Reindex(t.Context(), env, c) }},
		{"index work", func() error {
			return h.Work(t.Context(), env, types.WorkItem{Kind: KindIndex, Key: c.Key})
		}},
		{"deletion reconciliation", func() error { return h.Deleted(t.Context(), env, c, []types.Item{it}) }},
		{"version reconciliation", func() error {
			return syncVersion(t.Context(), env, c, "assertthat", "0.2.1")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil {
				t.Fatal("corrupt package was treated as valid metadata")
			}
		})
	}
}

func TestUnitCRANLifecycleSkipsIrrelevantItemsAndHonorsCancellation(t *testing.T) {
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "cran-cancel", Class: repo.Local, PackageType: repo.Cran})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	h := Handler{}
	env := format.Env{Store: st}
	folder := types.Item{RepoKey: c.Key, Name: "src/contrib", Folder: true}
	if err := h.Deployed(t.Context(), env, c, folder); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleting(t.Context(), env, c, folder); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleted(t.Context(), env, c, []types.Item{{
		RepoKey: c.Key, Name: "src/contrib/README",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := h.Work(t.Context(), env, types.WorkItem{Kind: "other", Key: c.Key}); err != nil {
		t.Fatal(err)
	}
	if err := setProperties(t.Context(), st, types.Item{ID: -1}, Package{
		Name: "demo", Version: "1.0", Feed: "src/contrib",
	}); err == nil {
		t.Fatal("missing item accepted CRAN properties")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"files", func() error { _, err := files(ctx, st, c); return err }},
		{"reindex", func() error { return h.Reindex(ctx, env, c) }},
		{"work", func() error { return h.Work(ctx, env, types.WorkItem{Kind: KindIndex, Key: c.Key}) }},
		{"version", func() error { return syncVersion(ctx, env, c, "demo", "1.0") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled CRAN lifecycle: %v", err)
			}
		})
	}
	body, err := os.ReadFile("testdata/assertthat_0.2.1.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	it, err := st.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key, Name: "src/contrib/assertthat_0.2.1.tar.gz",
		Actual: types.Checksums{SHA256: "fixture"}, Size: int64(len(body)),
	})
	if err != nil {
		t.Fatal(err)
	}
	env.Blobs = cranFixtureBlobs{body: body}
	if err := syncVersion(t.Context(), env, c, "assertthat", "9.9"); err != nil {
		t.Fatalf("absent version should not be indexed: %v", err)
	}
	if _, err := st.Packages().
		Find(t.Context(), c.Key, "assertthat"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("absent version was registered: item=%d err=%v", it.ID, err)
	}
}

func TestUnitCRANArchiveAndUnrelatedFilesDoNotEnterTerminalIndex(t *testing.T) {
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "cran-history", Class: repo.Local, PackageType: repo.Cran})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	const archived = "src/contrib/Archive/demo/demo_1.0.tar.gz"
	for _, p := range []string{archived, "src/contrib/README"} {
		if _, err := st.Items().Put(t.Context(), types.Item{
			RepoKey: c.Key, Name: p, Actual: types.Checksums{SHA256: "fixture"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	body := sourceArchive(t, "demo", "1.0", "demo/DESCRIPTION", true)
	derived := 0
	env := format.Env{
		Store: st, Blobs: cranFixtureBlobs{body: body},
		Derive: func(context.Context, format.Derived) (types.Item, error) {
			derived++
			return types.Item{}, nil
		},
	}
	if err := (Handler{}).Work(
		t.Context(),
		env,
		types.WorkItem{Kind: KindIndex, Key: c.Key},
	); err != nil {
		t.Fatal(err)
	}
	if derived != 0 {
		t.Fatalf("archive or README entered terminal PACKAGES index: %d derivations", derived)
	}
}

func TestUnitCRANLifecycleRefusesMissingPropertyTarget(t *testing.T) {
	st := inmem.New()
	c := repo.Config{Key: "cran-local", Class: repo.Local, PackageType: repo.Cran}
	const p = "src/contrib/demo_1.0.tar.gz"
	it := types.Item{
		ID: -1, RepoKey: c.Key, Name: p,
		Actual: types.Checksums{SHA256: "fixture"},
	}
	env := format.Env{Store: st, Inspection: inspection{
		sha: "fixture", path: p, pkg: Package{
			Name: "demo", Version: "1.0", Path: p, Feed: "src/contrib",
		},
	}}
	if err := (Handler{}).Deployed(t.Context(), env, c, it); err == nil {
		t.Fatal("deployment succeeded without a property target")
	}
	for _, config := range []repo.Config{
		{Key: c.Key, Class: repo.Local, PackageType: repo.Cran},
		{Key: "cran-proxy", Class: repo.Remote, PackageType: repo.Cran},
	} {
		if err := queue(
			t.Context(),
			format.Env{Enqueue: func(context.Context, string, string, []byte) error {
				t.Fatal("cached or remote content queued local index work")
				return nil
			}},
			config,
			true,
		); err != nil {
			t.Fatal(err)
		}
	}
}
