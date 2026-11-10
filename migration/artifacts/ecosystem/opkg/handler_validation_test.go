package opkg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
)

var errOpkgTest = errors.New("opkg test failure")

type (
	invalidOpkgBlobs struct{}
	opkgReadCloser   struct{ *bytes.Reader }
)

func (*opkgReadCloser) Close() error { return nil }
func (invalidOpkgBlobs) Open(_ context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if sha == "missing" {
		return nil, blob.Info{}, errOpkgTest
	}
	return &opkgReadCloser{bytes.NewReader([]byte("invalid ipk"))}, blob.Info{Size: 11}, nil
}

func (invalidOpkgBlobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{}, errOpkgTest
}

func TestUnitHandlerAdmissionAndDerivedOwnership(t *testing.T) {
	t.Parallel()
	h := Handler{}
	local := repo.Config{Key: "local", Class: repo.Local, PackageType: repo.Opkg}
	remote := repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Opkg}
	if len(h.WorkKinds()) != 1 || h.WorkKinds()[0] != KindIndex {
		t.Fatal("work kind")
	}
	if got := h.Owed(local, false, "feed/Packages.gz"); len(got) != 1 || got[0].Key != local.Key {
		t.Fatalf("local index work: %+v", got)
	}
	for _, tc := range []struct {
		c      repo.Config
		cached bool
		path   string
	}{
		{local, true, "feed/Packages"},
		{remote, false, "feed/Packages"},
		{local, false, "feed/alpha_1.0_all.ipk"},
	} {
		if got := h.Owed(tc.c, tc.cached, tc.path); len(got) != 0 {
			t.Fatalf("unexpected owed work: %+v", got)
		}
	}
	if h.Classify(local, "feed/Packages") != format.ClassDerived ||
		h.Classify(local, "feed/alpha_1.0_all.ipk") != format.ClassUnique {
		t.Fatal("derived ownership")
	}
	if _, err := h.Admit(t.Context(), format.Env{}, local,
		format.Write{Op: format.OpDeploy, Path: "feed/Packages"}); err == nil {
		t.Fatal("generated metadata upload accepted")
	}
	if _, err := h.Admit(t.Context(), format.Env{}, local,
		format.Write{Op: format.OpDeploy, Path: "feed/unsafe%name.ipk"}); err == nil {
		t.Fatal("unsafe package path accepted")
	}
	for _, w := range []format.Write{
		{Op: format.OpChecksum, Path: "feed/alpha_1.0_all.ipk"},
		{Op: format.OpDeploy, Path: "feed/README"},
		{Op: format.OpDeploy, Cached: true, Path: "feed/Packages"},
	} {
		if _, err := h.Admit(t.Context(), format.Env{}, local, w); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Inspect(t.Context(), format.Env{}, local, w); err != nil {
			t.Fatal(err)
		}
	}
	for _, sha := range []string{"", "missing", "invalid"} {
		w := format.Write{
			Op: format.OpDeploy, Path: "feed/alpha_1.0_all.ipk",
			Checksums: kernel.Checksums{SHA256: sha},
		}
		if _, err := h.Inspect(
			t.Context(),
			format.Env{Blobs: invalidOpkgBlobs{}},
			local,
			w,
		); err == nil {
			t.Fatalf("invalid %q package passed inspection", sha)
		}
		if _, err := h.Admit(
			t.Context(),
			format.Env{Blobs: invalidOpkgBlobs{}},
			local,
			w,
		); err == nil {
			t.Fatalf("invalid %q package passed admission", sha)
		}
	}
}

