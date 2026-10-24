package rpm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAdmitProtectsGeneratedMetadataAndRPMIdentity(t *testing.T) {
	t.Parallel()
	c := *repo.Defaults(&repo.Config{Key: "rpm-local", Class: repo.Local, PackageType: repo.RPM})
	h := Handler{}
	for _, tc := range []struct {
		name string
		w    format.Write
		code int
	}{
		{"generated manifest", format.Write{Path: "repodata/repomd.xml"}, http.StatusConflict},
		{"generated zstd primary", format.Write{Path: "repodata/stale-primary.xml.zst"}, http.StatusConflict},
		{"invalid RPM path", format.Write{Path: "pool/bad name.rpm"}, http.StatusBadRequest},
		{"checksum upload", format.Write{Op: format.OpChecksum, Path: "repodata/repomd.xml"}, 0},
		{"ordinary side file", format.Write{Path: "README.txt"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Admit(t.Context(), format.Env{}, c, tc.w)
			if tc.code == 0 && err == nil {
				return
			}
			var refusal *format.Rejection
			if !errors.As(err, &refusal) || refusal.Status != tc.code {
				t.Fatalf("admission returned %v; want status %d", err, tc.code)
			}
		})
	}
	blocked := c
	yes := true
	blocked.BlackedOut = &yes
	if _, err := h.Admit(t.Context(), format.Env{}, blocked,
		format.Write{Path: "pool/" + fixtureName}); err == nil {
		t.Fatal("blacked-out repository accepted RPM")
	}
	pkg, _, _ := metadataFixture(t)
	w := format.Write{Path: pkg.Path, Checksums: types.Checksums{SHA256: pkg.SHA256}}
	if _, err := h.Admit(t.Context(), format.Env{
		Inspection: inspection{sha: pkg.SHA256, path: pkg.Path, pkg: pkg},
	}, c, w); err != nil {
		t.Fatalf("valid inspected RPM refused: %v", err)
	}
	if _, err := h.Inspect(t.Context(), format.Env{}, c,
		format.Write{Path: "README.txt"}); err != nil {
		t.Fatalf("ordinary side file inspected as RPM: %v", err)
	}
	sideFile := types.Item{RepoKey: c.Key, Name: "README.txt"}
	if err := h.Deployed(t.Context(), format.Env{}, c, sideFile); err != nil {
		t.Fatalf("ordinary side file indexed as RPM: %v", err)
	}
	if err := h.Deleted(t.Context(), format.Env{}, c, []types.Item{sideFile}); err != nil {
		t.Fatalf("ordinary side file deletion changed RPM index: %v", err)
	}
	if got := h.Classify(c, "repodata/repomd.xml"); got != format.ClassDerived {
		t.Fatalf("generated manifest class = %v", got)
	}
}

func TestMetadataRootHonorsConfiguredDepth(t *testing.T) {
	t.Parallel()
	path := "distribution/architecture/pool/" + fixtureName
	depth := 2
	if root, ok := metadataRoot(path, &depth); !ok || root != "distribution/architecture/" {
		t.Fatalf("depth-two metadata root = %q, %v", root, ok)
	}
	for _, invalid := range []int{-1, 5} {
		if root, ok := metadataRoot(path, &invalid); ok {
			t.Fatalf("invalid depth %d yielded metadata root %q", invalid, root)
		}
	}
}

func TestIndexWorkTracksRepositoryAndPublicationFailures(t *testing.T) {
	st := inmem.New()
	ctx := t.Context()
	local := *repo.Defaults(&repo.Config{Key: "rpm-local", Class: repo.Local, PackageType: repo.RPM})
	remote := *repo.Defaults(&repo.Config{Key: "rpm-remote", Class: repo.Remote, PackageType: repo.RPM})
	other := *repo.Defaults(&repo.Config{Key: "other-local", Class: repo.Local, PackageType: repo.Generic})
	for _, c := range []repo.Config{local, remote, other} {
		if err := st.Repos().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler{}
	env := format.Env{Store: st}
	for _, work := range []types.WorkItem{
		{Kind: "rpm.retired", Key: local.Key},
		{Kind: KindIndex, Key: "missing"},
		{Kind: KindIndex, Key: remote.Key},
		{Kind: KindIndex, Key: other.Key},
		{Kind: KindIndex, Key: local.Key},
	} {
		if err := h.Work(ctx, env, work); err != nil {
			t.Fatalf("stale or empty work %+v: %v", work, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.Work(
		canceled,
		env,
		types.WorkItem{Kind: KindIndex, Key: local.Key},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled repository lookup: %v", err)
	}

	pkg, _, _ := metadataFixture(t)
	if _, err := st.Items().Put(ctx, types.Item{
		RepoKey: local.Key, Name: pkg.Path,
		Actual: types.Checksums{SHA256: pkg.SHA256},
	}); err != nil {
		t.Fatal(err)
	}
	const stale = "repodata/stale-primary.xml.gz"
	if _, err := st.Items().Put(ctx, types.Item{
		RepoKey: local.Key, Name: stale,
		Actual: types.Checksums{SHA256: strings.Repeat("b", 64)},
	}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("index persistence failed")
	env = format.Env{
		Store: st, Now: func() time.Time { return time.Unix(1700000000, 0) },
		Inspection: inspection{sha: pkg.SHA256, path: pkg.Path, pkg: pkg},
		Derive: func(context.Context, format.Derived) (types.Item, error) {
			return types.Item{}, failure
		},
		Underive: func(context.Context, repo.Config, bool, string) error { return failure },
	}
	work := types.WorkItem{Kind: KindIndex, Key: local.Key}
	if err := h.Work(ctx, env, work); !errors.Is(err, failure) {
		t.Fatalf("derived metadata failure: %v", err)
	}
	var published []string
	env.Derive = func(_ context.Context, d format.Derived) (types.Item, error) {
		published = append(published, d.Path)
		return types.Item{}, nil
	}
	if err := h.Work(ctx, env, work); !errors.Is(err, failure) {
		t.Fatalf("stale metadata removal failure: %v", err)
	}
	if len(published) < 2 || published[len(published)-1] != "repodata/repomd.xml" {
		t.Fatalf("manifest was not published after the primary index: %v", published)
	}
	env.Underive = func(_ context.Context, _ repo.Config, _ bool, path string) error {
		if path != stale {
			t.Errorf("removed %q; want stale %q", path, stale)
		}
		return nil
	}
	if err := h.Work(ctx, env, work); err != nil {
		t.Fatal(err)
	}
	local.PrimaryKeyPairRef = "missing"
	if err := st.Repos().Update(ctx, local); err != nil {
		t.Fatal(err)
	}
	if err := h.Work(ctx, env, work); err == nil {
		t.Fatal("missing GPG signing key allowed metadata publication")
	}
}

func TestSigningRejectsWrongKeyType(t *testing.T) {
	t.Parallel()
	st := inmem.New()
	ctx := t.Context()
	if err := st.Keys().
		CreateKeypair(ctx, types.Keypair{Name: "rsa", Type: types.KeypairRSA}); err != nil {
		t.Fatal(err)
	}
	c := repo.Config{PrimaryKeyPairRef: "rsa"}
	if err := signMetadata(
		ctx,
		st,
		c,
		map[string][]byte{"repodata/repomd.xml": []byte("x")},
	); err == nil {
		t.Fatal("non-GPG key accepted for repomd signing")
	}
}
