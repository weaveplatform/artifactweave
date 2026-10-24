package debian

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitAdmissionProtectsGeneratedMetadataAndSnapshots(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian})
	for _, tc := range []struct {
		name, path string
		ctx        context.Context
		wantStatus int
	}{
		{"generated Release", "dists/stable/Release", t.Context(), http.StatusConflict},
		{"generated Packages", "dists/stable/main/binary-amd64/Packages", t.Context(), http.StatusConflict},
		{"immutable snapshot", "snapshots/old/dists/stable/Release", t.Context(), http.StatusConflict},
		{"invalid descriptor", "../alpha.dsc", t.Context(), http.StatusBadRequest},
		{"debug disabled", "pool/alpha.ddeb", t.Context(), http.StatusBadRequest},
		{"invalid binary path", "../alpha.deb", t.Context(), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Admit(tc.ctx, format.Env{}, c,
				format.Write{Op: format.OpDeploy, Path: tc.path})
			rejection := new(format.Rejection)
			if !errors.As(err, &rejection) || rejection.Status != tc.wantStatus {
				t.Fatalf("admission of %s: %v", tc.path, err)
			}
		})
	}
	if _, err := h.Admit(t.Context(), format.Env{}, c,
		format.Write{Op: format.OpChecksum, Path: "dists/stable/Release"}); err != nil {
		t.Fatalf("checksum sidecar should remain available: %v", err)
	}
	if _, err := h.Admit(WithSnapshotWrite(t.Context()), format.Env{}, c,
		format.Write{Op: format.OpDeploy, Path: "snapshots/old/notes.txt"}); err != nil {
		t.Fatalf("trusted snapshot writer rejected: %v", err)
	}
	if err := h.Deleting(t.Context(), format.Env{}, c,
		types.Item{Name: "snapshots/old/notes.txt"}); err == nil {
		t.Fatal("snapshot delete accepted without trusted context")
	}
	if err := h.Deleting(WithSnapshotDelete(t.Context()), format.Env{}, c,
		types.Item{Name: "snapshots/old/notes.txt"}); err != nil {
		t.Fatalf("trusted snapshot delete rejected: %v", err)
	}
}

func TestUnitAdmissionBindsInspectedPackageCoordinates(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian})
	const binaryPath = "pool/main/a/alpha/alpha_1.0_amd64.deb"
	binary := Binary{Name: "alpha", Version: "1.0", Architecture: "amd64"}
	binaryWrite := format.Write{
		Op: format.OpDeploy, Path: binaryPath,
		Checksums: types.Checksums{SHA256: strings.Repeat("a", 64)},
		Properties: types.Properties{
			PropDistribution: {"stable"}, PropComponent: {"main"},
			PropArchitecture: {"amd64"},
		},
	}
	env := format.Env{Inspection: inspection{
		sha: binaryWrite.Checksums.SHA256, path: binaryPath, binary: binary,
	}}
	if _, err := h.Admit(t.Context(), env, c, binaryWrite); err != nil {
		t.Fatalf("valid inspected package rejected: %v", err)
	}
	for _, tc := range []struct {
		name  string
		props types.Properties
	}{
		{"partial coordinates", types.Properties{PropDistribution: {"stable"}}},
		{"path escape coordinate", types.Properties{
			PropDistribution: {"../stable"},
			PropComponent:    {"main"}, PropArchitecture: {"amd64"},
		}},
		{"architecture mismatch", types.Properties{
			PropDistribution: {"stable"},
			PropComponent:    {"main"}, PropArchitecture: {"arm64"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := binaryWrite
			w.Properties = tc.props
			if _, err := h.Admit(t.Context(), env, c, w); err == nil {
				t.Fatal("package with unsafe coordinates admitted")
			}
		})
	}
	const sourcePath = "pool/main/a/alpha/alpha_1.0-1.dsc"
	sourceWrite := format.Write{
		Op: format.OpDeploy, Path: sourcePath,
		Checksums:  types.Checksums{SHA256: strings.Repeat("b", 64)},
		Properties: types.Properties{"dsc.distribution": {"stable"}, "dsc.component": {"main"}},
	}
	srcEnv := format.Env{Inspection: sourceInspection{
		sha:  sourceWrite.Checksums.SHA256,
		path: sourcePath, source: Source{Name: "alpha", Version: "1.0-1"},
	}}
	if _, err := h.Admit(t.Context(), srcEnv, c, sourceWrite); err != nil {
		t.Fatalf("valid source coordinates rejected: %v", err)
	}
	sourceWrite.Properties["dsc.component"] = []string{"../main"}
	if _, err := h.Admit(t.Context(), srcEnv, c, sourceWrite); err == nil {
		t.Fatal("source coordinate path escape admitted")
	}
	trivial := true
	c.DebianTrivialLayout = &trivial
	if _, err := h.Admit(t.Context(), srcEnv, c, sourceWrite); err == nil {
		t.Fatal("source descriptor admitted in trivial layout")
	}
}