func TestUnitHandlerDeletionAndWorkFailures(t *testing.T) {
	t.Parallel()
	h := Handler{}
	st := inmem.New()
	ctx := t.Context()
	local := *repo.Defaults(&repo.Config{Key: "opkg-local", Class: repo.Local, PackageType: repo.Opkg})
	remote := *repo.Defaults(&repo.Config{Key: "opkg-remote", Class: repo.Remote, PackageType: repo.Opkg})
	other := *repo.Defaults(&repo.Config{Key: "other", Class: repo.Local, PackageType: repo.Generic})
	for _, c := range []repo.Config{local, remote, other} {
		if err := st.Repos().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	file := model.Item{
		RepoKey: local.Key, Path: "feed", Name: "alpha_1.0_all.ipk",
		Size: 123, Actual: kernel.Checksums{SHA256: strings.Repeat("a", 64)},
	}
	stored, err := st.Items().Put(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	old, err := st.Items().Put(ctx, model.Item{
		RepoKey: local.Key, Path: "old", Name: "Packages",
		Actual: kernel.Checksums{SHA256: "old"}, Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := validIndexPackage()
	env := format.Env{
		Store: st,
		Keys:  &accessapitest.Keys{},
		Inspection: inspection{
			sha:  stored.Actual.SHA256,
			path: stored.FullPath(), pkg: pkg,
		},
		Enqueue:  func(context.Context, string, string, []byte) error { return errOpkgTest },
		Derive:   func(context.Context, format.Derived) (model.Item, error) { return model.Item{}, errOpkgTest },
		Underive: func(context.Context, repo.Config, bool, string) error { return errOpkgTest },
	}
	if err := h.Deleting(ctx, env, local, stored); !errors.Is(err, errOpkgTest) {
		t.Fatalf("local delete queue: %v", err)
	}
	for _, tc := range []struct {
		c  repo.Config
		it model.Item
	}{
		{remote, stored},
		{local, model.Item{Folder: true, Name: "a.ipk"}},
		{local, model.Item{Name: "README"}},
		{local, model.Item{Name: "a.ipk", Cached: true}},
	} {
		if err := h.Deleting(ctx, env, tc.c, tc.it); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Deployed(ctx, env, local, model.Item{Folder: true, Name: "a.ipk"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(ctx, env, local, stored); !errors.Is(err, errOpkgTest) {
		t.Fatalf("local deploy queue: %v", err)
	}
	if err := h.Deleted(ctx, env, local, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleted(ctx, env, local, []model.Item{stored, stored}); err != nil {
		t.Fatalf("duplicate coordinate removal: %v", err)
	}
	if err := h.Deleted(ctx, env, local, []model.Item{{Name: "bad.ipk"}}); err == nil {
		t.Fatal("deleted invalid package accepted")
	}
	for _, tc := range []struct{ kind, key string }{
		{"retired", local.Key}, {KindIndex, "missing"}, {KindIndex, other.Key}, {KindIndex, remote.Key},
	} {
		if err := h.Work(ctx, env, work.WorkItem{Kind: tc.kind, Key: tc.key}); err != nil {
			t.Fatal(err)
		}
	}
	work := work.WorkItem{Kind: KindIndex, Key: local.Key}
	if err := h.Work(ctx, env, work); !errors.Is(err, errOpkgTest) {
		t.Fatalf("derive failure: %v", err)
	}
	env.Derive = func(_ context.Context, d format.Derived) (model.Item, error) {
		if d.Path != "feed/Packages" && d.Path != "feed/Packages.gz" && d.Path != "feed/Release" {
			t.Errorf("unexpected derived path: %s", d.Path)
		}
		return model.Item{}, nil
	}
	if err := h.Work(ctx, env, work); !errors.Is(err, errOpkgTest) {
		t.Fatalf("underive failure: %v", err)
	}
	env.Underive = func(_ context.Context, _ repo.Config, _ bool, path string) error {
		if path != old.FullPath() {
			t.Errorf("unexpected stale path: %s", path)
		}
		return nil
	}
	if err := h.Work(ctx, env, work); err != nil {
		t.Fatal(err)
	}
	env.SkipProperties = true
	env.Enqueue = func(context.Context, string, string, []byte) error { return nil }
	if err := h.Reindex(ctx, env, local); err != nil {
		t.Fatalf("property-free metadata reindex: %v", err)
	}
	local.PrimaryKeyPairRef = "missing"
	if err := st.Repos().Update(ctx, local); err != nil {
		t.Fatal(err)
	}
	if err := h.Work(ctx, env, work); err == nil {
		t.Fatal("missing signing key accepted")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.Work(ctx, env, work); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled work: %v", err)
	}
}

func TestUnitSignFeedBindsPackagesAndRejectsBadKeys(t *testing.T) {
	keys := &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{}}
	ctx := t.Context()
	local := repo.Config{Key: "opkg-local", Class: repo.Local, PackageType: repo.Opkg}
	files := map[string][]byte{PackagesName: []byte("Package: alpha\n\n")}
	if err := SignFeed(ctx, keys, local, files); err != nil || files[SignatureName] != nil {
		t.Fatalf("unsigned feed: %v", err)
	}
	local.PrimaryKeyPairRef = "missing"
	if err := SignFeed(ctx, keys, local, files); err == nil {
		t.Fatal("missing key accepted")
	}
	keys.Keypairs["wrong"] = accessapi.Keypair{Name: "wrong", Type: accessapi.KeypairRSA}
	local.PrimaryKeyPairRef = "wrong"
	if err := SignFeed(ctx, keys, local, files); err == nil {
		t.Fatal("RSA key accepted as GPG")
	}
	keys.Keypairs["bad"] = accessapi.Keypair{
		Name: "bad", Type: accessapi.KeypairGPG,
		PrivateKey: "not armored",
	}
	local.PrimaryKeyPairRef = "bad"
	if err := SignFeed(ctx, keys, local, files); err == nil {
		t.Fatal("bad GPG key accepted")
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	keys.Keypairs["good"] = accessapi.Keypair{
		Name: "good", Type: accessapi.KeypairGPG,
		PrivateKey: key.Private, PublicKey: key.Public,
	}
	local.PrimaryKeyPairRef = "good"
	if err := SignFeed(ctx, keys, local, files); err != nil {
		t.Fatal(err)
	}
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key.Public))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		ring,
		bytes.NewReader(
			files[PackagesName],
		),
		bytes.NewReader(files[SignatureName]),
		nil,
	); err != nil {
		t.Fatalf("generated index signature: %v", err)
	}
	virtual := repo.Config{Class: repo.Virtual, KeyPair: "good", PrimaryKeyPairRef: "missing"}
	if err := SignFeed(ctx, keys, virtual, files); err != nil {
		t.Fatalf("virtual signing ref: %v", err)
	}
}

func TestUnitIndexWorkSignsFeedWithEnvKeys(t *testing.T) {
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{
		Key: "opkg-local", Class: repo.Local, PackageType: repo.Opkg, PrimaryKeyPairRef: "feed",
	})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	stored, err := st.Items().Put(t.Context(), model.Item{
		RepoKey: c.Key, Path: "feed", Name: "alpha_1.0_all.ipk",
		Size: 123, Actual: kernel.Checksums{SHA256: strings.Repeat("a", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	generated := map[string][]byte{}
	env := format.Env{
		Store: st,
		// The store holds no key pair, so a feed signed with this one was
		// signed through Keys.
		Keys: &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{"feed": {
			Name: "feed", Type: accessapi.KeypairGPG, PrivateKey: key.Private,
		}}},
		Inspection: inspection{
			sha: stored.Actual.SHA256, path: stored.FullPath(), pkg: validIndexPackage(),
		},
		Derive: func(_ context.Context, d format.Derived) (model.Item, error) {
			body, err := io.ReadAll(d.Body)
			generated[d.Path] = body
			return model.Item{}, err
		},
	}
	work := work.WorkItem{Kind: KindIndex, Key: c.Key}
	if err := (Handler{}).Work(t.Context(), env, work); err != nil {
		t.Fatal(err)
	}
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key.Public))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		ring,
		bytes.NewReader(generated["feed/"+PackagesName]),
		bytes.NewReader(generated["feed/"+SignatureName]),
		nil,
	); err != nil {
		t.Fatalf("Packages.sig not signed with the key pair from Keys: %v", err)
	}
}

func TestUnitHandlerLifecycleRejectsMissingAndUninspectableIPKs(t *testing.T) {
	ctx := t.Context()
	h := Handler{}
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "opkg-lifecycle", Class: repo.Local, PackageType: repo.Opkg})
	if err := st.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	it, err := st.Items().Put(ctx, model.Item{
		RepoKey: c.Key, Path: "feed", Name: "alpha_1.0_all.ipk",
		Size: 10, Actual: kernel.Checksums{SHA256: "missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: st, Blobs: invalidOpkgBlobs{}}
	if err := h.Deployed(ctx, env, c, it); err == nil {
		t.Fatal("uninspectable deployed IPK accepted")
	}
	if err := h.Reindex(ctx, env, c); err == nil {
		t.Fatal("reindex silently published uninspectable IPK")
	}
	if err := h.Work(ctx, env, work.WorkItem{Kind: KindIndex, Key: c.Key}); err == nil {
		t.Fatal("index worker silently published uninspectable IPK")
	}
	env.Inspection = inspection{
		sha: it.Actual.SHA256, path: it.FullPath(), pkg: validIndexPackage(),
	}
	missing := it
	missing.ID = 999999
	if err := h.Deployed(ctx, env, c, missing); err == nil {
		t.Fatal("deployed IPK with vanished item accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.Reindex(canceled, env, c); err == nil {
		t.Fatal("canceled reindex accepted")
	}
	if err := h.Deleted(canceled, env, c, []model.Item{it}); err == nil {
		t.Fatal("canceled version cleanup accepted")
	}
}

type (
	opkgFailedSearchStore struct{ format.Store }
	opkgFailedSearchItems struct{ port.ItemStore }
)

func (s opkgFailedSearchStore) Items() port.ItemStore {
	return opkgFailedSearchItems{s.Store.Items()}
}

func (opkgFailedSearchItems) Search(context.Context, *aql.Query, aql.Options) ([]aql.Hit, error) {
	return nil, errOpkgTest
}

func TestUnitHandlerLifecycleStopsWhenPackageInventoryFails(t *testing.T) {
	ctx := t.Context()
	h := Handler{}
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "opkg-failed-search", Class: repo.Local, PackageType: repo.Opkg})
	if err := st.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	it, err := st.Items().Put(ctx, model.Item{
		RepoKey: c.Key, Path: "feed", Name: "alpha_1.0_all.ipk",
		Actual: kernel.Checksums{SHA256: "sha"}, Size: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	env := format.Env{
		Store: opkgFailedSearchStore{st},
		Inspection: inspection{
			sha: it.Actual.SHA256, path: it.FullPath(), pkg: validIndexPackage(),
		},
	}
	for name, run := range map[string]func() error{
		"deploy":  func() error { return h.Deployed(ctx, env, c, it) },
		"delete":  func() error { return h.Deleted(ctx, env, c, []model.Item{it}) },
		"reindex": func() error { return h.Reindex(ctx, env, c) },
		"work": func() error {
			return h.Work(ctx, env, work.WorkItem{Kind: KindIndex, Key: c.Key})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); !errors.Is(err, errOpkgTest) {
				t.Fatalf("package inventory fault was ignored: %v", err)
			}
		})
	}
}
