package debian

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitWorkPublishesOnlyCompleteSourcePackages(t *testing.T) {
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "deb-source", Class: repo.Local, PackageType: repo.Debian})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	entry := dpkgSourceIndexEntry(t)
	const folder = "pool/main/a/alpha/"
	var descriptor types.Item
	for _, name := range []string{
		"alpha_1.0-1.dsc", "alpha_1.0.orig.tar.gz", "alpha_1.0-1.debian.tar.xz",
	} {
		body, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		it, err := st.Items().Put(t.Context(), types.Item{
			RepoKey: c.Key, Name: folder + name, Size: int64(len(body)),
			Actual: sourceTestChecksums(body),
		})
		if err != nil {
			t.Fatal(err)
		}
		if name == "alpha_1.0-1.dsc" {
			descriptor = it
		}
	}
	if err := st.Items().SetProperties(t.Context(), descriptor.ID, types.Properties{
		"dsc.distribution": {"stable"}, "dsc.component": {"main"},
	}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	generated := map[string][]byte{}
	env := format.Env{Store: st, Inspection: sourceInspection{
		sha: descriptor.Actual.SHA256, path: descriptor.FullPath(), source: entry.Source,
	}, Derive: func(_ context.Context, d format.Derived) (types.Item, error) {
		body, err := io.ReadAll(d.Body)
		if err != nil {
			return types.Item{}, err
		}
		generated[d.Path] = body
		return types.Item{}, nil
	}, Underive: func(_ context.Context, _ repo.Config, _ bool, path string) error {
		delete(generated, path)
		return nil
	}}
	work := types.WorkItem{Kind: KindIndex, Key: c.Key}
	if err := (Handler{}).Work(t.Context(), env, work); err != nil {
		t.Fatal(err)
	}
	plain := generated["dists/stable/main/source/Sources"]
	compressed := generated["dists/stable/main/source/Sources.gz"]
	if len(plain) == 0 || len(compressed) == 0 ||
		!strings.Contains(string(plain), "Directory: pool/main/a/alpha\n") {
		t.Fatalf("incomplete deb-src publication: %v", keys(generated))
	}
	if rows, err := DecodeSourcesIndex(compressed); err != nil || len(rows) != 1 ||
		len(rows[0].Files) != 3 {
		t.Fatalf("generated source index: %+v, %v", rows, err)
	}
	if !bytes.Contains(generated["dists/stable/Release"], []byte("main/source/Sources.gz")) {
		t.Fatal("Release omits the source index")
	}
	if !bytes.Contains(generated["dists/stable/Release"], []byte("Architectures: source")) {
		t.Fatal("source-only Release omits source architecture")
	}
	if err := (Handler{}).Work(t.Context(), env,
		types.WorkItem{Kind: KindIndex, Key: "unknown-repository"}); err != nil {
		t.Fatalf("work for removed repository: %v", err)
	}
	keyed := c
	keyed.PrimaryKeyPairRef = "missing-keypair"
	if err := buildIndex(t.Context(), env, keyed); err == nil {
		t.Fatal("source Release published without its configured signing key")
	}
	if err := st.Keys().CreateKeypair(t.Context(), types.Keypair{
		Name: "rsa-key", Type: types.KeypairRSA,
	}); err != nil {
		t.Fatal(err)
	}
	keyed.PrimaryKeyPairRef = "rsa-key"
	if err := buildIndex(t.Context(), env, keyed); err == nil {
		t.Fatal("source Release published with a non-GPG key")
	}
	if err := st.Keys().CreateKeypair(t.Context(), types.Keypair{
		Name: "invalid-gpg", Type: types.KeypairGPG, PrivateKey: "not a key",
	}); err != nil {
		t.Fatal(err)
	}
	keyed.PrimaryKeyPairRef = "invalid-gpg"
	if err := buildIndex(t.Context(), env, keyed); err == nil {
		t.Fatal("source Release published with invalid GPG key material")
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Keys().CreateKeypair(t.Context(), types.Keypair{
		Name: "source-gpg", Type: types.KeypairGPG, PrivateKey: key.Private,
	}); err != nil {
		t.Fatal(err)
	}
	keyed.PrimaryKeyPairRef = "source-gpg"
	if err := buildIndex(t.Context(), env, keyed); err != nil {
		t.Fatalf("source Release signing: %v", err)
	}
	if len(generated["dists/stable/InRelease"]) == 0 ||
		len(generated["dists/stable/Release.gpg"]) == 0 {
		t.Fatal("signed source Release missing either apt signature")
	}
	env.Derive = func(context.Context, format.Derived) (types.Item, error) {
		return types.Item{}, errors.New("publication refused")
	}
	if err := (Handler{}).Work(t.Context(), env, work); err == nil {
		t.Fatal("derived metadata publication failure ignored")
	}
	env.Derive = func(_ context.Context, d format.Derived) (types.Item, error) {
		return types.Item{}, nil
	}
	stale := []byte("obsolete")
	if _, err := st.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key,
		Name:    "dists/stable/old/Release", Size: int64(len(stale)),
		Actual: sourceTestChecksums(stale),
	}); err != nil {
		t.Fatal(err)
	}
	env.Underive = func(context.Context, repo.Config, bool, string) error {
		return errors.New("removal refused")
	}
	if err := (Handler{}).Work(t.Context(), env, work); err == nil {
		t.Fatal("stale metadata removal failure ignored")
	}
	noInspection := env
	noInspection.Inspection = nil
	noInspection.Blobs = unavailableDebianBlobs{}
	if err := (Handler{}).Work(t.Context(), noInspection, work); err == nil {
		t.Fatal("unavailable source descriptor silently removed from apt index")
	}
}

type unavailableDebianBlobs struct{}

func (unavailableDebianBlobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	return nil, blob.Info{}, errors.New("blob store unavailable")
}

func (unavailableDebianBlobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{}, errors.New("blob store unavailable")
}
