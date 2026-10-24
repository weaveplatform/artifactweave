package opkg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
	return &opkgReadCloser{bytes.NewReader([]byte("invalid IPK"))}, blob.Info{Size: 11}, nil
}

func (invalidOpkgBlobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{}, errOpkgTest
}

func TestHandlerAdmissionAndDerivedOwnership(t *testing.T) {
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
			Checksums: types.Checksums{SHA256: sha},
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

func TestHandlerDeletionAndWorkFailures(t *testing.T) {
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
	file := types.Item{
		RepoKey: local.Key, Path: "feed", Name: "alpha_1.0_all.ipk",
		Size: 123, Actual: types.Checksums{SHA256: strings.Repeat("a", 64)},
	}
	stored, err := st.Items().Put(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	old, err := st.Items().Put(ctx, types.Item{
		RepoKey: local.Key, Path: "old", Name: "Packages",
		Actual: types.Checksums{SHA256: "old"}, Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := validIndexPackage()
	env := format.Env{
		Store: st,
		Inspection: inspection{
			sha:  stored.Actual.SHA256,
			path: stored.FullPath(), pkg: pkg,
		},
		Enqueue:  func(context.Context, string, string, []byte) error { return errOpkgTest },
		Derive:   func(context.Context, format.Derived) (types.Item, error) { return types.Item{}, errOpkgTest },
		Underive: func(context.Context, repo.Config, bool, string) error { return errOpkgTest },
	}
	if err := h.Deleting(ctx, env, local, stored); !errors.Is(err, errOpkgTest) {
		t.Fatalf("local delete queue: %v", err)
	}
	for _, tc := range []struct {
		c  repo.Config
		it types.Item
	}{
		{remote, stored},
		{local, types.Item{Folder: true, Name: "a.ipk"}},
		{local, types.Item{Name: "README"}},
		{local, types.Item{Name: "a.ipk", Cached: true}},
	} {
		if err := h.Deleting(ctx, env, tc.c, tc.it); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Deployed(ctx, env, local, types.Item{Folder: true, Name: "a.ipk"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(ctx, env, local, stored); !errors.Is(err, errOpkgTest) {
		t.Fatalf("local deploy queue: %v", err)
	}
	if err := h.Deleted(ctx, env, local, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleted(ctx, env, local, []types.Item{stored, stored}); err != nil {
		t.Fatalf("duplicate coordinate removal: %v", err)
	}
	if err := h.Deleted(ctx, env, local, []types.Item{{Name: "bad.ipk"}}); err == nil {
		t.Fatal("deleted invalid package accepted")
	}
	for _, tc := range []struct{ kind, key string }{
		{"retired", local.Key}, {KindIndex, "missing"}, {KindIndex, other.Key}, {KindIndex, remote.Key},
	} {
		if err := h.Work(ctx, env, types.WorkItem{Kind: tc.kind, Key: tc.key}); err != nil {
			t.Fatal(err)
		}
	}
	work := types.WorkItem{Kind: KindIndex, Key: local.Key}
	if err := h.Work(ctx, env, work); !errors.Is(err, errOpkgTest) {
		t.Fatalf("derive failure: %v", err)
	}
	env.Derive = func(_ context.Context, d format.Derived) (types.Item, error) {
		if d.Path != "feed/Packages" && d.Path != "feed/Packages.gz" && d.Path != "feed/Release" {
			t.Errorf("unexpected derived path: %s", d.Path)
		}
		return types.Item{}, nil
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

func TestSignFeedBindsPackagesAndRejectsBadKeys(t *testing.T) {
	st := inmem.New()
	ctx := t.Context()
	local := repo.Config{Key: "opkg-local", Class: repo.Local, PackageType: repo.Opkg}
	files := map[string][]byte{PackagesName: []byte("Package: alpha\n\n")}
	if err := SignFeed(ctx, st, local, files); err != nil || files[SignatureName] != nil {
		t.Fatalf("unsigned feed: %v", err)
	}
	local.PrimaryKeyPairRef = "missing"
	if err := SignFeed(ctx, st, local, files); err == nil {
		t.Fatal("missing key accepted")
	}
	if err := st.Keys().
		CreateKeypair(ctx, types.Keypair{Name: "wrong", Type: types.KeypairRSA}); err != nil {
		t.Fatal(err)
	}
	local.PrimaryKeyPairRef = "wrong"
	if err := SignFeed(ctx, st, local, files); err == nil {
		t.Fatal("RSA key accepted as GPG")
	}
	if err := st.Keys().CreateKeypair(ctx, types.Keypair{
		Name: "bad", Type: types.KeypairGPG,
		PrivateKey: "not armored",
	}); err != nil {
		t.Fatal(err)
	}
	local.PrimaryKeyPairRef = "bad"
	if err := SignFeed(ctx, st, local, files); err == nil {
		t.Fatal("bad GPG key accepted")
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Keys().CreateKeypair(ctx, types.Keypair{
		Name: "good", Type: types.KeypairGPG,
		PrivateKey: key.Private, PublicKey: key.Public,
	}); err != nil {
		t.Fatal(err)
	}
	local.PrimaryKeyPairRef = "good"
	if err := SignFeed(ctx, st, local, files); err != nil {
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
	if err := SignFeed(ctx, st, virtual, files); err != nil {
		t.Fatalf("virtual signing ref: %v", err)
	}
}
