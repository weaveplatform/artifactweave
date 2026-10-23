package agentpackages

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestHandlerAdmissionAndNoContentBranches(t *testing.T) {
	h := Handler{}
	ctx := context.Background()
	c := repo.Config{Key: "apm", Class: repo.Local, PackageType: repo.AgentPackages}
	if in, err := h.Inspect(ctx, format.Env{}, c, format.Write{}); err != nil || in != nil {
		t.Fatalf("empty inspection: %v, %v", in, err)
	}
	if in, err := h.Inspect(
		ctx,
		format.Env{},
		c,
		format.Write{Op: format.OpChecksum},
	); err != nil ||
		in != nil {
		t.Fatalf("checksum inspection: %v, %v", in, err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	missing := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := h.Inspect(ctx, format.Env{Blobs: blobs}, c,
		format.Write{Checksums: types.Checksums{SHA256: missing}}); err == nil {
		t.Fatal("inspection accepted a missing blob")
	}
	good := format.Write{Op: format.OpDeploy, Path: Path("acme", "pkg", "1.0.0")}
	for _, tc := range []struct {
		name       string
		write      format.Write
		inspection any
		status     int
	}{
		{"invalid path", format.Write{Op: format.OpDeploy, Path: "acme/pkg/no.zip"}, nil, http.StatusConflict},
		{"missing inspection", good, nil, http.StatusConflict},
		{"bad archive", good, &inspection{err: ErrArchive}, http.StatusConflict},
		{"wrong name", good, &inspection{meta: Metadata{Name: "other", Version: "1.0.0"}}, http.StatusConflict},
		{"wrong version", good, &inspection{meta: Metadata{Name: "pkg", Version: "2.0.0"}}, http.StatusConflict},
		{"valid archive", good, &inspection{meta: Metadata{Name: "pkg", Version: "1.0.0"}}, 0},
		{"cached content", format.Write{Op: format.OpDeploy, Cached: true, Path: "cached"}, nil, 0},
		{"checksum", format.Write{Op: format.OpChecksum, Path: good.Path}, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Admit(ctx, format.Env{Inspection: tc.inspection}, c, tc.write)
			if tc.status == 0 && err != nil {
				t.Fatal(err)
			}
			if tc.status != 0 {
				var rejection *format.Rejection
				if !errors.As(err, &rejection) || rejection.Status != tc.status {
					t.Fatalf("admission: %v", err)
				}
			}
		})
	}
	for _, w := range []format.Write{{Op: format.OpChecksum}, {Cached: true}} {
		if err := h.GuardWrite(ctx, format.Env{}, c, w); err != nil {
			t.Fatal(err)
		}
	}
	blackout := c
	blackout.BlackedOut = new(bool)
	*blackout.BlackedOut = true
	if _, err := h.Admit(ctx, format.Env{Inspection: &inspection{}}, blackout, good); err == nil {
		t.Fatal("blackout admitted a publish")
	}
	pattern := c
	pattern.IncludesPattern = new(string)
	*pattern.IncludesPattern = "different/**"
	if _, err := h.Admit(ctx, format.Env{Inspection: &inspection{}}, pattern, good); err == nil {
		t.Fatal("include pattern admitted a publish")
	}
	if err := h.GuardWrite(ctx, format.Env{Store: inmem.New()}, c, good); err != nil {
		t.Fatalf("new version: %v", err)
	}
	for _, it := range []types.Item{{Folder: true}, {Cached: true}} {
		if err := h.Deployed(ctx, format.Env{}, c, it); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := metadataOf(ctx, format.Env{Blobs: blobs}, types.Item{
		Actual: types.Checksums{SHA256: missing},
	}); err == nil {
		t.Fatal("metadata lookup accepted a missing blob")
	}
	if err := setProperties(ctx, inmem.New(), types.Item{ID: 999}, Metadata{}); err == nil {
		t.Fatal("missing item's properties were patched")
	}
}
