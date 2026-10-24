package alpine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type invalidBlobs struct{}

type testReadCloser struct{ *bytes.Reader }

func (*testReadCloser) Close() error { return nil }

func (invalidBlobs) Open(_ context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if sha == "missing" {
		return nil, blob.Info{}, errIndexWrite
	}
	return &testReadCloser{bytes.NewReader([]byte("not an APK"))}, blob.Info{Size: 10}, nil
}

func (invalidBlobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{}, errIndexWrite
}

func TestUnitHandlerSeparatesPackagesAndGeneratedIndexes(t *testing.T) {
	h := Handler{}
	local := *repo.Defaults(&repo.Config{Key: "alpine-local", Class: repo.Local, PackageType: repo.Alpine})
	remote := *repo.Defaults(&repo.Config{Key: "alpine-remote", Class: repo.Remote, PackageType: repo.Alpine})
	index := "v3.20/main/x86_64/APKINDEX.tar.gz"
	if got := h.Owed(local, false, index); len(got) != 1 || got[0].Kind != KindIndex {
		t.Fatalf("local index work: %+v", got)
	}
	for _, tc := range []struct {
		c      repo.Config
		cached bool
		path   string
	}{
		{local, true, index}, {remote, false, index}, {local, false, testPackagePath},
	} {
		if got := h.Owed(tc.c, tc.cached, tc.path); len(got) != 0 {
			t.Fatalf("unexpected work for %s: %+v", tc.path, got)
		}
	}
	if h.Classify(local, index) != format.ClassDerived ||
		h.Classify(local, testPackagePath) != format.ClassUnique {
		t.Fatal("Alpine index and package classifications differ from their ownership")
	}
	if _, err := h.Admit(t.Context(), format.Env{}, local,
		format.Write{Op: format.OpDeploy, Path: index}); err == nil {
		t.Fatal("client replaced generated index")
	}
	if _, err := h.Admit(t.Context(), format.Env{}, local,
		format.Write{Op: format.OpDeploy, Path: "hello.apk"}); err == nil {
		t.Fatal("package outside native layout accepted")
	}
	for _, wr := range []format.Write{
		{Op: format.OpChecksum, Path: testPackagePath},
		{Op: format.OpDeploy, Path: "README"},
		{Op: format.OpDeploy, Cached: true, Path: index},
	} {
		if _, err := h.Admit(t.Context(), format.Env{}, local, wr); err != nil {
			t.Fatalf("non-package write %q: %v", wr.Path, err)
		}
		if _, err := h.Inspect(t.Context(), format.Env{}, local, wr); err != nil {
			t.Fatalf("non-package inspection %q: %v", wr.Path, err)
		}
	}
}

func TestUnitHandlerQueuesOnlyLocalPackageChanges(t *testing.T) {
	h := Handler{}
	local := repo.Config{Key: "alpine-local", Class: repo.Local, PackageType: repo.Alpine}
	remote := repo.Config{Key: "alpine-remote", Class: repo.Remote, PackageType: repo.Alpine}
	refused := errors.New("queue unavailable")
	env := format.Env{Enqueue: func(_ context.Context, kind, key string, _ []byte) error {
		if kind != KindIndex || key != local.Key {
			t.Errorf("unexpected queue request: %s %s", kind, key)
		}
		return refused
	}}
	file := types.Item{RepoKey: local.Key, Path: "v3.20/main/x86_64", Name: "hello-1.0-r0.apk"}
	if err := h.Deleting(t.Context(), env, local, file); !errors.Is(err, refused) {
		t.Fatalf("local package delete did not surface queue failure: %v", err)
	}
	for _, tc := range []struct {
		c    repo.Config
		item types.Item
	}{
		{remote, file},
		{local, types.Item{Folder: true, Name: "hello-1.0-r0.apk"}},
		{local, types.Item{Name: "README"}},
		{local, types.Item{Name: "cached.apk", Cached: true}},
	} {
		if err := h.Deleting(t.Context(), env, tc.c, tc.item); err != nil {
			t.Fatalf("unrelated delete queued work: %v", err)
		}
	}
	if err := h.Deployed(
		t.Context(),
		env,
		local,
		types.Item{Folder: true, Name: "foo.apk"},
	); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleted(t.Context(), env, local, nil); err != nil {
		t.Fatal(err)
	}
}

func TestUnitHandlerRejectsMissingAndInvalidStoredPackages(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "alpine-local", Class: repo.Local, PackageType: repo.Alpine})
	env := format.Env{Blobs: invalidBlobs{}}
	for _, sha := range []string{"", "missing", "invalid"} {
		w := format.Write{
			Op: format.OpDeploy, Path: testPackagePath,
			Checksums: types.Checksums{SHA256: sha},
		}
		if _, err := h.Inspect(t.Context(), env, c, w); err == nil {
			t.Errorf("inspection accepted %q content", sha)
		}
		if _, err := h.Admit(t.Context(), env, c, w); err == nil {
			t.Errorf("admission accepted %q content", sha)
		}
	}
	st := inmem.New()
	if err := setProperties(
		t.Context(),
		st,
		types.Item{ID: 987},
		Package{Name: "hello"},
	); err == nil {
		t.Fatal("missing item properties accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.Reindex(ctx, format.Env{Store: st}, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reindex: %v", err)
	}
}
