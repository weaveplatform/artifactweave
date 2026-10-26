package conda

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type condaTestReadCloser struct{ *bytes.Reader }

func (condaTestReadCloser) Close() error { return nil }

type condaTestBlobs struct{ body []byte }

func (b condaTestBlobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	return condaTestReadCloser{bytes.NewReader(b.body)}, blob.Info{Size: int64(len(b.body))}, nil
}

func (b condaTestBlobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{Size: int64(len(b.body))}, nil
}

func TestUnitCondaReindexRestoresPackageAndChannelMetadata(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "conda-local", Class: repo.Local, PackageType: repo.Conda})
	if err := st.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	path := "team/noarch/" + fixtureName
	id, _ := ParsePackagePath(path)
	rec, err := ReadPackage(ctx, bytes.NewReader(data), int64(len(data)), id)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	it, err := st.Items().Put(ctx, types.Item{
		RepoKey: c.Key, Name: path, Size: int64(len(data)), Actual: types.Checksums{SHA256: digest},
	})
	if err != nil {
		t.Fatal(err)
	}
	generated := map[string][]byte{}
	enqueued := 0
	env := format.Env{
		Store: st, Inspection: inspection{sha: digest, path: path, record: rec},
		Derive: func(_ context.Context, d format.Derived) (types.Item, error) {
			body, err := io.ReadAll(d.Body)
			if err != nil {
				return types.Item{}, err
			}
			generated[d.Path] = body
			return types.Item{}, nil
		},
		Underive: func(_ context.Context, _ repo.Config, _ bool, p string) error {
			delete(generated, p)
			return nil
		},
		Enqueue: func(_ context.Context, kind, key string, _ []byte) error {
			if kind != KindIndex || key != c.Key {
				t.Errorf("unexpected work: %s %s", kind, key)
			}
			enqueued++
			return nil
		},
	}
	h := Handler{}
	env.SkipProperties = true
	if err := h.Reindex(ctx, env, c); err != nil {
		t.Fatal(err)
	}
	before, err := st.Items().Properties(ctx, it.ID)
	if err != nil || len(before[PropName]) != 0 {
		t.Fatalf("reindex with SkipProperties changed package annotations: %+v %v", before, err)
	}
	env.SkipProperties = false
	if err := h.Reindex(ctx, env, c); err != nil {
		t.Fatal(err)
	}
	if enqueued != 2 {
		t.Fatalf("reindex queued %d index jobs", enqueued)
	}
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil || props[PropName][0] != id.Name || props[PropChannel][0] != "team" {
		t.Fatalf("reindex did not restore package properties: %+v %v", props, err)
	}
	if err := h.Work(ctx, env, types.WorkItem{Kind: KindIndex, Key: c.Key}); err != nil {
		t.Fatal(err)
	}
	meta := generated["team/noarch/repodata.json"]
	if len(meta) == 0 || len(generated["team/noarch/repodata.json.zst"]) == 0 {
		t.Fatalf("reindex did not derive both metadata forms: %v", generated)
	}
	index, err := DecodeRepodata(
		meta,
		MetadataPath{
			Path:     "team/noarch/repodata.json",
			Channel:  "team",
			Subdir:   "noarch",
			Filename: "repodata.json",
		},
	)
	if err != nil || index.Packages[fixtureName].Name != id.Name {
		t.Fatalf("derived metadata does not describe stored package: %+v %v", index, err)
	}
	if err := h.Work(ctx, env, types.WorkItem{Kind: "unrelated", Key: c.Key}); err != nil {
		t.Fatalf("unrelated work failed: %v", err)
	}
	if err := h.Work(ctx, env, types.WorkItem{Kind: KindIndex, Key: "deleted"}); err != nil {
		t.Fatalf("deleted repository work failed: %v", err)
	}
	if h.Classify(c, "team/noarch/repodata.json") != format.ClassDerived ||
		h.Classify(c, path) != format.ClassUnique {
		t.Fatal("package and metadata ownership differ from the native channel")
	}
	if got := h.Owed(c, false, "team/noarch/repodata.json"); len(got) != 1 || got[0].Key != c.Key {
		t.Fatalf("generated metadata did not owe reindex: %+v", got)
	}
	if got := h.Owed(c, true, "team/noarch/repodata.json"); len(got) != 0 {
		t.Fatalf("cached metadata queued hosted work: %+v", got)
	}
}

