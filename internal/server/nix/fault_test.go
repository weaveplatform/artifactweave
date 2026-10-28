package nix_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type testSource struct {
	meta, archive []byte
	mode          string
	cached        *types.Item
	rechecks      int
}

func (*testSource) Item(context.Context, store.Store, repo.Config, string) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func (s *testSource) Read(
	r *http.Request,
	_ repo.Config,
	res source.Resource,
) (source.Result, error) {
	if s.mode == "read failure" {
		return source.Result{}, source.ErrUnavailable
	}
	artifact := res.Kind == source.ResourceKindArtifact
	data := s.meta
	if artifact {
		data = s.archive
	}
	result := source.Result{
		Stream: &source.Stream{
			Body:   io.NopCloser(bytes.NewReader(data)),
			Status: 200,
			Size:   int64(len(data)),
		},
		CheckRead: func(context.Context, string) error { return nil },
		Revalidate: func(context.Context) error {
			s.rechecks++
			if s.mode == "revoked" || s.mode == "revoked before archive" && s.rechecks >= 3 {
				return source.ErrDenied
			}
			return nil
		},
	}
	if s.mode == "missing revalidation" || artifact && s.mode == "missing archive revalidation" {
		result.Revalidate = nil
	}
	if s.mode == "missing path check" {
		result.CheckRead = nil
	}
	if s.mode == "path denied" {
		result.CheckRead = func(context.Context, string) error { return source.ErrDenied }
	}
	if s.mode == "empty result" {
		result.Stream = nil
	}
	if s.mode == "cached" || s.mode == "missing blob" {
		result.Stream = nil
		result.Item = s.cached
	}
	if s.mode == "broken body" {
		result.Stream.Body = errorBody{bytes.NewReader(nil)}
	}
	if s.mode == "validated broken body" {
		if err := res.ValidateMetadata(
			r.Context(),
			errorBody{bytes.NewReader(nil)},
			10,
			source.MetadataResponse{},
		); err != nil {
			return source.Result{}, err
		}
	}
	return result, nil
}

type errorBody struct{ *bytes.Reader }

func (errorBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (errorBody) Close() error             { return nil }

func TestAccNixRemoteRequiresValidatedAuthority(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want int
	}{
		{"missing revalidation", 502},
		{"missing path check", 502},
		{"path denied", 403},
		{"revoked", 403},
		{"empty result", 502},
		{"broken body", 502},
		{"validated broken body", 502},
		{"read failure", 502},
		{"missing blob", 500},
		{"cached", 200},
		{"malformed", 502},
		{"missing archive revalidation", 502},
		{"revoked before archive", 403},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newFixture(t)
			f.publish()
			c := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Nix, URL: "https://cache.example/nix"})
			if err := f.db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			it, err := f.db.Items().Get(t.Context(), "local", false, infoPath)
			if err != nil {
				t.Fatal(err)
			}
			src := &testSource{meta: f.meta, archive: f.data, mode: tc.mode, cached: &it}
			if tc.mode == "missing blob" {
				src.cached = &types.Item{Actual: types.Checksums{SHA256: "missing"}}
			}
			if tc.mode == "malformed" {
				src.meta = []byte("invalid")
			}
			f.mount(nil, func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src })
			path := "/artifactory/api/nix/remote/" + infoPath
			if tc.mode == "missing archive revalidation" || tc.mode == "revoked before archive" {
				path = "/artifactory/api/nix/remote/nar/" + infoPath[:32] + "/" + f.info.URL[4:]
			}
			status(t, f.do("GET", path, nil, ""), tc.want)
		})
	}
}

type hookBlobs struct {
	blob.Store
	before func()
}

func (b hookBlobs) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if b.before != nil {
		b.before()
	}
	return b.Store.Open(ctx, sha)
}

func TestAccNixHostedAuthorityAndSigningFailures(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want int
	}{
		{"missing key", 503},
		{"wrong key type", 503},
		{"broken private key", 503},
		{"bad alias", 503},
		{"unsigned", 200},
		{"repository changed", 503},
		{"repository deleted", 404},
		{"bad stored metadata", 502},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newFixture(t)
			f.publish()
			pair, err := f.db.Keys().Keypair(t.Context(), "signer")
			if err != nil {
				t.Fatal(err)
			}
			switch tc.mode {
			case "missing key":
				if err = f.db.Keys().DeleteKeypair(t.Context(), "signer"); err != nil {
					t.Fatal(err)
				}
			case "wrong key type":
				pair.Type = types.KeypairRSA
			case "broken private key":
				pair.PrivateKey = "broken"
			case "bad alias":
				pair.Alias = "bad:alias"
			case "unsigned":
				c := f.c
				c.PrimaryKeyPairRef = ""
				if err = f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			case "repository changed", "repository deleted":
				done := false
				f.items.Blobs = hookBlobs{Store: f.items.Blobs, before: func() {
					if done {
						return
					}
					done = true
					if tc.mode == "repository deleted" {
						err = f.db.Repos().Delete(t.Context(), f.c.Key)
					} else {
						c := f.c
						c.Description = "changed"
						err = f.db.Repos().Update(t.Context(), c)
					}
					if err != nil {
						t.Error(err)
					}
				}}
			case "bad stored metadata":
				it, e := f.db.Items().Get(t.Context(), f.c.Key, false, infoPath)
				if e != nil {
					t.Fatal(e)
				}
				it.Actual.SHA256 = f.info.FileHash
				if _, err = f.db.Items().Put(t.Context(), it); err != nil {
					t.Fatal(err)
				}
			}
			if tc.mode == "wrong key type" || tc.mode == "broken private key" ||
				tc.mode == "bad alias" {
				if err = f.db.Keys().UpdateKeypair(t.Context(), pair); err != nil {
					t.Fatal(err)
				}
			}
			status(t, f.do("GET", prefix+infoPath, nil, ""), tc.want)
		})
	}
}

