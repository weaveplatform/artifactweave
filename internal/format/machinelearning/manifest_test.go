package machinelearning_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	ml "github.com/weaveplatform/artifactweave/internal/format/machinelearning"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errFixture = errors.New("controlled model storage failure")

func modelManifest() ml.Manifest {
	return ml.Manifest{
		Created: "2026-10-28T12:00:00Z",
		Artifacts: []ml.Artifact{
			{
				Path:      "weights.bin",
				Size:      5,
				Checksums: ml.Checksums{SHA256: strings.Repeat("a", 64)},
			},
		},
		Format: ml.Serialization{
			Framework:        "sklearn",
			FrameworkVersion: "1.8.0",
			Format:           "pickle",
			Runtime:          "python",
			RuntimeVersion:   "3.12",
		},
	}
}

func manifestJSON(t *testing.T, m ml.Manifest) []byte {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errFixture }

func TestUnitManifestBoundsAndNativeSerializationMetadata(t *testing.T) {
	for _, created := range []string{"2026-10-28T12:00:00Z", "2026-10-28T12:00:00.123456+01:00", "2026-10-28T12:00:00.123456", "2026-10-28T12:00:00"} {
		m := modelManifest()
		m.Created = created
		got, err := ml.Decode(bytes.NewReader(manifestJSON(t, m)))
		if err != nil || got.Created != created {
			t.Fatalf("native timestamp %q: %v", created, err)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*ml.Manifest)
	}{
		{"date", func(m *ml.Manifest) { m.Created = "yesterday" }},
		{"no files", func(m *ml.Manifest) { m.Artifacts = nil }},
		{"many files", func(m *ml.Manifest) { m.Artifacts = make([]ml.Artifact, ml.MaxFiles+1) }},
		{"framework missing", func(m *ml.Manifest) { m.Format.Framework = "" }},
		{"format missing", func(m *ml.Manifest) { m.Format.Format = "" }},
		{"runtime missing", func(m *ml.Manifest) { m.Format.Runtime = "" }},
		{"runtime version missing", func(m *ml.Manifest) { m.Format.RuntimeVersion = "" }},
		{"metadata too long", func(m *ml.Manifest) { m.Format.FrameworkVersion = strings.Repeat("a", 129) }},
		{"metadata control", func(m *ml.Manifest) { m.Format.Runtime = "python\x00" }},
		{"unsafe file", func(m *ml.Manifest) { m.Artifacts[0].Path = "../secret" }},
		{"negative size", func(m *ml.Manifest) { m.Artifacts[0].Size = -1 }},
		{"too large", func(m *ml.Manifest) { m.Artifacts[0].Size = ml.MaxFileSize + 1 }},
		{"short digest", func(m *ml.Manifest) { m.Artifacts[0].Checksums.SHA256 = "a" }},
		{"uppercase digest", func(m *ml.Manifest) { m.Artifacts[0].Checksums.SHA256 = strings.Repeat("A", 64) }},
		{"nonhex digest", func(m *ml.Manifest) { m.Artifacts[0].Checksums.SHA256 = strings.Repeat("z", 64) }},
		{"duplicate artifact", func(m *ml.Manifest) { m.Artifacts = append(m.Artifacts, m.Artifacts[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := modelManifest()
			tc.change(&m)
			if _, err := ml.Decode(bytes.NewReader(manifestJSON(t, m))); err == nil {
				t.Fatal("invalid model manifest accepted")
			}
		})
	}
	for _, raw := range []string{"{", strings.Repeat(" ", ml.MaxManifestSize+1), "null", "[]"} {
		if _, err := ml.Decode(strings.NewReader(raw)); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	if _, err := ml.Decode(failingReader{}); !errors.Is(err, errFixture) {
		t.Fatalf("manifest reader failure: %v", err)
	}
	m := modelManifest()
	m.Dependencies = []ml.Artifact{m.Artifacts[0]}
	code := m.Artifacts[0]
	m.Code = &code
	got, err := ml.Decode(bytes.NewReader(manifestJSON(t, m)))
	if err != nil {
		t.Fatal(err)
	}
	files := got.Files()
	if len(files) != 3 || files[0].Path != "files/weights.bin" ||
		files[1].Path != "dependencies/weights.bin" ||
		files[2].Path != "code/weights.bin" {
		t.Fatalf("category paths: %+v", files)
	}
}

type (
	memoryBlobs struct {
		data                 map[string][]byte
		openError, statError bool
	}
	memoryReader struct{ *bytes.Reader }
)

func (memoryReader) Close() error { return nil }
func (b *memoryBlobs) Open(_ context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if b.openError {
		return nil, blob.Info{}, errFixture
	}
	data, ok := b.data[sha]
	if !ok {
		return nil, blob.Info{}, blob.ErrNotFound
	}
	return memoryReader{
		bytes.NewReader(data),
	}, blob.Info{
		Size:    int64(len(data)),
		Digests: blob.Digests{SHA256: sha},
	}, nil
}

func (b *memoryBlobs) Stat(_ context.Context, sha string) (blob.Info, error) {
	if b.statError {
		return blob.Info{}, errFixture
	}
	data, ok := b.data[sha]
	if !ok {
		return blob.Info{}, blob.ErrNotFound
	}
	return blob.Info{Size: int64(len(data)), Digests: blob.Digests{SHA256: sha}}, nil
}

type formatFixture struct {
	t     *testing.T
	db    *inmem.Store
	blobs *memoryBlobs
	env   format.Env
	c     repo.Config
	ctx   context.Context
	now   time.Time
}

func newFormatFixture(t *testing.T) *formatFixture {
	t.Helper()
	db := inmem.New()
	b := &memoryBlobs{data: map[string][]byte{}}
	c := *repo.Defaults(&repo.Config{Key: "ml", Class: repo.Local, PackageType: repo.MachineLearning})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	ctx := t.Context()
	return &formatFixture{
		t:     t,
		db:    db,
		blobs: b,
		env:   format.Env{Store: db, Blobs: b, Now: func() time.Time { return now }},
		c:     c,
		ctx:   ctx,
		now:   now,
	}
}

func (f *formatFixture) put(path string, data []byte) types.Item {
	f.t.Helper()
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	f.blobs.data[sha] = data
	it, err := f.db.Items().
		Put(f.ctx, types.Item{RepoKey: f.c.Key, Name: path, Size: int64(len(data)), Actual: types.Checksums{SHA256: sha}, Created: f.now, CreatedBy: "owner"})
	if err != nil {
		f.t.Fatal(err)
	}
	return it
}

func (f *formatFixture) publish(id ml.Identity) (ml.Manifest, types.Item, types.Item) {
	f.t.Helper()
	it := f.put(id.Prefix()+"/files/weights.bin", []byte("model"))
	m := modelManifest()
	m.ID = id.Name
	m.Version = id.Version
	m.Artifacts[0].Checksums.SHA256 = it.Actual.SHA256
	manifest := f.put(id.Manifest(), manifestJSON(f.t, m))
	return m, manifest, it
}

func TestUnitStoredManifestIdentityAndContentVerification(t *testing.T) {
	f := newFormatFixture(t)
	id := ml.Identity{Name: "group/model", Version: "1"}
	m, manifest, it := f.publish(id)
	got, stored, err := ml.Read(f.ctx, f.env, f.c, id)
	if err != nil || got.ID != id.Name || stored.ID != manifest.ID {
		t.Fatal(got, stored, err)
	}
	files, err := ml.Verify(f.ctx, f.env, f.c, id.Prefix(), m)
	if err != nil || len(files) != 1 || files[0].ID != it.ID {
		t.Fatal(files, err)
	}
	f.blobs.openError = true
	if _, _, err = ml.Read(f.ctx, f.env, f.c, id); !errors.Is(err, errFixture) {
		t.Fatal(err)
	}
	f.blobs.openError = false
	f.blobs.statError = true
	if _, err = ml.Verify(f.ctx, f.env, f.c, id.Prefix(), m); !errors.Is(err, errFixture) {
		t.Fatal(err)
	}
	f.blobs.statError = false
	for _, change := range []func(*types.Item){func(it *types.Item) { it.Folder = true }, func(it *types.Item) { it.Size++ }, func(it *types.Item) { it.Actual.SHA256 = strings.Repeat("a", 64) }} {
		bad := it
		change(&bad)
		if _, err = f.db.Items().Put(f.ctx, bad); err != nil {
			t.Fatal(err)
		}
		if _, err = ml.Verify(f.ctx, f.env, f.c, id.Prefix(), m); err == nil {
			t.Fatal("corrupt stored artifact verified")
		}
	}
	if _, err = f.db.Items().Delete(f.ctx, f.c.Key, false, it.FullPath()); err != nil {
		t.Fatal(err)
	}
	if _, err = ml.Verify(f.ctx, f.env, f.c, id.Prefix(), m); !ml.Missing(err) {
		t.Fatalf("missing file: %v", err)
	}
	for _, change := range []func(*types.Item){func(it *types.Item) { it.Folder = true }, func(it *types.Item) { it.Size = ml.MaxManifestSize + 1 }} {
		bad := manifest
		change(&bad)
		if _, err = f.db.Items().Put(f.ctx, bad); err != nil {
			t.Fatal(err)
		}
		if _, _, err = ml.Read(f.ctx, f.env, f.c, id); err == nil {
			t.Fatal("invalid stored manifest accepted")
		}
	}
	m.ID = "another"
	f.put(id.Manifest(), manifestJSON(t, m))
	if _, _, err = ml.Read(f.ctx, f.env, f.c, id); err == nil {
		t.Fatal("wrong model manifest identity accepted")
	}
	f.put(id.Manifest(), []byte("bad"))
	if _, _, err = ml.Read(f.ctx, f.env, f.c, id); err == nil {
		t.Fatal("corrupt stored manifest accepted")
	}
	if _, _, err = ml.Read(
		f.ctx,
		f.env,
		f.c,
		ml.Identity{Name: "missing", Version: "1"},
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal(err)
	}
}

func TestUnitManifestRejectsAmbiguousJSONAndExcessiveDepth(t *testing.T) {
	valid := string(manifestJSON(t, modelManifest()))
	for _, raw := range []string{
		strings.Replace(valid, `"created_date":`, `"created_date":"2020-01-01T00:00:00Z","created_date":`, 1),
		strings.Replace(valid, `"created_date":`, `"Created_Date":`, 1),
		strings.Replace(valid, `"sha2":`, `"sha2":"bad","sha2":`, 1),
		strings.Replace(valid, `"artifact_path":`, `"artifact_path":"../secret","artifact_path":`, 1),
		valid + ` {}`, strings.TrimSuffix(valid, "}") + `,"extension":` + strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34) + "}",
		strings.TrimSuffix(valid, "}") + `,"extension":{"oops":` + "}",
		strings.TrimSuffix(valid, "}") + `,"extension":[`,
	} {
		if _, err := ml.Decode(strings.NewReader(raw)); err == nil {
			t.Fatal("ambiguous or malformed JSON accepted")
		}
	}
}