func TestUnitCondaRejectsClientMetadataReplacementAndFailedQueue(t *testing.T) {
	h := Handler{}
	c := repo.Config{Key: "conda-local", Class: repo.Local, PackageType: repo.Conda}
	for _, write := range []format.Write{
		{Op: format.OpDeploy, Path: "noarch/repodata.json"},
		{Op: format.OpDeploy, Path: "noarch/repodata.json.zst"},
	} {
		if _, err := h.Admit(t.Context(), format.Env{}, c, write); err == nil {
			t.Fatalf("client replaced generated Conda metadata: %s", write.Path)
		}
	}
	if _, err := h.Admit(t.Context(), format.Env{}, c,
		format.Write{Op: format.OpDeploy, Path: "bad/path/demo-1-0.conda"}); err == nil {
		t.Fatal("invalid package path admitted")
	}
	for _, w := range []format.Write{
		{Op: format.OpChecksum, Path: "noarch/demo-1-0.conda"},
		{Op: format.OpDeploy, Path: "README"},
		{Op: format.OpDeploy, Path: "noarch/repodata.json", Cached: true},
	} {
		if _, err := h.Admit(t.Context(), format.Env{}, c, w); err != nil {
			t.Fatalf("safe non-package write denied: %v", err)
		}
	}
	refused := errors.New("index queue unavailable")
	env := format.Env{
		Enqueue: func(context.Context, string, string, []byte) error { return refused },
	}
	it := types.Item{RepoKey: c.Key, Path: "noarch", Name: fixtureName}
	if err := h.Deleting(t.Context(), env, c, it); !errors.Is(err, refused) {
		t.Fatalf("delete swallowed failed index queue: %v", err)
	}
	for _, inert := range []types.Item{{Folder: true, Name: fixtureName}, {Name: "README"}, {Name: fixtureName, Cached: true, Path: "noarch"}} {
		if err := h.Deleting(t.Context(), env, c, inert); err != nil {
			t.Fatalf("unrelated delete queued index work: %v", err)
		}
	}
}

func TestUnitCondaInspectAndAdmissionValidateActualArchive(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParsePackagePath("noarch/" + fixtureName)
	sha := fmt.Sprintf("%x", sha256.Sum256(body))
	h := Handler{}
	c := repo.Config{Key: "conda-local", Class: repo.Local, PackageType: repo.Conda}
	w := format.Write{Op: format.OpDeploy, Path: id.Path, Checksums: types.Checksums{SHA256: sha}}
	inspectionValue, err := h.Inspect(t.Context(), format.Env{Blobs: condaTestBlobs{body}}, c, w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Admit(t.Context(), format.Env{Inspection: inspectionValue}, c, w); err != nil {
		t.Fatalf("validated package rejected: %v", err)
	}
	if _, err := h.Admit(
		t.Context(),
		format.Env{Inspection: inspectionValue, Blobs: condaTestBlobs{[]byte("not bzip2")}},
		c,
		format.Write{
			Op:        format.OpDeploy,
			Path:      id.Path,
			Checksums: types.Checksums{SHA256: "other"},
		},
	); err == nil {
		t.Fatal("stale inspection bypassed archive validation")
	}
	if _, err := h.Inspect(
		t.Context(),
		format.Env{Blobs: condaTestBlobs{[]byte("bad")}},
		c,
		w,
	); err == nil {
		t.Fatal("malformed stored archive passed inspection")
	}
	if _, err := h.Admit(t.Context(), format.Env{Blobs: condaTestBlobs{}}, c, w); err == nil {
		t.Fatal("missing stored package body was admitted")
	}
	if _, err := h.Inspect(t.Context(), format.Env{}, c,
		format.Write{Op: format.OpChecksum, Path: id.Path}); err != nil {
		t.Fatalf("checksum deploy inspected an absent body: %v", err)
	}
	if err := h.Deployed(
		t.Context(),
		format.Env{Blobs: condaTestBlobs{[]byte("bad")}},
		c,
		types.Item{
			RepoKey: c.Key,
			Path:    "noarch",
			Name:    fixtureName,
			Actual:  types.Checksums{SHA256: sha},
		},
	); err == nil {
		t.Fatal("malformed restored package was indexed")
	}
}

func TestUnitCondaWorkRemovesStaleGeneratedMetadata(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "conda-empty", Class: repo.Local, PackageType: repo.Conda})
	if err := st.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"noarch/repodata.json", "noarch/current_repodata.json.zst"} {
		if _, err := st.Items().
			Put(ctx, types.Item{RepoKey: c.Key, Name: path, Size: 1}); err != nil {
			t.Fatal(err)
		}
	}
	removed := map[string]bool{}
	env := format.Env{
		Store: st,
		Underive: func(_ context.Context, _ repo.Config, cached bool, path string) error {
			if cached {
				t.Error("hosted index was removed from a remote cache")
			}
			removed[path] = true
			return nil
		},
	}
	if err := (Handler{}).Work(ctx, env, types.WorkItem{Kind: KindIndex, Key: c.Key}); err != nil {
		t.Fatal(err)
	}
	if !removed["noarch/repodata.json"] || !removed["noarch/current_repodata.json.zst"] {
		t.Fatalf("stale channel metadata retained: %+v", removed)
	}
	remote := *repo.Defaults(&repo.Config{Key: "conda-remote", Class: repo.Remote, PackageType: repo.Conda})
	if err := st.Repos().Create(ctx, remote); err != nil {
		t.Fatal(err)
	}
	if err := (Handler{}).Work(
		ctx,
		env,
		types.WorkItem{Kind: KindIndex, Key: remote.Key},
	); err != nil {
		t.Fatalf("remote work generated hosted index: %v", err)
	}
}