func TestAccNixPathAndVirtualPolicyFailures(t *testing.T) {
	f := newFixture(t)
	f.publish()
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		want := 404
		if method == "PUT" {
			want = 400
		}
		status(t, f.do(method, prefix+"%2e%2e/secret", nil, ""), want)
	}
	for _, kind := range []string{"empty", "excluded", "wrong-member"} {
		c := *repo.Defaults(&repo.Config{Key: kind, Class: repo.Virtual, PackageType: repo.Nix, Repositories: []string{"local"}})
		if kind == "empty" {
			c.Repositories = nil
		}
		if kind == "excluded" {
			p := "nar/**"
			c.ExcludesPattern = &p
		}
		if kind == "wrong-member" {
			wrong := repo.Config{Key: "other", Class: repo.Local, PackageType: repo.Generic}
			if err := f.db.Repos().Create(t.Context(), wrong); err != nil {
				t.Fatal(err)
			}
			c.Repositories = []string{"other"}
		}
		if err := f.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		code := 404
		if kind == "wrong-member" {
			code = 400
		}
		status(t, f.do("GET", "/artifactory/api/nix/"+kind+"/"+infoPath, nil, ""), code)
		status(t, f.do("POST", "/artifactory/api/nix/"+kind+"/reindex", nil, ""), 400)
	}
	excluded := "**"
	c := f.c
	c.ExcludesPattern = &excluded
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	status(t, f.do("GET", prefix+infoPath, nil, ""), 404)
}

func TestAccNixExactPathPermissionsAndRevocation(t *testing.T) {
	for _, mode := range []string{"metadata-only", "virtual-only", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.publish()
			v := *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Nix, Repositories: []string{"local"}})
			if err := f.db.Repos().Create(t.Context(), v); err != nil {
				t.Fatal(err)
			}
			targets := map[string]types.Target{"local": {}, "virtual": {}}
			if mode == "metadata-only" {
				targets["local"] = types.Target{Include: []string{infoPath}}
			}
			if mode == "virtual-only" {
				delete(targets, "local")
			}
			p := types.Permission{
				Name: "read",
				Resources: map[types.ResourceType]types.Resource{
					types.ResourceArtifact: {
						Actions: types.Grants{
							Users: map[string]types.Actions{"reader": types.ActionRead},
						},
						Targets: targets,
					},
				},
			}
			if err := f.db.Permissions().Create(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			if mode == "revoked" {
				done := false
				f.items.Blobs = hookBlobs{Store: f.items.Blobs, before: func() {
					if done {
						return
					}
					done = true
					if err := f.db.Permissions().Delete(t.Context(), "read"); err != nil {
						t.Error(err)
					}
				}}
			}
			path := prefix + infoPath
			want := 403
			if mode == "virtual-only" {
				path = "/artifactory/api/nix/virtual/" + infoPath
				want = 404
			}
			status(t, f.do("GET", path, nil, "reader"), want)
		})
	}
}

type failStore struct {
	store.Store
	repos store.RepoStore
	items store.ItemStore
}

func (s failStore) Repos() store.RepoStore {
	if s.repos != nil {
		return s.repos
	}
	return s.Store.Repos()
}
func (s failStore) Items() store.ItemStore {
	if s.items != nil {
		return s.items
	}
	return s.Store.Items()
}

type failRepos struct {
	store.RepoStore
	key string
}

func (s failRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if key == s.key {
		return repo.Config{}, io.ErrUnexpectedEOF
	}
	return s.RepoStore.Get(ctx, key)
}

type failItems struct{ store.ItemStore }

func (failItems) Descendants(
	context.Context,
	string,
	bool,
	string,
	store.Page,
) ([]types.Item, string, error) {
	return nil, "", io.ErrUnexpectedEOF
}

func TestAccNixFaultsDoNotLookLikeMisses(t *testing.T) {
	f := newFixture(t)
	f.publish()
	f.st = failStore{Store: f.db, items: failItems{f.db.Items()}}
	f.mount(nil)
	status(t, f.do("POST", prefix+"reindex", nil, ""), 500)
	v := *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Nix, Repositories: []string{"local"}})
	if err := f.db.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	f.st = failStore{Store: f.db, repos: failRepos{f.db.Repos(), "local"}}
	f.mount(nil)
	status(t, f.do("GET", "/artifactory/api/nix/virtual/"+infoPath, nil, ""), 500)
}

func TestAccNixRequestAndDeliveryFences(t *testing.T) {
	f := newFixture(t)
	f.publish()
	status(t, f.do("GET", prefix+"bad%00", nil, ""), 404)
	status(t, f.do("PUT", prefix+infoPath+"?bad=yes", f.meta, ""), 400)
	excluded := "nix-cache-info"
	c := f.c
	c.ExcludesPattern = &excluded
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	status(t, f.do("GET", prefix+"nix-cache-info", nil, ""), 404)
	v := *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Nix, Repositories: []string{"local"}})
	if err := f.db.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"local", "virtual"} {
		original := f.items.Blobs
		done := false
		f.items.Blobs = hookBlobs{Store: original, before: func() {
			if done {
				return
			}
			done = true
			changed, err := f.db.Repos().Get(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			changed.Description += "new revision"
			if err = f.db.Repos().Update(t.Context(), changed); err != nil {
				t.Fatal(err)
			}
		}}
		path := "/artifactory/api/nix/" + key + "/nar/" + infoPath[:32] + "/" + f.info.URL[4:]
		status(t, f.do("GET", path, nil, ""), 503)
		f.items.Blobs = original
	}
}
