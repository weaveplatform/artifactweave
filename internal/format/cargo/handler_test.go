package cargo_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/cargo"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func lifecycleCrate(t *testing.T, name, version string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	manifest := []byte("[package]\nname = \"" + name + "\"\nversion = \"" + version + "\"\n")
	if err := w.WriteHeader(
		&tar.Header{
			Name: name + "-" + version + "/Cargo.toml",
			Mode: 0o644,
			Size: int64(len(manifest)),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnitCrateLifecycleAndReindex(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "cargo-local", Class: repo.Local, PackageType: repo.Cargo})
	if err := st.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	items := &item.Service{
		Store:   st,
		Blobs:   blobs,
		Formats: func(repo.PackageType) format.Handler { return cargo.Handler{} },
	}
	path := cargo.CratePath("hello_world", "1.2.3")
	content := lifecycleCrate(t, "hello_world", "1.2.3")
	target := item.Target{Repo: c, Path: path}
	stored, err := items.Deploy(
		ctx,
		item.Deploy{Target: target, Body: bytes.NewReader(content), By: "test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	check := func(it types.Item) {
		t.Helper()
		props, err := st.Items().Properties(ctx, it.ID)
		if err != nil || len(props[cargo.RecordProperty]) != 1 ||
			!strings.Contains(props[cargo.RecordProperty][0], `"vers":"1.2.3"`) {
			t.Fatalf("index property: %v %v", props, err)
		}
	}
	check(stored)
	if err := (cargo.Handler{}).Reindex(ctx, items.Env(), c); err != nil {
		t.Fatal(err)
	}
	stored, err = st.Items().Get(ctx, c.Key, false, path)
	if err != nil {
		t.Fatal(err)
	}
	check(stored)
	if err := st.Items().PatchProperties(ctx, stored.ID, types.Properties{
		cargo.RecordProperty: {"invalid JSON"}, cargo.YankedProperty: {"true"},
	}, nil, stored.Updated); err != nil {
		t.Fatal(err)
	}
	if err := (cargo.Handler{}).Reindex(ctx, items.Env(), c); err == nil {
		t.Fatal("reindex accepted corrupt stored index metadata")
	}
	if err := st.Items().
		PatchProperties(ctx, stored.ID, nil, []string{cargo.RecordProperty}, stored.Updated); err != nil {
		t.Fatal(err)
	}
	if err := (cargo.Handler{}).Reindex(ctx, items.Env(), c); err != nil {
		t.Fatal(err)
	}
	stored, err = st.Items().Get(ctx, c.Key, false, path)
	if err != nil {
		t.Fatal(err)
	}
	check(stored)
	props, err := st.Items().Properties(ctx, stored.ID)
	if err != nil || !strings.Contains(props[cargo.RecordProperty][0], `"yanked":true`) {
		t.Fatalf("reindex lost yank state: %v %v", props, err)
	}
	if _, err := items.Delete(ctx, target, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Items().Get(ctx, c.Key, false, path); err == nil {
		t.Fatal("deleted crate remains visible")
	}
	tr := &trash.Service{Store: st, Items: items}
	if _, err := tr.Restore(ctx, c.Key, path, trash.Destination{Repo: c}, "test", nil); err != nil {
		t.Fatal(err)
	}
	stored, err = st.Items().Get(ctx, c.Key, false, path)
	if err != nil {
		t.Fatal(err)
	}
	check(stored)
	remote := c
	remote.Class = repo.Remote
	if err := (cargo.Handler{}).Reindex(ctx, items.Env(), remote); err == nil {
		t.Fatal("remote reindex accepted")
	}
}

func TestUnitHandlerRejectsInvalidWrites(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "cargo-local", Class: repo.Local, PackageType: repo.Cargo})
	if err := st.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	items := &item.Service{
		Store:   st,
		Blobs:   blobs,
		Formats: func(repo.PackageType) format.Handler { return cargo.Handler{} },
	}
	good := lifecycleCrate(t, "hello_world", "1.2.3")
	for _, tc := range []struct {
		path string
		body []byte
	}{
		{"not-a-crate.txt", good},
		{"crates/hello_world/hello_world-1.2.4.crate", good},
		{cargo.CratePath("hello_world", "1.2.3"), []byte("bad archive")},
	} {
		if _, err := items.Deploy(
			ctx,
			item.Deploy{
				Target: item.Target{Repo: c, Path: tc.path},
				Body:   bytes.NewReader(tc.body),
				By:     "test",
			},
		); err == nil {
			t.Errorf("accepted invalid Cargo write %s", tc.path)
		}
	}
	if cargo.Serves(repo.Generic) || !cargo.Serves(repo.Cargo) {
		t.Fatal("Cargo type selection")
	}
	h := cargo.Handler{}
	if _, err := h.Admit(
		ctx,
		items.Env(),
		c,
		format.Write{Op: format.OpDeploy, Path: "invalid.crate"},
	); err == nil {
		t.Fatal("invalid path admitted")
	}
	if _, err := h.Admit(
		ctx,
		items.Env(),
		c,
		format.Write{Op: format.OpChecksum, Path: "anything.sha1"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Inspect(
		ctx,
		items.Env(),
		c,
		format.Write{Op: format.OpChecksum, Path: "anything.sha1"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Inspect(
		ctx,
		items.Env(),
		c,
		format.Write{Op: format.OpDeploy, Path: "anything.txt"},
	); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(ctx, items.Env(), c, types.Item{Folder: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(ctx, items.Env(), c, types.Item{Name: "anything.txt"}); err != nil {
		t.Fatal(err)
	}
}
