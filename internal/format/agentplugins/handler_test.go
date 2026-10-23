package agentplugins

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestHandlerRejectsInvalidBundlesAndPolicies(t *testing.T) {
	ctx := context.Background()
	h := Handler{}
	c := repo.Config{Key: "plugins", Class: repo.Local, PackageType: repo.AgentPlugins}
	p := Path("reader", "1.0.0")
	blackout := true
	excluded := "**"
	w := format.Write{Op: format.OpDeploy, Path: p}
	good := format.Env{Inspection: &inspection{meta: Metadata{Harnesses: []string{"claude"}}}}
	for _, tc := range []struct {
		name   string
		env    format.Env
		path   string
		config repo.Config
		fail   bool
	}{
		{"valid", good, p, c, false},
		{"bad path", good, "reader/1.0.0/other.zip", c, true},
		{"reserved marketplace", good, "marketplace.json", c, true},
		{"missing inspection", format.Env{}, p, c, true},
		{"invalid inspection", format.Env{Inspection: &inspection{err: ErrArchive}}, p, c, true},
		{"root only", format.Env{Inspection: &inspection{meta: Metadata{Root: true}}}, p, c, true},
		{"blackout", good, p, repo.Config{Key: c.Key, Class: c.Class, PackageType: c.PackageType, BlackedOut: &blackout}, true},
		{"excluded", good, p, repo.Config{Key: c.Key, Class: c.Class, PackageType: c.PackageType, ExcludesPattern: &excluded}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w.Path = tc.path
			_, err := h.Admit(ctx, tc.env, tc.config, w)
			if (err != nil) != tc.fail {
				t.Fatalf("Admit: %v", err)
			}
		})
	}
	for _, w := range []format.Write{{Op: format.OpChecksum, Path: p}, {Op: format.OpDeploy, Cached: true, Path: p}} {
		if _, err := h.Admit(ctx, format.Env{}, c, w); err != nil {
			t.Fatalf("non-publish write: %v", err)
		}
	}
	if err := h.Deployed(ctx, format.Env{}, c, types.Item{Folder: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(ctx, format.Env{}, c, types.Item{Cached: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(ctx, format.Env{}, c, types.Item{Name: "misc.zip"}); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerMissingBlobAndReindexFault(t *testing.T) {
	ctx := context.Background()
	bs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("f", 64)
	h := Handler{}
	c := repo.Config{Key: "plugins", Class: repo.Local, PackageType: repo.AgentPlugins}
	env := format.Env{Store: inmem.New(), Blobs: bs}
	if _, err := h.Inspect(
		ctx,
		env,
		c,
		format.Write{Path: Path("reader", "1.0.0"), Checksums: types.Checksums{SHA256: sha}},
	); err == nil {
		t.Fatal("missing blob accepted")
	}
	if got, err := h.Inspect(
		ctx,
		env,
		c,
		format.Write{Path: "notes.txt"},
	); got != nil ||
		err != nil {
		t.Fatalf("non-ZIP: %v %v", got, err)
	}
	it := types.Item{
		RepoKey: c.Key,
		Name:    Path("reader", "1.0.0"),
		Actual:  types.Checksums{SHA256: sha},
	}
	it, err = env.Store.Items().Put(ctx, it)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Reindex(
		ctx,
		env,
		c,
	); err == nil ||
		!errors.Is(err, ErrArchive) && !strings.Contains(err.Error(), "open stored bundle") {
		t.Fatalf("expected corrupt source error: %v", err)
	}
	if err := h.Deployed(ctx, env, c, it); err == nil {
		t.Fatal("missing blob indexed")
	}
	rootOnly := format.Env{
		Store:      env.Store,
		Inspection: &inspection{sha: sha, meta: Metadata{Root: true}},
	}
	if err := h.Deployed(
		ctx,
		rootOnly,
		c,
		it,
	); err == nil ||
		!strings.Contains(err.Error(), "root-only") {
		t.Fatalf("root-only target bundle: %v", err)
	}
}