func TestUnitCondaReindexFailsOnCorruptStoredPackage(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "conda-broken", Class: repo.Local, PackageType: repo.Conda})
	if err := st.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	const sha = "invalid-stored-package"
	if _, err := st.Items().Put(ctx, types.Item{
		RepoKey: c.Key, Name: "noarch/" + fixtureName,
		Size: 3, Actual: types.Checksums{SHA256: sha},
	}); err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: st, Blobs: condaTestBlobs{[]byte("bad")}}
	if err := (Handler{}).Reindex(ctx, env, c); err == nil {
		t.Fatal("reindex accepted an invalid stored package")
	}
	if err := (Handler{}).Work(ctx, env, types.WorkItem{Kind: KindIndex, Key: c.Key}); err == nil {
		t.Fatal("worker published metadata for an invalid stored package")
	}
	if err := (Handler{}).Deployed(
		ctx,
		env,
		c,
		types.Item{RepoKey: c.Key, Name: "README"},
	); err != nil {
		t.Fatalf("unrelated file entered Conda index: %v", err)
	}
	if err := (Handler{}).Deployed(
		ctx,
		env,
		c,
		types.Item{RepoKey: c.Key, Name: fixtureName, Folder: true},
	); err != nil {
		t.Fatalf("folder entered Conda index: %v", err)
	}
	if err := (Handler{}).Deleted(
		ctx,
		env,
		c,
		[]types.Item{{RepoKey: c.Key, Name: "README"}},
	); err != nil {
		t.Fatalf("unrelated delete changed Conda versions: %v", err)
	}
}

func TestUnitCondaWorkerSurfacesPublicationAndRemovalFailures(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "conda-errors", Class: repo.Local, PackageType: repo.Conda})
	if err := st.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParsePackagePath("noarch/" + fixtureName)
	rec, err := ReadPackage(ctx, bytes.NewReader(body), int64(len(body)), id)
	if err != nil {
		t.Fatal(err)
	}
	sha := fmt.Sprintf("%x", sha256.Sum256(body))
	if _, err := st.Items().
		Put(ctx, types.Item{RepoKey: c.Key, Name: "README", Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Items().Put(ctx, types.Item{
		RepoKey: c.Key, Name: id.Path,
		Size: int64(len(body)), Actual: types.Checksums{SHA256: sha},
	}); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("derived write refused")
	env := format.Env{
		Store:      st,
		Inspection: inspection{sha: sha, path: id.Path, record: rec},
		Derive:     func(context.Context, format.Derived) (types.Item, error) { return types.Item{}, refused },
	}
	if err := (Handler{}).Work(
		ctx,
		env,
		types.WorkItem{Kind: KindIndex, Key: c.Key},
	); !errors.Is(
		err,
		refused,
	) {
		t.Fatalf("failed derived metadata write was ignored: %v", err)
	}
	if _, err := st.Items().
		Put(ctx, types.Item{RepoKey: c.Key, Name: "linux-64/repodata.json", Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Items().Delete(ctx, c.Key, false, id.Path); err != nil {
		t.Fatal(err)
	}
	env.Inspection = nil
	env.Underive = func(context.Context, repo.Config, bool, string) error { return refused }
	if err := (Handler{}).Work(
		ctx,
		env,
		types.WorkItem{Kind: KindIndex, Key: c.Key},
	); !errors.Is(
		err,
		refused,
	) {
		t.Fatalf("failed stale metadata removal was ignored: %v", err)
	}
	if _, err := (Handler{}).Inspect(
		ctx,
		env,
		c,
		format.Write{Op: format.OpDeploy, Path: "README"},
	); err != nil {
		t.Fatalf("unrelated file inspected as package: %v", err)
	}
	blackout := true
	c.BlackedOut = &blackout
	if _, err := (Handler{}).Admit(
		ctx,
		env,
		c,
		format.Write{Op: format.OpDeploy, Path: "README"},
	); err == nil {
		t.Fatal("blacked out repository accepted write")
	}
	if _, err := readStored(ctx, condaTestBlobs{}, "", id); err == nil {
		t.Fatal("missing package checksum was accepted")
	}
	if got := len((Handler{}).WorkKinds()); got != 1 {
		t.Fatalf("unexpected Conda worker kinds: %d", got)
	}
}