func TestUnitDebianWorkAndClassificationsRespectRepositoryClass(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian})
	if !Serves(c.PackageType) || len(h.WorkKinds()) != 2 ||
		h.Classify(c, "dists/stable/Release") != format.ClassDerived ||
		h.Classify(c, "pool/alpha.deb") != format.ClassUnique ||
		len(h.Owed(c, false, "dists/stable/Release")) != 1 ||
		len(h.Owed(c, true, "dists/stable/Release")) != 0 {
		t.Fatal("repository work or metadata classification changed")
	}
	if err := h.Work(
		t.Context(),
		format.Env{},
		types.WorkItem{Kind: "unrelated", Key: c.Key},
	); err != nil {
		t.Fatal(err)
	}
	st := inmem.New()
	remote := *repo.Defaults(&repo.Config{Key: "deb-remote", Class: repo.Remote, PackageType: repo.Debian})
	if err := st.Repos().Create(t.Context(), remote); err != nil {
		t.Fatal(err)
	}
	if err := h.Work(t.Context(), format.Env{Store: st},
		types.WorkItem{Kind: KindIndex, Key: remote.Key}); err != nil {
		t.Fatalf("remote cache tried to derive a partial apt index: %v", err)
	}
}

type debianReadCloser struct{ *bytes.Reader }

func (debianReadCloser) Close() error { return nil }

type debianFixtureBlobs struct{ body []byte }

func (b debianFixtureBlobs) Open(
	_ context.Context,
	_ string,
) (io.ReadSeekCloser, blob.Info, error) {
	return debianReadCloser{bytes.NewReader(b.body)}, blob.Info{Size: int64(len(b.body))}, nil
}

func (b debianFixtureBlobs) Stat(_ context.Context, _ string) (blob.Info, error) {
	return blob.Info{Size: int64(len(b.body))}, nil
}

func TestUnitInspectionReadsCommittedDebianContent(t *testing.T) {
	h := Handler{}
	c := repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian}
	for _, tc := range []struct {
		path, file string
		check      func(t *testing.T, got any)
	}{
		{"pool/alpha.deb", "testdata/alpha_1.0_amd64_0.deb", func(t *testing.T, got any) {
			t.Helper()
			in, ok := got.(inspection)
			if !ok || in.binary.Name != "alpha" || in.binary.Architecture != "amd64" {
				t.Fatalf("binary inspection: %+v", got)
			}
		}},
		{"pool/alpha.dsc", "testdata/alpha_1.0-1.dsc", func(t *testing.T, got any) {
			t.Helper()
			in, ok := got.(sourceInspection)
			if !ok || in.source.Name != "alpha" || len(in.source.Parts) != 2 {
				t.Fatalf("source inspection: %+v", got)
			}
		}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			sha := sourceTestChecksums(body).SHA256
			got, err := h.Inspect(t.Context(), format.Env{Blobs: debianFixtureBlobs{body}}, c,
				format.Write{
					Op: format.OpDeploy, Path: tc.path,
					Checksums: types.Checksums{SHA256: sha},
				})
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, got)
		})
	}
	if got, err := h.Inspect(t.Context(), format.Env{}, c,
		format.Write{Op: format.OpChecksum, Path: "pool/alpha.deb"}); err != nil {
		t.Fatal(err)
	} else if _, ok := got.(inspection); !ok {
		t.Fatalf("checksum inspection: %+v", got)
	}
	if _, err := h.Inspect(t.Context(), format.Env{Blobs: debianFixtureBlobs{[]byte("bad")}}, c,
		format.Write{
			Op: format.OpDeploy, Path: "pool/bad.deb",
			Checksums: types.Checksums{SHA256: "bad"},
		}); err == nil {
		t.Fatal("invalid committed binary inspected successfully")
	}
	if _, err := h.Inspect(t.Context(), format.Env{Blobs: debianFixtureBlobs{[]byte("bad")}}, c,
		format.Write{
			Op: format.OpDeploy, Path: "pool/bad.dsc",
			Checksums: types.Checksums{SHA256: "bad"},
		}); err == nil {
		t.Fatal("invalid committed descriptor inspected successfully")
	}
}

