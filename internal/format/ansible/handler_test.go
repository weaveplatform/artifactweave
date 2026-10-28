package ansible

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errFault = errors.New("injected failure")

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }

type blobFake struct {
	data []byte
	err  error
}

func (b blobFake) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	if b.err != nil {
		return nil, blob.Info{}, b.err
	}
	return readCloser{bytes.NewReader(b.data)}, blob.Info{Size: int64(len(b.data))}, nil
}

func (b blobFake) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{Size: int64(len(b.data))}, b.err
}

type faultStore struct {
	store.Store
	stage string
}

func (s faultStore) Items() store.ItemStore { return faultItems{s.Store.Items(), s.stage} }

func (s faultStore) Packages() store.PackageStore { return faultPackages{s.Store.Packages(), s.stage} }

type faultItems struct {
	store.ItemStore
	stage string
}

func (s faultItems) Properties(ctx context.Context, id int64) (types.Properties, error) {
	if s.stage == "properties" {
		return nil, errFault
	}
	return s.ItemStore.Properties(ctx, id)
}

func (s faultItems) PatchProperties(
	ctx context.Context,
	id int64,
	p types.Properties,
	remove []string,
	at time.Time,
) error {
	if s.stage == "patch" {
		return errFault
	}
	return s.ItemStore.PatchProperties(ctx, id, p, remove, at)
}

func (s faultItems) Descendants(
	ctx context.Context,
	key string,
	cached bool,
	path string,
	page store.Page,
) ([]types.Item, string, error) {
	if s.stage == "descendants" {
		return nil, "", errFault
	}
	if s.stage == "pagination" && page.After == "" {
		items, _, err := s.ItemStore.Descendants(ctx, key, cached, path, page)
		return items, "next", err
	}
	if page.After == "next" {
		return nil, "", nil
	}
	return s.ItemStore.Descendants(ctx, key, cached, path, page)
}

type faultPackages struct {
	store.PackageStore
	stage string
}

func (s faultPackages) Ensure(ctx context.Context, p types.Package) (types.Package, error) {
	if s.stage == "ensure" {
		return types.Package{}, errFault
	}
	return s.PackageStore.Ensure(ctx, p)
}

func (s faultPackages) List(
	ctx context.Context,
	key string,
	page store.Page,
) ([]types.Package, string, error) {
	if s.stage == "list" {
		return nil, "", errFault
	}
	return s.PackageStore.List(ctx, key, page)
}

func fixtureEnv(t *testing.T) (format.Env, repo.Config, types.Item) {
	t.Helper()
	st := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "ansible", Class: repo.Local, PackageType: repo.Ansible})
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/weave-sample-1.2.3.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ReadArchive(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	it, err := st.Items().
		Put(t.Context(), types.Item{RepoKey: c.Key, Name: Path("weave", "sample", "1.2.3"), Actual: types.Checksums{SHA256: meta.SHA256}, Size: int64(len(data))})
	if err != nil {
		t.Fatal(err)
	}
	return format.Env{Store: st, Blobs: blobFake{data: data}}, c, it
}

func TestUnitAnsibleFormatFailureBoundaries(t *testing.T) {
	for _, stage := range []string{"properties", "patch", "ensure", "descendants", "list"} {
		t.Run(stage, func(t *testing.T) {
			env, c, it := fixtureEnv(t)
			env.Store = faultStore{env.Store, stage}
			h := Handler{}
			if stage == "descendants" || stage == "list" {
				if err := h.Reindex(t.Context(), env, c); !errors.Is(err, errFault) {
					t.Fatalf("reindex error: %v", err)
				}
				return
			}
			if err := h.Deployed(t.Context(), env, c, it); !errors.Is(err, errFault) {
				t.Fatalf("deploy error: %v", err)
			}
			if stage != "ensure" {
				if err := h.Reindex(t.Context(), env, c); !errors.Is(err, errFault) {
					t.Fatalf("reindex error: %v", err)
				}
			}
		})
	}
	t.Run("unreadable archive", func(t *testing.T) {
		env, c, it := fixtureEnv(t)
		env.Blobs = blobFake{err: errFault}
		h := Handler{}
		if _, err := h.Inspect(
			t.Context(),
			env,
			c,
			format.Write{Path: it.FullPath(), Checksums: it.Actual},
		); !errors.Is(
			err,
			errFault,
		) {
			t.Fatal(err)
		}
		if err := h.Deployed(t.Context(), env, c, it); !errors.Is(err, errFault) {
			t.Fatal(err)
		}
		if err := h.Reindex(t.Context(), env, c); !errors.Is(err, ErrArchive) {
			t.Fatal(err)
		}
	})
}

func TestUnitAnsibleReindexRepairsPropertiesAndPages(t *testing.T) {
	env, c, it := fixtureEnv(t)
	h := Handler{}
	env.Store = faultStore{env.Store, "pagination"}
	for _, skip := range []bool{true, false, false} {
		env.SkipProperties = skip
		if err := h.Reindex(t.Context(), env, c); err != nil {
			t.Fatal(err)
		}
	}
	props, err := env.Store.Items().Properties(t.Context(), it.ID)
	if err != nil || props["ansible.name"][0] != "sample" {
		t.Fatalf("properties: %v %v", props, err)
	}
	if !Serves(repo.Ansible) || Serves(repo.Generic) {
		t.Fatal("format dispatch")
	}
}

func TestUnitAnsibleAdmissionRequiresCanonicalValidatedContent(t *testing.T) {
	env, c, it := fixtureEnv(t)
	h := Handler{}
	for _, tc := range []struct {
		name, path    string
		op            format.Op
		cached, valid bool
	}{
		{"archive", it.FullPath(), format.OpDeploy, false, true},
		{"wrong identity", Path("other", "sample", "1.2.3"), format.OpDeploy, false, false},
		{"wrong path", "weave-sample.tar.gz", format.OpDeploy, false, false},
		{"other file", "random.json", format.OpDeploy, false, false},
		{"checksum", it.FullPath(), format.OpChecksum, false, true},
		{"cache", "file", format.OpDeploy, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := format.Write{Path: tc.path, Op: tc.op, Cached: tc.cached, Checksums: it.Actual}
			e := env
			in, err := h.Inspect(t.Context(), e, c, w)
			if err != nil {
				t.Fatal(err)
			}
			e.Inspection = in
			_, err = h.Admit(t.Context(), e, c, w)
			if (err == nil) != tc.valid {
				t.Fatalf("admit: %v", err)
			}
		})
	}
	for _, it := range []types.Item{{Folder: true}, {Cached: true}, {Name: "unrelated"}} {
		if err := h.Deployed(t.Context(), env, c, it); err != nil {
			t.Fatal(err)
		}
	}
	env.Inspection = &inspection{sha: it.Actual.SHA256, err: errFault}
	if err := h.Deployed(t.Context(), env, c, it); !errors.Is(err, errFault) {
		t.Fatal(err)
	}
	c.IncludesPattern = new("safe/**")
	if _, err := h.Admit(t.Context(), env, c, format.Write{Path: "denied/file"}); err == nil {
		t.Fatal("pattern denied file admitted")
	}
	env.Blobs = blobFake{data: []byte(strings.Repeat("bad", 10))}
	if err := h.Reindex(t.Context(), env, c); !errors.Is(err, ErrArchive) {
		t.Fatal(err)
	}
}
