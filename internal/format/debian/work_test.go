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

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestWorkPublishesReleaseAndAllArchitectureIndexes(t *testing.T) {
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
	it, err := st.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key, Name: path, Size: int64(len(body)),
		Actual: types.Checksums{SHA256: fmt.Sprintf("%x", h), MD5: fmt.Sprintf("%x", m)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Items().SetProperties(t.Context(), it.ID, types.Properties{
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
		Derive: func(_ context.Context, d format.Derived) (types.Item, error) {
			body, err := io.ReadAll(d.Body)
			if err != nil {
				return types.Item{}, err
			}
			generated[d.Path] = body
			return types.Item{}, nil
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
		types.WorkItem{Kind: KindIndex, Key: c.Key},
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
	if err := (Handler{}).Reindex(t.Context(), env, c); err != nil {
		t.Fatal(err)
	}
	if got := len(generated); got < 6 {
		t.Fatalf("reindex did not retain the generated apt view: %d files", got)
	}
}