func TestUnitAdmissionRechecksStaleInspectionAgainstStoredBytes(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian})
	for _, tc := range []struct {
		path, file string
		props      types.Properties
		stale      any
	}{
		{
			"pool/alpha.deb", "testdata/alpha_1.0_amd64_0.deb",
			types.Properties{
				PropDistribution: {"stable"}, PropComponent: {"main"},
				PropArchitecture: {"amd64"},
			},
			inspection{sha: "stale", path: "pool/alpha.deb", binary: Binary{Name: "forged"}},
		},
		{
			"pool/alpha.dsc", "testdata/alpha_1.0-1.dsc",
			types.Properties{"dsc.distribution": {"stable"}, "dsc.component": {"main"}},
			sourceInspection{sha: "stale", path: "pool/alpha.dsc", source: Source{Name: "forged"}},
		},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			w := format.Write{
				Op: format.OpDeploy, Path: tc.path, Properties: tc.props,
				Checksums: types.Checksums{SHA256: sourceTestChecksums(body).SHA256},
			}
			env := format.Env{Blobs: debianFixtureBlobs{body}, Inspection: tc.stale}
			if _, err := h.Admit(t.Context(), env, c, w); err != nil {
				t.Fatalf("valid stored bytes rejected after stale inspection: %v", err)
			}
			env.Blobs = debianFixtureBlobs{[]byte("bad")}
			if _, err := h.Admit(t.Context(), env, c, w); err == nil {
				t.Fatal("stale inspection bypassed corrupt stored bytes")
			}
		})
	}
	for _, path := range []string{"pool/alpha.deb", "pool/alpha.dsc"} {
		if _, err := h.Admit(t.Context(), format.Env{}, c,
			format.Write{Op: format.OpDeploy, Path: path}); err == nil {
			t.Fatalf("admitted %s without content digest", path)
		}
	}
}

func TestUnitSourceLifecycleQueuesLocalIndexWork(t *testing.T) {
	h := Handler{}
	c := repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian}
	queued := 0
	env := format.Env{Enqueue: func(_ context.Context, kind, key string, _ []byte) error {
		if kind != KindIndex || key != c.Key {
			t.Fatalf("wrong source index work: %s %s", kind, key)
		}
		queued++
		return nil
	}}
	for _, path := range []string{"pool/alpha.dsc", "pool/alpha.diff.gz", "pool/alpha.orig.tar.gz"} {
		it := types.Item{Name: path}
		if err := h.Deployed(t.Context(), env, c, it); err != nil {
			t.Fatal(err)
		}
		if err := h.Deleting(t.Context(), env, c, it); err != nil {
			t.Fatal(err)
		}
	}
	if queued != 6 {
		t.Fatalf("source changes queued %d index builds, want 6", queued)
	}
	if err := h.Deployed(
		t.Context(),
		env,
		c,
		types.Item{Name: "pool/alpha.dsc", Cached: true},
	); err != nil {
		t.Fatal(err)
	}
	if queued != 6 {
		t.Fatal("cached source queued local metadata work")
	}
}

func TestUnitDeleteCoalescesTwoFilesOfOneDebianVersion(t *testing.T) {
	h := Handler{}
	c := repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian}
	body, err := os.ReadFile("testdata/alpha_1.0_amd64_0.deb")
	if err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: inmem.New(), Blobs: debianFixtureBlobs{body}}
	removed := []types.Item{
		{Name: "pool/alpha_1.0_amd64_0.deb", Actual: sourceTestChecksums(body)},
		{Name: "pool/alpha_1.0_amd64_1.deb", Actual: sourceTestChecksums(body)},
	}
	if err := h.Deleted(t.Context(), env, c, removed); err != nil {
		t.Fatalf("two removed files of one package version: %v", err)
	}
}

func TestUnitLifecycleRejectsUnavailablePackageContent(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian})
	st := inmem.New()
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	it, err := st.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key, Name: "pool/broken.deb", Size: 4,
		Actual: types.Checksums{SHA256: strings.Repeat("a", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: st, Blobs: debianFixtureBlobs{[]byte("bad")}}
	if err := h.Deployed(t.Context(), env, c, it); err == nil {
		t.Fatal("invalid package entered the index on deployment")
	}
	if err := h.Deleted(t.Context(), env, c, []types.Item{it}); err == nil {
		t.Fatal("invalid deleted package silently altered the index")
	}
	if err := h.Reindex(t.Context(), env, c); err == nil {
		t.Fatal("reindex silently accepted corrupt stored package")
	}
	if err := h.Work(t.Context(), env, types.WorkItem{Kind: KindIndex, Key: c.Key}); err == nil {
		t.Fatal("apt metadata was derived from corrupt stored package")
	}
	body, err := os.ReadFile("testdata/alpha_1.0_amd64_0.deb")
	if err != nil {
		t.Fatal(err)
	}
	binary, err := ReadBinary(t.Context(), bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	env.Blobs = debianFixtureBlobs{body}
	env.Inspection = inspection{sha: it.Actual.SHA256, path: it.FullPath(), binary: binary}
	missing := it
	missing.ID = -1
	if err := h.Deployed(t.Context(), env, c, missing); err == nil {
		t.Fatal("deployment ignored missing item properties")
	}
	if _, err := st.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key, Name: "pool/corrupt-peer.deb", Size: 4,
		Actual: types.Checksums{SHA256: strings.Repeat("b", 64)},
	}); err != nil {
		t.Fatal(err)
	}
	env.Blobs = debianFixtureBlobs{[]byte("bad")}
	if err := h.Deployed(t.Context(), env, c, it); err == nil {
		t.Fatal("version reconciliation ignored a corrupt peer package")
	}
}
