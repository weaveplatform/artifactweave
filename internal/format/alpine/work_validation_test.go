package alpine

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errIndexWrite = errors.New("index persistence failed")

func TestIndexWorkTracksCurrentRepository(t *testing.T) {
	st := inmem.New()
	ctx := t.Context()
	local := *repo.Defaults(&repo.Config{Key: "alpine-local", Class: repo.Local, PackageType: repo.Alpine})
	remote := *repo.Defaults(&repo.Config{Key: "alpine-remote", Class: repo.Remote, PackageType: repo.Alpine})
	generic := *repo.Defaults(&repo.Config{Key: "generic-local", Class: repo.Local, PackageType: repo.Generic})
	for _, c := range []repo.Config{local, remote, generic} {
		if err := st.Repos().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler{}
	env := format.Env{Store: st}
	for _, tc := range []struct{ name, kind, key string }{
		{"retired work kind", "alpine.old", local.Key},
		{"deleted repository", KindIndex, "missing"},
		{"other format", KindIndex, generic.Key},
		{"remote", KindIndex, remote.Key},
		{"empty local", KindIndex, local.Key},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := h.Work(ctx, env, types.WorkItem{Kind: tc.kind, Key: tc.key}); err != nil {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.Work(
		ctx,
		env,
		types.WorkItem{Kind: KindIndex, Key: local.Key},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled repository lookup: %v", err)
	}
}

func TestSigningKeyRejectsUnusablePairs(t *testing.T) {
	st := inmem.New()
	ctx := t.Context()
	local := repo.Config{Key: "alpine-local", Class: repo.Local, PackageType: repo.Alpine}
	if name, key, err := SigningKey(ctx, st, local); err != nil || name != "" || key != nil {
		t.Fatalf("unsigned repository: %q %v %v", name, key, err)
	}
	local.PrimaryKeyPairRef = "missing"
	if _, _, err := SigningKey(ctx, st, local); err == nil {
		t.Fatal("missing signing pair accepted")
	}
	for _, pair := range []types.Keypair{
		{Name: "gpg", Type: types.KeypairGPG},
		{Name: "bad-rsa", Type: types.KeypairRSA, PrivateKey: "not PEM"},
	} {
		if err := st.Keys().CreateKeypair(ctx, pair); err != nil {
			t.Fatal(err)
		}
		local.PrimaryKeyPairRef = pair.Name
		if _, _, err := SigningKey(ctx, st, local); err == nil {
			t.Fatalf("unusable signing pair %q accepted", pair.Name)
		}
	}
	virtual := repo.Config{Class: repo.Virtual, KeyPair: "missing", PrimaryKeyPairRef: "gpg"}
	if _, _, err := SigningKey(ctx, st, virtual); err == nil {
		t.Fatal("virtual repository ignored its missing key pair")
	}
}

func TestIndexWorkSurfacesDerivationAndRemovalFailures(t *testing.T) {
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "alpine-local", Class: repo.Local, PackageType: repo.Alpine})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		testPackagePath,
		"v3.19/main/x86_64/APKINDEX.tar.gz",
	} {
		if _, err := st.Items().
			Put(t.Context(), types.Item{RepoKey: c.Key, Name: path, Actual: types.Checksums{SHA256: "test"}}); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler{}
	work := types.WorkItem{Kind: KindIndex, Key: c.Key}
	meta := Package{
		Name: "hello", Version: "1.0-r0", Architecture: "x86_64",
		Branch: "v3.20", Component: "main", Path: testPackagePath,
		ArchiveSize: 100, InstalledSize: 5,
	}
	env := format.Env{
		Store:      st,
		Inspection: inspection{sha: "test", path: testPackagePath, packageInfo: meta},
		Derive: func(context.Context, format.Derived) (types.Item, error) {
			return types.Item{}, errIndexWrite
		},
		Underive: func(context.Context, repo.Config, bool, string) error {
			return errIndexWrite
		},
	}
	if err := h.Work(t.Context(), env, work); !errors.Is(err, errIndexWrite) {
		t.Fatalf("derive failure: %v", err)
	}
	env.Derive = func(_ context.Context, d format.Derived) (types.Item, error) {
		if d.Path != "v3.20/main/x86_64/APKINDEX.tar.gz" {
			t.Errorf("derived wrong index: %s", d.Path)
		}
		return types.Item{}, nil
	}
	if err := h.Work(t.Context(), env, work); !errors.Is(err, errIndexWrite) {
		t.Fatalf("stale index removal failure: %v", err)
	}
	env.Underive = func(_ context.Context, _ repo.Config, _ bool, path string) error {
		if path != "v3.19/main/x86_64/APKINDEX.tar.gz" {
			t.Errorf("removed wrong index: %s", path)
		}
		return nil
	}
	if err := h.Work(t.Context(), env, work); err != nil {
		t.Fatal(err)
	}
	c.PrimaryKeyPairRef = "missing"
	if err := st.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := h.Work(t.Context(), env, work); err == nil {
		t.Fatal("missing signing pair did not fail index work")
	}
}
