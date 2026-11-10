package debian

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // Debian Packages wire checksum.
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestUnitWorkPublishesReleaseAndAllArchitectureIndexes(t *testing.T) {
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "debian-local", Class: repo.Local, PackageType: repo.Debian})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	control := strings.Replace(validControl, "Architecture: amd64", "Architecture: all", 1)
	body := fixture(t, control)
	b, err := ReadBinary(t.Context(), bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(body)
	m := md5.Sum(body) //nolint:gosec // Debian Packages wire checksum.
	const path = "pool/main/h/hello/hello_1.0-1_all.deb"
	it, err := st.Items().Put(t.Context(), model.Item{
		RepoKey: c.Key, Name: path, Size: int64(len(body)),
		Actual: kernel.Checksums{SHA256: fmt.Sprintf("%x", h), MD5: fmt.Sprintf("%x", m)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Items().SetProperties(t.Context(), it.ID, model.Properties{
		PropDistribution: {"stable"}, PropComponent: {"main"}, PropArchitecture: {"all"},
	}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	generated := map[string][]byte{}
	env := format.Env{
		Store: st,
		Now:   func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) },
		Inspection: inspection{
			sha: it.Actual.SHA256, path: path, binary: b,
		},
		Derive: func(_ context.Context, d format.Derived) (model.Item, error) {
			body, err := io.ReadAll(d.Body)
			if err != nil {
				return model.Item{}, err
			}
			generated[d.Path] = body
			return model.Item{}, nil
		},
		Underive: func(_ context.Context, _ repo.Config, _ bool, path string) error {
			t.Errorf("unexpected removal: %s", path)
			return nil
		},
		Enqueue: func(_ context.Context, kind, key string, _ []byte) error {
			if kind != KindIndex || key != c.Key {
				t.Errorf("wrong index work: %s %s", kind, key)
			}
			return nil
		},
	}
	if err := (Handler{}).Work(
		t.Context(),
		env,
		work.WorkItem{Kind: KindIndex, Key: c.Key},
	); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"dists/stable/main/binary-all/Packages",
		"dists/stable/main/binary-amd64/Packages",
		"dists/stable/main/binary-amd64/Packages.gz",
		"dists/stable/Release",
	} {
		if len(generated[p]) == 0 {
			t.Errorf("missing generated apt metadata: %s", p)
		}
	}
	if !bytes.Equal(generated["dists/stable/main/binary-all/Packages"],
		generated["dists/stable/main/binary-amd64/Packages"]) {
		t.Fatal("Architecture: all package differs across architecture indexes")
	}
	index := string(generated["dists/stable/main/binary-amd64/Packages"])
	if !strings.Contains(index, "Filename: "+path) ||
		!strings.Contains(index, "Architecture: all") {
		t.Fatalf("apt index does not bind the package: %s", index)
	}
	if !strings.Contains(
		string(generated["dists/stable/Release"]),
		"main/binary-amd64/Packages.gz",
	) {
		t.Fatal("Release does not enumerate generated index")
	}
	if err := (Handler{}).Work(t.Context(), env, work.WorkItem{
		Kind: KindReindexNoProps, Key: c.Key,
	}); err != nil {
		t.Fatal(err)
	}
	props, err := st.Items().Properties(t.Context(), it.ID)
	if err != nil || len(props[PropName]) != 0 {
		t.Fatalf("writeProps=0 changed package properties: %v %+v", err, props)
	}
	if err := (Handler{}).Reindex(t.Context(), env, c); err != nil {
		t.Fatal(err)
	}
	props, err = st.Items().Properties(t.Context(), it.ID)
	if err != nil || len(props[PropName]) != 1 || props[PropName][0] != "hello" {
		t.Fatalf("default reindex did not annotate package: %v %+v", err, props)
	}
	if got := len(generated); got < 6 {
		t.Fatalf("reindex did not retain the generated apt view: %d files", got)
	}
}

func TestUnitWorkSignsReleaseWithEnvKeys(t *testing.T) {
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{
		Key: "debian-local", Class: repo.Local, PackageType: repo.Debian, PrimaryKeyPairRef: "apt",
	})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	body := fixture(t, validControl)
	b, err := ReadBinary(t.Context(), bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(body)
	m := md5.Sum(body) //nolint:gosec // Debian Packages wire checksum.
	const path = "pool/main/h/hello/hello_1.0-1_amd64.deb"
	it, err := st.Items().Put(t.Context(), model.Item{
		RepoKey: c.Key, Name: path, Size: int64(len(body)),
		Actual: kernel.Checksums{SHA256: fmt.Sprintf("%x", h), MD5: fmt.Sprintf("%x", m)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Items().SetProperties(t.Context(), it.ID, model.Properties{
		PropDistribution: {"stable"}, PropComponent: {"main"}, PropArchitecture: {"amd64"},
	}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	generated := map[string][]byte{}
	env := format.Env{
		Store: st,
		// The store holds no key pair, so a Release signed with this one was
		// signed through Keys.
		Keys: &accessapitest.Keys{Keypairs: map[string]accessapi.Keypair{"apt": {
			Name: "apt", Type: accessapi.KeypairGPG, PrivateKey: key.Private,
		}}},
		Inspection: inspection{sha: it.Actual.SHA256, path: path, binary: b},
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
	release := generated["dists/stable/Release"]
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key.Public))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(
		ring, bytes.NewReader(release), bytes.NewReader(generated["dists/stable/Release.gpg"]), nil,
	); err != nil {
		t.Fatalf("Release.gpg not signed with the key pair from Keys: %v", err)
	}
	inRelease := generated["dists/stable/InRelease"]
	block, _ := clearsign.Decode(inRelease)
	if block == nil || !bytes.Equal(block.Plaintext, release) {
		t.Fatalf("InRelease does not carry the Release body: %q", inRelease)
	}
	if _, err := openpgp.CheckDetachedSignature(
		ring, bytes.NewReader(block.Bytes), block.ArmoredSignature.Body, nil,
	); err != nil {
		t.Fatalf("InRelease not signed with the key pair from Keys: %v", err)
	}
}
