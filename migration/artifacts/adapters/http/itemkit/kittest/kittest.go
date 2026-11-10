package kittest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The keys of the repositories New seeds.
const (
	LocalKey     = "libs-local"
	RemoteKey    = "libs-remote"
	FederatedKey = "libs-federated"
	VirtualKey   = "libs"
)

// Admin is the principal Request acts as.
var Admin = kernel.Principal{Name: "admin", Admin: true}

// World is a seeded in-memory world for unit tests of the item kit.
type World struct {
	// Store is the in-memory metadata store.
	Store store.Store
	// Blobs is a file-system blob store in the test's temporary directory.
	Blobs blob.Store
	// Local, Remote, Federated and Virtual are the seeded generic
	// repositories, as stored. Virtual's members are Local, then Remote,
	// and Local is its default deployment repository.
	Local, Remote, Federated, Virtual repo.Config
}

// New returns a world of its own: a fresh in-memory store seeded with a
// generic repository of each class, and an empty blob store under t's
// temporary directory.
func New(t testing.TB) *World {
	t.Helper()
	ctx := t.Context()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &World{Store: inmem.New(), Blobs: blobs}
	for _, s := range []struct {
		into *repo.Config
		c    repo.Config
	}{
		{&w.Local, repo.Config{Key: LocalKey, Class: repo.Local}},
		{&w.Remote, repo.Config{Key: RemoteKey, Class: repo.Remote, URL: "https://remote.example"}},
		{&w.Federated, repo.Config{Key: FederatedKey, Class: repo.Federated}},
		{&w.Virtual, repo.Config{
			Key:                   VirtualKey,
			Class:                 repo.Virtual,
			Repositories:          []string{LocalKey, RemoteKey},
			DefaultDeploymentRepo: LocalKey,
		}},
	} {
		if err := w.Store.Repos().Create(ctx, *repo.Defaults(&s.c)); err != nil {
			t.Fatal(err)
		}
		if *s.into, err = w.Store.Repos().Get(ctx, s.c.Key); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// Put stores body as the file at fullPath in c, or in c's cache when
// cached, and returns the item as stored: the blob first, then the item
// with the blob's checksums as both actual and original, as a deploy that
// declared them leaves it. Parent folders are not created, because a
// lookup reads the file's own item.
func (w *World) Put(
	t testing.TB,
	c repo.Config,
	cached bool,
	fullPath string,
	body []byte,
) types.Item {
	t.Helper()
	ctx := t.Context()
	u, err := w.Blobs.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	sums := types.Checksums{
		SHA256: info.Digests.SHA256,
		SHA512: info.Digests.SHA512,
		SHA1:   info.Digests.SHA1,
		MD5:    info.Digests.MD5,
	}
	// A fixed time keeps Last-Modified and conditional requests repeatable.
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	it, err := w.Store.Items().Put(ctx, types.Item{
		RepoKey:    c.Key,
		Cached:     cached,
		Name:       fullPath,
		Size:       info.Size,
		Actual:     sums,
		Original:   sums,
		MimeType:   item.MimeType(path.Base(fullPath)),
		Created:    at,
		CreatedBy:  Admin.Name,
		Modified:   at,
		ModifiedBy: Admin.Name,
		Updated:    at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

// Kit returns a kit over the world, built from d with itemkit.New. Where d
// leaves them nil, Store and Blobs are the world's, and Access.Authorizer
// is a fake whose evaluation allows nothing: a request whose context
// carries no decider is refused rather than panicking.
func (w *World) Kit(d itemkit.Deps) *itemkit.Kit {
	if d.Store == nil {
		d.Store = w.Store
	}
	if d.Blobs == nil {
		d.Blobs = w.Blobs
	}
	if d.Access.Authorizer == nil {
		d.Access.Authorizer = &accessapitest.Authorizer{}
	}
	return itemkit.New(d)
}

// AdminAccess is Admin's access: its decider allows every question.
func AdminAccess() itemkit.Access {
	return itemkit.Access{Decider: &accessapitest.Decider{Admin: true}}
}

// Request returns a request for target made by Admin, under t's context:
// it carries the principal and AdminAccess's decider, as the authentication
// middleware leaves an authorised request.
func Request(t testing.TB, method, target string) *http.Request {
	r := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	return r.WithContext(accessapi.WithCaller(r.Context(), Admin, AdminAccess().Decider))
}
