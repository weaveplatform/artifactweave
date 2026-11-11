package alpine

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // APKINDEX v2 signature verification.
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var errIndexWrite = errors.New("index persistence failed")

func TestUnitIndexWorkTracksCurrentRepository(t *testing.T) {
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
			if err := h.Work(ctx, env, work.WorkItem{Kind: tc.kind, Key: tc.key}); err != nil {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.Work(
		ctx,
		env,
		work.WorkItem{Kind: KindIndex, Key: local.Key},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled repository lookup: %v", err)
	}
}

func TestUnitSigningKeyRejectsUnusablePairs(t *testing.T) {
	keys := &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{}}
	ctx := t.Context()
	local := repo.Config{Key: "alpine-local", Class: repo.Local, PackageType: repo.Alpine}
	if name, key, err := SigningKey(ctx, keys, local); err != nil || name != "" || key != nil {
		t.Fatalf("unsigned repository: %q %v %v", name, key, err)
	}
	local.PrimaryKeyPairRef = "missing"
	if _, _, err := SigningKey(ctx, keys, local); err == nil {
		t.Fatal("missing signing pair accepted")
	}
	for _, pair := range []accessapi.Keypair{
		{Name: "gpg", Type: accessapi.KeypairGPG},
		{Name: "bad-rsa", Type: accessapi.KeypairRSA, PrivateKey: "not PEM"},
	} {
		keys.Keypairs[pair.Name] = pair
		local.PrimaryKeyPairRef = pair.Name
		if _, _, err := SigningKey(ctx, keys, local); err == nil {
			t.Fatalf("unusable signing pair %q accepted", pair.Name)
		}
	}
	virtual := repo.Config{Class: repo.Virtual, KeyPair: "missing", PrimaryKeyPairRef: "gpg"}
	if _, _, err := SigningKey(ctx, keys, virtual); err == nil {
		t.Fatal("virtual repository ignored its missing key pair")
	}
}

func TestUnitIndexWorkSignsWithEnvKeys(t *testing.T) {
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{
		Key: "alpine-local", Class: repo.Local, PackageType: repo.Alpine, PrimaryKeyPairRef: "apk",
	})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Items().
		Put(t.Context(), model.Item{RepoKey: c.Key, Name: testPackagePath, Actual: kernel.Checksums{SHA256: "test"}}); err != nil {
		t.Fatal(err)
	}
	private := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(testSigningKey()),
	})
	var index []byte
	env := format.Env{
		Store: st,
		// The store holds no key pair, so an index signed with this one was
		// signed through Keys.
		Keys: &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{"apk": {
			Name: "apk", Type: accessapi.KeypairRSA, Alias: "builder@example",
			PrivateKey: string(private),
		}}},
		Inspection: inspection{sha: "test", path: testPackagePath, packageInfo: Package{
			Name: "hello", Version: "1.0-r0", Architecture: "x86_64",
			Branch: "v3.20", Component: "main", Path: testPackagePath,
			ArchiveSize: 100, InstalledSize: 5,
		}},
		Derive: func(_ context.Context, d format.Derived) (model.Item, error) {
			body, err := io.ReadAll(d.Body)
			index = body
			return model.Item{}, err
		},
	}
	work := work.WorkItem{Kind: KindIndex, Key: c.Key}
	if err := (Handler{}).Work(t.Context(), env, work); err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(index)
	signatures, err := readSegment(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	signature := signatures[".SIGN.RSA.builder@example.rsa.pub"]
	h := sha1.Sum(index[len(index)-r.Len():]) //nolint:gosec // APKINDEX v2 wire format.
	pub := &testSigningKey().PublicKey
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA1, h[:], signature); err != nil {
		t.Fatalf("index not signed with the key pair from Keys: %v", err)
	}
}

func TestUnitIndexWorkSurfacesDerivationAndRemovalFailures(t *testing.T) {
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
			Put(t.Context(), model.Item{RepoKey: c.Key, Name: path, Actual: kernel.Checksums{SHA256: "test"}}); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler{}
	work := work.WorkItem{Kind: KindIndex, Key: c.Key}
	meta := Package{
		Name: "hello", Version: "1.0-r0", Architecture: "x86_64",
		Branch: "v3.20", Component: "main", Path: testPackagePath,
		ArchiveSize: 100, InstalledSize: 5,
	}
	env := format.Env{
		Store:      st,
		Keys:       &accessapitest.Keys{},
		Inspection: inspection{sha: "test", path: testPackagePath, packageInfo: meta},
		Derive: func(context.Context, format.Derived) (model.Item, error) {
			return model.Item{}, errIndexWrite
		},
		Underive: func(context.Context, repo.Config, bool, string) error {
			return errIndexWrite
		},
	}
	if err := h.Work(t.Context(), env, work); !errors.Is(err, errIndexWrite) {
		t.Fatalf("derive failure: %v", err)
	}
	env.Derive = func(_ context.Context, d format.Derived) (model.Item, error) {
		if d.Path != "v3.20/main/x86_64/APKINDEX.tar.gz" {
			t.Errorf("derived wrong index: %s", d.Path)
		}
		return model.Item{}, nil
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
